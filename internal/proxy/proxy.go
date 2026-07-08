// Package proxy implements the local OpenAI-compatible HTTP gateway for
// autoapi. External clients (OpenAI SDK, curl, etc.) connect to the chi router
// on 0.0.0.0:8344 (default). The proxy authenticates requests using autoapi
// key IDs, evaluates routing rules, decrypts the upstream provider key, and
// forwards the request via httputil.ReverseProxy.
package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// statusClientClosed is the nginx convention for a request that the client
// closed before the server finished sending the response. Used to distinguish
// mid-stream client disconnects from genuine upstream failures in request logs.
const statusClientClosed = 499

// upstreamKeyProvider is the subset of *service.Service the proxy uses to
// decrypt provider upstream keys.
type upstreamKeyProvider interface {
	ResolveProviderKey(providerID string) (string, error)
}

// storeProxy is the subset of *store.Store methods the proxy needs. Passing
// the concrete store keeps the package dependency explicit while still making
// the constructor testable with a mock.
type storeProxy interface {
	ListProviders() ([]model.Provider, error)
	ListModelRules() ([]model.ModelRule, error)
	GetProvider(id string) (*model.Provider, error)
	ListAPIKeys() ([]model.ApiKey, error)
	GetProviderKeyCiphertext(providerID string) (ciphertext, nonce []byte, err error)
	InsertRequestLog(l model.RequestLog) error
	InsertRequestLogsBatch(logs []model.RequestLog) error
	ListModels(providerID string) ([]model.Model, error)
	GetSettings() (*model.Settings, error)
	Dashboard() (*model.DashboardData, error)
	UpdateProviderHealth(id string, status model.ProviderStatus, errorMessage string) error
	IncrementTargetStats(targetID string, hitDelta, failDelta int64) error
}

// Proxy implements api.ProxyService. It owns the chi router and the underlying
// http.Server. The zero value is not ready for use; call New.
type Proxy struct {
	store            storeProxy
	service          upstreamKeyProvider
	settingsProvider func() *model.Settings

	mu       sync.RWMutex
	listener net.Listener
	server   *http.Server
	router   chi.Router

	bufferPool    *bufferPool
	errorLog      *log.Logger
	activeConns   atomic.Int32
	writer        *logWriter

	breakersMu sync.RWMutex
	breakers   map[string]*CircuitBreaker
}

// New creates a Proxy. The settingsProvider is called on Start/Restart to read
// the current port/bind configuration. Pass a concrete *store.Store as the
// store argument.
func New(store storeProxy, service upstreamKeyProvider, settingsProvider func() *model.Settings) *Proxy {
	p := &Proxy{
		store:            store,
		service:          service,
		settingsProvider: settingsProvider,
		bufferPool: &bufferPool{pool: &sync.Pool{
			New: func() interface{} { return make([]byte, 32*1024) },
		}},
		// Route httputil.ReverseProxy error logging through slog.
		errorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
		breakers: make(map[string]*CircuitBreaker),
		writer:   newLogWriter(store),
	}
	p.router = p.setupRouter()
	return p
}

// Start opens the TCP listener and begins serving the chi router in a
// goroutine. It is safe to call multiple times; subsequent calls are no-ops.
func (p *Proxy) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.listener != nil {
		return nil
	}

	s := p.currentSettings()
	addr := net.JoinHostPort(s.Server.BindAddress, strconv.Itoa(s.Server.Port))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy: listen %s: %w", addr, err)
	}

	p.listener = ln
	p.server = &http.Server{
		Handler: p.router,
	}
	go func() {
		if err := p.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("proxy server exited", "err", err)
		}
	}()
	slog.Info("proxy started", "addr", ln.Addr().String())
	return nil
}

// Stop performs a graceful shutdown of the http.Server and log writer. It is
// safe to call multiple times; subsequent calls are no-ops.
func (p *Proxy) Stop() error {
	if err := p.stopServer(); err != nil {
		return err
	}
	if p.writer != nil {
		p.writer.Stop()
	}
	return nil
}

// stopServer stops the listener and http.Server without touching the log
// writer. Used by Restart to keep the async writer alive across rebinds.
func (p *Proxy) stopServer() error {
	p.mu.Lock()
	server := p.server
	listener := p.listener
	p.server = nil
	p.listener = nil
	p.mu.Unlock()

	if server == nil {
		return nil
	}
	if listener != nil {
		_ = listener.Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

// Restart stops then starts the proxy so that new settings (port/bind) take
// effect. The log writer stays alive across the restart.
func (p *Proxy) Restart() error {
	if err := p.stopServer(); err != nil {
		return err
	}
	return p.Start()
}

// IsRunning reports whether the proxy has an active listener and server.
func (p *Proxy) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.listener != nil && p.server != nil
}

// URL returns the running listener URL for the UI (e.g. http://0.0.0.0:8344).
// Returns an empty string if not running.
func (p *Proxy) URL() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.listener == nil {
		return ""
	}
	return fmt.Sprintf("http://%s", p.listener.Addr().String())
}

// ActiveConnections returns the number of requests currently being handled
// by the proxy middleware counter.
func (p *Proxy) ActiveConnections() int {
	return int(p.activeConns.Load())
}

// OnLogFlush registers a callback fired after each successful batch flush of
// request logs to the store. Used by the API layer to emit real-time UI events
// (e.g. a "log:new" Wails event) so the dashboard refreshes without polling.
//
// Must be called after New; the writer field is non-nil in any well-formed
// Proxy. Calling with nil removes any previously-registered callback.
func (p *Proxy) OnLogFlush(fn func()) {
	if p.writer == nil {
		return
	}
	p.writer.muFlush.Lock()
	p.writer.onFlush = fn
	p.writer.muFlush.Unlock()
}

// ---------------------------------------------------------------------------
// Router / middleware
// ---------------------------------------------------------------------------

func (p *Proxy) setupRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(p.connCounter)
	r.Use(slogMiddleware)

	r.Post("/v1/chat/completions", p.handleChatCompletions)
	r.Post("/v1/embeddings", p.handleEmbeddings)
	r.Post("/v1/images/generations", p.handleOpenAI)
	r.Post("/v1/audio/transcriptions", p.handleOpenAI)
	r.Post("/v1/files", p.handleOpenAI)
	r.Get("/v1/files", p.handleOpenAI)
	r.Get("/v1/models", p.handleModels)
	r.Get("/v1/stats/tokens", p.handleTokenStats)
	r.Get("/", p.handleRoot)
	r.NotFound(p.handleNotFound)

	return r
}

// connCounter increments an atomic request counter while each request is active.
func (p *Proxy) connCounter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.activeConns.Add(1)
		defer p.activeConns.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// slogMiddleware logs every request with structured slog output. It uses chi's
// response writer wrapper to capture status and bytes written.
func slogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("proxy request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (p *Proxy) currentSettings() *model.Settings {
	s := &model.Settings{}
	if p.settingsProvider != nil {
		if provided := p.settingsProvider(); provided != nil {
			*s = *provided
		}
	}
	if s.Server.Port == 0 {
		s.Server.Port = 8344
	}
	if s.Server.BindAddress == "" {
		s.Server.BindAddress = "0.0.0.0"
	}
	return s
}

func (p *Proxy) loadModelRules() []model.ModelRule {
	rules, err := p.store.ListModelRules()
	if err != nil {
		slog.Error("proxy: failed to load model rules", "err", err)
		return nil
	}
	return rules
}

// authenticate validates the Bearer token against autoapi API keys. The token
// is expected to be the api_keys.id UUID. Disabled or expired keys are
// rejected.
func (p *Proxy) authenticate(r *http.Request) (apiKeyID string, ok bool, err error) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false, nil
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	if token == "" {
		return "", false, nil
	}

	keys, err := p.store.ListAPIKeys()
	if err != nil {
		return "", false, err
	}
	now := time.Now().UnixMilli()
	for _, k := range keys {
		if k.ID == token {
			if k.ExpiresAt > 0 && k.ExpiresAt < now {
				return "", false, nil
			}
			return k.ID, true, nil
		}
	}
	return "", false, nil
}

// writeError writes an OpenAI-compatible error JSON body.
func (p *Proxy) writeError(w http.ResponseWriter, status int, typ, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    typ,
		},
	})
}

// logRequestEntry persists a request log. It treats a full log-writer queue
// as a soft drop (per oracle) and does not fail the request.
func (p *Proxy) logRequestEntry(log *model.RequestLog) {
	log.ID = newUUID()
	if log.Timestamp == 0 {
		log.Timestamp = time.Now().UnixMilli()
	}
	if p.writer == nil {
		// Defensive: should never happen after New.
		return
	}
	if !p.writer.Enqueue(*log) {
		slog.Warn("proxy: request log dropped: writer queue full", "api_key_id", log.APIKeyID)
	}
}

// resolveCandidates selects one or more provider/model candidates using the
// model-rule matcher, filtering out providers with an open circuit breaker.
func (p *Proxy) resolveCandidates(req *InboundRequest) ([]candidate, error) {
	rules := p.loadModelRules()
	settings := p.currentSettings().Routing

	// Snapshot the breaker map to avoid racing with breakerFor writes.
	p.breakersMu.RLock()
	breakers := make(map[string]*CircuitBreaker, len(p.breakers))
	for k, v := range p.breakers {
		breakers[k] = v
	}
	p.breakersMu.RUnlock()

	return selectCandidates(req, rules, settings.DefaultProviderID, settings.DefaultModel, breakers, p.store.GetProvider)
}

// breakerFor returns the circuit breaker for a provider, creating one if needed.
func (p *Proxy) breakerFor(providerID string) *CircuitBreaker {
	p.breakersMu.Lock()
	defer p.breakersMu.Unlock()
	if p.breakers == nil {
		p.breakers = make(map[string]*CircuitBreaker)
	}
	if cb, ok := p.breakers[providerID]; ok {
		return cb
	}
	cb := NewCircuitBreaker()
	p.breakers[providerID] = cb
	return cb
}

// forwardWithFailover tries each candidate in order. It buffers each upstream
// response in memory and only copies a successful response to the real
// ResponseWriter, guaranteeing the client never sees a failed provider's output.
// For streaming requests, it delegates to forwardStream so the client receives
// chunks in real time.
func (p *Proxy) forwardWithFailover(w http.ResponseWriter, r *http.Request, body []byte, candidates []candidate, isStream bool, inputEstimate int, logEntry *model.RequestLog) {
	if isStream {
		p.forwardStream(w, r, body, candidates, inputEstimate, logEntry)
		return
	}
	var lastErr error = fmt.Errorf("no candidate produced a response")
	var lastStatus int
	// lastCandidate tracks the most recently iterated candidate so the
	// all-candidates-exhausted branch below can populate log provider fields
	// when no candidate produced a successful response.
	var lastCandidate candidate

	for _, c := range candidates {
		lastCandidate = c
		if !p.breakerFor(c.provider.ID).Allow() {
			slog.Debug("proxy: candidate circuit open", "provider", c.provider.Name, "model", c.modelName)
			continue
		}

		upstreamKey, err := p.service.ResolveProviderKey(c.provider.ID)
		if err != nil {
			lastErr = fmt.Errorf("resolve key for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: candidate key resolution failed", "provider", c.provider.Name, "err", lastErr)
			continue
		}

		rewrittenBody, err := rewriteBodyModel(body, c.modelName)
		if err != nil {
			lastErr = fmt.Errorf("rewrite body for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: candidate body rewrite failed", "provider", c.provider.Name, "err", lastErr)
			continue
		}

		upstreamURL, err := url.Parse(store.JoinProviderURL(c.provider.BaseURL, r.URL.Path))
		if err != nil {
			lastErr = fmt.Errorf("invalid base URL for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: candidate URL invalid", "provider", c.provider.Name, "err", lastErr)
			continue
		}

		// Per-target retry loop: retry only on CategoryRetryable, up to
		// maxRetries additional attempts. The circuit breaker is recorded
		// ONCE on the final outcome (success or last failure), not per
		// attempt, to preserve its original "one signal per candidate"
		// calibration. Before each retry we re-check the breaker so an
		// open circuit aborts further attempts on this target and we fall
		// through to the next candidate.
		var succeeded bool
		var finalCat ErrorCategory
		var finalAttemptErr error
		for attempt := 0; attempt <= c.maxRetries; attempt++ {
			if attempt > 0 && !p.breakerFor(c.provider.ID).Allow() {
				slog.Debug("proxy: circuit opened mid-retry, falling through", "provider", c.provider.Name, "attempt", attempt)
				break
			}

			attemptReq := r.Clone(r.Context())
			attemptReq.Body = io.NopCloser(bytes.NewReader(rewrittenBody))
			attemptReq.ContentLength = int64(len(rewrittenBody))
			attemptReq.Header.Del("Transfer-Encoding")
			if attemptReq.Header.Get("Content-Type") == "" {
				attemptReq.Header.Set("Content-Type", "application/json")
			}
			attemptReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(rewrittenBody)))

		proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
		proxy.BufferPool = p.bufferPool
		proxy.ErrorLog = p.errorLog

			proxy.Director = func(req *http.Request) {
				req.URL.Scheme = upstreamURL.Scheme
				req.URL.Host = upstreamURL.Host
				req.URL.Path = upstreamURL.Path
				req.URL.RawPath = upstreamURL.RawPath
				req.URL.RawQuery = r.URL.RawQuery
				req.Host = upstreamURL.Host
				req.Header.Del("Authorization")
				req.Header.Set("Authorization", "Bearer "+upstreamKey)
				req.Header.Set("X-Autoapi-Route", c.ruleID)
				if req.Header.Get("Content-Type") == "" {
					req.Header.Set("Content-Type", "application/json")
				}
			}

			buf := &responseBuffer{statusCode: http.StatusOK, header: make(http.Header), body: bytes.NewBuffer(nil)}
			var attemptErr error

			proxy.ModifyResponse = func(resp *http.Response) error {
				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					return err
				}
				_ = resp.Body.Close()
				resp.Body = io.NopCloser(bytes.NewReader(respBody))

				if isStream {
					it, ot := parseStreamUsage(respBody)
					if it > 0 || ot > 0 {
						logEntry.InputTokens, logEntry.OutputTokens = it, ot
					} else {
						logEntry.InputTokens = inputEstimate
						logEntry.OutputTokens = len(respBody) / 4
					}
				} else {
					it, ot := parseJSONUsage(respBody)
					if it > 0 || ot > 0 {
						logEntry.InputTokens, logEntry.OutputTokens = it, ot
					} else {
						logEntry.InputTokens = inputEstimate
						logEntry.OutputTokens = len(respBody) / 4
					}
				}
				return nil
			}

			proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, err error) {
				attemptErr = err
			}

			proxy.ServeHTTP(buf, attemptReq)

			cat := CategorizeError(attemptErr, buf.statusCode)
			finalCat = cat
			finalAttemptErr = attemptErr
			slog.Debug("proxy: candidate attempt",
				"provider", c.provider.Name,
				"model", c.modelName,
				"attempt", attempt,
				"category", cat,
				"status", buf.statusCode,
				"err", attemptErr)

			if attemptErr == nil && buf.statusCode < 400 {
				// SUCCESS — copy response, record breaker + hit counter once.
				copyBufferedResponse(w, buf)
				logEntry.StatusCode = buf.statusCode
				logEntry.ProviderID = c.provider.ID
				logEntry.ProviderName = c.provider.Name
				logEntry.Model = c.modelName
				logEntry.RouteID = c.ruleID
				logEntry.RouteLabel = c.ruleLabel
				p.breakerFor(c.provider.ID).Record(true)
				if c.targetID != "" {
					if err := p.store.IncrementTargetStats(c.targetID, 1, 0); err != nil {
						slog.Error("proxy: increment target hit count", "err", err)
					}
				}
				if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusConnected, ""); err != nil {
					slog.Error("proxy: update provider health", "err", err)
				}
				succeeded = true
				break
			}

			// FAILED attempt — record failure counter on every failed attempt.
			lastErr = attemptErr
			if lastErr == nil {
				lastErr = fmt.Errorf("upstream %s returned status %d", c.provider.Name, buf.statusCode)
			}
			lastStatus = buf.statusCode
			if c.targetID != "" {
				if err := p.store.IncrementTargetStats(c.targetID, 0, 1); err != nil {
					slog.Error("proxy: increment target failure count", "err", err)
				}
			}

			switch cat {
			case CategoryClientAbort:
				p.writeError(w, http.StatusBadRequest, "client_error", lastErr.Error())
				logEntry.StatusCode = http.StatusBadRequest
				logEntry.Error = lastErr.Error()
				logEntry.ProviderID = c.provider.ID
				logEntry.ProviderName = c.provider.Name
				logEntry.Model = c.modelName
				logEntry.RouteID = c.ruleID
				logEntry.RouteLabel = c.ruleLabel
				// No breaker record: client errors aren't provider failures.
				return
			case CategoryNonRetryable:
				p.writeError(w, buf.statusCode, "invalid_request_error", lastErr.Error())
				logEntry.StatusCode = buf.statusCode
				logEntry.Error = lastErr.Error()
				logEntry.ProviderID = c.provider.ID
				logEntry.ProviderName = c.provider.Name
				logEntry.Model = c.modelName
				logEntry.RouteID = c.ruleID
				logEntry.RouteLabel = c.ruleLabel
				// Record breaker once on the (final) provider-side non-retryable.
				if isCircuitBreakerFailure(attemptErr, buf.statusCode) {
					p.breakerFor(c.provider.ID).Record(false)
					if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, lastErr.Error()); err != nil {
						slog.Error("proxy: update provider health", "err", err)
					}
				}
				return
			case CategoryRetryable:
				// Retryable: loop continues. Breaker is recorded only on the
				// final outcome below (or in the NonRetryable/ClientAbort
				// branches above for hard stops).
				continue
			}
		}

		if succeeded {
			return
		}

		// Exhausted retries (or breaker opened mid-retry): record breaker
		// once on the final outcome, then fall through to the next candidate.
		if finalCat == CategoryRetryable && isCircuitBreakerFailure(finalAttemptErr, lastStatus) {
			p.breakerFor(c.provider.ID).Record(false)
			if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, lastErr.Error()); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
		}
	}

	// lastErr is nil only if every candidate was rejected by its circuit
	// breaker (the loop body `continue`d without assigning it). Guard against
	// the nil dereference on lastErr.Error() in the writeError call below.
	if lastErr == nil {
		lastErr = fmt.Errorf("no available provider: all circuits open")
	}
	status := http.StatusBadGateway
	if lastStatus >= 500 {
		status = http.StatusServiceUnavailable
	}
	p.writeError(w, status, "upstream_error", lastErr.Error())
	logEntry.StatusCode = status
	logEntry.Error = lastErr.Error()
	if lastCandidate.provider != nil {
		logEntry.ProviderID = lastCandidate.provider.ID
		logEntry.ProviderName = lastCandidate.provider.Name
		logEntry.Model = lastCandidate.modelName
		logEntry.RouteID = lastCandidate.ruleID
		logEntry.RouteLabel = lastCandidate.ruleLabel
	}
}

// forwardStream proxies a streaming request to the first available candidate
// without buffering. Real-time chunks are flushed to the client as they arrive.
// Failover is intentionally disabled for streaming: once the first byte is
// committed to the client we cannot roll it back.
//
// Status code is captured from upstream via ReverseProxy.ModifyResponse, since
// chi's WrapResponseWriter status is unreliable in streaming scenarios (headers
// may not have been written when ServeHTTP returns to the post-block).
//
// Time to first token/byte (TTFT) is captured by wrapping resp.Body in
// firstByteTrackingReadCloser inside ModifyResponse. The wrapper is a pure
// passthrough so FlushInterval=-1 real-time flushing is preserved. ReverseProxy
// calls Body.Close() before ServeHTTP returns (same goroutine), so the
// FirstTokenMs value is set before the post-ServeHTTP block reads it.
func (p *Proxy) forwardStream(w http.ResponseWriter, r *http.Request, body []byte, candidates []candidate, inputEstimate int, logEntry *model.RequestLog) {
	logEntry.IsStream = true

	var chosen candidate
	var upstreamKey string
	var found bool

	for _, c := range candidates {
		if !p.breakerFor(c.provider.ID).Allow() {
			continue
		}
		key, err := p.service.ResolveProviderKey(c.provider.ID)
		if err != nil {
			slog.Debug("proxy: stream candidate key resolution failed", "provider", c.provider.Name, "err", err)
			continue
		}
		chosen = c
		upstreamKey = key
		found = true
		break
	}

	if !found {
		p.writeError(w, http.StatusServiceUnavailable, "service_unavailable", "no available provider")
		logEntry.StatusCode = http.StatusServiceUnavailable
		logEntry.Error = "no available provider"
		return
	}

	rewrittenBody, _ := rewriteBodyModel(body, chosen.modelName)
	upstreamURL, err := url.Parse(store.JoinProviderURL(chosen.provider.BaseURL, r.URL.Path))
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "invalid provider base URL")
		logEntry.StatusCode = http.StatusInternalServerError
		logEntry.Error = err.Error()
		logEntry.ProviderID = chosen.provider.ID
		logEntry.ProviderName = chosen.provider.Name
		logEntry.Model = chosen.modelName
		logEntry.RouteID = chosen.ruleID
		logEntry.RouteLabel = chosen.ruleLabel
		return
	}

	attemptReq := r.Clone(r.Context())
	attemptReq.Body = io.NopCloser(bytes.NewReader(rewrittenBody))
	attemptReq.ContentLength = int64(len(rewrittenBody))
	attemptReq.Header.Del("Transfer-Encoding")
	if attemptReq.Header.Get("Content-Type") == "" {
		attemptReq.Header.Set("Content-Type", "application/json")
	}
	attemptReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(rewrittenBody)))

	proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	proxy.FlushInterval = -1
	proxy.BufferPool = p.bufferPool
	proxy.ErrorLog = p.errorLog

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = upstreamURL.Scheme
		req.URL.Host = upstreamURL.Host
		req.URL.Path = upstreamURL.Path
		req.URL.RawPath = upstreamURL.RawPath
		req.URL.RawQuery = r.URL.RawQuery
		req.Host = upstreamURL.Host
		req.Header.Del("Authorization")
		req.Header.Set("Authorization", "Bearer "+upstreamKey)
		req.Header.Set("X-Autoapi-Route", chosen.ruleID)
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	// upstreamStatus is captured by ModifyResponse. 0 means the connection
	// failed before any response header arrived (ErrorHandler was called
	// instead). We use this to detect that the post-ServeHTTP block cannot
	// rely on the ModifyResponse-set status code.
	var upstreamStatus int
	var attemptErr error

	proxy.ModifyResponse = func(resp *http.Response) error {
		upstreamStatus = resp.StatusCode
		logEntry.StatusCode = resp.StatusCode
		// Set provider/model fields NOW (not just after ServeHTTP) so they are
		// present even if ServeHTTP panics or the client disconnects mid-stream
		// before the post-ServeHTTP block runs.
		logEntry.ProviderID = chosen.provider.ID
		logEntry.ProviderName = chosen.provider.Name
		logEntry.Model = chosen.modelName
		logEntry.RouteID = chosen.ruleID
		logEntry.RouteLabel = chosen.ruleLabel
		// Wrap body to capture TTFT (time to first token/byte). Pure
		// passthrough — no buffering — so FlushInterval=-1 real-time
		// flushing is preserved. ReverseProxy calls Body.Close() before
		// ServeHTTP returns (same goroutine), so onStart fires before the
		// post-ServeHTTP block reads logEntry.FirstTokenMs.
		resp.Body = &firstByteTrackingReadCloser{
			inner: resp.Body,
			onStart: func() {
				logEntry.FirstTokenMs = int(time.Now().UnixMilli() - logEntry.Timestamp)
			},
		}
		return nil
	}

	// ErrorHandler only needs to record the error. Do NOT call writeError here
	// — the client connection may already be in a half-closed state, and
	// ReverseProxy has already attempted to flush a 502. Writing again would
	// produce a "superfluous WriteHeader call" warning at best, or a panic on
	// a closed connection at worst. The post-ServeHTTP block below derives
	// the response status from attemptErr.
	proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, err error) {
		attemptErr = err
	}

	ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
	// Catch panics from proxy.ServeHTTP (e.g. body wrapper edge cases) so the
	// logEntry still gets provider/model fields for diagnosis. Without this,
	// the chi Recoverer (outer middleware) catches it but the handler's
	// deferred logRequestEntry fires with only ModifyResponse-set fields.
	defer func() {
		if rvr := recover(); rvr != nil {
			slog.Error("forwardStream panic during ServeHTTP",
				"panic", rvr,
				"provider", chosen.provider.Name,
				"upstreamStatus", upstreamStatus,
				"attemptErr", attemptErr,
			)
			logEntry.ProviderID = chosen.provider.ID
			logEntry.ProviderName = chosen.provider.Name
			logEntry.Model = chosen.modelName
			logEntry.RouteID = chosen.ruleID
			logEntry.RouteLabel = chosen.ruleLabel
			logEntry.LatencyMs = int(time.Now().UnixMilli() - logEntry.Timestamp)
			if logEntry.StatusCode == 0 {
				logEntry.StatusCode = http.StatusInternalServerError
			}
			logEntry.Error = fmt.Sprintf("internal error: %v", rvr)
			panic(rvr) // re-panic so Recoverer still handles the HTTP response
		}
	}()
	proxy.ServeHTTP(ww, attemptReq)

	logEntry.InputTokens = inputEstimate
	logEntry.OutputTokens = ww.BytesWritten() / 4
	logEntry.ProviderID = chosen.provider.ID
	logEntry.ProviderName = chosen.provider.Name
	logEntry.Model = chosen.modelName
	logEntry.RouteID = chosen.ruleID
	logEntry.RouteLabel = chosen.ruleLabel

	// Connection failed before headers arrived — ModifyResponse was not called
	// and upstreamStatus stayed 0. Classify the failure and decide whether to
	// penalize the provider.
	if upstreamStatus == 0 && attemptErr != nil {
		if isClientDisconnect(attemptErr) {
			// Client went away mid-request. Do not penalize the provider.
			logEntry.StatusCode = statusClientClosed
			logEntry.Error = "client disconnected: " + attemptErr.Error()
			return
		}
		// Genuine upstream/transport failure before headers.
		logEntry.StatusCode = http.StatusBadGateway
		logEntry.Error = attemptErr.Error()
		if isCircuitBreakerFailure(attemptErr, http.StatusBadGateway) {
			p.breakerFor(chosen.provider.ID).Record(false)
			if err := p.store.UpdateProviderHealth(chosen.provider.ID, model.ProviderStatusError, attemptErr.Error()); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
		}
		return
	}

	// Headers did arrive. If the client disconnected mid-stream, the upstream
	// may have already returned 200; the broken-pipe error from ReverseProxy
	// matches isNetError, which would otherwise trip the circuit breaker.
	if attemptErr != nil && isClientDisconnect(attemptErr) {
		logEntry.StatusCode = statusClientClosed // 499
		logEntry.Error = "client disconnected: " + attemptErr.Error()
		return
	}

	// Defensive: ReverseProxy should always invoke ModifyResponse or ErrorHandler,
	// so upstreamStatus==0 here (no headers, no error) is theoretically
	// unreachable. Guard against it so a future regression cannot silently log a
	// status=0 success and record a false-positive breaker success.
	if upstreamStatus == 0 {
		upstreamStatus = http.StatusBadGateway
		logEntry.StatusCode = http.StatusBadGateway
	}

	// Success path: 2xx/3xx upstream and no client error.
	if upstreamStatus < 400 && attemptErr == nil {
		p.breakerFor(chosen.provider.ID).Record(true)
		if chosen.targetID != "" {
			if err := p.store.IncrementTargetStats(chosen.targetID, 1, 0); err != nil {
				slog.Error("proxy: increment target hit count (stream)", "err", err)
			}
		}
		if err := p.store.UpdateProviderHealth(chosen.provider.ID, model.ProviderStatusConnected, ""); err != nil {
			slog.Error("proxy: update provider health", "err", err)
		}
		return
	}

	// Non-2xx upstream status (recorded by ModifyResponse). The proxy
	// already copied the upstream response to the client, so we only need to
	// log the error and penalize the breaker for retryable failures.
	if attemptErr == nil {
		attemptErr = fmt.Errorf("upstream %s returned status %d", chosen.provider.Name, upstreamStatus)
	}
	logEntry.Error = attemptErr.Error()
	if chosen.targetID != "" {
		if err := p.store.IncrementTargetStats(chosen.targetID, 0, 1); err != nil {
			slog.Error("proxy: increment target failure count (stream)", "err", err)
		}
	}
	if isCircuitBreakerFailure(attemptErr, upstreamStatus) {
		p.breakerFor(chosen.provider.ID).Record(false)
		if err := p.store.UpdateProviderHealth(chosen.provider.ID, model.ProviderStatusError, attemptErr.Error()); err != nil {
			slog.Error("proxy: update provider health", "err", err)
		}
	}
}

// responseBuffer is an in-memory http.ResponseWriter used to capture a single
// upstream attempt without writing anything to the client.
type responseBuffer struct {
	statusCode int
	header     http.Header
	body       *bytes.Buffer
	wrote      bool
}

func (rb *responseBuffer) Header() http.Header { return rb.header }

func (rb *responseBuffer) Write(p []byte) (int, error) {
	if !rb.wrote {
		rb.statusCode = http.StatusOK
		rb.wrote = true
	}
	return rb.body.Write(p)
}

func (rb *responseBuffer) WriteHeader(code int) {
	if !rb.wrote {
		rb.statusCode = code
		rb.wrote = true
	}
}

// Flush is a no-op because the response is fully buffered before copying.
func (rb *responseBuffer) Flush() {}

func copyBufferedResponse(w http.ResponseWriter, buf *responseBuffer) {
	for k, vv := range buf.header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(buf.statusCode)
	_, _ = w.Write(buf.body.Bytes())
}

// bufferPool wraps sync.Pool to satisfy httputil.BufferPool.
type bufferPool struct {
	pool *sync.Pool
}

func (bp *bufferPool) Get() []byte { return bp.pool.Get().([]byte) }
func (bp *bufferPool) Put(b []byte) { bp.pool.Put(b) }

// firstByteTrackingReadCloser wraps an io.ReadCloser to record the time of the
// first successful Read. Used in forwardStream to capture TTFT (time to first
// token/byte) for streaming responses. It is a pure passthrough — no buffering
// or transformation — so real-time SSE flushing is preserved.
//
// onStart fires after the inner Read returns its first chunk (n > 0 or
// io.EOF), not when Read is invoked. Capturing before the inner Read would
// measure dispatch time, not data arrival.
//
// ReverseProxy calls Body.Close() before ServeHTTP returns (same goroutine),
// guaranteeing onStart has fired before the post-ServeHTTP block executes.
type firstByteTrackingReadCloser struct {
	inner   io.ReadCloser
	onStart func()
	once    sync.Once
}

func (f *firstByteTrackingReadCloser) Read(p []byte) (int, error) {
	n, err := f.inner.Read(p)
	// Fire onStart on the first Read that returns from the inner reader,
	// including EOF (which is the end of the stream but still a real
	// arrival event). This guarantees the timestamp reflects actual data
	// arrival, not the moment Read was called.
	if n > 0 || err == io.EOF {
		f.once.Do(f.onStart)
	}
	return n, err
}

func (f *firstByteTrackingReadCloser) Close() error {
	return f.inner.Close()
}

func newUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(fmt.Sprintf("proxy: uuid generation failed: %v", err))
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

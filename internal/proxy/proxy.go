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

// upstreamResponseHeaderTimeout is the max time the proxy waits for the
// upstream provider to send response headers. Without this bound an
// unresponsive upstream (e.g. a hung LLM gateway) would keep a streaming
// request pending indefinitely because the client's HTTP server is
// blocked reading the upstream connection. The bound applies per attempt
// so retries fail fast.
const upstreamResponseHeaderTimeout = 30 * time.Second

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

	bufferPool  *bufferPool
	errorLog    *log.Logger
	activeConns atomic.Int32
	writer      *logWriter

	// transport is the shared http.RoundTripper used for every upstream
	// call (both streaming and non-streaming). It is built from
	// http.DefaultTransport with a ResponseHeaderTimeout so an
	// unresponsive upstream fails fast (otherwise a stuck provider would
	// block a streaming request until the client times out, with no
	// chance to retry a different target).
	transport http.RoundTripper

	breakersMu sync.RWMutex
	breakers   map[string]*CircuitBreaker
}

// New creates a Proxy. The settingsProvider is called on Start/Restart to read
// the current port/bind configuration. Pass a concrete *store.Store as the
// store argument.
func New(store storeProxy, service upstreamKeyProvider, settingsProvider func() *model.Settings) *Proxy {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = upstreamResponseHeaderTimeout
	p := &Proxy{
		store:            store,
		service:          service,
		settingsProvider: settingsProvider,
		bufferPool: &bufferPool{pool: &sync.Pool{
			New: func() interface{} { return make([]byte, 32*1024) },
		}},
		// Route httputil.ReverseProxy error logging through slog.
		errorLog:  slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
		transport: transport,
		breakers:  make(map[string]*CircuitBreaker),
		writer:    newLogWriter(store),
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
	slog.Info("proxy: stopping")
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
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("proxy: shutdown failed", "err", err)
		return err
	}
	return nil
}

// Restart stops then starts the proxy so that new settings (port/bind) take
// effect. The log writer stays alive across the restart.
func (p *Proxy) Restart() error {
	slog.Info("proxy: restarting")
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
	slog.Debug("proxy: OnLogFlush callback registered")
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
		slog.Debug("proxy request",
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
	slog.Debug("proxy: new circuit breaker", "provider", providerID)
	return cb
}

// forwardWithFailover tries each candidate in order. It buffers each upstream
// response in memory and only copies a successful response to the real
// ResponseWriter, guaranteeing the client never sees a failed provider's output.
// For streaming requests, it delegates to forwardStream so the client receives
// chunks in real time.
//
// Per-candidate attempts are also appended to logEntry.Chain as
// model.RequestLogChainEntry rows so the UI can show the failover path
// ("tried 2 targets"). The final ProviderID/ProviderName/Model on the log
// entry is the successful candidate (or the last attempted candidate when
// every attempt failed) — the same data the rest of the proxy already used.
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
	// attemptOrder is monotonically incremented across every candidate and
	// every retry so the Chain entries form a stable per-request timeline.
	attemptOrder := 0

	for _, c := range candidates {
		lastCandidate = c
		if !p.breakerFor(c.provider.ID).Allow() {
			slog.Debug("proxy: candidate circuit open", "provider", c.provider.Name, "model", c.modelName)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "circuit_open",
				StatusCode:   0,
				Error:        "circuit breaker open",
				LatencyMs:    0,
			})
			continue
		}

		upstreamKey, err := p.service.ResolveProviderKey(c.provider.ID)
		if err != nil {
			lastErr = fmt.Errorf("resolve key for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: candidate key resolution failed", "provider", c.provider.Name, "err", lastErr)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "preflight_error",
				StatusCode:   0,
				Error:        lastErr.Error(),
				LatencyMs:    0,
			})
			continue
		}

		rewrittenBody, err := rewriteBodyModel(body, c.modelName)
		if err != nil {
			lastErr = fmt.Errorf("rewrite body for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: candidate body rewrite failed", "provider", c.provider.Name, "err", lastErr)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "preflight_error",
				StatusCode:   0,
				Error:        lastErr.Error(),
				LatencyMs:    0,
			})
			continue
		}

		upstreamURL, err := url.Parse(store.JoinProviderURL(c.provider.BaseURL, r.URL.Path))
		if err != nil {
			lastErr = fmt.Errorf("invalid base URL for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: candidate URL invalid", "provider", c.provider.Name, "err", lastErr)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "preflight_error",
				StatusCode:   0,
				Error:        lastErr.Error(),
				LatencyMs:    0,
			})
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
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "circuit_open",
					StatusCode:   0,
					Error:        "circuit breaker opened during retries",
					LatencyMs:    0,
				})
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
			// Per-target ResponseHeaderTimeout: when the target has a
			// non-zero timeout, clone p.transport and override the
			// header deadline. Non-streaming has no first-byte concept,
			// so ResponseHeaderTimeout is the right knob. When c.timeout
			// is 0 we keep the shared transport (with its 30s default).
			if c.timeout > 0 {
				if t1, ok := p.transport.(*http.Transport); ok {
					clone := t1.Clone()
					clone.ResponseHeaderTimeout = c.timeout
					proxy.Transport = clone
				} else {
					proxy.Transport = p.transport
				}
			} else {
				proxy.Transport = p.transport
			}

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
			// attemptStart is captured immediately before ServeHTTP so the
			// per-attempt latency is a tight measure of upstream round-trip,
			// excluding any candidate-selection / body-rewrite work above.
			attemptStart := time.Now()

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
			latencyMs := int(time.Since(attemptStart).Milliseconds())
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
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "success",
					StatusCode:   buf.statusCode,
					Error:        "",
					LatencyMs:    latencyMs,
				})
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
				if attempt > 0 {
					slog.Info("proxy: succeeded after retry",
						"provider", c.provider.Name,
						"model", c.modelName,
						"attempts", attempt+1,
						"target", c.targetID)
				}
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
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "client_abort",
					StatusCode:   buf.statusCode,
					Error:        lastErr.Error(),
					LatencyMs:    latencyMs,
				})
				// No breaker record: client errors aren't provider failures.
				slog.Warn("proxy: client abort",
					"provider", c.provider.Name,
					"model", c.modelName,
					"status", buf.statusCode,
					"err", lastErr.Error())
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
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "non_retryable",
					StatusCode:   buf.statusCode,
					Error:        lastErr.Error(),
					LatencyMs:    latencyMs,
				})
				// Record breaker once on the (final) provider-side non-retryable.
				slog.Warn("proxy: non-retryable upstream failure",
					"provider", c.provider.Name,
					"model", c.modelName,
					"status", buf.statusCode,
					"err", lastErr.Error())
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
				// branches above for hard stops). Record the failed attempt
				// so the chain timeline is complete.
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "retryable",
					StatusCode:   buf.statusCode,
					Error:        lastErr.Error(),
					LatencyMs:    latencyMs,
				})
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
	slog.Error("proxy: all candidates exhausted",
		"model", logEntry.Model,
		"route", logEntry.RouteID,
		"lastStatus", lastStatus,
		"err", lastErr.Error())
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

// forwardStream proxies a streaming request to one of the available
// candidates. It performs best-effort failover and per-target retry BEFORE
// committing the first byte to the client: each upstream attempt runs under
// a per-candidate first-byte timeout (Option A: single cumulative deadline
// = headers arrival + first byte read), and only when a 2xx response is
// received do we copy headers and start streaming the body. After the
// 2xx commit, failover stops — the client has already seen the status — and
// the upstream body is forwarded chunk-by-chunk in real time (http.Flusher
// after every Write, preserving SSE compatibility).
//
// True pass-through: there is no buffered body. The first byte through
// the wrapper triggers a single point of success (breaker Record(true),
// hit counter, provider health) so we never double-count. The SSE usage
// parser (streamUsageAccumulator) processes data lines incrementally while
// chunks are forwarded to the client; the [DONE] marker is observed via
// Done() — if the body closes before [DONE], the breaker is penalized
// once via Record(false). Client disconnects (broken pipe writing to the
// client) do NOT penalize the provider.
//
// Per-chain TTFT/latency semantics:
//   - Failed chain entry: LatencyMs = attempt wall-clock; FirstTokenMs = 0.
//   - Success chain entry: LatencyMs = full stream wall-clock;
//     FirstTokenMs = first-byte time within the success attempt.
//   - Top-level logEntry.FirstTokenMs = Σ all prior failed chain
//     LatencyMs + success chain FirstTokenMs. (Sum is computed in
//     forwardStream; handler.go sets the top-level LatencyMs as the
//     overall wall-clock and never needs to be touched here.)
//
// Once we start streaming (i.e. headers are committed), we do NOT retry:
// the client has already seen the response status, so failover would
// produce a malformed or duplicated stream.
func (p *Proxy) forwardStream(w http.ResponseWriter, r *http.Request, body []byte, candidates []candidate, inputEstimate int, logEntry *model.RequestLog) {
	logEntry.IsStream = true

	var lastErr error = fmt.Errorf("no candidate produced a response")
	var lastStatus int
	var lastCandidate candidate
	attemptOrder := 0
	// firstByteCumulativeMs accumulates the LatencyMs of every failed
	// chain entry (across all candidates and all per-target retries) so
	// the top-level logEntry.FirstTokenMs can be computed as
	// "Σ failed chain latencies + success chain FirstTokenMs" per the
	// oracle-approved design.
	firstByteCumulativeMs := 0

	for _, c := range candidates {
		lastCandidate = c
		if !p.breakerFor(c.provider.ID).Allow() {
			slog.Debug("proxy: stream candidate circuit open", "provider", c.provider.Name, "model", c.modelName)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "circuit_open",
				StatusCode:   0,
				Error:        "circuit breaker open",
				LatencyMs:    0,
			})
			continue
		}

		upstreamKey, err := p.service.ResolveProviderKey(c.provider.ID)
		if err != nil {
			slog.Debug("proxy: stream candidate key resolution failed", "provider", c.provider.Name, "err", err)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "preflight_error",
				StatusCode:   0,
				Error:        err.Error(),
				LatencyMs:    0,
			})
			continue
		}

		rewrittenBody, err := rewriteBodyModel(body, c.modelName)
		if err != nil {
			slog.Debug("proxy: stream candidate body rewrite failed", "provider", c.provider.Name, "err", err)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "preflight_error",
				StatusCode:   0,
				Error:        err.Error(),
				LatencyMs:    0,
			})
			continue
		}

		upstreamURL, err := url.Parse(store.JoinProviderURL(c.provider.BaseURL, r.URL.Path))
		if err != nil {
			lastErr = fmt.Errorf("invalid base URL for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: stream candidate URL invalid", "provider", c.provider.Name, "err", err)
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "preflight_error",
				StatusCode:   0,
				Error:        lastErr.Error(),
				LatencyMs:    0,
			})
			continue
		}

		// Per-target retry loop. Each iteration is a fresh streamAttempt
		// call (one upstream HTTP attempt) against the same upstreamURL
		// with the same body. We retry only on CategoryRetryable and only
		// up to c.maxRetries additional attempts, so a target with
		// MaxRetries=0 gets exactly one attempt.
		var succeeded bool
		for attempt := 0; attempt <= c.maxRetries; attempt++ {
			if attempt > 0 && !p.breakerFor(c.provider.ID).Allow() {
				slog.Debug("proxy: stream circuit opened mid-retry, falling through", "provider", c.provider.Name, "attempt", attempt)
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "circuit_open",
					StatusCode:   0,
					Error:        "circuit breaker opened during retries",
					LatencyMs:    0,
				})
				break
			}

			result, newOrder := p.streamAttempt(r.Context(), w, r, c, upstreamKey, rewrittenBody, upstreamURL, attemptOrder, inputEstimate, logEntry)
			attemptOrder = newOrder

			switch result.Status {
			case "success":
				// Top-level FirstTokenMs = Σ prior failed chain
				// LatencyMs + success chain FirstTokenMs.
				logEntry.FirstTokenMs = firstByteCumulativeMs + result.FirstTokenMs
				logEntry.StatusCode = result.StatusCode
				succeeded = true
				return
			case "client_abort":
				logEntry.StatusCode = result.StatusCode
				logEntry.Error = result.Error
				succeeded = true
				return
			case "non_retryable":
				logEntry.StatusCode = result.StatusCode
				logEntry.Error = result.Error
				succeeded = true
				return
			case "retryable":
				firstByteCumulativeMs += result.LatencyMs
				if result.StatusCode != 0 {
					lastErr = fmt.Errorf("upstream %s returned status %d", c.provider.Name, result.StatusCode)
					lastStatus = result.StatusCode
				} else {
					lastErr = fmt.Errorf("upstream %s: %s", c.provider.Name, result.Error)
					lastStatus = 0
				}
				if c.targetID != "" {
					if err := p.store.IncrementTargetStats(c.targetID, 0, 1); err != nil {
						slog.Error("proxy: increment target failure count (stream)", "err", err)
					}
				}
				slog.Debug("proxy: stream retrying same target",
					"provider", c.provider.Name,
					"model", c.modelName,
					"attempt", attempt,
					"maxRetries", c.maxRetries)
				continue
			default:
				slog.Error("proxy: stream unknown attempt status", "status", result.Status)
				return
			}
		}

		if succeeded {
			return
		}

		// Exhausted retries on this candidate (or breaker opened
		// mid-retry). Record breaker once on the final outcome, then
		// fall through to the next candidate.
		if isCircuitBreakerFailure(nil, lastStatus) {
			p.breakerFor(c.provider.ID).Record(false)
			if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, lastErr.Error()); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
		} else if lastStatus == 0 && lastErr != nil && isNetError(lastErr) {
			// Pure transport failure (refused, timeout) — counts as
			// circuit-breaker failure but with no HTTP status code.
			p.breakerFor(c.provider.ID).Record(false)
			if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, lastErr.Error()); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
		}
		slog.Info("proxy: stream failing over to next candidate",
			"last_provider", c.provider.Name,
			"last_status", lastStatus,
			"last_err", lastErr.Error())
	}

	// All candidates exhausted (or all skipped by preflight checks).
	if lastErr == nil {
		lastErr = fmt.Errorf("no available provider: all circuits open")
	}
	status := http.StatusBadGateway
	if lastStatus >= 500 {
		status = http.StatusServiceUnavailable
	}
	p.writeError(w, status, "upstream_error", lastErr.Error())
	slog.Error("proxy: stream all candidates exhausted",
		"model", logEntry.Model,
		"route", logEntry.RouteID,
		"lastStatus", lastStatus,
		"err", lastErr.Error())
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

// streamAttempt performs ONE upstream streaming HTTP attempt under a
// per-candidate first-byte timeout (Option A: single cumulative deadline
// = headers arrival + first byte read). It owns the per-attempt chain
// entry and the breaker / hit / health side effects.
//
// Outcomes:
//   - "success": 2xx received and stream completed (or broke mid-stream).
//     Committed = true. Caller computes top-level FirstTokenMs.
//   - "client_abort": request context canceled (Do error or write error).
//     Committed = true. No breaker penalty.
//   - "non_retryable": upstream 4xx hard (e.g. 400/422) OR transport
//     error classified as non-retryable. Committed = true (error
//     response already written to client).
//   - "retryable": transport error or 5xx; the caller can retry the
//     same candidate (if budget remains) or fall through.
//
// Resource safety: resp.Body.Close() is deferred immediately after a
// successful Do, so every code path releases the upstream connection.
func (p *Proxy) streamAttempt(ctx context.Context, w http.ResponseWriter, r *http.Request, c candidate, upstreamKey string, rewrittenBody []byte, upstreamURL *url.URL, attemptOrder int, inputEstimate int, logEntry *model.RequestLog) (streamAttemptResult, int) {
	// Per-candidate first-byte timeout. We use the TRANSPORT's
	// ResponseHeaderTimeout (not a context.WithTimeout on the
	// request) because http.Client monitors req.Context() for the
	// ENTIRE request lifecycle, including body reads. A context
	// deadline would therefore kill long LLM streams (o1/o3
	// reasoning, slow providers, long outputs) as soon as the
	// deadline expires — even after the first byte had already
	// arrived. ResponseHeaderTimeout, in contrast, ONLY bounds the
	// time between sending the request and receiving response
	// headers; once headers arrive, body reads are unbounded. This
	// is the correct "first-byte timeout" semantics: if the
	// upstream does not send response headers within `timeout`,
	// the attempt fails fast and we failover. Once headers arrive,
	// SSE providers send the first body chunk almost immediately,
	// so the first-byte window is effectively covered.
	timeout := c.timeout
	if timeout == 0 {
		timeout = defaultFirstByteTimeout
	}

	attemptReq := r.Clone(ctx)
	attemptReq.URL = cloneURL(upstreamURL)
	attemptReq.Host = upstreamURL.Host
	attemptReq.RequestURI = "" // outbound requests must have an empty RequestURI
	attemptReq.Body = io.NopCloser(bytes.NewReader(rewrittenBody))
	attemptReq.ContentLength = int64(len(rewrittenBody))
	attemptReq.Header = r.Header.Clone()
	attemptReq.Header.Del("Authorization")
	attemptReq.Header.Set("Authorization", "Bearer "+upstreamKey)
	attemptReq.Header.Set("X-Autoapi-Route", c.ruleID)
	attemptReq.Header.Del("Transfer-Encoding")
	if attemptReq.Header.Get("Content-Type") == "" {
		attemptReq.Header.Set("Content-Type", "application/json")
	}
	attemptReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(rewrittenBody)))

	// Per-attempt transport with the candidate-specific first-byte
	// timeout. Clone p.transport (not http.DefaultTransport) so
	// streaming inherits any future tuning on p.transport (TLS,
	// dialer, etc.) rather than only DefaultTransport's defaults.
	// The ResponseHeaderTimeout override still applies after the
	// clone.
	transport := p.transport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	client := &http.Client{Transport: transport}

	attemptStart := time.Now()
	resp, doErr := client.Do(attemptReq)

	if doErr != nil {
		// No upstream connection to release — proceed to classify.
		latencyMs := int(time.Since(attemptStart).Milliseconds())
		cat := CategorizeError(doErr, 0)
		if isTimeoutError(doErr) {
			slog.Info("proxy: stream first-byte timeout",
				"provider", c.provider.Name,
				"model", c.modelName,
				"err", doErr)
		} else {
			slog.Debug("proxy: stream attempt transport error",
				"provider", c.provider.Name,
				"model", c.modelName,
				"category", cat,
				"err", doErr)
		}
		switch cat {
		case CategoryClientAbort:
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "client_abort",
				StatusCode:   statusClientClosed,
				Error:        "client disconnected: " + doErr.Error(),
				LatencyMs:    latencyMs,
			})
			return streamAttemptResult{
				Status:     "client_abort",
				StatusCode: statusClientClosed,
				Error:      "client disconnected: " + doErr.Error(),
				LatencyMs:  latencyMs,
			}, attemptOrder
		case CategoryNonRetryable:
			p.writeError(w, http.StatusBadGateway, "upstream_error", doErr.Error())
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "non_retryable",
				StatusCode:   http.StatusBadGateway,
				Error:        doErr.Error(),
				LatencyMs:    latencyMs,
			})
			return streamAttemptResult{
				Status:     "non_retryable",
				StatusCode: http.StatusBadGateway,
				Error:      doErr.Error(),
				LatencyMs:  latencyMs,
			}, attemptOrder
		case CategoryRetryable:
			fallthrough
		default:
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "retryable",
				StatusCode:   0,
				Error:        doErr.Error(),
				LatencyMs:    latencyMs,
			})
			return streamAttemptResult{
				Status:    "retryable",
				Error:     doErr.Error(),
				LatencyMs: latencyMs,
			}, attemptOrder
		}
	}

	// We have a response. Defer Close so every path (success, fail,
	// panic) releases the upstream connection.
	defer resp.Body.Close()

	// Set provider/model fields NOW (in case of mid-stream abort or
	// panic) so the log entry always carries the chosen provider.
	logEntry.ProviderID = c.provider.ID
	logEntry.ProviderName = c.provider.Name
	logEntry.Model = c.modelName
	logEntry.RouteID = c.ruleID
	logEntry.RouteLabel = c.ruleLabel

	upstreamStatus := resp.StatusCode

	// Non-2xx: still need to surface the upstream error to the
	// client. We must read the body (typically small for an error
	// envelope) and forward it. Not a real "stream" — just one-shot
	// forwarding.
	if upstreamStatus >= 400 {
		upstreamBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			// Body read error after non-2xx headers: treat as
			// retryable transport error.
			latencyMs := int(time.Since(attemptStart).Milliseconds())
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "retryable",
				StatusCode:   upstreamStatus,
				Error:        readErr.Error(),
				LatencyMs:    latencyMs,
			})
			slog.Debug("proxy: stream non-2xx body read error",
				"provider", c.provider.Name,
				"model", c.modelName,
				"status", upstreamStatus,
				"err", readErr)
			return streamAttemptResult{
				Status:     "retryable",
				StatusCode: upstreamStatus,
				Error:      readErr.Error(),
				LatencyMs:  latencyMs,
			}, attemptOrder
		}

		cat := CategorizeError(nil, upstreamStatus)
		errStr := fmt.Sprintf("upstream %s returned status %d", c.provider.Name, upstreamStatus)
		latencyMs := int(time.Since(attemptStart).Milliseconds())
		attemptOrder++

		if cat == CategoryNonRetryable || cat == CategoryClientAbort {
			// 4xx hard stop: write upstream error to client and
			// return; no further retry/failover.
			p.writeStreamError(w, upstreamStatus, resp.Header, upstreamBody, logEntry, c, attemptOrder, latencyMs, "non_retryable", errStr)
			if isCircuitBreakerFailure(nil, upstreamStatus) {
				p.breakerFor(c.provider.ID).Record(false)
				if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, errStr); err != nil {
					slog.Error("proxy: update provider health", "err", err)
				}
			}
			return streamAttemptResult{
				Status:     "non_retryable",
				StatusCode: upstreamStatus,
				Error:      errStr,
				LatencyMs:  latencyMs,
			}, attemptOrder
		}

		// CategoryRetryable: 5xx (or 408/429 etc.). Record chain
		// entry and return so the caller can retry or fail over.
		logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
			AttemptOrder: attemptOrder,
			ProviderID:   c.provider.ID,
			ProviderName: c.provider.Name,
			ModelName:    c.modelName,
			TargetID:     c.targetID,
			Status:       "retryable",
			StatusCode:   upstreamStatus,
			Error:        errStr,
			LatencyMs:    latencyMs,
		})
		slog.Debug("proxy: stream upstream non-2xx",
			"provider", c.provider.Name,
			"model", c.modelName,
			"status", upstreamStatus,
			"category", cat)
		return streamAttemptResult{
			Status:     "retryable",
			StatusCode: upstreamStatus,
			Error:      errStr,
			LatencyMs:  latencyMs,
		}, attemptOrder
	}

	// 2xx — TRUE PASS-THROUGH STREAMING. No more buffering; chunks
	// flow upstream → client in real time.
	slog.Debug("proxy: stream upstream success",
		"provider", c.provider.Name,
		"model", c.modelName,
		"status", upstreamStatus)

	ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
	flusher, _ := ww.(http.Flusher)
	for k, vv := range resp.Header {
		for _, v := range vv {
			ww.Header().Add(k, v)
		}
	}
	ww.WriteHeader(upstreamStatus)
	if flusher != nil {
		flusher.Flush()
	}

	// TTFT is captured inline on the first body Read that returns
	// n>0. This is the single point of success: breaker Record(true),
	// hit counter, provider health all fire here. Body reads are
	// not bounded by any timeout (the per-candidate first-byte
	// window is enforced via Transport.ResponseHeaderTimeout on the
	// Do call, not on body reads), so a long LLM stream can run for
	// arbitrarily long.
	var firstByteTime time.Duration
	firstByteRecorded := false
	usageAcc := &streamUsageAccumulator{}

	buf := make([]byte, 32*1024)
	var streamErr error
	var writeErr error
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if !firstByteRecorded {
				firstByteTime = time.Since(attemptStart)
				firstByteRecorded = true
				p.recordStreamSuccess(c)
			}
			usageAcc.Feed(buf[:n])
			if _, werr := ww.Write(buf[:n]); werr != nil {
				writeErr = werr
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			streamErr = readErr
			break
		}
	}

	// End-of-stream handling.
	attemptLatencyMs := int(time.Since(attemptStart).Milliseconds())
	attemptOrder++

	// Token accounting from the SSE accumulator. Fall back to the
	// input estimate if usage was not present; output has no
	// reliable fallback (no buffered body) so it stays 0.
	input, output, cacheHit, cacheCreation := usageAcc.Usage()
	if input == 0 && output == 0 && cacheHit == 0 && cacheCreation == 0 {
		input = inputEstimate
	}
	logEntry.InputTokens = input
	logEntry.OutputTokens = output
	logEntry.CacheCreation = cacheCreation
	logEntry.CacheHit = cacheHit
	logEntry.StatusCode = upstreamStatus

	chainEntry := model.RequestLogChainEntry{
		AttemptOrder: attemptOrder,
		ProviderID:   c.provider.ID,
		ProviderName: c.provider.Name,
		ModelName:    c.modelName,
		TargetID:     c.targetID,
		Status:       "success",
		StatusCode:   upstreamStatus,
		LatencyMs:    attemptLatencyMs,
		FirstTokenMs: int(firstByteTime.Milliseconds()),
	}

	switch {
	case writeErr != nil && isClientDisconnect(writeErr):
		// Client disconnected mid-stream. Do NOT penalize provider.
		chainEntry.Status = "client_abort"
		chainEntry.Error = "client disconnected: " + writeErr.Error()
		logEntry.Chain = append(logEntry.Chain, chainEntry)
		logEntry.Error = "client disconnected: " + writeErr.Error()
		slog.Warn("proxy: stream client disconnect",
			"provider", c.provider.Name,
			"model", c.modelName,
			"err", writeErr.Error())
		return streamAttemptResult{
			Status:       "client_abort",
			StatusCode:   statusClientClosed,
			Error:        "client disconnected: " + writeErr.Error(),
			LatencyMs:    attemptLatencyMs,
			FirstTokenMs: int(firstByteTime.Milliseconds()),
			StreamErr:    writeErr,
		}, attemptOrder
	case writeErr != nil:
		// Other write error (e.g. response writer closed).
		chainEntry.Error = writeErr.Error()
		logEntry.Chain = append(logEntry.Chain, chainEntry)
		logEntry.Error = writeErr.Error()
		return streamAttemptResult{
			Status:       "success",
			StatusCode:   upstreamStatus,
			Error:        writeErr.Error(),
			LatencyMs:    attemptLatencyMs,
			FirstTokenMs: int(firstByteTime.Milliseconds()),
			StreamErr:    writeErr,
		}, attemptOrder
	case streamErr != nil:
		// If the stream already observed [DONE], the response was
		// delivered successfully. A subsequent read error (commonly
		// context.Canceled when the client closes the connection
		// right after receiving the full response) is NOT a failure
		// of any kind — treat it as a clean completion.
		if usageAcc.Done() {
			logEntry.Chain = append(logEntry.Chain, chainEntry)
			return streamAttemptResult{
				Status:       "success",
				StatusCode:   upstreamStatus,
				LatencyMs:    attemptLatencyMs,
				FirstTokenMs: int(firstByteTime.Milliseconds()),
			}, attemptOrder
		}
		// Mid-stream read error BEFORE [DONE]. Distinguish upstream
		// failure from client disconnect: a client closing the
		// connection kills resp.Body.Read via context cancellation,
		// but that is NOT a provider failure and must not penalize
		// the breaker.
		if !isClientDisconnect(streamErr) {
			p.breakerFor(c.provider.ID).Record(false)
			if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, streamErr.Error()); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
		}
		if isClientDisconnect(streamErr) {
			chainEntry.Status = "client_abort"
			chainEntry.Error = "client disconnected: " + streamErr.Error()
			slog.Warn("proxy: stream client disconnect (read path)",
				"provider", c.provider.Name,
				"model", c.modelName,
				"err", streamErr.Error())
		} else {
			chainEntry.Error = streamErr.Error()
		}
		logEntry.Chain = append(logEntry.Chain, chainEntry)
		logEntry.Error = chainEntry.Error
		return streamAttemptResult{
			Status:       "success",
			StatusCode:   upstreamStatus,
			Error:        chainEntry.Error,
			LatencyMs:    attemptLatencyMs,
			FirstTokenMs: int(firstByteTime.Milliseconds()),
			StreamErr:    streamErr,
		}, attemptOrder
	default:
		// Clean EOF. If [DONE] was not seen, this is a mid-stream
		// failure and the provider misbehaved.
		if !usageAcc.Done() {
			p.breakerFor(c.provider.ID).Record(false)
			if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, "stream closed without [DONE]"); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
		}
		logEntry.Chain = append(logEntry.Chain, chainEntry)
		return streamAttemptResult{
			Status:       "success",
			StatusCode:   upstreamStatus,
			LatencyMs:    attemptLatencyMs,
			FirstTokenMs: int(firstByteTime.Milliseconds()),
		}, attemptOrder
	}
}

// recordStreamSuccess records a successful streaming attempt: hit
// counter, breaker success, provider health.
func (p *Proxy) recordStreamSuccess(c candidate) {
	p.breakerFor(c.provider.ID).Record(true)
	if c.targetID != "" {
		if err := p.store.IncrementTargetStats(c.targetID, 1, 0); err != nil {
			slog.Error("proxy: increment target hit count (stream)", "err", err)
		}
	}
	if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusConnected, ""); err != nil {
		slog.Error("proxy: update provider health", "err", err)
	}
}

// writeStreamError writes an upstream non-2xx response back to the
// client, preserving headers, and appends a chain entry.
func (p *Proxy) writeStreamError(w http.ResponseWriter, upstreamStatus int, upstreamHeader http.Header, upstreamBody []byte, logEntry *model.RequestLog, c candidate, attemptOrder int, latencyMs int, status string, errStr string) {
	for k, vv := range upstreamHeader {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(upstreamStatus)
	if len(upstreamBody) > 0 {
		_, _ = w.Write(upstreamBody)
	} else {
		// Fall back to a generic OpenAI-style error envelope so the
		// client always sees a parseable body.
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": errStr,
				"type":    "upstream_error",
			},
		})
	}
	logEntry.StatusCode = upstreamStatus
	logEntry.Error = errStr
	logEntry.ProviderID = c.provider.ID
	logEntry.ProviderName = c.provider.Name
	logEntry.Model = c.modelName
	logEntry.RouteID = c.ruleID
	logEntry.RouteLabel = c.ruleLabel
	logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
		AttemptOrder: attemptOrder,
		ProviderID:   c.provider.ID,
		ProviderName: c.provider.Name,
		ModelName:    c.modelName,
		TargetID:     c.targetID,
		Status:       status,
		StatusCode:   upstreamStatus,
		Error:        errStr,
		LatencyMs:    latencyMs,
	})
}

// cloneURL returns a deep copy of u. Needed because the request and the
// upstreamURL share nested fields, and http.Client.Do mutates the
// request's URL in place when the Director runs. Using a fresh copy per
// attempt keeps each retry's URL independent.
func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	c := *u
	if u.User != nil {
		userCopy := *u.User
		c.User = &userCopy
	}
	return &c
}

// isTimeoutError reports whether err represents an upstream header or
// request timeout — used to decide whether to log at Info level
// (timeouts are operationally interesting; transient network errors
// stay at Debug).
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
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

func (bp *bufferPool) Get() []byte  { return bp.pool.Get().([]byte) }
func (bp *bufferPool) Put(b []byte) { bp.pool.Put(b) }

// streamAttemptResult is the outcome of a single streaming HTTP attempt.
// Status describes the categorical outcome (mirrors the chain entry
// statuses) and the other fields are the values forwardStream needs to
// build the top-level log entry, decide retry vs. failover, and compute
// cumulative latency.
type streamAttemptResult struct {
	Status       string // "success", "retryable", "non_retryable", "client_abort"
	StatusCode   int
	Error        string
	LatencyMs    int // attempt wall-clock (for retryable entries; sum from caller)
	FirstTokenMs int // success only: time from attemptStart to first body byte

	// StreamErr is non-nil when the upstream body broke mid-stream (or
	// the client disconnected) after at least one byte was committed.
	// forwardStream uses it to distinguish client-side aborts from
	// provider-side failures.
	StreamErr error
}

// firstByteTrackingReadCloser removed: TTFT is now captured inline in
// streamAttempt. The old wrapper buffered nothing, but with the
// pass-through body Read it is no longer needed at all.

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

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
			// Share the proxy's transport so the upstream header timeout
			// applies to non-streaming retries too (otherwise a hung
			// upstream would block a retryable attempt until the client
			// gave up).
			proxy.Transport = p.transport

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
// committing the first byte to the client: each upstream attempt is run
// through the proxy's transport with the configured ResponseHeaderTimeout,
// and only when a 2xx response is received do we copy headers and start
// streaming the body. This is safe because the client has not seen any
// response yet, so we can still abandon the attempt and try the next
// target.
//
// Real-time chunks are flushed to the client as they arrive
// (http.Flusher.Flush after every Write), preserving SSE compatibility.
// Time to first token/byte (TTFT) is captured by wrapping resp.Body in
// firstByteTrackingReadCloser, which is a pure passthrough wrapper —
// no buffering — so it does not affect real-time flushing.
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

	// httpStreamClient is created once and reused. The shared transport
	// carries the ResponseHeaderTimeout (see upstreamResponseHeaderTimeout)
	// so a hung upstream fails the attempt quickly, leaving room for a
	// failover to the next candidate.
	client := &http.Client{Transport: p.transport}

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

		// Per-target retry loop. Each iteration is a fresh
		// http.Client.Do call (against the same upstreamURL with the same
		// body). We retry only on CategoryRetryable and only up to
		// c.maxRetries additional attempts, so a target with MaxRetries=0
		// gets exactly one attempt.
		var succeeded bool
		var finalCat ErrorCategory
		var finalAttemptErr error
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

			attemptReq := r.Clone(r.Context())
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

			attemptStart := time.Now()
			resp, doErr := client.Do(attemptReq)
			latencyMs := int(time.Since(attemptStart).Milliseconds())

			if doErr != nil {
				// Network / transport error before any headers arrived.
				finalCat = CategorizeError(doErr, 0)
				finalAttemptErr = doErr
				if isTimeoutError(doErr) {
					slog.Info("proxy: stream upstream header timeout",
						"provider", c.provider.Name,
						"model", c.modelName,
						"attempt", attempt,
						"err", doErr)
				} else {
					slog.Debug("proxy: stream attempt transport error",
						"provider", c.provider.Name,
						"model", c.modelName,
						"attempt", attempt,
						"category", finalCat,
						"err", doErr)
				}
				switch finalCat {
				case CategoryClientAbort:
					// The client canceled mid-request. Do not penalize
					// the provider; return without writing anything to
					// the client (its connection is gone).
					logEntry.StatusCode = statusClientClosed
					logEntry.Error = "client disconnected: " + doErr.Error()
					attemptOrder++
					logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
						AttemptOrder: attemptOrder,
						ProviderID:   c.provider.ID,
						ProviderName: c.provider.Name,
						ModelName:    c.modelName,
						TargetID:     c.targetID,
						Status:       "client_abort",
						StatusCode:   statusClientClosed,
						Error:        logEntry.Error,
						LatencyMs:    latencyMs,
					})
					return
				case CategoryNonRetryable:
					// Should be rare for transport errors, but handle it
					// for completeness.
					p.writeError(w, http.StatusBadGateway, "upstream_error", doErr.Error())
					logEntry.StatusCode = http.StatusBadGateway
					logEntry.Error = doErr.Error()
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
						StatusCode:   http.StatusBadGateway,
						Error:        doErr.Error(),
						LatencyMs:    latencyMs,
					})
					return
				case CategoryRetryable:
					lastErr = doErr
					lastStatus = 0
					if c.targetID != "" {
						if err := p.store.IncrementTargetStats(c.targetID, 0, 1); err != nil {
							slog.Error("proxy: increment target failure count (stream)", "err", err)
						}
					}
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
					slog.Debug("proxy: stream retrying same target",
						"provider", c.provider.Name,
						"model", c.modelName,
						"attempt", attempt,
						"maxRetries", c.maxRetries)
					continue
				}
			}

			// We have a response with headers. Read the body up front so
			// we can decide between streaming (2xx) and short-circuit
			// (non-2xx) before writing anything to the client.
			upstreamStatus := resp.StatusCode
			respBody, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				// Body read failed after headers. Treat as a transport
				// error: classify and decide retry vs. fail.
				finalCat = CategorizeError(readErr, upstreamStatus)
				finalAttemptErr = readErr
				lastErr = readErr
				lastStatus = upstreamStatus
				if c.targetID != "" {
					if err := p.store.IncrementTargetStats(c.targetID, 0, 1); err != nil {
						slog.Error("proxy: increment target failure count (stream)", "err", err)
					}
				}
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
				slog.Debug("proxy: stream body read error",
					"provider", c.provider.Name,
					"model", c.modelName,
					"attempt", attempt,
					"status", upstreamStatus,
					"err", readErr)
				continue
			}

			// 2xx: stream the body to the client with real-time
			// flushing. After this point, no further retry is possible:
			// the client has already seen the upstream status.
			if upstreamStatus < 400 {
				slog.Debug("proxy: stream upstream success",
					"provider", c.provider.Name,
					"model", c.modelName,
					"attempt", attempt,
					"status", upstreamStatus)
				streamErr := p.writeStreamSuccess(w, r, c, resp, respBody, inputEstimate, logEntry)

				// Capture chain entry for the success path. Latency for
				// the chain entry covers the full attempt including
				// streaming the body to the client, since that's what
				// the user actually waited for.
				attemptLatencyMs := int(time.Since(attemptStart).Milliseconds())
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "success",
					StatusCode:   upstreamStatus,
					Error:        "",
					LatencyMs:    attemptLatencyMs,
				})
				if streamErr != nil && isClientDisconnect(streamErr) {
					// Mid-stream client abort: do not penalize the
					// provider, mark the request closed by the client.
					logEntry.StatusCode = statusClientClosed
					logEntry.Error = "client disconnected: " + streamErr.Error()
					slog.Warn("proxy: stream client disconnect",
						"provider", c.provider.Name,
						"model", c.modelName,
						"err", streamErr.Error())
				}
				succeeded = true
				return
			}

			// Non-2xx upstream: classify the response and decide whether
			// to retry on the same target, fall through to the next
			// candidate, or fail immediately to the client.
			cat := CategorizeError(nil, upstreamStatus)
			finalCat = cat
			finalAttemptErr = nil
			lastErr = fmt.Errorf("upstream %s returned status %d", c.provider.Name, upstreamStatus)
			lastStatus = upstreamStatus
			slog.Debug("proxy: stream upstream non-2xx",
				"provider", c.provider.Name,
				"model", c.modelName,
				"attempt", attempt,
				"status", upstreamStatus,
				"category", cat)

			if cat == CategoryNonRetryable || cat == CategoryClientAbort {
				// Non-retryable (4xx hard) or client abort. Write the
				// upstream error verbatim to the client and return.
				p.writeStreamError(w, upstreamStatus, resp.Header, respBody, logEntry, c, attemptOrder+1, latencyMs, "non_retryable", lastErr.Error())
				if isCircuitBreakerFailure(nil, upstreamStatus) {
					p.breakerFor(c.provider.ID).Record(false)
					if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, lastErr.Error()); err != nil {
						slog.Error("proxy: update provider health", "err", err)
					}
				}
				return
			}

			// CategoryRetryable: record the failed attempt and loop
			// for another try (or fall through to the next candidate if
			// we've exhausted maxRetries).
			if c.targetID != "" {
				if err := p.store.IncrementTargetStats(c.targetID, 0, 1); err != nil {
					slog.Error("proxy: increment target failure count (stream)", "err", err)
				}
			}
			attemptOrder++
			logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
				AttemptOrder: attemptOrder,
				ProviderID:   c.provider.ID,
				ProviderName: c.provider.Name,
				ModelName:    c.modelName,
				TargetID:     c.targetID,
				Status:       "retryable",
				StatusCode:   upstreamStatus,
				Error:        lastErr.Error(),
				LatencyMs:    latencyMs,
			})
			slog.Debug("proxy: stream retrying same target",
				"provider", c.provider.Name,
				"model", c.modelName,
				"attempt", attempt,
				"maxRetries", c.maxRetries)
		}

		if succeeded {
			return
		}

		// Exhausted retries on this candidate (or breaker opened
		// mid-retry). Record breaker once on the final outcome, then
		// fall through to the next candidate.
		if finalCat == CategoryRetryable && isCircuitBreakerFailure(finalAttemptErr, lastStatus) {
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

// writeStreamSuccess copies the upstream response headers + body to the
// client with real-time flushing. It returns the read error from the
// streaming copy, if any, so the caller can decide whether to penalize
// the provider (a network mid-stream error) or treat the abort as a
// client-side disconnect.
//
// The function also drives the TTFT timer, the success breaker/hit
// counters, and the logEntry status / provider fields so the caller's
// post-ServeHTTP bookkeeping is minimal.
func (p *Proxy) writeStreamSuccess(w http.ResponseWriter, r *http.Request, c candidate, resp *http.Response, respBody []byte, inputEstimate int, logEntry *model.RequestLog) error {
	// Set provider/model fields NOW (not just after streaming) so they
	// are present even if streaming panics or the client disconnects
	// mid-stream before the function returns.
	logEntry.ProviderID = c.provider.ID
	logEntry.ProviderName = c.provider.Name
	logEntry.Model = c.modelName
	logEntry.RouteID = c.ruleID
	logEntry.RouteLabel = c.ruleLabel
	logEntry.StatusCode = resp.StatusCode

	ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
	flusher, _ := ww.(http.Flusher)

	for k, vv := range resp.Header {
		for _, v := range vv {
			ww.Header().Add(k, v)
		}
	}
	ww.WriteHeader(resp.StatusCode)
	if flusher != nil {
		flusher.Flush()
	}

	// Wrap the body so the first successful Read records TTFT.
	// firstByteTrackingReadCloser is a pure passthrough — it does not
	// buffer — so Flush-after-each-Write below preserves SSE real-time
	// flushing.
	body := respBody
	tracker := &firstByteTrackingReadCloser{
		inner: io.NopCloser(bytes.NewReader(body)),
		onStart: func() {
			logEntry.FirstTokenMs = int(time.Now().UnixMilli() - logEntry.Timestamp)
		},
	}
	// Fire onStart eagerly if the body is empty: the streaming contract
	// says onStart fires on first data or EOF, and io.Read on a
	// zero-length buffer returns (0, io.EOF) immediately.
	if len(body) == 0 {
		tracker.onStart()
	} else {
		// Read once to drive the TTFT tracker for the first chunk.
		buf := make([]byte, 32*1024)
		n, readErr := tracker.Read(buf)
		if n > 0 {
			if _, werr := ww.Write(buf[:n]); werr != nil {
				_ = tracker.Close()
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			logEntry.OutputTokens = inputEstimate
			p.recordStreamSuccess(c)
			return nil
		}
		if readErr != nil {
			_ = tracker.Close()
			logEntry.OutputTokens = inputEstimate
			p.recordStreamSuccess(c)
			return readErr
		}
		// Continue draining the rest of the body with the same buffer.
		for {
			n, readErr = tracker.Read(buf)
			if n > 0 {
				if _, werr := ww.Write(buf[:n]); werr != nil {
					_ = tracker.Close()
					return werr
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				_ = tracker.Close()
				logEntry.Error = readErr.Error()
				// Mid-stream upstream error: partial response has
				// already been written; the client may see a
				// truncated stream. Penalize the provider because
				// the upstream misbehaved.
				if isCircuitBreakerFailure(readErr, resp.StatusCode) {
					p.breakerFor(c.provider.ID).Record(false)
				}
				return readErr
			}
		}
		_ = tracker.Close()
	}

	// Token accounting: try to parse SSE usage from the full buffered
	// body. Falls back to a coarse heuristic if the upstream omitted
	// usage information.
	if it, ot := parseStreamUsage(body); it > 0 || ot > 0 {
		logEntry.InputTokens, logEntry.OutputTokens = it, ot
	} else {
		logEntry.InputTokens = inputEstimate
		logEntry.OutputTokens = len(body) / 4
	}
	p.recordStreamSuccess(c)
	return nil
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

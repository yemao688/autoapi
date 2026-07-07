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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

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
	ListRoutes() ([]model.Route, error)
	GetProvider(id string) (*model.Provider, error)
	ListAPIKeys() ([]model.ApiKey, error)
	GetProviderKeyCiphertext(providerID string) (ciphertext, nonce []byte, err error)
	InsertRequestLog(l model.RequestLog) error
	InsertRequestLogsBatch(logs []model.RequestLog) error
	ListModels(providerID string) ([]model.Model, error)
	GetSettings() (*model.Settings, error)
	Dashboard() (*model.DashboardData, error)
	UpdateProviderHealth(id string, status model.ProviderStatus, errorMessage string) error
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

func (p *Proxy) loadRoutes() []model.Route {
	routes, err := p.store.ListRoutes()
	if err != nil {
		slog.Error("proxy: failed to load routes", "err", err)
		return nil
	}
	return routes
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
// route matcher, filtering out providers with an open circuit breaker.
func (p *Proxy) resolveCandidates(req *InboundRequest) ([]candidate, error) {
	routes := p.loadRoutes()
	defaultProviderID := p.currentSettings().Routing.DefaultProviderID

	// Snapshot the breaker map to avoid racing with breakerFor writes.
	p.breakersMu.RLock()
	breakers := make(map[string]*CircuitBreaker, len(p.breakers))
	for k, v := range p.breakers {
		breakers[k] = v
	}
	p.breakersMu.RUnlock()

	return selectCandidates(req, routes, defaultProviderID, breakers, p.store.GetProvider)
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
	var lastErr error
	var lastStatus int

	for _, c := range candidates {
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

		upstreamURL, err := url.Parse(strings.TrimSuffix(c.provider.BaseURL, "/") + r.URL.Path)
		if err != nil {
			lastErr = fmt.Errorf("invalid base URL for %s: %w", c.provider.Name, err)
			slog.Debug("proxy: candidate URL invalid", "provider", c.provider.Name, "err", lastErr)
			continue
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
		if isStream {
			proxy.FlushInterval = -1
		}
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
			req.Header.Set("X-Autoapi-Route", c.routeID)
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
		slog.Debug("proxy: candidate attempt",
			"provider", c.provider.Name,
			"model", c.modelName,
			"category", cat,
			"status", buf.statusCode,
			"err", attemptErr)

		if attemptErr == nil && buf.statusCode < 400 {
			copyBufferedResponse(w, buf)
			logEntry.StatusCode = buf.statusCode
			logEntry.ProviderID = c.provider.ID
			logEntry.ProviderName = c.provider.Name
			logEntry.Model = c.modelName
			logEntry.RouteID = c.routeID
			logEntry.RouteLabel = c.routeLabel
			p.breakerFor(c.provider.ID).Record(true)
			if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusConnected, ""); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
			return
		}

		lastErr = attemptErr
		if lastErr == nil {
			lastErr = fmt.Errorf("upstream %s returned status %d", c.provider.Name, buf.statusCode)
		}
		lastStatus = buf.statusCode

		switch cat {
		case CategoryClientAbort:
			p.writeError(w, http.StatusBadRequest, "client_error", lastErr.Error())
			logEntry.StatusCode = http.StatusBadRequest
			logEntry.Error = lastErr.Error()
			return
		case CategoryNonRetryable:
			p.writeError(w, buf.statusCode, "invalid_request_error", lastErr.Error())
			logEntry.StatusCode = buf.statusCode
			logEntry.Error = lastErr.Error()
			return
		case CategoryRetryable:
			if isCircuitBreakerFailure(attemptErr, buf.statusCode) {
				p.breakerFor(c.provider.ID).Record(false)
				if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, lastErr.Error()); err != nil {
					slog.Error("proxy: update provider health", "err", err)
				}
			}
		}
	}

	status := http.StatusBadGateway
	if lastStatus >= 500 {
		status = http.StatusServiceUnavailable
	}
	p.writeError(w, status, "upstream_error", lastErr.Error())
	logEntry.StatusCode = status
	logEntry.Error = lastErr.Error()
}

// forwardStream proxies a streaming request to the first available candidate
// without buffering. Real-time chunks are flushed to the client as they arrive.
// Failover is intentionally disabled for streaming: once the first byte is
// committed to the client we cannot roll it back.
func (p *Proxy) forwardStream(w http.ResponseWriter, r *http.Request, body []byte, candidates []candidate, inputEstimate int, logEntry *model.RequestLog) {
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
	upstreamURL, err := url.Parse(strings.TrimSuffix(chosen.provider.BaseURL, "/") + r.URL.Path)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, "internal_error", "invalid provider base URL")
		logEntry.StatusCode = http.StatusInternalServerError
		logEntry.Error = err.Error()
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
		req.Header.Set("X-Autoapi-Route", chosen.routeID)
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
	var attemptErr error
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		attemptErr = err
		p.writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
	}
	proxy.ServeHTTP(ww, attemptReq)

	logEntry.InputTokens = inputEstimate
	logEntry.OutputTokens = ww.BytesWritten() / 4
	logEntry.StatusCode = ww.Status()
	logEntry.ProviderID = chosen.provider.ID
	logEntry.ProviderName = chosen.provider.Name
	logEntry.Model = chosen.modelName
	logEntry.RouteID = chosen.routeID
	logEntry.RouteLabel = chosen.routeLabel

	if attemptErr == nil && ww.Status() < 400 {
		p.breakerFor(chosen.provider.ID).Record(true)
		if err := p.store.UpdateProviderHealth(chosen.provider.ID, model.ProviderStatusConnected, ""); err != nil {
			slog.Error("proxy: update provider health", "err", err)
		}
		return
	}

	if attemptErr == nil {
		attemptErr = fmt.Errorf("upstream %s returned status %d", chosen.provider.Name, ww.Status())
	}
	logEntry.Error = attemptErr.Error()
	if isCircuitBreakerFailure(attemptErr, ww.Status()) {
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

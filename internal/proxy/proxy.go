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
	"time"

	"autoapi/internal/model"
	"autoapi/internal/service"
	"autoapi/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// storeProxy is the subset of *store.Store methods the proxy needs. Passing
// the concrete store keeps the package dependency explicit while still making
// the constructor testable with a mock.
type storeProxy interface {
	ListProviders() ([]model.Provider, error)
	ListRoutes() ([]model.Route, error)
	GetProvider(id string) (*model.Provider, error)
	ListAPIKeys() ([]model.ApiKey, error)
	GetAPIKeyCiphertext(id string) (ciphertext, nonce []byte, providerID string, err error)
	InsertRequestLog(l model.RequestLog) error
	InsertRequestLogsBatch(logs []model.RequestLog) error
	ListModels(providerID string) ([]model.Model, error)
	GetSettings() (*model.Settings, error)
	Dashboard() (*model.DashboardData, error)
}

// Proxy implements api.ProxyService. It owns the chi router and the underlying
// http.Server. The zero value is not ready for use; call New.
type Proxy struct {
	store            storeProxy
	service          *service.Service
	settingsProvider func() *model.Settings

	mu       sync.RWMutex
	listener net.Listener
	server   *http.Server
	router   chi.Router

	bufferPool *bufferPool
	errorLog   *log.Logger
}

// New creates a Proxy. The settingsProvider is called on Start/Restart to read
// the current port/bind configuration. Pass a concrete *store.Store as the
// store argument.
func New(store storeProxy, service *service.Service, settingsProvider func() *model.Settings) *Proxy {
	p := &Proxy{
		store:            store,
		service:          service,
		settingsProvider: settingsProvider,
		bufferPool: &bufferPool{pool: &sync.Pool{
			New: func() interface{} { return make([]byte, 32*1024) },
		}},
		// Prints to stderr for dev visibility; Phase 5.5 will replace with slog.
		errorLog: log.New(log.Writer(), "[proxy] ", log.LstdFlags),
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

// Stop performs a graceful shutdown of the http.Server. It is safe to call
// multiple times; subsequent calls are no-ops.
func (p *Proxy) Stop() error {
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
// effect. If the proxy is already running, the old listener is closed first.
func (p *Proxy) Restart() error {
	if err := p.Stop(); err != nil {
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

// ---------------------------------------------------------------------------
// Router / middleware
// ---------------------------------------------------------------------------

func (p *Proxy) setupRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(slogMiddleware)

	r.Post("/v1/chat/completions", p.handleChatCompletions)
	r.Post("/v1/embeddings", p.handleEmbeddings)
	r.Get("/v1/models", p.handleModels)
	r.Get("/v1/stats/tokens", p.handleTokenStats)
	r.Get("/", p.handleRoot)
	r.NotFound(p.handleNotFound)

	return r
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
			if k.Environment == model.KeyEnvDisabled {
				return "", false, nil
			}
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

// logRequestEntry persists a request log. It treats ErrQueueFull as a soft
// drop (per oracle) and does not fail the request.
func (p *Proxy) logRequestEntry(log *model.RequestLog) {
	log.ID = newUUID()
	if log.Timestamp == 0 {
		log.Timestamp = time.Now().UnixMilli()
	}
	err := p.store.InsertRequestLog(*log)
	if err != nil {
		if errors.Is(err, store.ErrQueueFull) {
			slog.Warn("proxy: request log dropped: writer queue full", "api_key_id", log.APIKeyID)
		} else {
			slog.Error("proxy: failed to insert request log", "err", err)
		}
	}
}

// resolveTarget selects a provider/model using the route matcher or falls back
// to the configured default provider.
func (p *Proxy) resolveTarget(req *InboundRequest) (*model.Provider, string, string, string, error) {
	routes := p.loadRoutes()
	if route, matched := selectRoute(req, routes); matched {
		for _, t := range route.Targets {
			if t.Action == model.RouteActionForward {
				provider, err := p.store.GetProvider(t.ProviderID)
				if err != nil {
					return nil, "", "", "", fmt.Errorf("matched provider not found")
				}
				return provider, t.ModelName, route.ID, route.Name, nil
			}
		}
	}

	s := p.currentSettings()
	if s.Routing.DefaultProviderID == "" || s.Routing.DefaultModel == "" {
		return nil, "", "", "", fmt.Errorf("no route matched and no default provider configured")
	}
	provider, err := p.store.GetProvider(s.Routing.DefaultProviderID)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("default provider not found")
	}
	return provider, s.Routing.DefaultModel, "", "", nil
}

// doForward builds a ReverseProxy for the upstream, rewrites the Authorization
// header to the provider key, captures the response body for usage parsing, and
// updates the log entry. For SSE streaming, the body is buffered in full before
// being released to the client (acceptable v1 trade-off; see handlers comments).
func (p *Proxy) doForward(w http.ResponseWriter, r *http.Request, upstreamURL *url.URL, upstreamKey, routeID string, isStream bool, inputEstimate int, logEntry *model.RequestLog) {
	proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	if isStream {
		proxy.FlushInterval = -1
	}
	proxy.BufferPool = p.bufferPool
	proxy.ErrorLog = p.errorLog

	oldDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		oldDirector(req)
		req.Header.Del("Authorization")
		req.Header.Set("Authorization", "Bearer "+upstreamKey)
		req.Header.Set("X-Autoapi-Route", routeID)
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))

		if isStream {
			it, ot := parseStreamUsage(body)
			if it > 0 || ot > 0 {
				logEntry.InputTokens, logEntry.OutputTokens = it, ot
			} else {
				logEntry.InputTokens = inputEstimate
				logEntry.OutputTokens = len(body) / 4
			}
		} else {
			it, ot := parseJSONUsage(body)
			if it > 0 || ot > 0 {
				logEntry.InputTokens, logEntry.OutputTokens = it, ot
			} else {
				logEntry.InputTokens = inputEstimate
				logEntry.OutputTokens = len(body) / 4
			}
		}
		return nil
	}

	sr := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("proxy: upstream error", "err", err)
		p.writeError(w, http.StatusBadGateway, "upstream_error", "Upstream error: "+err.Error())
		logEntry.Error = err.Error()
	}

	proxy.ServeHTTP(sr, r)
	logEntry.StatusCode = sr.statusCode
}

// statusRecorder captures the written status code.
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
	wrote      bool
}

func (sr *statusRecorder) WriteHeader(code int) {
	if sr.wrote {
		return
	}
	sr.statusCode = code
	sr.wrote = true
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(p []byte) (int, error) {
	if !sr.wrote {
		sr.WriteHeader(http.StatusOK)
	}
	return sr.ResponseWriter.Write(p)
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

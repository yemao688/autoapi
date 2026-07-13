// Package proxy implements the local OpenAI-compatible HTTP gateway for
// autoapi. External clients (OpenAI SDK, curl, etc.) connect to the chi router
// on 0.0.0.0:8344 (default). The proxy authenticates requests using autoapi
// key IDs, evaluates routing rules, decrypts the upstream provider key, and
// forwards the request via httputil.ReverseProxy.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"autoapi/internal/metrics"
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

// maxTotalAttempts caps the total number of upstream attempts per inbound
// request across all candidates and retries. Prevents a misbehaving
// upstream (fast 500s) from causing N×(M+1) billable calls when many
// targets each have a high per-target MaxRetries value.
const maxTotalAttempts = 8

// retryBackoff returns the delay before the n-th retry (n=1,2,3,...).
// Exponential: base * 2^(n-1), capped at maxDelay, with up to +25% jitter
// to de-sync parallel retries. n=0 returns 0 so the helper is safe to
// call before every attempt.
func retryBackoff(n int) time.Duration {
	const (
		base     = 200 * time.Millisecond
		maxDelay = 2 * time.Second
	)
	if n <= 0 {
		return 0
	}
	// 2^(n-1): n=1→1, n=2→2, n=3→4, n=4→8. Guard against n large enough
	// to overflow uint (extremely unlikely but defensive).
	shift := n - 1
	if shift > 30 {
		shift = 30
	}
	d := base * time.Duration(1<<uint(shift))
	if d > maxDelay {
		d = maxDelay
	}
	// Add up to +25% jitter so concurrent retries don't synchronize.
	jitter := time.Duration(mathrand.Int63n(int64(d) / 4))
	return d + jitter
}

// parseRetryAfter parses an HTTP Retry-After header value into a duration.
// Supports both delta-seconds ("120") and HTTP-date (RFC1123) formats.
// Returns 0 if the value is missing, malformed, negative, or otherwise
// unusable. The caller is responsible for capping the returned duration
// against its own budget — a hostile Retry-After: 86400 must never pin
// the request.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	// Delta-seconds form (most common in practice).
	if n, err := strconv.Atoi(v); err == nil {
		if n <= 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	// HTTP-date form (RFC1123, RFC850, asctime — http.ParseTime handles
	// all three). A date in the past returns 0.
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 0
		}
		return d
	}
	return 0
}

// budgetDeadline returns the absolute deadline of a context derived from
// context.WithTimeout, or the zero time.Time if the context has no
// deadline. Used by the backoff-sleep path to clamp a delay to the
// remaining wall-clock budget. Safe on a plain context (returns zero;
// caller must treat that as "no budget").
func budgetDeadline(ctx context.Context) time.Time {
	if d, ok := ctx.Deadline(); ok {
		return d
	}
	return time.Time{}
}

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
	UpdateRequestLogsBatch(logs []model.RequestLog) error
	ListModels(providerID string) ([]model.Model, error)
	GetModel(providerID, name string) (*model.Model, error)
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
	defaultPort      int
	settingsProvider func() (*model.Settings, error)

	lifecycleMu    sync.Mutex
	mu             sync.RWMutex
	listener       net.Listener
	server         *http.Server
	activeSettings model.ServerSettings
	router         chi.Router
	listen         func(network, address string) (net.Listener, error)

	bufferPool  *bufferPool
	errorLog    *log.Logger
	activeConns atomic.Int32
	writer      *logWriter
	metricSink  metricSink

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

// BreakerStatuses returns a detached snapshot and never claims a half-open probe.
func (p *Proxy) BreakerStatuses() map[string]BreakerStatus {
	p.breakersMu.RLock()
	defer p.breakersMu.RUnlock()
	out := make(map[string]BreakerStatus, len(p.breakers))
	for id, cb := range p.breakers {
		out[id] = BreakerStatus{State: cb.CurrentState()}
	}
	return out
}

// New creates a Proxy. The settingsProvider is called on Start/Restart to read
// the current port/bind configuration. Pass a concrete *store.Store as the
// store argument.
func New(store storeProxy, service upstreamKeyProvider, defaultPort int, settingsProvider func() (*model.Settings, error), registries ...*metrics.Registry) *Proxy {
	if defaultPort == 0 {
		defaultPort = 8344
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = upstreamResponseHeaderTimeout
	p := &Proxy{
		store:            store,
		service:          service,
		defaultPort:      defaultPort,
		settingsProvider: settingsProvider,
		bufferPool: &bufferPool{pool: &sync.Pool{
			New: func() interface{} { return make([]byte, 32*1024) },
		}},
		// Route httputil.ReverseProxy error logging through slog.
		errorLog:  slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
		transport: transport,
		breakers:  make(map[string]*CircuitBreaker),
		writer:    newLogWriter(store),
		listen:    net.Listen,
	}
	if len(registries) > 0 && registries[0] != nil {
		p.metricSink = registries[0]
	}
	p.router = p.setupRouter()
	return p
}

// Start opens the TCP listener and begins serving the chi router in a
// goroutine. It is safe to call multiple times; subsequent calls are no-ops.
func (p *Proxy) Start() error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()

	settings, err := p.currentSettings()
	if err != nil {
		return err
	}
	return p.startWithSettingsLocked(settings)
}

func (p *Proxy) startWithSettingsLocked(settings *model.Settings) error {
	p.mu.RLock()
	if p.listener != nil {
		p.mu.RUnlock()
		return nil
	}
	p.mu.RUnlock()

	ln, server, err := p.newServer(settings)
	if err != nil {
		return err
	}

	p.installServer(ln, server)
	p.serve(server, ln)
	slog.Info("proxy started", "addr", ln.Addr().String())
	return nil
}

func (p *Proxy) newServer(settings *model.Settings) (net.Listener, *http.Server, error) {
	addr := net.JoinHostPort(settings.Server.BindAddress, strconv.Itoa(settings.Server.Port))
	ln, err := p.listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("proxy: listen %s: %w", addr, err)
	}
	return ln, &http.Server{Handler: p.router}, nil
}

func (p *Proxy) serve(server *http.Server, listener net.Listener) {
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			slog.Error("proxy server exited", "err", err)
		}
	}()
}

type serverInstance struct {
	listener net.Listener
	server   *http.Server
}

// Stop performs a graceful shutdown of the HTTP listener/server only. It does
// NOT stop the log writer, so request logging remains available across
// Stop/Start cycles. Use Shutdown for final teardown.
func (p *Proxy) Stop() error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()

	slog.Info("proxy: stopping")
	instance := p.detachServer()
	if err := shutdownServer(instance); err != nil {
		return err
	}
	return nil
}

// Shutdown performs a graceful shutdown of the HTTP listener/server and stops
// the log writer, flushing any pending logs. This is intended for final app
// teardown only. The log writer is always stopped even if HTTP shutdown fails.
func (p *Proxy) Shutdown() error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()

	slog.Info("proxy: shutting down")
	instance := p.detachServer()
	httpErr := shutdownServer(instance)
	if p.transport != nil {
		if t, ok := p.transport.(*http.Transport); ok {
			t.CloseIdleConnections()
		}
	}
	if p.writer != nil {
		p.writer.Stop()
	}
	return httpErr
}

func (p *Proxy) detachServer() serverInstance {
	p.mu.Lock()
	instance := serverInstance{listener: p.listener, server: p.server}
	p.server = nil
	p.listener = nil
	p.activeSettings = model.ServerSettings{}
	p.mu.Unlock()
	return instance
}

func (p *Proxy) installServer(listener net.Listener, server *http.Server) {
	p.mu.Lock()
	p.listener = listener
	p.server = server
	p.activeSettings = serverSettingsForListener(listener)
	p.mu.Unlock()
}

func shutdownServer(instance serverInstance) error {
	if instance.server == nil {
		return nil
	}
	if instance.listener != nil {
		_ = instance.listener.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := instance.server.Shutdown(ctx); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.Error("proxy: shutdown failed", "err", err)
		return err
	}
	return nil
}

// Restart atomically switches to the requested listener. The old server stays
// live until the new address has been acquired and installed.
func (p *Proxy) Restart() error {
	p.lifecycleMu.Lock()
	defer p.lifecycleMu.Unlock()

	slog.Info("proxy: restarting")
	settings, err := p.currentSettings()
	if err != nil {
		return err
	}

	p.mu.RLock()
	if p.listener != nil && listenerMatchesSettings(p.listener, settings.Server) {
		p.mu.RUnlock()
		return nil
	}
	oldInstance := serverInstance{listener: p.listener, server: p.server}
	oldSettings := p.activeSettings
	wasRunning := p.listener != nil && p.server != nil
	p.mu.RUnlock()

	if wasRunning && oldSettings.Port == settings.Server.Port {
		return p.restartSamePort(settings, oldInstance, oldSettings)
	}

	newListener, newServer, err := p.newServer(settings)
	if err != nil {
		return err
	}
	p.installServer(newListener, newServer)
	p.serve(newServer, newListener)

	slog.Info("proxy restarted", "addr", newListener.Addr().String())
	return shutdownServer(oldInstance)
}

func (p *Proxy) restartSamePort(settings *model.Settings, oldInstance serverInstance, oldSettings model.ServerSettings) error {
	p.detachServer()
	if err := shutdownServer(oldInstance); err != nil {
		return p.restoreAfterRestartFailure(err, oldSettings)
	}

	newListener, newServer, bindErr := p.newServer(settings)
	if bindErr != nil {
		return p.restoreAfterRestartFailure(bindErr, oldSettings)
	}
	p.installServer(newListener, newServer)
	p.serve(newServer, newListener)
	slog.Info("proxy restarted", "addr", newListener.Addr().String())
	return nil
}

func (p *Proxy) restoreAfterRestartFailure(originalErr error, oldSettings model.ServerSettings) error {
	old := &model.Settings{Server: oldSettings}
	listener, server, restoreErr := p.newServer(old)
	if restoreErr != nil {
		return fmt.Errorf("%w; restore previous listener failed: %v", originalErr, restoreErr)
	}
	p.installServer(listener, server)
	p.serve(server, listener)
	return originalErr
}

func serverSettingsForListener(listener net.Listener) model.ServerSettings {
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		return model.ServerSettings{Port: addr.Port, BindAddress: addr.IP.String()}
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return model.ServerSettings{}
	}
	parsedPort, _ := strconv.Atoi(port)
	return model.ServerSettings{Port: parsedPort, BindAddress: host}
}

func listenerMatchesSettings(listener net.Listener, settings model.ServerSettings) bool {
	current, ok := listener.Addr().(*net.TCPAddr)
	if !ok || current.Port != settings.Port {
		return false
	}
	target := net.ParseIP(settings.BindAddress)
	if target == nil {
		resolved, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(settings.BindAddress, strconv.Itoa(settings.Port)))
		if err != nil {
			return false
		}
		target = resolved.IP
	}
	if current.IP.IsUnspecified() && (target == nil || target.IsUnspecified()) {
		return true
	}
	return current.IP.Equal(target)
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
	r.Post("/v1/responses", p.handleResponses)
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

func (p *Proxy) currentSettings() (*model.Settings, error) {
	s := &model.Settings{}
	if p.settingsProvider != nil {
		provided, err := p.settingsProvider()
		if err != nil {
			return nil, fmt.Errorf("proxy: read settings: %w", err)
		}
		if provided != nil {
			*s = *provided
		}
	}
	if s.Server.Port == 0 {
		s.Server.Port = p.defaultPort
	}
	if s.Server.BindAddress == "" {
		s.Server.BindAddress = "0.0.0.0"
	}
	return s, nil
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

// logRequestEntry persists the final state of a request log. It updates the
// row that was inserted at request start (by insertPendingLog) with
// completion-time fields. It treats a full log-writer queue as a soft drop
// and does not fail the request.
func (p *Proxy) logRequestEntry(log *model.RequestLog) {
	if log.Cost == 0 {
		log.CostAvailable = true
		for _, attempt := range log.Chain {
			if attempt.UpstreamStarted {
				log.Cost += attempt.RequestCost
				if !attempt.RequestCostAvailable {
					log.CostAvailable = false
				}
			}
		}
	}
	if log.Timestamp == 0 {
		log.Timestamp = time.Now().UnixMilli()
	}
	if p.writer == nil {
		// Defensive: should never happen after New.
		return
	}
	if !p.writer.EnqueueUpdate(*log) {
		slog.Warn("proxy: request log update dropped: writer queue full", "id", log.ID)
	}
}

func markTransportAttempt(e *model.RequestLogChainEntry, c candidate) {
	e.UpstreamStarted = true
	e.RequestCost = c.requestPrice
	e.RequestCostAvailable = c.requestPriceAvailable
}

// insertPendingLog enqueues a pending log entry (status_code=0) at request
// start so the user can see in-flight requests in the log table before they
// complete. The entry's ID must be set by the caller; the same ID is used
// by logRequestEntry (deferred) to update the row with completion fields.
func (p *Proxy) insertPendingLog(log *model.RequestLog) {
	log.ID = store.NewUUID()
	if log.Timestamp == 0 {
		log.Timestamp = time.Now().UnixMilli()
	}
	log.StatusCode = 0 // 0 = pending; updated by logRequestEntry on completion
	if p.writer == nil {
		return
	}
	if !p.writer.Enqueue(*log) {
		slog.Warn("proxy: pending log dropped: writer queue full", "model", log.Model)
	}
}

// resolveCandidates selects one or more provider/model candidates using the
// model-rule matcher, filtering out providers with an open circuit breaker.
func (p *Proxy) resolveCandidates(req *InboundRequest) ([]candidate, error) {
	rules := p.loadModelRules()

	// Snapshot the breaker map to avoid racing with breakerFor writes.
	p.breakersMu.RLock()
	breakers := make(map[string]*CircuitBreaker, len(p.breakers))
	for k, v := range p.breakers {
		breakers[k] = v
	}
	p.breakersMu.RUnlock()

	candidates, err := selectCandidates(req, rules, breakers, p.store.GetProvider)
	if err != nil {
		return nil, err
	}
	// Price snapshots are needed by execution and request-log accounting for
	// every strategy, including priority_first. Only planCandidates may skip
	// scoring/reordering for the priority fast path.
	for i := range candidates {
		if m, e := p.store.GetModel(candidates[i].provider.ID, candidates[i].modelName); e == nil && m != nil {
			candidates[i].requestPrice = m.RequestPrice
			candidates[i].requestPriceAvailable = true
		}
	}
	return p.planCandidates(req, candidates), nil
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

// breakerState reads a provider breaker without creating one or claiming a
// half-open probe. A provider with no breaker has the implicit closed state.
func (p *Proxy) breakerState(providerID string) (State, bool) {
	p.breakersMu.RLock()
	cb, ok := p.breakers[providerID]
	p.breakersMu.RUnlock()
	if !ok || cb == nil {
		return StateClosed, false
	}
	return cb.CurrentState(), true
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
	// firstByteBudget is the rule-level maximum time the proxy is
	// willing to wait for the first response byte from ANY upstream
	// candidate (across all candidates and all per-target retries).
	// All candidates in the same rule share the same budget (the
	// matcher copies the rule's setting onto every candidate).
	firstByteBudget := candidates[0].firstByteBudget
	firstByteDeadline := time.Now().Add(firstByteBudget)
	// budgetCtx is derived from r.Context() so client-disconnect
	// still cancels everything, but it also carries a first-byte
	// deadline that fires when the rule-level budget is exhausted.
	// The budgetCtx is used for the backoff select only; the actual
	// HTTP request uses r.Context() (with transport-level
	// ResponseHeaderTimeout = remaining) so the body download is
	// NOT cut off by the first-byte deadline — the budget is
	// ONLY counted before the first byte is received.
	budgetCtx, budgetCancel := context.WithDeadline(r.Context(), firstByteDeadline)
	defer budgetCancel()
	var lastErr error = fmt.Errorf("no candidate produced a response")
	var lastStatus int
	// lastCandidate tracks the most recently iterated candidate so the
	// all-candidates-exhausted branch below can populate log provider fields
	// when no candidate produced a successful response.
	var lastCandidate candidate
	// attemptOrder is monotonically incremented across every candidate and
	// every retry so the Chain entries form a stable per-request timeline.
	attemptOrder := 0
	// totalAttempts counts upstream attempts (across all candidates and all
	// per-target retries) so we can cap them with maxTotalAttempts. Backoff
	// is applied BEFORE every attempt after the very first.
	totalAttempts := 0
	// attemptsCapped is set when totalAttempts reaches maxTotalAttempts OR
	// the first-byte budget is exhausted; it signals the outer loop
	// to stop iterating after the current candidate finishes its retry loop.
	attemptsCapped := false
	// retryAfter persists the most recent 429/503 Retry-After value from
	// a failed attempt; the next backoff will honor it instead of the
	// computed exponential backoff. Reset after consumption. Declared
	// outside the candidate loop so the value carries over to the first
	// attempt on a new target after a failed one.
	retryAfter := time.Duration(0)

	// outer is the label on the candidate loop. Used by the global attempt
	// cap (maxTotalAttempts) to break out of BOTH the candidate and retry
	// loops at once when the cap is reached. Plain `break` inside the retry
	// loop still only exits the inner loop, so success / circuit-open /
	// client-abort paths are unaffected.
outer:
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
			// Global attempt cap: stops the N×(M+1) explosion when
			// many candidates each have a high MaxRetries. The cap is
			// checked BEFORE the per-target circuit check and backoff
			// so a fully-spent budget is honored even mid-retry.
			if totalAttempts >= maxTotalAttempts {
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "attempts_capped",
					StatusCode:   0,
					Error:        fmt.Sprintf("global attempt cap (%d) reached", maxTotalAttempts),
					LatencyMs:    0,
				})
				lastErr = fmt.Errorf("global attempt cap (%d) reached", maxTotalAttempts)
				lastStatus = http.StatusServiceUnavailable
				attemptsCapped = true
				break
			}

			// First-byte budget check at the TOP of each attempt.
			// If the rule-level first-byte budget is exhausted, abort
			// the whole chain with a budget_exceeded chain entry; the
			// all-candidates-exhausted path then surfaces a 503.
			if remaining := time.Until(firstByteDeadline); remaining <= 0 {
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "budget_exceeded",
					StatusCode:   0,
					Error:        fmt.Sprintf("first-byte budget (%s) exceeded", firstByteBudget),
					LatencyMs:    0,
				})
				lastErr = fmt.Errorf("first-byte budget (%s) exceeded", firstByteBudget)
				lastStatus = http.StatusServiceUnavailable
				attemptsCapped = true
				break
			}

			// Backoff between attempts: fires before every attempt
			// except the very first (totalAttempts==0). Interrupts
			// on (a) client disconnect, (b) first-byte budget
			// exhaustion, or (c) the planned delay elapsing. The
			// 429/503 Retry-After header from the previous attempt
			// is honored here instead of the computed exponential
			// backoff when present.
			if totalAttempts > 0 {
				delay := retryBackoff(totalAttempts)
				if retryAfter > 0 {
					delay = retryAfter
					retryAfter = 0
				}
				// Cap the delay to the remaining first-byte budget
				// so a hostile Retry-After: 86400 cannot pin us.
				if remaining := time.Until(firstByteDeadline); delay > remaining && remaining > 0 {
					delay = remaining
				} else if remaining <= 0 {
					delay = 0
				}
				select {
				case <-budgetCtx.Done():
					// budgetCtx fires for both client-disconnect
					// and deadline-exceeded (first-byte budget).
					// Distinguish by checking r.Context() directly:
					// only the request context is canceled on client
					// disconnect, never on timeout (the request
					// context is the parent of budgetCtx).
					if r.Context().Err() != nil {
						// Client disconnect.
						logEntry.StatusCode = statusClientClosed
						logEntry.Error = "client disconnected during retry backoff"
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
							StatusCode:   statusClientClosed,
							Error:        "client disconnected during retry backoff",
							LatencyMs:    0,
						})
						slog.Warn("proxy: client disconnected during retry backoff",
							"provider", c.provider.Name,
							"model", c.modelName,
							"target", c.targetID)
					} else {
						// First-byte budget exhausted during backoff.
						logEntry.StatusCode = http.StatusServiceUnavailable
						logEntry.Error = fmt.Sprintf("first-byte budget (%s) exceeded", firstByteBudget)
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
							Status:       "budget_exceeded",
							StatusCode:   0,
							Error:        fmt.Sprintf("first-byte budget (%s) exceeded", firstByteBudget),
							LatencyMs:    0,
						})
						slog.Warn("proxy: first-byte budget exceeded during backoff",
							"provider", c.provider.Name,
							"budget", firstByteBudget)
					}
					return
				case <-time.After(delay):
				}
			}
			totalAttempts++

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

			// Use r.Context() (NOT budgetCtx) so the body download is
			// NOT cut off by the first-byte deadline — the budget is
			// ONLY counted before the first byte is received. The
			// first-byte deadline is enforced via the transport's
			// ResponseHeaderTimeout (set below to `remaining`).
			attemptReq := r.Clone(r.Context())
			attemptReq.Body = io.NopCloser(bytes.NewReader(rewrittenBody))
			attemptReq.ContentLength = int64(len(rewrittenBody))
			attemptReq.Header.Del("Transfer-Encoding")
			if attemptReq.Header.Get("Content-Type") == "" {
				attemptReq.Header.Set("Content-Type", "application/json")
			}
			attemptReq.Header.Set("Content-Length", fmt.Sprintf("%d", len(rewrittenBody)))

			// remaining is the time left in the first-byte budget at
			// the start of this attempt. We set it as the per-attempt
			// ResponseHeaderTimeout so the upstream headers must arrive
			// within `remaining`; once headers arrive the body download
			// is unbounded (r.Context() governs it instead).
			remaining := time.Until(firstByteDeadline)
			proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
			proxy.BufferPool = p.bufferPool
			proxy.ErrorLog = p.errorLog
			// Per-attempt ResponseHeaderTimeout = remaining (clamped
			// to the first-byte budget remaining at the start of this
			// attempt). Non-streaming has no first-byte concept, so
			// ResponseHeaderTimeout is the right knob. We always clone
			// p.transport so the per-attempt timeout is honored.
			if t1, ok := p.transport.(*http.Transport); ok {
				clone := t1.Clone()
				clone.ResponseHeaderTimeout = remaining
				proxy.Transport = clone
				defer clone.CloseIdleConnections()
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

			buf := &responseBuffer{statusCode: 0, header: make(http.Header), body: bytes.NewBuffer(nil)}
			var attemptErr error
			// attemptRespBody captures the upstream response body read
			// inside ModifyResponse so the failure branches below can
			// include the actual upstream error message (e.g. OpenAI's
			// error envelope with "model_not_found" or "invalid_parameter")
			// in the log entry instead of just "returned status 400".
			var attemptRespBody []byte
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
				attemptRespBody = respBody

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

			// effectiveStatus is the status code used for chain logging and retry
			// categorization. If the upstream never wrote a response (ModifyResponse
			// error, connection drop before headers), buf.statusCode is 0 (unset) and
			// buf.wrote is false. In that case, treat it as 502 Bad Gateway for error
			// classification purposes — the upstream did NOT return a 200.
			effectiveStatus := buf.statusCode
			if attemptErr != nil && !buf.wrote {
				effectiveStatus = http.StatusBadGateway
			}

			cat := CategorizeError(attemptErr, effectiveStatus)
			finalCat = cat
			finalAttemptErr = attemptErr
			latencyMs := int(time.Since(attemptStart).Milliseconds())
			attemptOutcome := model.AttemptOutcomeRetryable
			if cat == CategoryClientAbort {
				attemptOutcome = model.AttemptOutcomeClientAbort
			} else if cat == CategoryNonRetryable {
				attemptOutcome = model.AttemptOutcomeNonRetryable
			} else if attemptErr == nil && effectiveStatus < 400 {
				attemptOutcome = model.AttemptOutcomeSuccess
			}
			p.emitAttempt(c, r.URL.Path, middleware.GetReqID(r.Context()), attemptOutcome, effectiveStatus, buf.wrote, 0, 0)
			slog.Debug("proxy: candidate attempt",
				"provider", c.provider.Name,
				"model", c.modelName,
				"attempt", attempt,
				"category", cat,
				"status", effectiveStatus,
				"err", attemptErr)

			if attemptErr == nil && effectiveStatus < 400 {
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
					UpstreamStarted:      true,
					RequestCost:          c.requestPrice,
					RequestCostAvailable: c.requestPriceAvailable,
					AttemptOrder:         attemptOrder,
					ProviderID:           c.provider.ID,
					ProviderName:         c.provider.Name,
					ModelName:            c.modelName,
					TargetID:             c.targetID,
					Status:               "success",
					StatusCode:           effectiveStatus,
					Error:                "",
					LatencyMs:            latencyMs,
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
				lastErr = fmt.Errorf("upstream %s returned status %d", c.provider.Name, effectiveStatus)
			}
			// Enrich the error with the upstream's response body so the
			// log entry carries actionable detail (e.g. "model_not_found"
			// or "invalid_parameter") instead of just the status code.
			if upstreamMsg := extractUpstreamError(attemptRespBody); upstreamMsg != "" {
				lastErr = fmt.Errorf("%s: %s", lastErr.Error(), upstreamMsg)
			}
			lastStatus = effectiveStatus
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
					UpstreamStarted:      true,
					RequestCost:          c.requestPrice,
					RequestCostAvailable: c.requestPriceAvailable,
					AttemptOrder:         attemptOrder,
					ProviderID:           c.provider.ID,
					ProviderName:         c.provider.Name,
					ModelName:            c.modelName,
					TargetID:             c.targetID,
					Status:               "client_abort",
					StatusCode:           effectiveStatus,
					Error:                lastErr.Error(),
					LatencyMs:            latencyMs,
				})
				// No breaker record: client errors aren't provider failures.
				slog.Warn("proxy: client abort",
					"provider", c.provider.Name,
					"model", c.modelName,
					"status", effectiveStatus,
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
					UpstreamStarted:      true,
					RequestCost:          c.requestPrice,
					RequestCostAvailable: c.requestPriceAvailable,
					AttemptOrder:         attemptOrder,
					ProviderID:           c.provider.ID,
					ProviderName:         c.provider.Name,
					ModelName:            c.modelName,
					TargetID:             c.targetID,
					Status:               "non_retryable",
					StatusCode:           effectiveStatus,
					Error:                lastErr.Error(),
					LatencyMs:            latencyMs,
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
				// Capture the upstream Retry-After header (RFC 7231)
				// for 429/503 responses. The next backoff (back in
				// the retry loop) will honor it instead of the
				// computed exponential delay. parseRetryAfter
				// tolerates missing/malformed values by returning 0,
				// which means "use the regular backoff".
				if buf.statusCode == 429 || buf.statusCode == 503 {
					if v := parseRetryAfter(buf.header.Get("Retry-After")); v > 0 {
						retryAfter = v
					}
				}
				chainErr := attemptErr
				if attemptErr != nil {
					if errors.Is(attemptErr, io.ErrUnexpectedEOF) || errors.Is(attemptErr, io.EOF) {
						chainErr = fmt.Errorf("upstream response truncated")
					} else if isConnReset(attemptErr) {
						chainErr = fmt.Errorf("upstream response truncated")
					}
				} else if lastErr != nil {
					chainErr = lastErr
				}
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					UpstreamStarted:      true,
					RequestCost:          c.requestPrice,
					RequestCostAvailable: c.requestPriceAvailable,
					AttemptOrder:         attemptOrder,
					ProviderID:           c.provider.ID,
					ProviderName:         c.provider.Name,
					ModelName:            c.modelName,
					TargetID:             c.targetID,
					Status:               "retryable",
					StatusCode:           effectiveStatus,
					Error:                chainErr.Error(),
					LatencyMs:            latencyMs,
				})
				continue
			}
		}

		if succeeded {
			return
		}

		// If the global attempt cap fired, exit the candidate loop
		// entirely and fall through to the all-candidates-exhausted
		// path which will surface a 503 with the cap message.
		if attemptsCapped {
			break outer
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

	// firstByteBudget is the rule-level maximum time the proxy is willing to
	// wait for the first response byte from ANY upstream candidate (across all
	// candidates and all per-target retries). The budget is ONLY counted before
	// the first byte is received; once a response is established (streaming
	// first byte committed), the budget stops.
	firstByteBudget := candidates[0].firstByteBudget
	firstByteDeadline := time.Now().Add(firstByteBudget)
	budgetCtx, budgetCancel := context.WithDeadline(r.Context(), firstByteDeadline)
	defer budgetCancel()
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
	// totalAttempts counts upstream attempts across all candidates and all
	// per-target retries; capped at maxTotalAttempts to prevent the
	// N×(M+1) explosion.
	totalAttempts := 0
	// attemptsCapped signals the outer loop to stop after the current
	// candidate's retry loop completes.
	attemptsCapped := false
	// retryAfter persists the most recent 429/503 Retry-After value from
	// a failed attempt; the next backoff will honor it instead of the
	// computed exponential backoff. Reset after consumption.
	retryAfter := time.Duration(0)

	// outer is the label on the candidate loop. The global attempt cap
	// uses a labeled break to exit BOTH loops at once. Plain `break` in
	// the retry loop still only exits the inner loop, so success /
	// circuit-open paths are unaffected.
outer:
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
			// Global attempt cap: stops the N×(M+1) explosion when
			// many candidates each have a high MaxRetries.
			if totalAttempts >= maxTotalAttempts {
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "attempts_capped",
					StatusCode:   0,
					Error:        fmt.Sprintf("global attempt cap (%d) reached", maxTotalAttempts),
					LatencyMs:    0,
				})
				lastErr = fmt.Errorf("global attempt cap (%d) reached", maxTotalAttempts)
				lastStatus = http.StatusServiceUnavailable
				attemptsCapped = true
				break
			}

			// First-byte budget. The rule-level budget is the total
			// time the proxy will wait for the first response byte
			// from ANY upstream candidate (across all candidates
			// and all per-target retries). The check is purely a
			// time check (time.Until(firstByteDeadline)) so the
			// client-disconnect signal stays in the backoff select
			// below where it can be distinguished via r.Context().
			if remaining := time.Until(firstByteDeadline); remaining <= 0 {
				attemptOrder++
				logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{
					AttemptOrder: attemptOrder,
					ProviderID:   c.provider.ID,
					ProviderName: c.provider.Name,
					ModelName:    c.modelName,
					TargetID:     c.targetID,
					Status:       "budget_exceeded",
					StatusCode:   0,
					Error:        fmt.Sprintf("first-byte budget (%s) exceeded", firstByteBudget),
					LatencyMs:    0,
				})
				lastErr = fmt.Errorf("first-byte budget (%s) exceeded", firstByteBudget)
				lastStatus = http.StatusServiceUnavailable
				attemptsCapped = true
				break
			}

			// Backoff between attempts: fires before every attempt
			// except the very first (totalAttempts==0). Interrupts
			// on (a) client disconnect, (b) first-byte budget
			// exhaustion, or (c) the planned delay elapsing. The
			// 429/503 Retry-After header from the previous attempt
			// is honored here instead of the computed exponential
			// backoff when present.
			if totalAttempts > 0 {
				delay := retryBackoff(totalAttempts)
				if retryAfter > 0 {
					delay = retryAfter
					retryAfter = 0
				}
				// Cap the delay to the remaining first-byte budget
				// so a hostile Retry-After: 86400 cannot pin us.
				if remaining := time.Until(firstByteDeadline); delay > remaining && remaining > 0 {
					delay = remaining
				} else if remaining <= 0 {
					delay = 0
				}
				select {
				case <-budgetCtx.Done():
					if r.Context().Err() != nil {
						// Client disconnect.
						logEntry.StatusCode = statusClientClosed
						logEntry.Error = "client disconnected during retry backoff"
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
							StatusCode:   statusClientClosed,
							Error:        "client disconnected during retry backoff",
							LatencyMs:    0,
						})
						slog.Warn("proxy: stream client disconnected during retry backoff",
							"provider", c.provider.Name,
							"model", c.modelName,
							"target", c.targetID)
					} else {
						// First-byte budget exhausted during backoff.
						logEntry.StatusCode = http.StatusServiceUnavailable
						logEntry.Error = fmt.Sprintf("first-byte budget (%s) exceeded", firstByteBudget)
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
							Status:       "budget_exceeded",
							StatusCode:   0,
							Error:        fmt.Sprintf("first-byte budget (%s) exceeded", firstByteBudget),
							LatencyMs:    0,
						})
						slog.Warn("proxy: stream first-byte budget exceeded during backoff",
							"provider", c.provider.Name,
							"budget", firstByteBudget)
					}
					return
				case <-time.After(delay):
				}
			}
			totalAttempts++

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

			result, newOrder := p.streamAttempt(budgetCtx, w, r, c, upstreamKey, rewrittenBody, upstreamURL, attemptOrder, inputEstimate, logEntry)
			attemptOrder = newOrder

			switch result.Status {
			case model.OutcomeSuccess:
				// Top-level FirstTokenMs = Σ prior failed chain
				// LatencyMs + success chain FirstTokenMs.
				logEntry.FirstTokenMs = firstByteCumulativeMs + result.FirstTokenMs
				logEntry.StatusCode = result.StatusCode
				succeeded = true
				return
			case model.OutcomeTruncated, model.OutcomeDownstreamError:
				// The response was committed; never retry or fail over after
				// forwarding any upstream body bytes.
				logEntry.StatusCode = result.StatusCode
				logEntry.Error = result.Error
				return
			case model.OutcomeClientAbort:
				logEntry.StatusCode = result.StatusCode
				logEntry.Error = result.Error
				succeeded = true
				return
			case model.AttemptOutcomeNonRetryable:
				logEntry.StatusCode = result.StatusCode
				logEntry.Error = result.Error
				succeeded = true
				return
			case model.AttemptOutcomeRetryable:
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
				// Honor the upstream's Retry-After on the next
				// backoff (429/503 only — other retryable codes
				// return RetryAfter=0 from streamAttempt).
				if result.RetryAfter > 0 {
					retryAfter = result.RetryAfter
				}
				slog.Debug("proxy: stream retrying same target",
					"provider", c.provider.Name,
					"model", c.modelName,
					"attempt", attempt,
					"maxRetries", c.maxRetries,
					"retry_after", result.RetryAfter)
				continue
			default:
				slog.Error("proxy: stream unknown attempt status", "status", result.Status)
				return
			}
		}

		if succeeded {
			return
		}

		// If the global attempt cap fired, exit the candidate loop
		// entirely and fall through to the all-candidates-exhausted
		// path which will surface a 503 with the cap message.
		if attemptsCapped {
			break outer
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
func (p *Proxy) streamAttempt(ctx context.Context, w http.ResponseWriter, r *http.Request, c candidate, upstreamKey string, rewrittenBody []byte, upstreamURL *url.URL, attemptOrder int, inputEstimate int, logEntry *model.RequestLog) (result streamAttemptResult, order int) {
	order = attemptOrder
	emitted := false
	defer func() {
		if emitted {
			return
		}
		emitted = true
		outcome := model.AttemptOutcome(result.Status)
		if outcome == "" {
			outcome = model.AttemptOutcomeUnknown
		}
		p.emitAttempt(c, r.URL.Path, middleware.GetReqID(r.Context()), outcome, result.StatusCode, result.StatusCode >= 200 && result.StatusCode < 300, int64(result.FirstTokenMs), int64(result.FirstTokenMs))
	}()
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
	//
	// The budget is the rule-level firstByteBudget, but a single
	// attempt should not wait longer than the remaining time until
	// the first-byte deadline.
	timeout := c.firstByteBudget
	if c.targetFirstBodyByteTimeout > 0 && c.targetFirstBodyByteTimeout < timeout {
		timeout = c.targetFirstBodyByteTimeout
	}
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}
	if timeout == 0 {
		timeout = defaultFirstByteTimeout
	}

	// Use r.Context() (NOT ctx/budgetCtx) so the body download is
	// NOT cut off by the first-byte deadline — the budget is
	// ONLY counted before the first byte is received. The
	// first-byte deadline is enforced via the transport's
	// ResponseHeaderTimeout (set below to `timeout`).
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

	// Per-attempt transport with the candidate-specific first-byte
	// timeout. Clone p.transport (not http.DefaultTransport) so
	// streaming inherits any future tuning on p.transport (TLS,
	// dialer, etc.) rather than only DefaultTransport's defaults.
	// The ResponseHeaderTimeout override still applies after the
	// clone.
	transport := p.transport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = timeout
	client := &http.Client{Transport: transport}
	defer transport.CloseIdleConnections()

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
				UpstreamStarted:      true,
				RequestCost:          c.requestPrice,
				RequestCostAvailable: c.requestPriceAvailable,
				AttemptOrder:         attemptOrder,
				ProviderID:           c.provider.ID,
				ProviderName:         c.provider.Name,
				ModelName:            c.modelName,
				TargetID:             c.targetID,
				Status:               "client_abort",
				StatusCode:           statusClientClosed,
				Error:                "client disconnected: " + doErr.Error(),
				LatencyMs:            latencyMs,
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
				UpstreamStarted:      true,
				RequestCost:          c.requestPrice,
				RequestCostAvailable: c.requestPriceAvailable,
				AttemptOrder:         attemptOrder,
				ProviderID:           c.provider.ID,
				ProviderName:         c.provider.Name,
				ModelName:            c.modelName,
				TargetID:             c.targetID,
				Status:               "non_retryable",
				StatusCode:           http.StatusBadGateway,
				Error:                doErr.Error(),
				LatencyMs:            latencyMs,
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
				UpstreamStarted:      true,
				RequestCost:          c.requestPrice,
				RequestCostAvailable: c.requestPriceAvailable,
				AttemptOrder:         attemptOrder,
				ProviderID:           c.provider.ID,
				ProviderName:         c.provider.Name,
				ModelName:            c.modelName,
				TargetID:             c.targetID,
				Status:               "retryable",
				StatusCode:           0,
				Error:                doErr.Error(),
				LatencyMs:            latencyMs,
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
				UpstreamStarted:      true,
				RequestCost:          c.requestPrice,
				RequestCostAvailable: c.requestPriceAvailable,
				AttemptOrder:         attemptOrder,
				ProviderID:           c.provider.ID,
				ProviderName:         c.provider.Name,
				ModelName:            c.modelName,
				TargetID:             c.targetID,
				Status:               "retryable",
				StatusCode:           upstreamStatus,
				Error:                readErr.Error(),
				LatencyMs:            latencyMs,
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
			UpstreamStarted:      true,
			RequestCost:          c.requestPrice,
			RequestCostAvailable: c.requestPriceAvailable,
			AttemptOrder:         attemptOrder,
			ProviderID:           c.provider.ID,
			ProviderName:         c.provider.Name,
			ModelName:            c.modelName,
			TargetID:             c.targetID,
			Status:               "retryable",
			StatusCode:           upstreamStatus,
			Error:                errStr,
			LatencyMs:            latencyMs,
		})
		slog.Debug("proxy: stream upstream non-2xx",
			"provider", c.provider.Name,
			"model", c.modelName,
			"status", upstreamStatus,
			"category", cat)
		// Capture Retry-After for 429/503 so the caller honors it
		// instead of the computed exponential backoff.
		var retryAfter time.Duration
		if upstreamStatus == 429 || upstreamStatus == 503 {
			retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
		}
		return streamAttemptResult{
			Status:     "retryable",
			StatusCode: upstreamStatus,
			Error:      errStr,
			LatencyMs:  latencyMs,
			RetryAfter: retryAfter,
		}, attemptOrder
	}

	// 2xx — first obtain a body byte before committing downstream headers.
	slog.Debug("proxy: stream upstream success",
		"provider", c.provider.Name,
		"model", c.modelName,
		"status", upstreamStatus)

	deadline := time.Time{}
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	deadline = effectiveAttemptFirstBodyByteDeadline(time.Now(), deadline, c.targetFirstBodyByteTimeout)
	initial, initialErr := readFirstBodyByte(r.Context(), resp.Body, deadline)
	if len(initial) == 0 {
		if initialErr == nil {
			initialErr = io.EOF
		}
		category := CategorizeError(initialErr, 0)
		latencyMs := int(time.Since(attemptStart).Milliseconds())
		attemptOrder++
		status := model.AttemptOutcomeRetryable
		if category == CategoryClientAbort {
			status = model.OutcomeClientAbort
		}
		if category == CategoryNonRetryable {
			status = model.AttemptOutcomeNonRetryable
		}
		logEntry.Chain = append(logEntry.Chain, model.RequestLogChainEntry{AttemptOrder: attemptOrder, ProviderID: c.provider.ID, ProviderName: c.provider.Name, ModelName: c.modelName, TargetID: c.targetID, Status: string(status), Error: initialErr.Error(), LatencyMs: latencyMs, UpstreamStarted: true, RequestCost: c.requestPrice, RequestCostAvailable: c.requestPriceAvailable})
		return streamAttemptResult{Status: status, Error: initialErr.Error(), LatencyMs: latencyMs, StatusCode: 0}, attemptOrder
	}

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
	usageAcc := &streamUsageAccumulator{}

	buf := make([]byte, 32*1024)
	var streamErr error
	var writeErr error
	firstByteTime = time.Since(attemptStart)
	usageAcc.Feed(initial)
	if _, writeErr = ww.Write(initial); writeErr == nil && flusher != nil {
		flusher.Flush()
	}
	if writeErr == nil && initialErr != nil && initialErr != io.EOF {
		streamErr = initialErr
	}
	for {
		if streamErr != nil {
			break
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
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
		UpstreamStarted:      true,
		RequestCost:          c.requestPrice,
		RequestCostAvailable: c.requestPriceAvailable,
		AttemptOrder:         attemptOrder,
		ProviderID:           c.provider.ID,
		ProviderName:         c.provider.Name,
		ModelName:            c.modelName,
		TargetID:             c.targetID,
		Status:               "success",
		StatusCode:           upstreamStatus,
		LatencyMs:            attemptLatencyMs,
		FirstTokenMs:         int(firstByteTime.Milliseconds()),
	}
	markTransportAttempt(&chainEntry, c)

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
		chainEntry.Status = "downstream_error"
		chainEntry.Error = writeErr.Error()
		logEntry.Chain = append(logEntry.Chain, chainEntry)
		logEntry.Error = writeErr.Error()
		return streamAttemptResult{
			Status:       "downstream_error",
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
		if usageAcc.Successful() {
			p.recordStreamSuccess(c)
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
			if c.targetID != "" {
				if err := p.store.IncrementTargetStats(c.targetID, 0, 1); err != nil {
					slog.Error("proxy: increment target failure count (truncated stream)", "err", err)
				}
			}
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
			chainEntry.Status = "truncated"
			chainEntry.Error = streamErr.Error()
		}
		logEntry.Chain = append(logEntry.Chain, chainEntry)
		logEntry.Error = chainEntry.Error
		return streamAttemptResult{
			Status:       model.AttemptOutcome(chainEntry.Status),
			StatusCode:   upstreamStatus,
			Error:        chainEntry.Error,
			LatencyMs:    attemptLatencyMs,
			FirstTokenMs: int(firstByteTime.Milliseconds()),
			StreamErr:    streamErr,
		}, attemptOrder
	default:
		// Clean EOF. If [DONE] was not seen, this is a mid-stream
		// failure and the provider misbehaved.
		if !usageAcc.Successful() {
			chainEntry.Status = "truncated"
			p.breakerFor(c.provider.ID).Record(false)
			if c.targetID != "" {
				if err := p.store.IncrementTargetStats(c.targetID, 0, 1); err != nil {
					slog.Error("proxy: increment target failure count (truncated stream)", "err", err)
				}
			}
			if err := p.store.UpdateProviderHealth(c.provider.ID, model.ProviderStatusError, "stream closed without [DONE]"); err != nil {
				slog.Error("proxy: update provider health", "err", err)
			}
		}
		if usageAcc.Successful() {
			p.recordStreamSuccess(c)
		}
		logEntry.Chain = append(logEntry.Chain, chainEntry)
		return streamAttemptResult{
			Status:       model.AttemptOutcome(chainEntry.Status),
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
		UpstreamStarted:      true,
		RequestCost:          c.requestPrice,
		RequestCostAvailable: c.requestPriceAvailable,
		AttemptOrder:         attemptOrder,
		ProviderID:           c.provider.ID,
		ProviderName:         c.provider.Name,
		ModelName:            c.modelName,
		TargetID:             c.targetID,
		Status:               status,
		StatusCode:           upstreamStatus,
		Error:                errStr,
		LatencyMs:            latencyMs,
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
	Status       model.AttemptOutcome
	StatusCode   int
	Error        string
	LatencyMs    int // attempt wall-clock (for retryable entries; sum from caller)
	FirstTokenMs int // success only: time from attemptStart to first body byte

	// RetryAfter is the upstream's Retry-After header value (parsed
	// via parseRetryAfter) for retryable 429/503 responses. Non-zero
	// only on retryable outcomes; the caller uses it to override the
	// computed exponential backoff on the next attempt. Capped to
	// the remaining wall-clock budget in the caller.
	RetryAfter time.Duration

	// StreamErr is non-nil when the upstream body broke mid-stream (or
	// the client disconnected) after at least one byte was committed.
	// forwardStream uses it to distinguish client-side aborts from
	// provider-side failures.
	StreamErr error
}

// firstByteTrackingReadCloser removed: TTFT is now captured inline in
// streamAttempt. The old wrapper buffered nothing, but with the
// pass-through body Read it is no longer needed at all.

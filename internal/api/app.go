// Package api exposes the application's business logic to the Vue frontend
// through Wails Bind. Every exported method on App becomes callable from the
// frontend as window.go.main.App.<Method>(...).
//
// Two-surface contract (per oracle review §2.3):
//   - The WebView talks to Go ONLY through this bridge (window.go.*).
//   - External OpenAI-compatible clients talk to Go ONLY through the HTTP
//     proxy on 0.0.0.0:8344.
//   - The UI never fetches 8344 directly; metrics/logs flow through methods here.
//
// This file declares the contract (signatures). The implementations live in
// internal/service and are injected into App at startup. During Phase 1a this
// file returns placeholder/zero values so the frontend can wire against typed
// bindings before the backend is complete.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"autoapi/internal/dock"
	"autoapi/internal/logger"
	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/proxy"
	"autoapi/internal/routing"
	"autoapi/internal/scoring"
	"autoapi/internal/store"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func requestCost(m *model.Model, attempts int) model.EffectiveCost {
	if m == nil || attempts < 1 {
		return model.DefaultEffectiveCost()
	}
	return model.EffectiveCost{Cost: m.RequestPrice * float64(attempts), Currency: "USD", Available: true}
}

// Deps bundles the collaborators App needs. Any nil field makes the
// corresponding method return an "not yet implemented" error, so the contract
// is exercisable end-to-end from day one.
type Deps struct {
	Store      StoreService
	Service    BusinessService
	Proxy      ProxyService
	Checkpoint interface{ Stop() }
	Metrics    MetricsRegistry
}

type MetricsRegistry interface {
	CurrentSnapshot(model.TargetMetricKey) metrics.Snapshot
}

// StoreService is the persistence interface implemented by internal/store.
// Declared here so the api layer depends on the contract, not the concrete store.
type StoreService interface {
	// Providers
	ListProviders() ([]model.Provider, error)
	GetProvider(id string) (*model.Provider, error)
	CreateProvider(in model.ProviderInput) (*model.Provider, error)
	UpdateProvider(id string, in model.ProviderInput) (*model.Provider, error)
	DeleteProvider(id string) error
	SetProviderEnabled(id string, enabled bool) error
	ListProviderCapabilities(providerID string) ([]model.ProviderCapability, error)
	SetProviderCapability(providerID, protocol, feature string, enabled bool) error

	// Models (lookup, populated by upstream)
	ListModels(providerID string) ([]model.Model, error)
	GetModel(providerID, name string) (*model.Model, error)
	SetModelsActive(providerID string, modelNames []string, active bool) error
	DeleteModel(providerID, modelName string) error
	ClearProviderModels(providerID string) error
	UpdateProviderModel(in model.ProviderModelUpdate) error
	RecalcModelsCount(providerID string) error
	UpdateProviderTestResult(id string, status model.ProviderStatus, avgLatency int, errMsg string) error

	// API keys are simple access tokens; the App layer no longer encrypts here.
	ListAPIKeys() ([]model.ApiKey, error)
	CreateAPIKey(in model.ApiKeyInput) (*model.ApiKey, error)
	UpdateAPIKey(id string, in model.ApiKeyInput) (*model.ApiKey, error)
	DeleteAPIKey(id string) error

	// Provider upstream keys (ciphertext). The App layer composes Service.Encrypt +
	// Store.UpdateProviderKeyCiphertext; the store never sees plaintext.
	GetProviderKeyCiphertext(providerID string) (ciphertext, nonce []byte, err error)
	UpdateProviderKeyCiphertext(providerID string, ciphertext, nonce []byte, masked string) error

	// Model rules (formerly "routes" — the rule's Name is the model name
	// exposed to clients via /v1/models).
	ListModelRulesForDisplay() ([]model.ModelRule, error)
	ListModelRules() ([]model.ModelRule, error)
	GetModelRule(id string) (*model.ModelRule, error)
	CreateModelRule(in model.ModelRuleInput) (*model.ModelRule, error)
	UpdateModelRule(id string, in model.ModelRuleInput) (*model.ModelRule, error)
	DeleteModelRule(id string) error
	ReorderModelRules(orderedIDs []string) error // persists display order; ErrConflict on stale set
	ReorderModelRuleTargets(ruleID string, orderedTargetIDs []string) error

	// Logs & stats
	QueryLogs(q model.LogQuery) ([]model.RequestLog, int64, error)
	GetRequestLog(id string) (*model.RequestLog, error)
	Dashboard() (*model.DashboardData, error)
	UsageStats(q model.LogQuery) (*model.UsageStats, error)
	GetUsageTrends(q model.UsageTrendsQuery) (*model.UsageTrends, error)
	PurgeLogs(olderThanDays int) (int, error)
	ClearLogs() (int, error)

	// Settings
	GetSettings() (*model.Settings, error)
	SaveSettings(s model.Settings) error
	ResetSettings() (*model.Settings, error)
	ListEndpoints() ([]model.Endpoint, error)
	StorageDir() string

	// Export / import
	Export(format model.ExportFormat) ([]byte, string, error) // (data, filename, err)

	// Lifecycle
	Close() error
}

// BusinessService is the higher-level logic implemented by internal/service
// (provider testing, secret encryption, system health, etc.).
type BusinessService interface {
	TestProvider(providerID string) (*model.ProviderTestResult, error)
	TestAllProviders() ([]model.ProviderTestResult, error)
	FetchUpstreamModels(providerID string) ([]model.Model, error)
	TestModelLatency(providerID, modelName string) (*model.ModelTestResult, error)
	TestModelChat(providerID, modelName string, stream bool) (*model.ModelChatTestResult, error)
	GetSystemHealth() (*model.ServiceHealth, error)

	// Secret encryption. Encrypt produces ciphertext+nonce for storage in the
	// providers table; Decrypt reverses it. The App layer uses these to encrypt
	// upstream provider keys before passing ciphertext to the store.
	Encrypt(plaintext []byte) (ciphertext, nonce []byte, err error)
	Decrypt(ciphertext, nonce []byte) ([]byte, error)

	// ResolveProviderKey returns the decrypted upstream key for a provider.
	ResolveProviderKey(providerID string) (string, error)
	// AddProviderModels adds model names to a provider's local catalog.
	AddProviderModels(providerID string, names []string) error
}

// ProxyService controls the local OpenAI-compatible HTTP gateway.
type ProxyService interface {
	Start() error
	Stop() error
	Shutdown() error
	IsRunning() bool
	URL() string
	// Restart rebinds the listener (called when the user changes port/bind in settings).
	Restart() error
	// OnLogFlush registers a callback fired after each successful batch flush
	// of request logs. The App layer uses this to emit real-time Wails events
	// to the frontend.
	OnLogFlush(fn func())
}

type breakerStatusProvider interface {
	BreakerStatuses() map[string]proxy.BreakerStatus
}

// App is the single struct bound to the Wails runtime. All methods here are
// auto-generated as TypeScript bindings under frontend/wailsjs/go/main/App.
type App struct {
	ctx                 context.Context
	deps                Deps
	appInfo             model.AppInfo
	visibilityMu        sync.Mutex
	visibility          string
	initiallyBackground bool
	quitting            atomic.Bool
}

// GetTargetDiagnostics computes shadow-only diagnostics from detached snapshots.
func (a *App) GetTargetDiagnostics() ([]model.TargetShadowScore, error) {
	if a.deps.Store == nil || a.deps.Metrics == nil {
		return nil, errNotImpl
	}
	rules, err := a.deps.Store.ListModelRules()
	if err != nil {
		return nil, err
	}
	breakers := map[string]proxy.BreakerStatus{}
	if p, ok := a.deps.Proxy.(breakerStatusProvider); ok {
		breakers = p.BreakerStatuses()
	}
	inputs := make([]scoring.TargetInput, 0)
	meta := make([]model.TargetShadowScore, 0)
	providerUnavailable := make([]bool, 0)
	priceUnavailable := make([]bool, 0)
	for _, rule := range rules {
		for _, target := range rule.Targets {
			p, pErr := a.deps.Store.GetProvider(target.ProviderID)
			providerBad := pErr != nil || p == nil
			endpoint := "/v1/chat/completions"
			key := model.TargetMetricKey{TargetID: target.ID, ProviderID: target.ProviderID, ModelName: target.ModelName, Endpoint: endpoint}
			ms := a.deps.Metrics.CurrentSnapshot(key)
			m, priceErr := a.deps.Store.GetModel(target.ProviderID, target.ModelName)
			priceBad := priceErr != nil || m == nil
			cost := requestCost(m, 1+target.MaxRetries)
			hard := scoring.HardState{Disabled: !rule.Enabled || !target.Enabled}
			if b, ok := breakers[target.ProviderID]; ok {
				hard.CircuitOpen = b.State == proxy.StateOpen
				hard.HalfOpen = b.State == proxy.StateHalfOpen
			}
			if providerBad {
				hard.Disabled = true
			}
			inputs = append(inputs, scoring.TargetInput{Target: target, Metrics: ms, Cost: cost, HardState: hard})
			name := ""
			if p != nil {
				name = p.Name
			}
			meta = append(meta, model.TargetShadowScore{TargetID: target.ID, RuleID: rule.ID, RuleName: rule.Name, ProviderID: target.ProviderID, ProviderName: name, ModelName: target.ModelName, CircuitState: breakerState(breakers, target.ProviderID)})
			providerUnavailable = append(providerUnavailable, providerBad)
			priceUnavailable = append(priceUnavailable, priceBad)
		}
	}
	scores := scoring.ScoreTargets(inputs, scoring.ScoreContext{})
	out := make([]model.TargetShadowScore, len(scores))
	for i, s := range scores {
		v := meta[i]
		converted := replayShadowScore(s, inputs[i].Metrics, "/v1/chat/completions", inputs[i].Cost)
		v.Tier = converted.Tier
		v.Metrics = converted.Metrics
		v.MetricsFresh = converted.MetricsFresh
		v.Endpoint = converted.Endpoint
		v.EndpointAssumed = converted.EndpointAssumed
		v.Reliability = converted.Reliability
		v.Latency = converted.Latency
		v.TTFT = converted.TTFT
		v.Capacity = converted.Capacity
		v.CostEfficiency = converted.CostEfficiency
		v.Confidence = converted.Confidence
		v.Overall = converted.Overall
		v.ExplorationBonus = converted.ExplorationBonus
		v.EstimatedCost = converted.EstimatedCost
		v.SampleCount = converted.SampleCount
		v.Availability = converted.Availability
		v.Reason = converted.Reason
		v.Cost = converted.Cost
		// These are diagnostic-only overrides. The scorer remains unchanged and
		// unavailable provider/price failures must not be hidden by its generic
		// disabled or price reason.
		if providerUnavailable[i] {
			v.Availability, v.Reason = scoring.Unavailable, "provider_unavailable"
		} else if priceUnavailable[i] {
			v.Availability, v.Reason = scoring.Unavailable, "price_unavailable"
		}
		out[i] = v
	}
	return out, nil
}

// GetModelRuleShadowComparisons builds detached routing plans for display only.
// It is intentionally not called by matcher/proxy failover and emits no events.
func (a *App) GetModelRuleShadowComparisons() ([]model.ModelRuleShadowComparison, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	rules, err := a.deps.Store.ListModelRules()
	if err != nil {
		return nil, err
	}
	result := make([]model.ModelRuleShadowComparison, 0, len(rules))
	breakers, breakerSnapshots := map[string]proxy.BreakerStatus{}, false
	if p, ok := a.deps.Proxy.(breakerStatusProvider); ok {
		breakers, breakerSnapshots = p.BreakerStatuses(), true
	}
	for _, rule := range rules {
		inputs := make([]routing.CandidatePlanInput, 0, len(rule.Targets))
		assumptions := []string{"capabilities_assumed_satisfied", "budget_assumed_satisfied", "cooldown_state_assumed_inactive", "retry_and_stream_unchanged"}
		circuitStates := make([]string, 0, len(rule.Targets))
		for i, target := range rule.Targets {
			endpoint := "/v1/chat/completions"
			key := model.TargetMetricKey{TargetID: target.ID, ProviderID: target.ProviderID, ModelName: target.ModelName, Endpoint: endpoint}
			var snapshot metrics.Snapshot
			if a.deps.Metrics != nil {
				snapshot = a.deps.Metrics.CurrentSnapshot(key)
			}
			p, pErr := a.deps.Store.GetProvider(target.ProviderID)
			providerOK := pErr == nil && p != nil && p.Enabled
			m, priceErr := a.deps.Store.GetModel(target.ProviderID, target.ModelName)
			cost := requestCost(m, 1+target.MaxRetries)
			if pErr != nil {
				assumptions = append(assumptions, "provider_error:"+target.ProviderID)
			}
			if priceErr != nil {
				assumptions = append(assumptions, "price_error:"+target.ID)
			}
			circuitState, circuitOpen := "closed", false
			if !breakerSnapshots {
				assumptions = append(assumptions, "circuit_state_assumed_closed/unavailable")
			} else if b, ok := breakers[target.ProviderID]; ok {
				circuitState, circuitOpen = b.State.String(), b.State == proxy.StateOpen
				assumptions = append(assumptions, "circuit_state_observed:"+circuitState)
			} else {
				assumptions = append(assumptions, "circuit_state_assumed_closed/unavailable")
			}
			circuitStates = append(circuitStates, circuitState)
			score := scoring.ScoreTargets([]scoring.TargetInput{{Target: target, Metrics: snapshot, Cost: cost, HardState: scoring.HardState{Disabled: !rule.Enabled || !target.Enabled || !providerOK, CircuitOpen: circuitOpen}}}, scoring.ScoreContext{})[0]
			inputs = append(inputs, routing.CandidatePlanInput{OriginalIndex: i, TargetID: target.ID, Tier: target.Tier, Enabled: target.Enabled && rule.Enabled, HardAvailable: providerOK, CircuitOpen: circuitOpen, Cooldown: false, CapabilitySatisfied: true, BudgetSatisfied: true, TargetScore: score, EffectiveCost: cost})
		}
		plan := routing.BuildCandidatePlan(inputs, routing.Strategy(rule.Strategy), routing.Policy{})
		candidates := make([]model.ShadowPlanCandidate, 0, len(plan.Candidates))
		for _, c := range plan.Candidates {
			candidates = append(candidates, model.ShadowPlanCandidate{TargetID: c.TargetID, Tier: c.Tier, Available: c.Available, Reason: c.Reason, Changed: c.Changed, CircuitState: circuitStates[c.OriginalIndex]})
		}
		rejected := make([]model.ShadowPlanCandidate, 0)
		for _, in := range inputs {
			if in.Enabled && in.HardAvailable && !in.CircuitOpen {
				continue
			}
			reason := "unavailable"
			if !in.Enabled {
				reason = "disabled"
			} else if in.CircuitOpen {
				reason = "circuit_open"
			}
			rejected = append(rejected, model.ShadowPlanCandidate{TargetID: in.TargetID, Tier: in.Tier, Available: false, Reason: reason, Changed: true, CircuitState: circuitStates[in.OriginalIndex]})
		}
		result = append(result, model.ModelRuleShadowComparison{RuleID: rule.ID, RuleName: rule.Name, Strategy: string(plan.Strategy), OriginalOrder: plan.OriginalOrder, PlannedOrder: plan.PlannedOrder, Changed: plan.Changed, Candidates: candidates, Rejected: rejected, Assumptions: assumptions})
	}
	return result, nil
}

func breakerState(m map[string]proxy.BreakerStatus, id string) string {
	if b, ok := m[id]; ok {
		return b.State.String()
	}
	return "closed"
}

// SetInitialVisibility records Wails' StartHidden option before startup.
func (a *App) SetInitialVisibility(background bool) { a.initiallyBackground = background }

// GetAppVisibilityState returns the native lifecycle state for startup
// reconciliation by the frontend.
func (a *App) GetAppVisibilityState() string {
	a.visibilityMu.Lock()
	defer a.visibilityMu.Unlock()
	return a.visibility
}

func (a *App) EnterBackground() error {
	if a.ctx == nil {
		return fmt.Errorf("app: EnterBackground called before Startup")
	}
	a.visibilityMu.Lock()
	if a.visibility == "background" {
		a.visibilityMu.Unlock()
		return nil
	}
	a.visibility = "background"
	a.visibilityMu.Unlock()
	runtime.WindowHide(a.ctx)
	runtime.Hide(a.ctx)
	dock.HideDockIcon()
	runtime.EventsEmit(a.ctx, "app:visibility", "background")
	return nil
}

func (a *App) EnterForeground() error {
	if a.ctx == nil {
		return fmt.Errorf("app: EnterForeground called before Startup")
	}
	a.visibilityMu.Lock()
	if a.visibility == "foreground" {
		a.visibilityMu.Unlock()
		return nil
	}
	a.visibility = "foreground"
	a.visibilityMu.Unlock()
	dock.ShowDockIcon()
	runtime.Show(a.ctx)
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	runtime.EventsEmit(a.ctx, "app:visibility", "foreground")
	return nil
}

// NewApp constructs an App with the given dependencies. Pass Deps{} (zero) to
// get a contract-only instance that returns ErrNotImplemented from every call.
func NewApp(deps Deps) *App {
	return &App{deps: deps, visibility: "foreground"}
}

// SetAppInfo injects build-time version metadata. Called from main after
// NewApp but before wails.Run.
func (a *App) SetAppInfo(info model.AppInfo) {
	a.appInfo = info
}

// Startup is invoked by Wails OnStartup. We save the ctx for runtime calls
// (events, dialogs) and start the proxy if configured. Also wires a debounced
// "log:new" Wails event so the dashboard refreshes after new request logs are
// persisted, without the frontend having to poll QueryLogs.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	slog.Info("app: starting up")
	// Wails may have created the window hidden before OnStartup. Route the
	// initial state through the same authority so Dock visibility and the
	// frontend event are consistent from the first callback onward.
	if a.initiallyBackground {
		_ = a.EnterBackground()
	}
	if a.deps.Proxy != nil {
		if err := a.deps.Proxy.Start(); err != nil {
			slog.Error("app: failed to start proxy", "err", err)
		} else {
			slog.Info("app: proxy started")
		}
		a.wireLogEventEmitter()
	}
}

// wireLogEventEmitter subscribes a debounced real-time log event to the proxy's
// log-writer. Every batched insert into request_logs triggers this callback; we
// coalesce bursts within 200ms so a high-traffic period does not flood the
// frontend with events. The frontend's log:new handler then re-queries
// QueryLogs and patches the UI.
func (a *App) wireLogEventEmitter() {
	var debounce *time.Timer
	slog.Debug("app: registering OnLogFlush with proxy")
	a.deps.Proxy.OnLogFlush(func() {
		slog.Debug("app: log flush received, resetting debounce")
		if debounce != nil {
			debounce.Stop()
		}
		debounce = time.AfterFunc(200*time.Millisecond, func() {
			if a.ctx == nil {
				slog.Debug("app: skipping log:new emit, context nil")
				return
			}
			slog.Debug("app: emitting log:new event")
			runtime.EventsEmit(a.ctx, "log:new")
		})
	})
	slog.Debug("app: log event emitter wired")
}

// PingLogEvent immediately emits a log:new Wails event so the frontend can
// verify the event channel is alive without waiting for a real request log.
func (a *App) PingLogEvent() {
	if a.ctx == nil {
		slog.Warn("app: PingLogEvent called with nil context")
		return
	}
	slog.Debug("app: PingLogEvent emitted")
	runtime.EventsEmit(a.ctx, "log:new")
}

// Shutdown is invoked by Wails OnShutdown. Stop the proxy cleanly.
func (a *App) Shutdown(ctx context.Context) {
	slog.Info("app: shutting down")
	if a.deps.Checkpoint != nil {
		a.deps.Checkpoint.Stop()
	}
	if a.deps.Proxy != nil {
		_ = a.deps.Proxy.Shutdown()
	}
	if a.deps.Store != nil {
		_ = a.deps.Store.Close()
	}
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

// ----- Lifecycle / system -----

// GetSystemHealth returns the live dashboard telemetry.
func (a *App) GetSystemHealth() (model.ServiceHealth, error) {
	if a.deps.Service == nil {
		return model.ServiceHealth{}, errNotImpl
	}
	h, err := a.deps.Service.GetSystemHealth()
	if err != nil || h == nil {
		return model.ServiceHealth{}, err
	}
	return *h, nil
}

// ----- Window / app control (used by the tray / application menu) -----
//
// These methods are the bridge between the Wails native menu (or, in builds
// that support it, a system tray icon) and the Go side. They all check
// a.ctx for nil because menu callbacks may fire before OnStartup populated it
// (e.g. when the menu is built at startup time and the user clicks an item
// while the runtime is still wiring up), and runtime.* helpers below would
// otherwise log.Fatal on a nil context.

// ShowWindow restores and activates the main window. runtime.Show is needed
// on macOS because HideWindowOnClose hides the entire application (NSApp),
// not just the window. WindowShow handles the case where the window was
// individually hidden via WindowHide. WindowUnminimise handles minimised state.
func (a *App) ShowWindow() error {
	return a.EnterForeground()
}

// HideWindow hides the main window. Used when the user picks "hide" from the
// tray / menu bar instead of closing the app.
func (a *App) HideWindow() error {
	return a.EnterBackground()
}

// Quit terminates the application. The Wails runtime.Quit helper requires a
// non-nil ctx; we silently no-op when the context has not been set yet
// (e.g. the menu is clicked during shutdown), which is preferable to the
// log.Fatal that runtime.Quit would otherwise trigger.
func (a *App) Quit() {
	// Record the explicit quit intent before asking the runtime to close so
	// OnBeforeClose can distinguish a menu/tray Quit from a window close.
	a.quitting.Store(true)
	if a.ctx == nil {
		return
	}
	runtime.Quit(a.ctx)
}

// IsQuitting reports whether an explicit quit intent has been recorded (e.g.
// from the application menu, tray menu, or frontend binding). It is safe for
// concurrent use and is intended for lifecycle handlers such as
// OnBeforeClose.
func (a *App) IsQuitting() bool {
	return a.quitting.Load()
}

// NavigateTo asks the frontend to push a route. We use a Wails event rather
// than mutating the router directly because the Vue router lives inside the
// WebView; the AppWindow component listens for the "navigate" event and
// forwards the path to vue-router.
func (a *App) NavigateTo(path string) {
	if a.ctx == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "app:navigate", path)
}

// RestartProxy rebinds the local HTTP listener (used after the user toggles
// port/bind in settings, or via the tray "restart" shortcut).
func (a *App) RestartProxy() error {
	if a.deps.Proxy == nil {
		return errNotImpl
	}
	return a.deps.Proxy.Restart()
}

// StartProxy starts the local HTTP listener.
func (a *App) StartProxy() error {
	if a.deps.Proxy == nil {
		return errNotImpl
	}
	return a.deps.Proxy.Start()
}

// StopProxy stops the local HTTP listener without tearing down the log writer,
// so it can be restarted later without losing request logging.
func (a *App) StopProxy() error {
	if a.deps.Proxy == nil {
		return errNotImpl
	}
	return a.deps.Proxy.Stop()
}

// ----- Providers -----

func (a *App) ListProviders() ([]model.Provider, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListProviders()
}

func (a *App) GetProvider(id string) (*model.Provider, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.GetProvider(id)
}

func (a *App) CreateProvider(in model.ProviderInput) (*model.Provider, error) {
	if a.deps.Store == nil || a.deps.Service == nil {
		return nil, errNotImpl
	}
	// The store creates the provider without key columns; we encrypt the upstream
	// key separately and update the ciphertext columns immediately after.
	upstreamKey := in.UpstreamKey
	in.UpstreamKey = ""
	p, err := a.deps.Store.CreateProvider(in)
	if err != nil {
		return nil, err
	}
	if upstreamKey != "" {
		ct, nonce, err := a.deps.Service.Encrypt([]byte(upstreamKey))
		if err != nil {
			return nil, err
		}
		if err := a.deps.Store.UpdateProviderKeyCiphertext(p.ID, ct, nonce, maskKey(upstreamKey)); err != nil {
			_ = a.deps.Store.DeleteProvider(p.ID)
			return nil, err
		}
	}
	slog.Info("app: provider created", "id", p.ID, "name", p.Name)
	return a.deps.Store.GetProvider(p.ID)
}

func (a *App) UpdateProvider(id string, in model.ProviderInput) (*model.Provider, error) {
	if a.deps.Store == nil || a.deps.Service == nil {
		return nil, errNotImpl
	}
	// Update the provider body without touching key columns; then encrypt and
	// store the upstream key separately if a new one was supplied.
	upstreamKey := in.UpstreamKey
	in.UpstreamKey = ""
	_, err := a.deps.Store.UpdateProvider(id, in)
	if err != nil {
		return nil, err
	}
	if upstreamKey != "" {
		ct, nonce, err := a.deps.Service.Encrypt([]byte(upstreamKey))
		if err != nil {
			return nil, err
		}
		if err := a.deps.Store.UpdateProviderKeyCiphertext(id, ct, nonce, maskKey(upstreamKey)); err != nil {
			return nil, err
		}
	}
	slog.Info("app: provider updated", "id", id, "name", in.Name)
	return a.deps.Store.GetProvider(id)
}

func (a *App) DeleteProvider(id string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	if err := a.deps.Store.DeleteProvider(id); err != nil {
		return err
	}
	slog.Info("app: provider deleted", "id", id)
	return nil
}

func (a *App) SetProviderEnabled(id string, enabled bool) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	if err := a.deps.Store.SetProviderEnabled(id, enabled); err != nil {
		return err
	}
	slog.Info("app: provider enabled updated", "id", id, "enabled", enabled)
	return nil
}

func (a *App) ListProviderCapabilities(providerID string) ([]model.ProviderCapability, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListProviderCapabilities(providerID)
}

func (a *App) SetProviderCapability(providerID, protocol string, enabled bool) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.SetProviderCapability(providerID, protocol, "native", enabled)
}

func (a *App) TestProvider(id string) (*model.ProviderTestResult, error) {
	if a.deps.Service == nil {
		return nil, errNotImpl
	}
	return a.deps.Service.TestProvider(id)
}

func (a *App) TestAllProviders() ([]model.ProviderTestResult, error) {
	if a.deps.Service == nil {
		return nil, errNotImpl
	}
	return a.deps.Service.TestAllProviders()
}

func (a *App) FetchUpstreamModels(providerID string) ([]model.Model, error) {
	if a.deps.Service == nil {
		return nil, errNotImpl
	}
	return a.deps.Service.FetchUpstreamModels(providerID)
}

func (a *App) TestModelLatency(providerID, modelName string) (*model.ModelTestResult, error) {
	if a.deps.Service == nil {
		return nil, errNotImpl
	}
	return a.deps.Service.TestModelLatency(providerID, modelName)
}

func (a *App) TestModelChat(providerID, modelName string, stream bool) (*model.ModelChatTestResult, error) {
	if a.deps.Service == nil {
		return nil, errNotImpl
	}
	return a.deps.Service.TestModelChat(providerID, modelName, stream)
}

func (a *App) SetModelsActive(providerID string, modelNames []string, active bool) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.SetModelsActive(providerID, modelNames, active)
}

func (a *App) ListModels(providerID string) ([]model.Model, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListModels(providerID)
}

// AddProviderModels adds the given model names to a provider's local catalog.
func (a *App) AddProviderModels(providerID string, names []string) error {
	if a.deps.Service == nil {
		return errNotImpl
	}
	return a.deps.Service.AddProviderModels(providerID, names)
}

// DeleteModel removes a single model from a provider's catalog.
func (a *App) DeleteModel(providerID, modelName string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.DeleteModel(providerID, modelName)
}

// ClearProviderModels removes all models for a provider.
func (a *App) ClearProviderModels(providerID string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.ClearProviderModels(providerID)
}

// UpdateProviderModel atomically changes a provider model's name and request price.
func (a *App) UpdateProviderModel(in model.ProviderModelUpdate) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.OldName = strings.TrimSpace(in.OldName)
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("model name must not be empty")
	}
	return a.deps.Store.UpdateProviderModel(in)
}

// GetProviderKey returns the decrypted upstream key for display in the UI.
func (a *App) GetProviderKey(providerID string) (string, error) {
	if a.deps.Service == nil {
		return "", errNotImpl
	}
	return a.deps.Service.ResolveProviderKey(providerID)
}

// ----- API keys -----

func (a *App) ListAPIKeys() ([]model.ApiKey, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListAPIKeys()
}

func (a *App) CreateAPIKey(in model.ApiKeyInput) (*model.ApiKey, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.CreateAPIKey(in)
}

func (a *App) UpdateAPIKey(id string, in model.ApiKeyInput) (*model.ApiKey, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.UpdateAPIKey(id, in)
}

func (a *App) DeleteAPIKey(id string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.DeleteAPIKey(id)
}

// ----- Model rules -----

func (a *App) ListModelRules() ([]model.ModelRule, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListModelRulesForDisplay()
}

func (a *App) GetModelRule(id string) (*model.ModelRule, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.GetModelRule(id)
}

func (a *App) CreateModelRule(in model.ModelRuleInput) (*model.ModelRule, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.CreateModelRule(in)
}

func (a *App) UpdateModelRule(id string, in model.ModelRuleInput) (*model.ModelRule, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.UpdateModelRule(id, in)
}

func (a *App) DeleteModelRule(id string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.DeleteModelRule(id)
}

// ReorderModelRules persists a new display order for model rules. A stale set
// (empty, duplicate, unknown, missing, or count mismatch) is surfaced as
// {Conflict:true} so the UI can reload instead of failing silently.
func (a *App) ReorderModelRules(orderedIDs []string) (model.ReorderModelRulesResult, error) {
	if a.deps.Store == nil {
		return model.ReorderModelRulesResult{}, errNotImpl
	}
	err := a.deps.Store.ReorderModelRules(orderedIDs)
	if errors.Is(err, store.ErrConflict) {
		return model.ReorderModelRulesResult{Conflict: true}, nil
	}
	if err != nil {
		return model.ReorderModelRulesResult{}, err
	}
	return model.ReorderModelRulesResult{}, nil
}

// ReorderModelRuleTargets reorders the targets within a model rule by updating
// only their tier values. Unlike UpdateModelRule, it does NOT delete or recreate
// targets — counters and IDs are fully preserved.
func (a *App) ReorderModelRuleTargets(ruleID string, orderedTargetIDs []string) (model.ReorderModelRuleTargetsResult, error) {
	if a.deps.Store == nil {
		return model.ReorderModelRuleTargetsResult{}, errNotImpl
	}
	err := a.deps.Store.ReorderModelRuleTargets(ruleID, orderedTargetIDs)
	if errors.Is(err, store.ErrConflict) {
		return model.ReorderModelRuleTargetsResult{Conflict: true}, nil
	}
	if err != nil {
		return model.ReorderModelRuleTargetsResult{}, err
	}
	return model.ReorderModelRuleTargetsResult{}, nil
}

// ----- Dashboard / usage -----

func (a *App) GetDashboard() (*model.DashboardData, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	d, err := a.deps.Store.Dashboard()
	if err != nil {
		return nil, err
	}
	if a.deps.Service != nil {
		if h, hErr := a.deps.Service.GetSystemHealth(); hErr == nil && h != nil {
			d.ServiceHealth = *h
		}
	}
	return d, nil
}

func (a *App) GetUsageStats(q model.LogQuery) (*model.UsageStats, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.UsageStats(q)
}

func (a *App) QueryLogs(q model.LogQuery) (*model.LogQueryResult, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	logs, total, err := a.deps.Store.QueryLogs(q)
	if err != nil {
		return nil, err
	}
	return &model.LogQueryResult{Logs: logs, Total: total}, nil
}

// ReplayLog reconstructs a historical request using detached runtime metrics.
// It is strictly shadow-only: it does not inspect or update proxy breaker state.
func (a *App) ReplayLog(id string) (*model.ReplayResult, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	logPtr, err := a.deps.Store.GetRequestLog(id)
	if err != nil {
		return nil, err
	}
	if logPtr == nil || logPtr.ID == "" {
		return nil, fmt.Errorf("request log %q not found", id)
	}
	log := *logPtr
	rule, err := a.deps.Store.GetModelRule(log.RouteID)
	if (err != nil || rule == nil) && log.RouteLabel != "" {
		if rules, listErr := a.deps.Store.ListModelRules(); listErr == nil {
			for i := range rules {
				if rules[i].Name == log.RouteLabel {
					rule = &rules[i]
					err = nil
					break
				}
			}
		}
	}
	if err != nil || rule == nil {
		if err == nil {
			err = errors.New("rule not found")
		}
		return nil, fmt.Errorf("replay rule %q: %w", log.RouteID, err)
	}
	endpoint := "/v1/chat/completions"
	snapshots := map[model.TargetMetricKey]metrics.Snapshot{}
	if a.deps.Metrics != nil {
		for _, attempt := range log.Chain {
			k := model.TargetMetricKey{TargetID: attempt.TargetID, ProviderID: attempt.ProviderID, ModelName: attempt.ModelName, Endpoint: endpoint}
			snapshots[k] = a.deps.Metrics.CurrentSnapshot(k)
		}
	}
	costs := make([]model.EffectiveCost, len(log.Chain))
	for i, attempt := range log.Chain {
		var target model.ModelRuleTarget
		for _, candidate := range rule.Targets {
			if candidate.ID == attempt.TargetID && candidate.ProviderID == attempt.ProviderID && candidate.ModelName == attempt.ModelName {
				target = candidate
				break
			}
		}
		if target.ID == "" {
			costs[i] = model.DefaultEffectiveCost()
			continue
		}
		if attempt.UpstreamStarted && attempt.RequestCostAvailable {
			costs[i] = model.EffectiveCost{Cost: attempt.RequestCost, Currency: "USD", Available: true}
		} else {
			costs[i] = model.DefaultEffectiveCost()
		}
	}
	scores := scoring.ReplayOneRequest(log, *rule, endpoint, snapshots, costs)
	result := &model.ReplayResult{LogID: log.ID, Timestamp: log.Timestamp, RuleID: rule.ID, RuleName: rule.Name, RequestOutcome: outcomeFor(log), Endpoint: endpoint, EndpointAssumed: true}
	if len(log.Chain) == 0 {
		result.Warnings = append(result.Warnings, "no attempt chain; legacy log has low replay confidence")
	}
	for _, attempt := range log.Chain {
		if attempt.UpstreamStarted && !attempt.RequestCostAvailable {
			result.Warnings = append(result.Warnings, "one or more upstream attempt prices were unavailable")
			break
		}
	}
	for i, attempt := range log.Chain {
		var target model.ModelRuleTarget
		for _, t := range rule.Targets {
			if t.ID == attempt.TargetID && t.ProviderID == attempt.ProviderID && t.ModelName == attempt.ModelName {
				target = t
				break
			}
		}
		providerMissing := false
		provider, e := a.deps.Store.GetProvider(attempt.ProviderID)
		if e != nil || provider == nil {
			providerMissing = true
		}
		entry := model.ReplayAttemptScore{Attempt: attempt, TargetID: attempt.TargetID, ProviderID: attempt.ProviderID, ModelName: attempt.ModelName, TargetMissing: target.ID == "", ProviderMissing: providerMissing, ReplayLimitation: "historical breaker state unavailable"}
		if i < len(scores) {
			entry.Score = replayShadowScore(scores[i], snapshots[model.TargetMetricKey{TargetID: attempt.TargetID, ProviderID: attempt.ProviderID, ModelName: attempt.ModelName, Endpoint: endpoint}], endpoint, costs[i])
		}
		if entry.TargetMissing || entry.ProviderMissing {
			entry.Score.Availability = scoring.Unavailable
			entry.Score.Reason = "target_missing"
			if entry.ProviderMissing {
				entry.Score.Reason = "provider_missing"
			}
			entry.Score.Overall = 0
			entry.ReplayLimitation = entry.Score.Reason + "; historical breaker state unavailable"
		}
		result.Attempts = append(result.Attempts, entry)
	}
	result.SelectedTarget = selectedTarget(log.Chain)
	return result, nil
}

// replayShadowScore is the single conversion boundary between the pure scorer
// and the API model. Runtime data and the detached cost are deliberately copied
// from the same replay inputs so the explanation cannot drift from the score.
func replayShadowScore(s scoring.TargetScore, snapshot metrics.Snapshot, endpoint string, cost model.EffectiveCost) model.TargetShadowScore {
	return model.TargetShadowScore{
		TargetID:         s.TargetID,
		Tier:             s.Tier,
		Reliability:      s.Reliability,
		Latency:          s.Latency,
		TTFT:             s.TTFT,
		Capacity:         s.Capacity,
		CostEfficiency:   s.CostEfficiency,
		Confidence:       s.Confidence,
		Overall:          s.Overall,
		EstimatedCost:    s.EstimatedCost,
		SampleCount:      s.SampleCount,
		Availability:     s.Availability,
		Reason:           s.Reason,
		ExplorationBonus: s.ExplorationBonus,
		Metrics: model.TargetRuntimeSummary{
			Key:          snapshot.Key,
			Requests:     snapshot.Requests,
			Attempts:     snapshot.Attempts,
			Successes:    snapshot.Successes,
			Failures:     snapshot.Failures,
			Status429:    snapshot.Status429,
			Status5xx:    snapshot.Status5xx,
			Transport:    snapshot.Transport,
			ClientAborts: snapshot.ClientAborts,
			Truncated:    snapshot.Truncated,
			Downstream:   snapshot.Downstream,
			LastUsed:     snapshot.LastUsed,
			LastSuccess:  snapshot.LastSuccess,
			LastFailure:  snapshot.LastFailure,
		},
		MetricsFresh:    !snapshot.LastUsed.IsZero(),
		Endpoint:        endpoint,
		EndpointAssumed: true,
		Cost:            cost,
	}
}

func outcomeFor(log model.RequestLog) string {
	if log.StatusCode == 499 {
		return "aborted"
	}
	if len(log.Chain) == 0 {
		return "unknown"
	}
	committed := log.StatusCode >= 200 && log.StatusCode < 300
	for _, attempt := range log.Chain {
		status := model.AttemptOutcome(attempt.Status)
		if status == model.AttemptOutcomeSuccess || attempt.StatusCode >= 200 && attempt.StatusCode < 300 || attempt.FirstTokenMs > 0 {
			committed = true
		}
		if status == model.AttemptOutcomeClientAbort || attempt.StatusCode == 499 {
			return "aborted"
		}
		if status == model.AttemptOutcomeTruncated || status == model.AttemptOutcomeDownstreamError {
			if committed || attempt.StatusCode >= 200 && attempt.StatusCode < 300 {
				return "partial"
			}
		}
	}
	for _, attempt := range log.Chain {
		if model.AttemptOutcome(attempt.Status) == model.AttemptOutcomeSuccess {
			return "success"
		}
	}
	known := false
	for _, attempt := range log.Chain {
		status := model.AttemptOutcome(attempt.Status)
		if status.Valid() && status != model.AttemptOutcomeUnknown {
			known = true
			break
		}
	}
	if !known {
		return "unknown"
	}
	return "failure"
}

func isRealUpstreamAttempt(status model.AttemptOutcome) bool {
	return status != model.AttemptOutcomePreflightError && status != model.AttemptOutcomeCircuitOpen
}

// additionalRetriesFor counts only attempts that could have called upstream.
// AttemptOrder is not a billing ordinal because it also covers preflight and
// circuit entries.
func additionalRetriesFor(chain []model.RequestLogChainEntry, current model.RequestLogChainEntry) int {
	count := 0
	for _, attempt := range chain {
		if attempt == current {
			break
		}
		if isRealUpstreamAttempt(model.AttemptOutcome(attempt.Status)) {
			count++
		}
	}
	return count
}

func selectedTarget(chain []model.RequestLogChainEntry) string {
	for i := len(chain) - 1; i >= 0; i-- {
		a := chain[i]
		status := model.AttemptOutcome(a.Status)
		if status == model.AttemptOutcomeSuccess || status == model.AttemptOutcomeTruncated || status == model.AttemptOutcomeDownstreamError || status == model.AttemptOutcomeClientAbort || a.StatusCode == 499 {
			return a.TargetID
		}
	}
	return ""
}

// GetUsageTrends returns pre-aggregated usage-trend data (input / output /
// cache-creation / cache-hit tokens + cost, bucketed over the filtered range)
// for the usage-stats chart. The store picks hourly vs daily bucket size
// based on the date range; the frontend does not need to know the underlying
// resolution, just read UsageTrends.BucketSize.
func (a *App) GetUsageTrends(q model.UsageTrendsQuery) (*model.UsageTrends, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.GetUsageTrends(q)
}

// ----- Settings -----

func (a *App) GetSettings() (*model.Settings, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.GetSettings()
}

func (a *App) SaveSettings(s model.Settings) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.persistSettingsWithRollback(func() (*model.Settings, error) {
		if err := a.deps.Store.SaveSettings(s); err != nil {
			return nil, err
		}
		return &s, nil
	})
}

// ResetSettings restores and persists profile-aware defaults.
func (a *App) ResetSettings() (*model.Settings, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	var settings *model.Settings
	err := a.persistSettingsWithRollback(func() (*model.Settings, error) {
		var err error
		settings, err = a.deps.Store.ResetSettings()
		return settings, err
	})
	return settings, err
}

func (a *App) persistSettingsWithRollback(persist func() (*model.Settings, error)) error {
	previous, err := a.deps.Store.GetSettings()
	if err != nil {
		return fmt.Errorf("settings: read previous settings: %w", err)
	}
	updated, err := persist()
	if err != nil {
		return err
	}
	if a.deps.Proxy != nil {
		slog.Info("app: settings changed, restarting proxy")
		if restartErr := a.deps.Proxy.Restart(); restartErr != nil {
			rollbackErr := a.deps.Store.SaveSettings(*previous)
			var restoreErr error
			if rollbackErr == nil {
				restoreErr = a.deps.Proxy.Restart()
			}
			switch {
			case rollbackErr != nil:
				return fmt.Errorf("settings: restart proxy: %w; rollback settings failed: %v", restartErr, rollbackErr)
			case restoreErr != nil:
				return fmt.Errorf("settings: restart proxy: %w; restore previous listener failed: %v", restartErr, restoreErr)
			default:
				return fmt.Errorf("settings: restart proxy: %w; previous settings restored", restartErr)
			}
		}
	}
	a.updateLogger(updated.Logging)
	return nil
}

func (a *App) updateLogger(settings model.LoggingSettings) {
	logPath := filepath.Join(a.deps.Store.StorageDir(), "logs", "autoapi.log")
	_ = logger.Update(logger.Config{
		Enabled:    settings.Enabled,
		Level:      settings.Level,
		MaxSizeMB:  settings.MaxSizeMB,
		MaxAgeDays: settings.MaxAgeDays,
		MaxBackups: settings.MaxBackups,
		Path:       logPath,
	})
}

// GetRuntimePaths returns resolved profile paths for display in the UI.
func (a *App) GetRuntimePaths() (RuntimePaths, error) {
	if a.deps.Store == nil {
		return RuntimePaths{}, errNotImpl
	}
	storageDir := a.deps.Store.StorageDir()
	if storageDir == "" {
		return RuntimePaths{}, fmt.Errorf("store: storage dir not available")
	}
	return RuntimePaths{
		StorageDir: storageDir,
		LogPath:    filepath.Join(storageDir, "logs", "autoapi.log"),
	}, nil
}

// RuntimePaths exposes platform-specific storage locations to the UI so the
// Settings page can display where the SQLite database and log file live.
type RuntimePaths struct {
	StorageDir string `json:"storage_dir"`
	LogPath    string `json:"log_path"`
}

func (a *App) ListEndpoints() ([]model.Endpoint, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListEndpoints()
}

func (a *App) OpenStorageFolder() error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	dir := a.deps.Store.StorageDir()
	if dir == "" {
		return fmt.Errorf("store: storage dir not available")
	}
	switch stdruntime.GOOS {
	case "darwin":
		return exec.Command("open", dir).Start()
	case "windows":
		return exec.Command("explorer", dir).Start()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

// ----- Data export / purge -----

func (a *App) ExportData(format model.ExportFormat) (ExportResult, error) {
	if a.deps.Store == nil {
		return ExportResult{}, errNotImpl
	}
	slog.Info("app: export triggered", "format", format)
	data, filename, err := a.deps.Store.Export(format)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Filename: filename, Data: data}, nil
}

// ExportResult carries the file bytes back to the UI; the frontend triggers a
// browser download via a Blob.
type ExportResult struct {
	Filename string `json:"filename"`
	Data     []byte `json:"data"` // base64 over the Wails bridge
}

func (a *App) PurgeLogs(olderThanDays int) (int, error) {
	if a.deps.Store == nil {
		return 0, errNotImpl
	}
	slog.Info("app: purge logs triggered", "days", olderThanDays)
	return a.deps.Store.PurgeLogs(olderThanDays)
}

// ClearLogs deletes all request logs.
func (a *App) ClearLogs() (int, error) {
	if a.deps.Store == nil {
		return 0, errNotImpl
	}
	slog.Info("app: clear logs triggered")
	return a.deps.Store.ClearLogs()
}

// ----- Proxy control -----

func (a *App) GetProxyStatus() (ProxyStatus, error) {
	if a.deps.Proxy == nil {
		return ProxyStatus{Running: false}, nil
	}
	url := ""
	if a.deps.Proxy.IsRunning() {
		url = a.deps.Proxy.URL()
	}
	return ProxyStatus{Running: a.deps.Proxy.IsRunning(), URL: url}, nil
}

// ProxyStatus is a tiny DTO for the dashboard "service running" indicator.
type ProxyStatus struct {
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
}

// maskKey produces a display-only mask for a provider upstream key.
func maskKey(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	r := []rune(plaintext)
	n := len(r)
	if n <= 4 {
		return string(r[:1]) + "****"
	}
	prefixLen := 12
	if prefixLen > n-4 {
		prefixLen = n - 4
	}
	prefix := string(r[:prefixLen])
	suffix := string(r[n-4:])
	return prefix + "****" + suffix
}

// ----- App info / lifecycle -----

// GetAppInfo returns build-time and runtime metadata for the About section.
func (a *App) GetAppInfo() (model.AppInfo, error) {
	return a.appInfo, nil
}

// HideApp hides the window and removes the Dock icon, switching the app to
// a background accessory process. The HTTP proxy on :8344 keeps running.
// On non-macOS platforms, only the window is hidden.
func (a *App) HideApp() error {
	return a.EnterBackground()
}

// ShowApp restores the Dock icon and brings the window back to the
// foreground. This is the inverse of HideApp.
func (a *App) ShowApp() error {
	return a.EnterForeground()
}

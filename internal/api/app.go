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
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"time"

	"autoapi/internal/logger"
	"autoapi/internal/model"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Deps bundles the collaborators App needs. Any nil field makes the
// corresponding method return an "not yet implemented" error, so the contract
// is exercisable end-to-end from day one.
type Deps struct {
	Store   StoreService
	Service BusinessService
	Proxy   ProxyService
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

	// Models (lookup, populated by upstream)
	ListModels(providerID string) ([]model.Model, error)
	SetModelsActive(providerID string, modelNames []string, active bool) error
	DeleteModel(providerID, modelName string) error
	ClearProviderModels(providerID string) error
	UpdateModelName(providerID, oldName, newName string) error
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
	ListModelRules() ([]model.ModelRule, error)
	GetModelRule(id string) (*model.ModelRule, error)
	CreateModelRule(in model.ModelRuleInput) (*model.ModelRule, error)
	UpdateModelRule(id string, in model.ModelRuleInput) (*model.ModelRule, error)
	DeleteModelRule(id string) error
	ReorderModelRules(orderedIDs []string) error // no-op kept for API compatibility
	ReorderModelRuleTargets(ruleID string, orderedTargetIDs []string) error

	// Logs & stats
	QueryLogs(q model.LogQuery) ([]model.RequestLog, int64, error)
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
}

// BusinessService is the higher-level logic implemented by internal/service
// (provider testing, secret encryption, system health, etc.).
type BusinessService interface {
	TestProvider(providerID string) (*model.ProviderTestResult, error)
	TestAllProviders() ([]model.ProviderTestResult, error)
	FetchUpstreamModels(providerID string) ([]model.Model, error)
	TestModelLatency(providerID, modelName string) (*model.ModelTestResult, error)
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

// App is the single struct bound to the Wails runtime. All methods here are
// auto-generated as TypeScript bindings under frontend/wailsjs/go/main/App.
type App struct {
	ctx  context.Context
	deps Deps
}

// NewApp constructs an App with the given dependencies. Pass Deps{} (zero) to
// get a contract-only instance that returns ErrNotImplemented from every call.
func NewApp(deps Deps) *App {
	return &App{deps: deps}
}

// Startup is invoked by Wails OnStartup. We save the ctx for runtime calls
// (events, dialogs) and start the proxy if configured. Also wires a debounced
// "log:new" Wails event so the dashboard refreshes after new request logs are
// persisted, without the frontend having to poll QueryLogs.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	slog.Info("app: starting up")
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
	if a.deps.Proxy != nil {
		_ = a.deps.Proxy.Shutdown()
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

// ShowWindow makes the main window visible. On a hidden or minimised window
// this both shows and un-minimises it. Returns an error only when ctx is
// unavailable; runtime.WindowShow is a no-op when the window is already
// visible, so we do not propagate its lack of return value.
func (a *App) ShowWindow() error {
	if a.ctx == nil {
		return fmt.Errorf("app: ShowWindow called before Startup")
	}
	runtime.WindowShow(a.ctx)
	// WindowUnminimise has no effect if the window is not minimised; calling
	// it ensures the window is brought back to the foreground on platforms
	// where Show alone leaves a minimised window hidden. Wails v2 has no
	// WindowRaise, so this is the closest portable approximation.
	runtime.WindowUnminimise(a.ctx)
	return nil
}

// HideWindow hides the main window. Used when the user picks "hide" from the
// tray / menu bar instead of closing the app.
func (a *App) HideWindow() error {
	if a.ctx == nil {
		return fmt.Errorf("app: HideWindow called before Startup")
	}
	runtime.WindowHide(a.ctx)
	return nil
}

// Quit terminates the application. The Wails runtime.Quit helper requires a
// non-nil ctx; we silently no-op when the context has not been set yet
// (e.g. the menu is clicked during shutdown), which is preferable to the
// log.Fatal that runtime.Quit would otherwise trigger.
func (a *App) Quit() {
	if a.ctx == nil {
		return
	}
	runtime.Quit(a.ctx)
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

// UpdateModelName renames a model in a provider's catalog. Both names are
// trimmed; an empty new name is rejected.
func (a *App) UpdateModelName(providerID, oldName, newName string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("model name must not be empty")
	}
	return a.deps.Store.UpdateModelName(providerID, oldName, newName)
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
	return a.deps.Store.ListModelRules()
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

// ReorderModelRules is kept as a no-op for API compatibility. Drag-reorder
// was removed when route rules became model rules because model rules are
// keyed by a unique Name (the client-facing model name) and there is no
// meaningful order to preserve.
func (a *App) ReorderModelRules(orderedIDs []string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.ReorderModelRules(orderedIDs)
}

// ReorderModelRuleTargets reorders the targets within a model rule by updating
// only their tier values. Unlike UpdateModelRule, it does NOT delete or recreate
// targets — counters and IDs are fully preserved.
func (a *App) ReorderModelRuleTargets(ruleID string, orderedTargetIDs []string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.ReorderModelRuleTargets(ruleID, orderedTargetIDs)
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

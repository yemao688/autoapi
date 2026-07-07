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

	"autoapi/internal/model"
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

	// API keys
	ListAPIKeys() ([]model.ApiKey, error)
	CreateAPIKey(in model.ApiKeyInput) (*model.ApiKey, error)
	UpdateAPIKey(id string, in model.ApiKeyInput) (*model.ApiKey, error)
	DeleteAPIKey(id string) error

	// Routes
	ListRoutes() ([]model.Route, error)
	GetRoute(id string) (*model.Route, error)
	CreateRoute(in model.RouteInput) (*model.Route, error)
	UpdateRoute(id string, in model.RouteInput) (*model.Route, error)
	DeleteRoute(id string) error
	ReorderRoutes(orderedIDs []string) error

	// Logs & stats
	QueryLogs(q model.LogQuery) ([]model.RequestLog, int64, error)
	Dashboard() (*model.DashboardData, error)
	UsageStats() (*model.UsageStats, error)
	PurgeLogs(olderThanDays int) (int, error)

	// Settings
	GetSettings() (*model.Settings, error)
	SaveSettings(s model.Settings) error
	ListEndpoints() ([]model.Endpoint, error)

	// Export / import
	Export(format model.ExportFormat) ([]byte, string, error) // (data, filename, err)
}

// BusinessService is the higher-level logic implemented by internal/service
// (provider testing, secret encryption, master password, etc.).
type BusinessService interface {
	TestProvider(providerID string) (*model.ProviderTestResult, error)
	TestAllProviders() ([]model.ProviderTestResult, error) // keyed by provider id via result? — return list parallel to providers
	SetMasterPassword(password string) error
	ChangeMasterPassword(old, new string) error
	HasMasterPassword() bool
	Unlock(password string) error
	IsUnlocked() bool
}

// ProxyService controls the local OpenAI-compatible HTTP gateway.
type ProxyService interface {
	Start() error
	Stop() error
	IsRunning() bool
	// Restart rebinds the listener (called when the user changes port/bind in settings).
	Restart() error
}

// App is the single struct bound to the Wails runtime. All methods here are
// auto-generated as TypeScript bindings under frontend/wailsjs/go/main/App.
type App struct {
	ctx   context.Context
	deps  Deps
}

// NewApp constructs an App with the given dependencies. Pass Deps{} (zero) to
// get a contract-only instance that returns ErrNotImplemented from every call.
func NewApp(deps Deps) *App {
	return &App{deps: deps}
}

// Startup is invoked by Wails OnStartup. We save the ctx for runtime calls
// (events, dialogs) and start the proxy if configured.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	if a.deps.Proxy != nil {
		_ = a.deps.Proxy.Start() // best-effort; surface via IsRunning()
	}
}

// Shutdown is invoked by Wails OnShutdown. Stop the proxy cleanly.
func (a *App) Shutdown(ctx context.Context) {
	if a.deps.Proxy != nil {
		_ = a.deps.Proxy.Stop()
	}
}

// ----- Lifecycle / system -----

// GetSystemHealth returns the live dashboard telemetry.
func (a *App) GetSystemHealth() (model.ServiceHealth, error) {
	if a.deps.Store == nil {
		return model.ServiceHealth{}, errNotImpl
	}
	d, err := a.deps.Store.Dashboard()
	if err != nil || d == nil {
		return model.ServiceHealth{}, err
	}
	return d.ServiceHealth, nil
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
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.CreateProvider(in)
}

func (a *App) UpdateProvider(id string, in model.ProviderInput) (*model.Provider, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.UpdateProvider(id, in)
}

func (a *App) DeleteProvider(id string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.DeleteProvider(id)
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

func (a *App) ListModels(providerID string) ([]model.Model, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListModels(providerID)
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

// ----- Routes -----

func (a *App) ListRoutes() ([]model.Route, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListRoutes()
}

func (a *App) GetRoute(id string) (*model.Route, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.GetRoute(id)
}

func (a *App) CreateRoute(in model.RouteInput) (*model.Route, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.CreateRoute(in)
}

func (a *App) UpdateRoute(id string, in model.RouteInput) (*model.Route, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.UpdateRoute(id, in)
}

func (a *App) DeleteRoute(id string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.DeleteRoute(id)
}

func (a *App) ReorderRoutes(orderedIDs []string) error {
	if a.deps.Store == nil {
		return errNotImpl
	}
	return a.deps.Store.ReorderRoutes(orderedIDs)
}

// ----- Dashboard / usage -----

func (a *App) GetDashboard() (*model.DashboardData, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.Dashboard()
}

func (a *App) GetUsageStats() (*model.UsageStats, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.UsageStats()
}

func (a *App) QueryLogs(q model.LogQuery) ([]model.RequestLog, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	logs, _, err := a.deps.Store.QueryLogs(q)
	return logs, err
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
	if err := a.deps.Store.SaveSettings(s); err != nil {
		return err
	}
	// Port/bind changed → restart proxy so it picks up the new listener.
	if a.deps.Proxy != nil {
		return a.deps.Proxy.Restart()
	}
	return nil
}

func (a *App) ListEndpoints() ([]model.Endpoint, error) {
	if a.deps.Store == nil {
		return nil, errNotImpl
	}
	return a.deps.Store.ListEndpoints()
}

// ----- Data export / purge -----

func (a *App) ExportData(format model.ExportFormat) (ExportResult, error) {
	if a.deps.Store == nil {
		return ExportResult{}, errNotImpl
	}
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
	return a.deps.Store.PurgeLogs(olderThanDays)
}

// ----- Master password / unlock -----

func (a *App) HasMasterPassword() (bool, error) {
	if a.deps.Service == nil {
		return false, errNotImpl
	}
	return a.deps.Service.HasMasterPassword(), nil
}

func (a *App) IsUnlocked() (bool, error) {
	if a.deps.Service == nil {
		return false, errNotImpl
	}
	return a.deps.Service.IsUnlocked(), nil
}

func (a *App) SetMasterPassword(password string) error {
	if a.deps.Service == nil {
		return errNotImpl
	}
	return a.deps.Service.SetMasterPassword(password)
}

func (a *App) ChangeMasterPassword(old, new string) error {
	if a.deps.Service == nil {
		return errNotImpl
	}
	return a.deps.Service.ChangeMasterPassword(old, new)
}

func (a *App) Unlock(password string) error {
	if a.deps.Service == nil {
		return errNotImpl
	}
	return a.deps.Service.Unlock(password)
}

// ----- Proxy control -----

func (a *App) GetProxyStatus() (ProxyStatus, error) {
	if a.deps.Proxy == nil {
		return ProxyStatus{Running: false}, nil
	}
	return ProxyStatus{Running: a.deps.Proxy.IsRunning()}, nil
}

// ProxyStatus is a tiny DTO for the dashboard "service running" indicator.
type ProxyStatus struct {
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
}

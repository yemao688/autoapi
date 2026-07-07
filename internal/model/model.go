// Package model defines the domain entities and DTOs exchanged between the
// Go backend and the Vue frontend via the Wails Bind bridge.
//
// Design notes:
//   - IDs are string UUIDs (not autoincrement) so they can be generated client-side
//     or in tests without round-tripping the DB.
//   - All timestamps are Unix milliseconds (int64) for simple JSON marshalling and
//     easy "X minutes ago" rendering on the frontend.
//   - API keys are NEVER returned in cleartext. List endpoints return KeyPrefix +
//     KeySuffix only; the full key is encrypted at rest (key_ciphertext column).
//   - Models are a LOOKUP table populated from upstream /v1/models during provider
//     testing, not a first-class user-CRUD entity (per oracle review §4).
package model

import "time"

// nowMs returns current time in Unix milliseconds.
func nowMs() int64 { return time.Now().UnixMilli() }

// ----- Enums / shared types -----

// ProviderStatus is the connectivity state of an upstream provider.
type ProviderStatus string

const (
	ProviderStatusConnected ProviderStatus = "connected"
	ProviderStatusError     ProviderStatus = "error"
	ProviderStatusUnknown   ProviderStatus = "unknown"
)

// Permission scope for an API key.
type KeyPermission string

const (
	KeyPermissionReadWrite KeyPermission = "read_write"
	KeyPermissionReadOnly  KeyPermission = "read_only"
)

// Environment tag for an API key (production / test / disabled).
type KeyEnvironment string

const (
	KeyEnvProduction KeyEnvironment = "production"
	KeyEnvTest       KeyEnvironment = "test"
	KeyEnvDisabled   KeyEnvironment = "disabled"
)

// RouteActionType describes what a route target does when matched.
type RouteActionType string

const (
	RouteActionForward RouteActionType = "forward" // forward to provider+model
	RouteActionSkip    RouteActionType = "skip"    // skip a provider
)

// ConditionOperator is the comparison operator for a route condition.
// Mirrors the operators shown in the prototype routes.html.
type ConditionOperator string

const (
	OpMatches ConditionOperator = "matches" // glob on a string field
	OpEquals  ConditionOperator = "equals"
	OpLT      ConditionOperator = "lt"
	OpGT      ConditionOperator = "gt"
	OpBetween ConditionOperator = "between" // value is "lo,hi"
	OpIn      ConditionOperator = "in"      // value is comma list
)

// ----- Domain entities -----

// Provider is an upstream LLM gateway (OpenAI / Anthropic / DeepSeek / Moonshot / GLM / custom).
type Provider struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	BaseURL       string         `json:"base_url"`
	Status        ProviderStatus `json:"status"`
	APIKeyID      string         `json:"api_key_id"` // FK to api_keys.id (nullable)
	APIKeyRef     string         `json:"api_key_ref,omitempty"` // display only: "sk-***3fA9"
	ModelsCount   int            `json:"models_count"`
	MonthlyTokens int64          `json:"monthly_tokens"`
	AvgLatencyMs  int            `json:"avg_latency_ms"`
	LastTestedAt  int64          `json:"last_tested_at"` // ms; 0 = never
	ErrorMessage  string         `json:"error_message,omitempty"`
	IsCustom      bool           `json:"is_custom"` // true for self-hosted / OpenAI-compatible gateways
	CreatedAt     int64          `json:"created_at"`
	UpdatedAt     int64          `json:"updated_at"`
}

// Model is a model offered by a provider (lookup table, populated from upstream).
type Model struct {
	ID           string `json:"id"`
	ProviderID   string `json:"provider_id"`
	Name         string `json:"name"`
	ContextWindow int   `json:"context_window"` // max tokens, 0 if unknown
	CreatedAt    int64 `json:"created_at"`
}

// ApiKey is a credential used to authenticate against an upstream provider.
// The cleartext key is stored AES-256-GCM encrypted; only prefix+suffix are
// ever returned to the UI.
type ApiKey struct {
	ID           string         `json:"id"`
	ProviderID   string         `json:"provider_id"` // nullable; custom keys may be provider-less
	Name         string         `json:"name"`
	KeyPrefix    string         `json:"key_prefix"`   // e.g. "sk-prod-"
	KeySuffix    string         `json:"key_suffix"`   // e.g. "3fA9"
	KeyMasked    string         `json:"key_masked"`   // pre-rendered "sk-prod-****3fA9"
	Permission   KeyPermission  `json:"permission"`
	Environment  KeyEnvironment `json:"environment"`
	MonthlyTokens int64         `json:"monthly_tokens"`
	LastUsedAt   int64          `json:"last_used_at"`  // ms; 0 = never
	ExpiresAt    int64          `json:"expires_at"`    // ms; 0 = no expiry
	CreatedAt    int64          `json:"created_at"`
	UpdatedAt    int64          `json:"updated_at"`
}

// Route is an ordered routing rule. Lower priority number = higher precedence.
type Route struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Priority    int    `json:"priority"` // 1-based
	Enabled     bool   `json:"enabled"`
	CreatedAt   int64 `json:"created_at"`
	UpdatedAt   int64 `json:"updated_at"`

	Conditions []RouteCondition `json:"conditions"`
	Targets    []RouteTarget    `json:"targets"`

	// Aggregated stats for display
	MonthlyHits    int64   `json:"monthly_hits"`
	MonthlySavings float64 `json:"monthly_savings"`
}

// RouteCondition is a single match clause within a route.
type RouteCondition struct {
	ID       string            `json:"id"`
	RouteID  string            `json:"route_id"`
	Field    string            `json:"field"`    // e.g. "model", "header.x-priority", "estimated_tokens", "task", "time.hour"
	Operator ConditionOperator `json:"operator"`
	Value    string            `json:"value"`    // semantics depend on operator
}

// RouteTarget is what happens when a route matches.
type RouteTarget struct {
	ID         string          `json:"id"`
	RouteID    string          `json:"route_id"`
	ProviderID string          `json:"provider_id"`
	ModelName  string          `json:"model_name"` // empty when action=skip
	Action     RouteActionType `json:"action"`
}

// RequestLog is one proxied request through the gateway.
type RequestLog struct {
	ID           string `json:"id"`
	Timestamp    int64  `json:"timestamp"` // ms
	StatusCode   int    `json:"status_code"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	LatencyMs    int    `json:"latency_ms"`
	RouteID      string `json:"route_id"` // empty = default route
	RouteLabel   string `json:"route_label"`
	APIKeyID     string `json:"api_key_id"`
	Error        string `json:"error,omitempty"`
}

// ----- Aggregation DTOs (dashboard / usage-stats) -----

// Stat is a single KPI card value with delta context.
type Stat struct {
	Label string  `json:"label"`
	Value string  `json:"value"` // pre-formatted ("245,832", "¥458.76")
	Delta string  `json:"delta"` // "+12.4%", "-3.2%"
	Trend string  `json:"trend"`  // "up" | "down" | "flat"
	Note  string  `json:"note,omitempty"`
}

// TokenTrendPoint is one bucket of a token-usage time series.
type TokenTrendPoint struct {
	Date         string `json:"date"` // YYYY-MM-DD
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Cost         float64 `json:"cost"`
}

// ProviderShare is one slice of the provider pie chart.
type ProviderShare struct {
	ProviderID   string  `json:"provider_id"`
	ProviderName string  `json:"provider_name"`
	Tokens       int64   `json:"tokens"`
	Percent      float64 `json:"percent"`
}

// ModelRanking is one row of the model usage ranking table.
type ModelRanking struct {
	Model        string  `json:"model"`
	ProviderName string  `json:"provider_name"`
	Requests     int64   `json:"requests"`
	Tokens       int64   `json:"tokens"`
	Cost         float64 `json:"cost"`
}

// DashboardData aggregates everything the dashboard page needs in one call.
type DashboardData struct {
	Stats           []Stat             `json:"stats"`
	TokenTrend      []TokenTrendPoint  `json:"token_trend"`     // last 7 days
	Providers       []Provider         `json:"providers"`
	RecentActivity  []RequestLog       `json:"recent_activity"` // last 10
	ServiceHealth   ServiceHealth      `json:"service_health"`
}

// ServiceHealth is the live system telemetry shown on the dashboard.
type ServiceHealth struct {
	Status            string `json:"status"` // "running" | "paused" | "error"
	UptimeSeconds     int64  `json:"uptime_seconds"`
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryMB          int    `json:"memory_mb"`
	ActiveConnections int    `json:"active_connections"`
	WebSocketCount    int    `json:"websocket_count"`
	HTTPCount         int    `json:"http_count"`
	ProxyURL          string `json:"proxy_url"` // e.g. "http://0.0.0.0:8344"
}

// UsageStats aggregates the usage-stats page data (token view + log view).
type UsageStats struct {
	TokenStats   []Stat             `json:"token_stats"`
	TokenTrend30 []TokenTrendPoint  `json:"token_trend_30"` // last 30 days
	Providers    []ProviderShare    `json:"providers"`
	ModelRanking []ModelRanking     `json:"model_ranking"`

	LogStats []Stat        `json:"log_stats"`
	Logs     []RequestLog  `json:"logs"`
	LogTotal int64         `json:"log_total"`
}

// ----- Settings -----

// Settings is the complete user configuration, split into the 7 sections shown
// in the prototype settings.html. Stored as a single JSON blob for simplicity;
// only the network-impacting fields (server) are also kept denormalised so the
// proxy can read them without parsing JSON.
type Settings struct {
	General    GeneralSettings    `json:"general"`
	Appearance AppearanceSettings `json:"appearance"`
	Routing    RoutingSettings    `json:"routing"`
	Server     ServerSettings     `json:"server"`
	Data       DataSettings       `json:"data"`
	Advanced   AdvancedSettings   `json:"advanced"`
}

type GeneralSettings struct {
	LaunchAtLogin    bool   `json:"launch_at_login"`
	StartupAction    string `json:"startup_action"` // "show" | "hide"
	MenuBarItem      bool   `json:"menu_bar_item"`
	CloseAction      string `json:"close_action"`   // "background" | "quit"
}

type AppearanceSettings struct {
	Theme       string `json:"theme"`        // "light" | "dark" | "system"
	Density     string `json:"density"`      // "compact" | "standard" | "comfortable"
	AccentColor string `json:"accent_color"` // hex "#0071e3"
}

type RoutingSettings struct {
	DefaultProviderID string `json:"default_provider_id"`
	DefaultModel      string `json:"default_model"`
	AutoRetry         bool   `json:"auto_retry"`
	StreamingSSE      bool   `json:"streaming_sse"`
}

type ServerSettings struct {
	Port        int    `json:"port"`         // 8344
	BindAddress string `json:"bind_address"` // "0.0.0.0"
	// Endpoints lists the OpenAI-compatible paths served by the proxy.
	// Display-only — derived from the registered chi routes at runtime.
}

type DataSettings struct {
	LogRetentionDays int    `json:"log_retention_days"` // 0 = keep forever
	StoragePath      string `json:"storage_path"`       // display-only
}

type AdvancedSettings struct {
	DebugMode         bool   `json:"debug_mode"`
	Experimental      bool   `json:"experimental"`
	HTTPProxy         string `json:"http_proxy"` // "system" | "none" | url
}

// Endpoint describes one proxy URL shown on the settings → server page.
type Endpoint struct {
	Method string `json:"method"` // "POST" | "GET" | "WS"
	Path   string `json:"path"`   // "/v1/chat/completions"
	Desc   string `json:"desc"`
}

// ----- API key plaintext (write-only — never returned) -----

// ApiKeyInput is the payload for creating/updating an API key. The cleartext
// Key field is encrypted immediately and discarded.
type ApiKeyInput struct {
	ProviderID  string         `json:"provider_id"`
	Name        string         `json:"name"`
	Key         string         `json:"key"` // cleartext — write-only
	Permission  KeyPermission  `json:"permission"`
	Environment KeyEnvironment `json:"environment"`
	ExpiresAt   int64          `json:"expires_at"`
}

// ----- Provider test result -----

// ProviderTestResult is returned by the "Test" button on a provider card.
type ProviderTestResult struct {
	OK         bool     `json:"ok"`
	LatencyMs  int      `json:"latency_ms"`
	Error      string   `json:"error,omitempty"`
	Models     []string `json:"models"` // model names discovered via /v1/models
}

// ----- Request payloads -----

type ProviderInput struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	APIKeyID  string `json:"api_key_id"`
	IsCustom  bool   `json:"is_custom"`
}

type RouteInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Priority    int               `json:"priority"`
	Enabled     bool              `json:"enabled"`
	Conditions  []RouteCondition  `json:"conditions"`
	Targets     []RouteTarget     `json:"targets"`
}

// LogQuery is the filter for the usage-stats → logs view.
type LogQuery struct {
	StartDate int64    `json:"start_date"` // ms; 0 = no lower bound
	EndDate   int64    `json:"end_date"`
	Provider  string   `json:"provider"`   // "" = all
	Status    string   `json:"status"`     // "" | "success" | "failed" | "rate_limited"
	Page      int      `json:"page"`
	PageSize  int      `json:"page_size"`
}

// ExportFormat selects the export payload type.
type ExportFormat string

const (
	ExportAllJSON    ExportFormat = "all_json"
	ExportSettingsJSON ExportFormat = "settings_json"
	ExportTokensCSV  ExportFormat = "tokens_csv"
	ExportLogsCSV    ExportFormat = "logs_csv"
)

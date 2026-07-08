// Package model defines the domain entities and DTOs exchanged between the
// Go backend and the Vue frontend via the Wails Bind bridge.
//
// Design notes:
//   - IDs are string UUIDs (not autoincrement) so they can be generated client-side
//     or in tests without round-tripping the DB.
//   - All timestamps are Unix milliseconds (int64) for simple JSON marshalling and
//     easy "X minutes ago" rendering on the frontend.
//   - API keys are simple access tokens whose value is the row ID; the cleartext
//     token is never stored separately and remains visible in the list.
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
// It stores its own encrypted upstream credential; API keys are now simple access tokens.
type Provider struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	BaseURL       string         `json:"base_url"`
	Status        ProviderStatus `json:"status"`
	KeyCiphertext []byte         `json:"-"` // encrypted upstream provider key
	KeyNonce      []byte         `json:"-"` // AES-GCM nonce for the key
	KeyMasked     string         `json:"key_masked"` // display-only, e.g. "sk-****abcd"
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
	ID            string `json:"id"`
	ProviderID    string `json:"provider_id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window"` // max tokens, 0 if unknown
	OwnedBy       string `json:"owned_by"`
	Active        bool   `json:"active"`
	LatencyMs     int    `json:"latency_ms"`
	UpdatedAt     int64  `json:"updated_at"`
	CreatedAt     int64  `json:"created_at"`
}

// ModelTestResult is returned by the per-model latency test.
type ModelTestResult struct {
	OK        bool  `json:"ok"`
	LatencyMs int   `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// ApiKey is an access token for the autoapi proxy. The token value is the row
// ID itself; no secret material is stored in this table.
type ApiKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expires_at"` // ms; 0 = no expiry
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Route is an ordered routing rule. The list order from the store is the
// evaluation order (top = highest priority); see internal/store/routes.go
// (ListRoutes ORDER BY priority, ReorderRoutes writes from slice index).
type Route struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   int64  `json:"created_at"`
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
	ID           string `json:"id"`
	RouteID      string `json:"route_id"`
	ProviderID   string `json:"provider_id"`
	ModelName    string `json:"model_name"`
	MaxRetries   int    `json:"max_retries"`   // 0 = try once, no in-target retry; N = up to N additional attempts on retryable errors before falling through
	HitCount     int64  `json:"hit_count"`     // incremented once on successful dispatch
	FailureCount int64  `json:"failure_count"` // incremented on each failed attempt (hit + failure = total attempts)
}

// RequestLog is one proxied request through the gateway.
type RequestLog struct {
	ID           string  `json:"id"`
	Timestamp    int64   `json:"timestamp"` // ms
	StatusCode   int     `json:"status_code"`
	ProviderID   string  `json:"provider_id"`
	ProviderName string  `json:"provider_name"`
	Model        string  `json:"model"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Cost         float64 `json:"cost"` // estimated USD
	LatencyMs    int     `json:"latency_ms"`
	FirstTokenMs int     `json:"first_token_ms"` // TTFT for streaming; 0 for non-streaming
	IsStream     bool    `json:"is_stream"`      // true if the request was streaming
	RouteID      string  `json:"route_id"` // empty = default route
	RouteLabel   string  `json:"route_label"`
	APIKeyID     string  `json:"api_key_id"`
	Error        string  `json:"error,omitempty"`
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
	Cost         float64 `json:"cost"`
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

// ApiKeyInput is the payload for creating/updating an API key.
// API keys are simple access tokens; only the name and expiry are editable.
type ApiKeyInput struct {
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expires_at"`
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
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	UpstreamKey string `json:"upstream_key"` // cleartext provider key; encrypted by App layer before storage
	IsCustom    bool   `json:"is_custom"`
}

type RouteInput struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Enabled     bool              `json:"enabled"`
	Conditions  []RouteCondition  `json:"conditions"`
	Targets     []RouteTarget     `json:"targets"`
}

// LogQuery is the filter for the usage-stats → logs view.
//
// All filter fields are optional (empty/zero means "no filter for this field").
// Status accepts: "" (all), "success", "failed", "rate_limited".
type LogQuery struct {
	StartDate int64  `json:"start_date"` // ms; 0 = no lower bound
	EndDate   int64  `json:"end_date"`   // ms; 0 = no upper bound
	Provider  string `json:"provider"`   // exact match on provider_id; "" = all
	RouteID   string `json:"route_id"`   // exact match on route_id; "" = all
	Model     string `json:"model"`      // exact match on model; "" = all
	Status    string `json:"status"`     // "" | "success" | "failed" | "rate_limited"
	Search    string `json:"search"`     // LIKE %term% across model/route_label/error
	Page      int    `json:"page"`       // 1-indexed; <=0 normalised to 1
	PageSize  int    `json:"page_size"`  // <=0 or >1000 normalised to 50
}

// LogQueryResult is the paged response for the logs view. Total reflects the
// number of rows that match the filter (ignoring page/page_size); logs is the
// current page slice (possibly empty). This mirrors the ExportResult pattern
// so the Wails bridge returns a single object instead of two separate values.
type LogQueryResult struct {
	Logs  []RequestLog `json:"logs"`
	Total int64        `json:"total"`
}

// ----- Chart aggregation DTOs (Phase 4 — request-log charts) -----

// TimeBucket is one slice of the time-series chart. The Bucket label is a
// locale-neutral ISO string that the frontend can format as needed: "YYYY-MM-DD"
// for daily, "YYYY-MM-DD HH:00" for hourly.
type TimeBucket struct {
	Bucket       string  `json:"bucket"`         // "YYYY-MM-DD" or "YYYY-MM-DD HH:00"
	Success      int64   `json:"success"`        // status 2xx with no error
	RateLimited  int64   `json:"rate_limited"`   // status == 429
	Error        int64   `json:"error"`          // status >= 400 (excluding 429) or error != ''
	InputTokens  int64   `json:"input_tokens"`   // sum
	OutputTokens int64   `json:"output_tokens"`  // sum
	Cost         float64 `json:"cost"`           // sum
	AvgLatencyMs int64   `json:"avg_latency_ms"` // AVG(latency_ms)
	AvgTTFTMs    int64   `json:"avg_ttft_ms"`    // AVG(first_token_ms) over streamed rows
}

// StatusBreakdown is one slice of the status-code donut chart. Percent is
// rounded to 2 decimal places out of 100. Labels are "2xx", "429", "错误",
// or "其他" for statuses that do not fit the above classes.
type StatusBreakdown struct {
	Label   string  `json:"label"`   // "2xx" | "4xx" | "5xx" | "429"
	Count   int64   `json:"count"`   // request count in this class
	Percent float64 `json:"percent"` // share of the filtered set, 0..100
}

// ChartAggregates is the response for a single chart-data fetch. Range is a
// free-form description (e.g. "last_24h") so the frontend can show a label
// without re-formatting start/end itself; BucketSize is the resolution used.
type ChartAggregates struct {
	Range           string           `json:"range"`            // e.g. "2024-01-01..2024-01-31"
	BucketSize      string           `json:"bucket_size"`      // "hour" | "day"
	Buckets         []TimeBucket     `json:"buckets"`          // time series, ordered ascending
	StatusBreakdown []StatusBreakdown `json:"status_breakdown"` // status-code donut
	ProviderShares  []ProviderShare   `json:"provider_shares"`  // reused pie series
}

// ChartQuery is the filter for GetChartAggregates. Same semantics as LogQuery
// but without pagination — chart data always includes the full filtered range.
// Status, Page, and PageSize are intentionally omitted because the chart
// surface does not need them.
type ChartQuery struct {
	StartDate int64  `json:"start_date"` // ms; 0 = no lower bound
	EndDate   int64  `json:"end_date"`   // ms; 0 = no upper bound
	Provider  string `json:"provider"`   // exact match on provider_id; "" = all
	RouteID   string `json:"route_id"`   // exact match on route_id; "" = all
	Model     string `json:"model"`      // exact match on model; "" = all
	Search    string `json:"search"`     // LIKE %term% across model/route_label/error
}

// ExportFormat selects the export payload type.
type ExportFormat string

const (
	ExportAllJSON    ExportFormat = "all_json"
	ExportSettingsJSON ExportFormat = "settings_json"
	ExportTokensCSV  ExportFormat = "tokens_csv"
	ExportLogsCSV    ExportFormat = "logs_csv"
)

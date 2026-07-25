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

// ----- Domain entities -----

// Provider is an upstream LLM gateway (OpenAI / Anthropic / DeepSeek / Moonshot / GLM / custom).
// It stores its own encrypted upstream credential; API keys are now simple access tokens.
type Provider struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	BaseURL          string         `json:"base_url"`
	Status           ProviderStatus `json:"status"`
	KeyCiphertext    []byte         `json:"-"`          // encrypted upstream provider key
	KeyNonce         []byte         `json:"-"`          // AES-GCM nonce for the key
	KeyMasked        string         `json:"key_masked"` // display-only, e.g. "sk-****abcd"
	ModelsCount      int            `json:"models_count"`
	MonthlyTokens    int64          `json:"monthly_tokens"`
	AvgLatencyMs     int            `json:"avg_latency_ms"`
	LastTestedAt     int64          `json:"last_tested_at"` // ms; 0 = never
	ErrorMessage     string         `json:"error_message,omitempty"`
	IsCustom         bool           `json:"is_custom"` // true for self-hosted / OpenAI-compatible gateways
	ResponsesEnabled bool           `json:"responses_enabled"`
	MessagesEnabled  bool           `json:"messages_enabled"`
	GeminiEnabled    bool           `json:"gemini_enabled"`
	Enabled          bool           `json:"enabled"`
	CreatedAt        int64          `json:"created_at"`
	UpdatedAt        int64          `json:"updated_at"`
}

type ProviderCapability struct {
	ProviderID string `json:"provider_id"`
	Protocol   string `json:"protocol"`
	Feature    string `json:"feature"`
	Enabled    bool   `json:"enabled"`
	Source     string `json:"source"`
	UpdatedAt  int64  `json:"updated_at"`
}

// Feature is a canonical capability that an inbound request may require.
type Feature string

const (
	FeatureTools            Feature = "tools"
	FeatureVision           Feature = "vision"
	FeatureReasoning        Feature = "reasoning"
	FeatureStructuredOutput Feature = "structured_output"
	FeatureStateful         Feature = "stateful"
	FeatureCacheControl     Feature = "cache_control"
	FeatureAudio            Feature = "audio"
	FeatureDocument         Feature = "document"
	FeatureStreaming        Feature = "streaming"
)

// RequestRequirements captures the canonical capabilities required by an
// inbound request, plus flags that constrain whether conversion is safe.
type RequestRequirements struct {
	Features        []Feature `json:"features"`
	NativeOnly      bool      `json:"native_only"`
	UnknownSemantic bool      `json:"unknown_semantic"`
}

func (r *RequestRequirements) Has(f Feature) bool {
	if r == nil {
		return false
	}
	for _, x := range r.Features {
		if x == f {
			return true
		}
	}
	return false
}

const (
	FeatureCapabilityEnforcementObserve = "observe"
	FeatureCapabilityEnforcementEnforce = "enforce"
)

// NormalizeFeatureCapabilityEnforcement coerces empty or unexpected values to
// the safe default "observe".
func NormalizeFeatureCapabilityEnforcement(v string) string {
	switch v {
	case FeatureCapabilityEnforcementEnforce:
		return v
	default:
		return FeatureCapabilityEnforcementObserve
	}
}

type ModelCapability struct {
	ProviderID string `json:"provider_id"`
	ModelName  string `json:"model_name"`
	Protocol   string `json:"protocol"`
	Feature    string `json:"feature"`
	Enabled    bool   `json:"enabled"`
	Source     string `json:"source"`
	UpdatedAt  int64  `json:"updated_at"`
}

type ProviderModelRef struct {
	ProviderID string `json:"provider_id"`
	ModelName  string `json:"model_name"`
}

// Model is a model offered by a provider (lookup table, populated from upstream).
type Model struct {
	ID            string  `json:"id"`
	ProviderID    string  `json:"provider_id"`
	Name          string  `json:"name"`
	ContextWindow int     `json:"context_window"` // max tokens, 0 if unknown
	OwnedBy       string  `json:"owned_by"`
	Active        bool    `json:"active"`
	RequestPrice  float64 `json:"request_price"` // USD per upstream request
	LatencyMs     int     `json:"latency_ms"`
	UpdatedAt     int64   `json:"updated_at"`
	CreatedAt     int64   `json:"created_at"`
}

// ProviderModelUpdate atomically changes a provider model's public name and
// per-call price. All model-rule target references are changed with it.
type ProviderModelUpdate struct {
	ProviderID   string  `json:"provider_id"`
	OldName      string  `json:"old_name"`
	Name         string  `json:"name"`
	RequestPrice float64 `json:"request_price"`
}

// ModelTestResult is returned by the per-model latency test.
type ModelTestResult struct {
	OK        bool   `json:"ok"`
	LatencyMs int    `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// ModelChatTestResult is returned by the per-provider model chat test.
type ModelChatTestResult struct {
	OK                 bool   `json:"ok"`
	Response           string `json:"response"`
	HTTPStatus         int    `json:"http_status"`
	LatencyMs          int    `json:"latency_ms"`
	FirstByteLatencyMs int    `json:"first_byte_latency_ms,omitempty"`
	FinishReason       string `json:"finish_reason,omitempty"`
	Error              string `json:"error,omitempty"`
}

// UpstreamMonitorModel identifies one enabled upstream model that can be
// probed by the monitoring screen.
type UpstreamMonitorModel struct {
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	ModelName    string `json:"model_name"`
	Protocol     string `json:"protocol"`
	Enabled      bool   `json:"enabled"`
}

type UpstreamMonitorSelection struct {
	ProviderID string `json:"provider_id"`
	ModelName  string `json:"model_name"`
	Protocol   string `json:"protocol"`
}

type UpstreamMonitorResult struct {
	ProviderID         string `json:"provider_id"`
	ModelName          string `json:"model_name"`
	Protocol           string `json:"protocol"`
	Status             string `json:"status"` // available, empty, or error
	HTTPStatus         int    `json:"http_status"`
	Detail             string `json:"detail,omitempty"`
	Response           string `json:"response,omitempty"`
	Error              string `json:"error,omitempty"`
	FirstByteLatencyMs int    `json:"first_byte_latency_ms"`
	TotalLatencyMs     int    `json:"total_latency_ms"`
}

type UpstreamMonitorBatch struct {
	Results       []UpstreamMonitorResult `json:"results"`
	CompletedAtMs int64                   `json:"completed_at_ms"`
	CompletionMs  int                     `json:"completion_ms"`
	Total         int                     `json:"total"`
	Available     int                     `json:"available"`
	Empty         int                     `json:"empty"`
	Errors        int                     `json:"errors"`
}

// ApiKey is an access token for the autoapi proxy. The token value is the row
// ID itself; no secret material is stored in this table.
type ApiKey struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ExpiresAt       int64    `json:"expires_at"` // ms; 0 = no expiry
	CreatedAt       int64    `json:"created_at"`
	UpdatedAt       int64    `json:"updated_at"`
	Enabled         bool     `json:"enabled"`
	LastUsedAt      int64    `json:"last_used_at"`
	TodayTokens     int64    `json:"today_tokens"`
	ThirtyDayTokens int64    `json:"thirty_day_tokens"`
	AllowedRuleIDs  []string `json:"allowed_rule_ids"`
}

// ModelRule is the client-facing model definition. A rule's Name is the model
// name exposed to clients; the Targets inside a rule are the upstream
// provider/model pairs the request may be forwarded to. The list order from
// the store is the user-defined order; ordering is preserved for stable
// listing (ORDER BY created_at DESC).
//
// FirstByteTimeoutSeconds is the per-RULE maximum first-byte budget: the
// total time the proxy is willing to wait for the first response byte from
// ANY upstream candidate (across all candidates and all per-target retries)
// before declaring "first-byte budget exceeded" and falling through. The
// budget is ONLY counted before the first byte is received — once a
// response is established (streaming first byte committed, or non-streaming
// response fully buffered), the budget stops. 0 = use the proxy default
// (60 seconds).
type ModelRule struct {
	ID                      string `json:"id"`
	Name                    string `json:"name"`
	Enabled                 bool   `json:"enabled"`
	FirstByteTimeoutSeconds int    `json:"first_byte_timeout_seconds"`
	Strategy                string `json:"strategy"`
	CreatedAt               int64  `json:"created_at"`
	UpdatedAt               int64  `json:"updated_at"`

	Targets []ModelRuleTarget `json:"targets"`

	// TodaySuccessRate is the percentage of successful requests for this
	// rule today (local midnight to now). nil when there are no completed
	// requests yet; 0 when all requests failed.
	TodaySuccessRate *float64 `json:"today_success_rate"`

	// TodayRequestCount is the number of completed requests for this rule
	// today. status_code = 0 is excluded (not counted as completed).
	TodayRequestCount int64 `json:"today_request_count"`
}

// ModelRuleTarget is what happens when a rule is selected. The
// per-target first-byte timeout moved to the enclosing ModelRule
// (FirstByteTimeoutSeconds): the budget is now a per-rule concern
// covering the total time across all candidates and retries.
type ModelRuleTarget struct {
	ID         string `json:"id"`
	RuleID     string `json:"rule_id"`
	ProviderID string `json:"provider_id"`
	ModelName  string `json:"model_name"`
	MaxRetries int    `json:"max_retries"` // 0 = try once, no in-target retry; N = up to N additional attempts on retryable errors before falling through
	// Tier is the user-configurable priority group. Smaller values are tried
	// first. The stored value is always an int; it is 0 for the highest-priority
	// group (or for a target that accepted the legacy positional default).
	Tier int `json:"tier"`
	// FirstTokenTimeoutSeconds caps each attempt until its first upstream
	// response-body byte. Zero inherits the rule cumulative budget. It is
	// persisted in the legacy rule_targets.timeout_ms column.
	FirstTokenTimeoutSeconds int   `json:"first_token_timeout_seconds"`
	HitCount                 int64 `json:"hit_count"`     // incremented once on successful dispatch
	FailureCount             int64 `json:"failure_count"` // incremented on each failed attempt (hit + failure = total attempts)
	Enabled                  bool  `json:"enabled"`       // when false, the proxy skips this target during candidate selection (tier order preserved)
}

// ModelRuleTargetInput is the payload for creating or updating a target.
// Tier is a pointer so that a missing tier (nil) can be distinguished from an
// explicit tier of 0. When Tier is nil, the store falls back to the legacy
// positional order (slice index). The output ModelRuleTarget.Tier is always an
// int that reflects the resolved value.
type ModelRuleTargetInput struct {
	ID                       string `json:"id"`
	ProviderID               string `json:"provider_id"`
	ModelName                string `json:"model_name"`
	MaxRetries               int    `json:"max_retries"`
	Tier                     *int   `json:"tier"`
	FirstTokenTimeoutSeconds int    `json:"first_token_timeout_seconds"`
	Enabled                  bool   `json:"enabled"`
}

// RequestLog is one proxied request through the gateway.
type RequestLog struct {
	ID              string  `json:"id"`
	Timestamp       int64   `json:"timestamp"` // ms
	StatusCode      int     `json:"status_code"`
	ProviderID      string  `json:"provider_id"`
	ProviderName    string  `json:"provider_name"`
	Model           string  `json:"model"`
	ReasoningEffort string  `json:"reasoning_effort"`
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	CacheCreation   int64   `json:"cache_creation"` // prompt-cache creation tokens; 0 until upstream support lands
	CacheHit        int64   `json:"cache_hit"`      // prompt-cache hit tokens; 0 until upstream support lands
	Cost            float64 `json:"cost"`           // estimated USD
	CostAvailable   bool    `json:"cost_available"` // true when every charged attempt has a price snapshot
	LatencyMs       int     `json:"latency_ms"`
	FirstTokenMs    int     `json:"first_token_ms"` // TTFT for streaming; 0 for non-streaming
	IsStream        bool    `json:"is_stream"`      // true if the request was streaming
	RouteID         string  `json:"route_id"`       // empty = default route
	RouteLabel      string  `json:"route_label"`
	APIKeyID        string  `json:"api_key_id"`
	APIKeyName      string  `json:"api_key_name"`
	Error           string  `json:"error,omitempty"`

	// Request context captured by the proxy for diagnostics and the
	// expanded log row UI. UserAgent and ClientIP may be empty when the
	// upstream is not a real HTTP call (e.g. internal admin endpoints) or
	// when the request did not reach the proxy. RequestID is the chi
	// middleware.RequestID value so the same identifier appears in
	// slogMiddleware output and any frontend error reports.
	UserAgent string `json:"user_agent"`
	ClientIP  string `json:"client_ip"`
	RequestID string `json:"request_id"`

	// RequestURI is the path component of the proxied request URL (e.g.
	// "/v1/chat/completions"). Captured at request start and immutable —
	// the two-phase update does not overwrite it. Query string is
	// intentionally excluded to avoid persisting API keys or sensitive
	// parameters.
	RequestURI string `json:"request_uri"`

	// Chain captures the per-attempt history of a single proxied request.
	// Non-streaming requests with failover may have multiple entries; a
	// single successful attempt produces one entry; preflight failures
	// (invalid key, bad URL) are recorded as well so the UI can show
	// "tried N targets" when failover kicked in. Chain is persisted as
	// JSON in SQLite (see migration 012_request_log_details) and
	// marshalled by the store on insert / scan.
	Chain []RequestLogChainEntry `json:"chain"`

	// Chain summary fields are populated by lightweight log queries so list
	// views can render attempt outcomes without receiving every chain entry.
	ChainCount       int    `json:"chain_count"`
	FinalChainStatus string `json:"final_chain_status"`
	HitProviderName  string `json:"hit_provider_name"`
	HitModelName     string `json:"hit_model_name"`
}

// RequestLogChainEntry is one attempt recorded in a RequestLog.Chain slice.
// Status describes the categorical outcome of the attempt (success,
// retryable failure, etc.) and is rendered as a small badge in the log
// detail row.
type RequestLogChainEntry struct {
	AttemptOrder int    `json:"attempt_order"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	ModelName    string `json:"model_name"`
	TargetID     string `json:"target_id"`
	Endpoint     string `json:"endpoint"`
	Status       string `json:"status"` // success, retryable, non_retryable, circuit_open, preflight_error, client_abort, truncated, downstream_error
	StatusCode   int    `json:"status_code"`
	Error        string `json:"error"`
	LatencyMs    int    `json:"latency_ms"`
	FirstTokenMs int    `json:"first_token_ms"` // TTFT for this chain entry (streaming); 0 for non-streaming or failed attempts
	// UpstreamStarted is true only after an upstream transport attempt was made.
	// RequestCost is the immutable per-call price snapshot for that attempt.
	UpstreamStarted      bool    `json:"upstream_started"`
	RequestCost          float64 `json:"request_cost"`
	RequestCostAvailable bool    `json:"request_cost_available"`
}

// AttemptOutcome is the categorical completion state of a single attempt.
type AttemptOutcome string

const (
	AttemptOutcomeSuccess         AttemptOutcome = "success"
	AttemptOutcomeRetryable       AttemptOutcome = "retryable"
	AttemptOutcomeNonRetryable    AttemptOutcome = "non_retryable"
	AttemptOutcomeCircuitOpen     AttemptOutcome = "circuit_open"
	AttemptOutcomePreflightError  AttemptOutcome = "preflight_error"
	AttemptOutcomeClientAbort     AttemptOutcome = "client_abort"
	AttemptOutcomeTruncated       AttemptOutcome = "truncated"
	AttemptOutcomeDownstreamError AttemptOutcome = "downstream_error"
	AttemptOutcomeConversionError AttemptOutcome = "conversion_error"
	AttemptOutcomeUnknown         AttemptOutcome = "unknown"

	// Legacy aliases (kept for compatibility with existing proxy code and tests).
	// Deprecated: use the AttemptOutcome* constants instead.
	OutcomeSuccess         = AttemptOutcomeSuccess
	OutcomeTruncated       = AttemptOutcomeTruncated
	OutcomeDownstreamError = AttemptOutcomeDownstreamError
	OutcomeClientAbort     = AttemptOutcomeClientAbort
)

func (o AttemptOutcome) Valid() bool {
	switch o {
	case AttemptOutcomeSuccess, AttemptOutcomeRetryable, AttemptOutcomeNonRetryable,
		AttemptOutcomeCircuitOpen, AttemptOutcomePreflightError, AttemptOutcomeClientAbort,
		AttemptOutcomeTruncated, AttemptOutcomeDownstreamError, AttemptOutcomeConversionError, AttemptOutcomeUnknown:
		return true
	}
	return false
}

func (o AttemptOutcome) Normalized() AttemptOutcome {
	if o.Valid() {
		return o
	}
	return AttemptOutcomeUnknown
}

// RequestOutcome is the categorical completion state of a whole request.
type RequestOutcome string

const (
	RequestOutcomeSuccess RequestOutcome = "success"
	RequestOutcomePartial RequestOutcome = "partial_success"
	RequestOutcomeFailure RequestOutcome = "failure"
	RequestOutcomeAborted RequestOutcome = "aborted"
	RequestOutcomeUnknown RequestOutcome = "unknown"
)

func (o RequestOutcome) Valid() bool {
	switch o {
	case RequestOutcomeSuccess, RequestOutcomePartial, RequestOutcomeFailure,
		RequestOutcomeAborted, RequestOutcomeUnknown:
		return true
	}
	return false
}

func (o RequestOutcome) Normalized() RequestOutcome {
	if o.Valid() {
		return o
	}
	return RequestOutcomeUnknown
}

// ----- Aggregation DTOs (dashboard / usage-stats) -----

// Stat is a single KPI card value with delta context.
type Stat struct {
	Label string `json:"label"`
	Value string `json:"value"` // pre-formatted ("245,832", "¥458.76")
	Delta string `json:"delta"` // "+12.4%", "-3.2%"
	Trend string `json:"trend"` // "up" | "down" | "flat"
	Note  string `json:"note,omitempty"`
}

// TokenTrendPoint is one bucket of a token-usage time series.
type TokenTrendPoint struct {
	Date         string  `json:"date"` // YYYY-MM-DD
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
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

// ModelRuleSummary is a lightweight dashboard entry for a model rule.
// Targets are intentionally omitted; the dashboard only shows identity and
// today's request quality.
type ModelRuleSummary struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	TodaySuccessRate  *float64 `json:"today_success_rate"`
	TodayRequestCount int64    `json:"today_request_count"`
}

// DashboardData aggregates everything the dashboard page needs in one call.
type DashboardData struct {
	Stats          []Stat             `json:"stats"`
	TokenTrend     []TokenTrendPoint  `json:"token_trend"` // last 7 days
	Providers      []Provider         `json:"providers"`
	ModelRules     []ModelRuleSummary `json:"model_rules"`
	RecentActivity []RequestLog       `json:"recent_activity"` // last 10
	ServiceHealth  ServiceHealth      `json:"service_health"`
}

// ServiceHealth is the live system telemetry shown on the dashboard.
type ServiceHealth struct {
	Status            string  `json:"status"` // "running" | "paused" | "error"
	UptimeSeconds     int64   `json:"uptime_seconds"`
	CPUPercent        float64 `json:"cpu_percent"`
	MemoryMB          int     `json:"memory_mb"`
	ActiveConnections int     `json:"active_connections"`
	WebSocketCount    int     `json:"websocket_count"`
	HTTPCount         int     `json:"http_count"`
	ProxyURL          string  `json:"proxy_url"`   // e.g. "http://0.0.0.0:8344" — bind URL reported by the proxy
	APIAddress        string  `json:"api_address"` // e.g. "http://192.168.1.5:8344" — host-reachable URL with the local IPv4; empty when proxy is not running
}

// UsageStats aggregates the usage-stats page data. All fields are
// computed under the same LogQuery filter so cards, charts, and lists
// stay consistent with each other.
//
// TokenTrend30, Logs, and LogTotal were removed: the frontend fetches
// usage trends via GetUsageTrends and paginated logs via QueryLogs
// independently, each with their own filter snapshot.
type UsageStats struct {
	TokenStats   []Stat          `json:"token_stats"`
	Providers    []ProviderShare `json:"providers"`
	ModelRanking []ModelRanking  `json:"model_ranking"`
	LogStats     []Stat          `json:"log_stats"`
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
	Logging    LoggingSettings    `json:"logging"`
}

const (
	CloseActionBackground = "background"
	CloseActionQuit       = "quit"

	StartupActionShowWindow  = "show_window"
	StartupActionStartHidden = "start_hidden"
)

type GeneralSettings struct {
	LaunchAtLogin bool   `json:"launch_at_login"`
	StartupAction string `json:"startup_action"` // "show_window" | "start_hidden" (legacy "minimize_menubar"/"no_window" normalized to "start_hidden")
	MenuBarItem   bool   `json:"menu_bar_item"`
	CloseAction   string `json:"close_action"` // "background" | "quit"
}

type AppearanceSettings struct {
	Theme       string `json:"theme"`        // "light" | "dark" | "system"
	Density     string `json:"density"`      // "compact" | "standard" | "comfortable"
	AccentColor string `json:"accent_color"` // hex "#0071e3"
}

type RoutingSettings struct {
	StreamingSSE bool `json:"streaming_sse"`
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
	DebugMode                    bool   `json:"debug_mode"`
	Experimental                 bool   `json:"experimental"`
	HTTPProxy                    string `json:"http_proxy"`                     // "system" | "none" | url
	FeatureCapabilityEnforcement string `json:"feature_capability_enforcement"` // "observe" | "enforce"
	TargetBreakerThreshold       int    `json:"target_breaker_threshold"`
	TargetBreakerWindowSeconds   int    `json:"target_breaker_window_seconds"`
}

const (
	DefaultTargetBreakerThreshold     = 5
	DefaultTargetBreakerWindowSeconds = 300
)

func NormalizeTargetBreakerThreshold(v int) int {
	if v < 1 || v > 50 {
		return DefaultTargetBreakerThreshold
	}
	return v
}

func NormalizeTargetBreakerWindowSeconds(v int) int {
	if v < 30 || v > 3600 {
		return DefaultTargetBreakerWindowSeconds
	}
	return v
}

func NormalizeTargetBreakerSettings(s *AdvancedSettings) {
	if s == nil {
		return
	}
	s.TargetBreakerThreshold = NormalizeTargetBreakerThreshold(s.TargetBreakerThreshold)
	s.TargetBreakerWindowSeconds = NormalizeTargetBreakerWindowSeconds(s.TargetBreakerWindowSeconds)
}

// LoggingSettings configures the application diagnostic logger. The fields
// are persisted as a single section in the settings table and surfaced to
// the user via the settings panel; the Go side reads them at startup
// (and on every SaveSettings) to (re-)initialise the slog handler that
// tees output to stderr and a rotating file under the user-configurable
// path. Storage path is left to the Go composition root (see
// app.go) so that the directory can be derived from the resolved
// storage location rather than typed into the UI.
type LoggingSettings struct {
	Enabled    bool   `json:"enabled"`
	Level      string `json:"level"`        // "error" | "warn" | "info" | "debug" | "trace"
	MaxSizeMB  int    `json:"max_size_mb"`  // per-file size cap in MB before rotation
	MaxAgeDays int    `json:"max_age_days"` // days to retain old log files
	MaxBackups int    `json:"max_backups"`  // max number of rotated files to keep
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
	Name           string   `json:"name"`
	ExpiresAt      int64    `json:"expires_at"`
	AllowedRuleIDs []string `json:"allowed_rule_ids"`
}

// ----- Provider test result -----

// ProviderTestResult is returned by the "Test" button on a provider card.
type ProviderTestResult struct {
	OK        bool     `json:"ok"`
	LatencyMs int      `json:"latency_ms"`
	Error     string   `json:"error,omitempty"`
	Models    []string `json:"models"` // model names discovered via /v1/models
}

// ----- Request payloads -----

type ProviderInput struct {
	Name             string `json:"name"`
	BaseURL          string `json:"base_url"`
	UpstreamKey      string `json:"upstream_key"` // cleartext provider key; encrypted by App layer before storage
	IsCustom         bool   `json:"is_custom"`
	ResponsesEnabled bool   `json:"responses_enabled"` // legacy compatibility field; provider save ignores it
	MessagesEnabled  bool   `json:"messages_enabled"`  // legacy compatibility field; provider save ignores it
	GeminiEnabled    bool   `json:"gemini_enabled"`    // legacy compatibility field; provider save ignores it
}

type ModelRuleInput struct {
	Name                    string                 `json:"name"`
	Enabled                 bool                   `json:"enabled"`
	FirstByteTimeoutSeconds int                    `json:"first_byte_timeout_seconds"`
	Strategy                string                 `json:"strategy"`
	Targets                 []ModelRuleTargetInput `json:"targets"`
}

// ReorderModelRuleTargetsResult reports an expected stale-target conflict
// without turning it into a transport/API failure. Operational store errors
// are still returned as Go errors.
type ReorderModelRuleTargetsResult struct {
	Conflict bool `json:"conflict"`
}

// ReorderModelRulesResult reports whether a rule reorder failed because the
// rule set changed underneath the caller (e.g. concurrent edit or stale UI).
// Operational store errors are returned as Go errors.
type ReorderModelRulesResult struct {
	Conflict bool `json:"conflict"`
}

// Note on `ModelRuleTarget.Enabled` in this input: a target with an empty ID
// is treated as a NEW target by the store, and the store coerces `Enabled`
// to true on insert when the caller leaves it at the Go/JSON zero value
// (`false`). Targets with a non-empty ID are UPDATEs and their `Enabled`
// value is written through verbatim — that is the supported path for the
// frontend's per-target toggle. See internal/store/routes.go
// (insertTargets / upsertTargets) for the implementation.

// LogQuery is the filter for the usage-stats → logs view.
//
// All filter fields are optional (empty/zero means "no filter for this field").
// Status accepts: "" (all), "success", "failed", "rate_limited".
type LogQuery struct {
	StartDate int64  `json:"start_date"` // ms; 0 = no lower bound
	EndDate   int64  `json:"end_date"`   // ms; 0 = no upper bound
	APIKeyID  string `json:"api_key_id"` // exact match on API key ID; "" = all
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

// ----- Chart aggregation DTOs (single usage-trend chart) -----

// UsageTrendBucket is one slice of the usage-trend line/area chart. The Bucket
// label is a locale-neutral ISO string the frontend can format as needed:
// "YYYY-MM-DD" for daily, "YYYY-MM-DD HH:00" for hourly. Token counts are
// aggregated per bucket; Cost is the sum of the per-row USD cost.
type UsageTrendBucket struct {
	Bucket        string  `json:"bucket"`         // "YYYY-MM-DD" or "YYYY-MM-DD HH:00"
	Cost          float64 `json:"cost"`           // sum
	CacheCreation int64   `json:"cache_creation"` // sum
	CacheHit      int64   `json:"cache_hit"`      // sum
	Input         int64   `json:"input"`          // SUM(input_tokens)
	Output        int64   `json:"output"`         // SUM(output_tokens)
}

// UsageTrends is the response for a single chart-data fetch. Range is a
// free-form description (e.g. "2024-01-01..2024-01-31") so the frontend can
// show a label without re-formatting start/end itself; BucketSize is the
// resolution used ("hour" or "day").
type UsageTrends struct {
	Range      string             `json:"range"`       // e.g. "2024-01-01..2024-01-31"
	BucketSize string             `json:"bucket_size"` // "hour" | "day"
	Buckets    []UsageTrendBucket `json:"buckets"`     // time series, ordered ascending
}

// UsageTrendsQuery is the filter for GetUsageTrends. Same semantics as
// LogQuery but without pagination or status — chart data always spans the
// full filtered range. Status, Page, and PageSize are intentionally omitted
// because the chart surface does not need them.
type UsageTrendsQuery struct {
	StartDate int64  `json:"start_date"` // ms; 0 = no lower bound
	EndDate   int64  `json:"end_date"`   // ms; 0 = no upper bound
	APIKeyID  string `json:"api_key_id"` // exact match on API key ID; "" = all
	Provider  string `json:"provider"`   // exact match on provider_id; "" = all
	RouteID   string `json:"route_id"`   // exact match on route_id; "" = all
	Model     string `json:"model"`      // exact match on model; "" = all
	Search    string `json:"search"`     // LIKE %term% across model/route_label/error
}

// ExportFormat selects the export payload type.
type ExportFormat string

const (
	ExportAllJSON      ExportFormat = "all_json"
	ExportSettingsJSON ExportFormat = "settings_json"
	ExportTokensCSV    ExportFormat = "tokens_csv"
	ExportLogsCSV      ExportFormat = "logs_csv"
)

// AppInfo carries build-time and runtime metadata for the Settings > About
// section. Version and Build are injected via -ldflags at build time.
type AppInfo struct {
	Version   string `json:"version"`
	Build     string `json:"build"`     // short commit hash or date stamp
	Platform  string `json:"platform"`  // darwin / windows / linux
	Arch      string `json:"arch"`      // arm64 / amd64
	GoVersion string `json:"goVersion"` // Go toolchain version
}

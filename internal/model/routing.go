// Package model routing contracts (Phase 0/1A)
//
// This file contains only data contracts for future routing, metrics and
// pricing work. Nothing in this file changes the current routing, retry,
// circuit-breaker or provider-selection behaviour of the proxy.
package model

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// TargetIdentity is the stable configuration identity of a target.
// It is the source of truth for routing attribution: metrics and cost are
// associated with a single ModelRuleTarget row.
type TargetIdentity struct {
	TargetID string `json:"target_id"`
}

// Valid reports whether the identity is non-empty after trimming whitespace.
func (id TargetIdentity) Valid() bool {
	return strings.TrimSpace(id.TargetID) != ""
}

// Normalized returns a trimmed copy of the identity.
func (id TargetIdentity) Normalized() TargetIdentity {
	return TargetIdentity{TargetID: strings.TrimSpace(id.TargetID)}
}

// TargetMetricKey identifies the metric aggregation dimension for a target.
// It is keyed by the upstream provider and model; Endpoint is optional and
// used when the same upstream model is reachable via multiple paths. Price
// lookups will use the same dimensions.
type TargetMetricKey struct {
	TargetID   string `json:"target_id,omitempty"`
	ProviderID string `json:"provider_id"`
	ModelName  string `json:"model_name"`
	Endpoint   string `json:"endpoint"`
}

// RouteModeKey identifies the exact wire mode used by an upstream attempt.
// It is runtime-only attribution data and is not part of persisted summaries.
type RouteModeKey struct {
	TargetID         string `json:"target_id"`
	InboundProtocol  string `json:"inbound_protocol"`
	UpstreamProtocol string `json:"upstream_protocol"`
}

func (k RouteModeKey) Valid() bool {
	return strings.TrimSpace(k.TargetID) != "" && strings.TrimSpace(k.InboundProtocol) != "" && strings.TrimSpace(k.UpstreamProtocol) != ""
}

func (k RouteModeKey) Normalized() RouteModeKey {
	return RouteModeKey{TargetID: strings.TrimSpace(k.TargetID), InboundProtocol: strings.TrimSpace(k.InboundProtocol), UpstreamProtocol: strings.TrimSpace(k.UpstreamProtocol)}
}

const (
	MetricProviderClient    = "__client__"
	MetricProviderPreflight = "__preflight__"
)

// Valid reports whether the required dimensions (provider + model) are present.
func (k TargetMetricKey) Valid() bool {
	provider := strings.TrimSpace(k.ProviderID)
	return provider != "" && provider != MetricProviderClient && provider != MetricProviderPreflight && strings.TrimSpace(k.ModelName) != ""
}

func (k TargetMetricKey) validFor(kind MetricEventKind) bool {
	if kind == MetricEventRequest {
		return strings.TrimSpace(k.Endpoint) != "" && (k.Valid() || k.ProviderID == MetricProviderClient || k.ProviderID == MetricProviderPreflight)
	}
	return k.Valid()
}

// Normalized returns a trimmed copy of the metric key.
func (k TargetMetricKey) Normalized() TargetMetricKey {
	return TargetMetricKey{
		TargetID:   strings.TrimSpace(k.TargetID),
		ProviderID: strings.TrimSpace(k.ProviderID),
		ModelName:  strings.TrimSpace(k.ModelName),
		Endpoint:   strings.TrimSpace(k.Endpoint),
	}
}

// MetricEventKind identifies the aggregation level of a runtime event. An
// attempt is one upstream call; a request is the complete client operation.
type MetricEventKind string

const (
	MetricEventAttempt MetricEventKind = "attempt"
	MetricEventRequest MetricEventKind = "request"
)

// MetricFailureClass is deliberately independent from AttemptOutcome. It is
// the small set of health dimensions needed by the registry.
type MetricFailureClass string

const (
	MetricFailureNone             MetricFailureClass = "none"
	MetricFailure429              MetricFailureClass = "429"
	MetricFailure5xx              MetricFailureClass = "5xx"
	MetricFailureTransport        MetricFailureClass = "transport"
	MetricFailureClientAbort      MetricFailureClass = "client_abort"
	MetricFailureUpstreamTrunc    MetricFailureClass = "upstream_truncated"
	MetricFailureHTTPNonRetryable MetricFailureClass = "http_non_retryable"
	MetricFailureDownstream       MetricFailureClass = "downstream"
	MetricFailureConversionLocal  MetricFailureClass = "conversion_local"
)

func (c MetricFailureClass) Valid() bool {
	switch c {
	case "", MetricFailureNone, MetricFailure429, MetricFailure5xx, MetricFailureTransport, MetricFailureClientAbort, MetricFailureUpstreamTrunc, MetricFailureHTTPNonRetryable, MetricFailureDownstream, MetricFailureConversionLocal:
		return true
	default:
		return false
	}
}

func (c MetricFailureClass) Normalized() MetricFailureClass {
	if c.Valid() {
		return c
	}
	return MetricFailureNone
}

// TargetMetricEvent is an immutable, JSON-compatible runtime observation.
// Durations are milliseconds; timestamps are UTC wall-clock time. Only one
// event level is populated per event, so attempt retries cannot inflate request
// success counts.
type TargetMetricEvent struct {
	Key             TargetMetricKey    `json:"key"`
	RouteMode       RouteModeKey       `json:"route_mode,omitempty"`
	Kind            MetricEventKind    `json:"kind"`
	RequestID       string             `json:"request_id,omitempty"`
	AttemptID       string             `json:"attempt_id,omitempty"`
	AttemptOutcome  AttemptOutcome     `json:"attempt_outcome,omitempty"`
	RequestOutcome  RequestOutcome     `json:"request_outcome,omitempty"`
	FailureClass    MetricFailureClass `json:"failure_class,omitempty"`
	StatusCode      int                `json:"status_code,omitempty"`
	StreamCommitted bool               `json:"stream_committed,omitempty"`
	FirstByteMs     int64              `json:"first_byte_ms,omitempty"`
	TTFTMs          int64              `json:"ttft_ms,omitempty"`
	RetryAfterMs    int64              `json:"retry_after_ms,omitempty"`
	At              time.Time          `json:"at"`
}

// TargetRuntimeSummary is the compact persistent checkpoint; samples are not persisted.
type TargetRuntimeSummary struct {
	Key          TargetMetricKey `json:"key"`
	Requests     int64           `json:"requests"`
	Attempts     int64           `json:"attempts"`
	Successes    int64           `json:"successes"`
	Failures     int64           `json:"failures"`
	Status429    int64           `json:"status_429"`
	Status5xx    int64           `json:"status_5xx"`
	Transport    int64           `json:"transport"`
	ClientAborts int64           `json:"client_aborts"`
	Truncated    int64           `json:"truncated"`
	Downstream   int64           `json:"downstream"`
	LastUsed     time.Time       `json:"last_used"`
	LastSuccess  time.Time       `json:"last_success"`
	LastFailure  time.Time       `json:"last_failure"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// Validate accepts only persisted, real upstream target summaries.
func (s TargetRuntimeSummary) Validate() error {
	if !s.Key.Valid() || s.Key.TargetID == "" {
		return fmt.Errorf("invalid target metric key")
	}
	for name, v := range map[string]int64{"requests": s.Requests, "attempts": s.Attempts, "successes": s.Successes, "failures": s.Failures, "status_429": s.Status429, "status_5xx": s.Status5xx, "transport": s.Transport, "client_aborts": s.ClientAborts, "truncated": s.Truncated, "downstream": s.Downstream} {
		if v < 0 {
			return fmt.Errorf("negative %s", name)
		}
	}
	if s.LastUsed.IsZero() || s.UpdatedAt.IsZero() || s.LastUsed.After(time.Now().UTC().Add(time.Second)) {
		return fmt.Errorf("invalid last_used")
	}
	if (!s.LastSuccess.IsZero() && s.LastSuccess.After(s.LastUsed)) || (!s.LastFailure.IsZero() && s.LastFailure.After(s.LastUsed)) {
		return fmt.Errorf("event time after last_used")
	}
	return nil
}

// Valid validates the event without changing it. Zero timestamps are accepted
// for callers that use registry time; non-zero future timestamps are rejected
// so callers cannot bypass TTL eviction with an event from the future.
func (e TargetMetricEvent) Valid() bool {
	if (e.Kind != MetricEventAttempt && e.Kind != MetricEventRequest) || !e.Key.validFor(e.Kind) {
		return false
	}
	if !e.At.IsZero() && e.At.After(time.Now().UTC()) {
		return false
	}
	if e.FirstByteMs < 0 || e.TTFTMs < 0 || e.RetryAfterMs < 0 || e.StatusCode < 0 {
		return false
	}
	if e.Kind == MetricEventAttempt && !e.AttemptOutcome.Valid() {
		return false
	}
	if e.Kind == MetricEventAttempt && !e.RouteMode.Valid() {
		return false
	}
	if e.Kind == MetricEventRequest && !e.RequestOutcome.Valid() {
		return false
	}
	if !e.FailureClass.Valid() {
		return false
	}
	return true
}

// EffectiveCost is a routing-time estimate of what a single upstream attempt
// may cost. It is intentionally separate from the final cost recorded in
// request logs: estimates may be wrong, incomplete, or unavailable.
//
// Availability invariant: IsAvailable() returns true only when Available is true
// and Cost is a finite, non-NaN number. Unknown prices must never be treated as
// free.
type EffectiveCost struct {
	Cost     float64 `json:"cost"`
	Currency string  `json:"currency"`
	// Available is false when no price could be resolved (e.g. unknown model).
	// Callers must treat Cost as invalid in that case.
	Available bool `json:"available"`
}

// IsAvailable reports whether the cost estimate can be used for comparisons.
// An unavailable estimate or a NaN/Inf/negative cost is explicitly rejected so
// unknown prices are never treated as free.
func (ec EffectiveCost) IsAvailable() bool {
	return ec.Available && !math.IsNaN(ec.Cost) && !math.IsInf(ec.Cost, 0) && ec.Cost >= 0
}

// DefaultEffectiveCost returns the sentinel for an unresolved price.
func DefaultEffectiveCost() EffectiveCost {
	return EffectiveCost{
		Cost:      0,
		Available: false,
	}
}

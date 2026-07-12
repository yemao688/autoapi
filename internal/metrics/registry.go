// Package metrics provides the bounded, process-local target runtime metrics
// registry. It is intentionally not wired into proxy forwarding yet.
package metrics

import (
	"sync"
	"time"

	"autoapi/internal/model"
)

const (
	DefaultCapacity = 1024
	DefaultTTL      = 30 * time.Minute
	defaultSamples  = 64
)

type sample struct {
	value int64
	at    time.Time
}
type aggregate struct {
	lastUsed, lastSuccess, lastFailure                                                                            time.Time
	requests, attempts, successes, failures, status429, status5xx, transport, clientAborts, truncated, downstream int64
	firstBytes, ttfts                                                                                             []sample
}

// Snapshot is a detached view of one target's counters. Samples are bounded
// observations, not a promise of a percentile or a complete event history.
type Snapshot struct {
	Key          model.TargetMetricKey `json:"key"`
	Requests     int64                 `json:"requests"`
	Attempts     int64                 `json:"attempts"`
	Successes    int64                 `json:"successes"`
	Failures     int64                 `json:"failures"`
	Status429    int64                 `json:"status_429"`
	Status5xx    int64                 `json:"status_5xx"`
	Transport    int64                 `json:"transport"`
	ClientAborts int64                 `json:"client_aborts"`
	Truncated    int64                 `json:"truncated"`
	Downstream   int64                 `json:"downstream"`
	FirstByteMs  []int64               `json:"first_byte_ms,omitempty"`
	TTFTMs       []int64               `json:"ttft_ms,omitempty"`
	LastUsed     time.Time             `json:"last_used,omitempty"`
	LastSuccess  time.Time             `json:"last_success,omitempty"`
	LastFailure  time.Time             `json:"last_failure,omitempty"`
}

// Restore replaces the registry with detached cumulative summaries. It is intended
// for startup only; samples are intentionally empty and summaries are not added.
func (r *Registry) Restore(items []model.TargetRuntimeSummary, now time.Time) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = make(map[model.TargetMetricKey]*aggregate)
	// Restore is a complete replace. Duplicate keys are last-write-wins, and
	// capacity is deterministic: retain the first unique keys in input order.
	for _, v := range items {
		k := v.Key.Normalized()
		if v.Validate() != nil || v.LastUsed.After(now) || now.Sub(v.LastUsed) > r.ttl {
			continue
		}
		if _, exists := r.entries[k]; !exists && len(r.entries) >= r.capacity {
			break
		}
		r.entries[k] = &aggregate{requests: v.Requests, attempts: v.Attempts, successes: v.Successes, failures: v.Failures, status429: v.Status429, status5xx: v.Status5xx, transport: v.Transport, clientAborts: v.ClientAborts, truncated: v.Truncated, downstream: v.Downstream, lastUsed: v.LastUsed, lastSuccess: v.LastSuccess, lastFailure: v.LastFailure}
	}
}

// Registry aggregates events with a fixed key capacity and TTL. Submit is
// safe for concurrent callers; Snapshot never exposes registry-owned slices.
type Registry struct {
	mu       sync.RWMutex
	capacity int
	ttl      time.Duration
	entries  map[model.TargetMetricKey]*aggregate
}

func New(capacity int, ttl time.Duration) *Registry {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Registry{capacity: capacity, ttl: ttl, entries: make(map[model.TargetMetricKey]*aggregate)}
}

func (r *Registry) Submit(e model.TargetMetricEvent) bool {
	if !e.Valid() {
		return false
	}
	now := e.At
	if now.IsZero() {
		now = time.Now().UTC()
	}
	k := e.Key.Normalized()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	a := r.entries[k]
	if a == nil {
		if len(r.entries) >= r.capacity {
			r.evictOldestLocked()
		}
		a = &aggregate{}
		r.entries[k] = a
	}
	if e.Kind == model.MetricEventRequest {
		a.requests++
		if e.RequestOutcome == model.RequestOutcomeSuccess || e.RequestOutcome == model.RequestOutcomePartial {
			a.successes++
		} else if e.RequestOutcome == model.RequestOutcomeFailure {
			a.failures++
		}
	}
	if e.Kind == model.MetricEventAttempt {
		a.attempts++
		if e.AttemptOutcome == model.AttemptOutcomeSuccess {
			a.successes++
		} else if e.FailureClass == model.MetricFailure5xx || e.FailureClass == model.MetricFailureTransport || e.FailureClass == model.MetricFailureUpstreamTrunc {
			a.failures++
		}
	}
	if e.FailureClass == model.MetricFailure429 {
		a.status429++
	} else if e.FailureClass == model.MetricFailure5xx {
		a.status5xx++
	} else if e.FailureClass == model.MetricFailureTransport {
		a.transport++
	} else if e.FailureClass == model.MetricFailureClientAbort {
		a.clientAborts++
	} else if e.FailureClass == model.MetricFailureUpstreamTrunc {
		a.truncated++
	} else if e.FailureClass == model.MetricFailureDownstream {
		a.downstream++
	}
	if e.FirstByteMs > 0 {
		appendSample(&a.firstBytes, sample{e.FirstByteMs, now})
	}
	if e.TTFTMs > 0 {
		appendSample(&a.ttfts, sample{e.TTFTMs, now})
	}
	a.lastUsed = now
	if e.AttemptOutcome == model.AttemptOutcomeSuccess || e.RequestOutcome == model.RequestOutcomeSuccess || e.RequestOutcome == model.RequestOutcomePartial {
		a.lastSuccess = now
	}
	if e.FailureClass != "" && e.FailureClass != model.MetricFailureNone && e.FailureClass != model.MetricFailureClientAbort {
		a.lastFailure = now
	}
	return true
}

func appendSample(dst *[]sample, s sample) {
	if len(*dst) >= defaultSamples {
		copy((*dst), (*dst)[1:])
		*dst = (*dst)[:defaultSamples-1]
	}
	*dst = append(*dst, s)
}
func (r *Registry) pruneLocked(now time.Time) {
	for k, a := range r.entries {
		if !a.lastUsed.IsZero() && now.Sub(a.lastUsed) > r.ttl {
			delete(r.entries, k)
		}
	}
}
func (r *Registry) evictOldestLocked() {
	var key model.TargetMetricKey
	var oldest time.Time
	for k, a := range r.entries {
		if oldest.IsZero() || a.lastUsed.Before(oldest) {
			key, oldest = k, a.lastUsed
		}
	}
	delete(r.entries, key)
}

func (r *Registry) Snapshot(k model.TargetMetricKey, now time.Time) Snapshot {
	k = k.Normalized()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	a := r.entries[k]
	out := Snapshot{Key: k}
	if a == nil {
		return out
	}
	out.Requests, out.Attempts, out.Successes, out.Failures = a.requests, a.attempts, a.successes, a.failures
	out.Status429, out.Status5xx, out.Transport, out.ClientAborts, out.Truncated = a.status429, a.status5xx, a.transport, a.clientAborts, a.truncated
	out.Downstream = a.downstream
	out.LastUsed, out.LastSuccess, out.LastFailure = a.lastUsed, a.lastSuccess, a.lastFailure
	for _, s := range a.firstBytes {
		out.FirstByteMs = append(out.FirstByteMs, s.value)
	}
	for _, s := range a.ttfts {
		out.TTFTMs = append(out.TTFTMs, s.value)
	}
	return out
}

// CurrentSnapshot is the safe read-only lookup used by diagnostic consumers.
func (r *Registry) CurrentSnapshot(k model.TargetMetricKey) Snapshot {
	return r.Snapshot(k, time.Now().UTC())
}

// Has reports whether a non-expired entry exists without exposing registry state.
func (r *Registry) Has(k model.TargetMetricKey, now time.Time) bool {
	return r.Snapshot(k, now).LastUsed.After(time.Time{})
}

func (r *Registry) Snapshots(now time.Time) []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	r.pruneLocked(now)
	out := make([]Snapshot, 0, len(r.entries))
	for k := range r.entries {
		out = append(out, r.snapshotLocked(k))
	}
	return out
}
func (r *Registry) snapshotLocked(k model.TargetMetricKey) Snapshot {
	a := r.entries[k]
	out := Snapshot{Key: k}
	if a == nil {
		return out
	}
	out.Requests, out.Attempts, out.Successes, out.Failures = a.requests, a.attempts, a.successes, a.failures
	out.Status429, out.Status5xx, out.Transport, out.ClientAborts, out.Truncated = a.status429, a.status5xx, a.transport, a.clientAborts, a.truncated
	out.Downstream = a.downstream
	out.LastUsed, out.LastSuccess, out.LastFailure = a.lastUsed, a.lastSuccess, a.lastFailure
	for _, s := range a.firstBytes {
		out.FirstByteMs = append(out.FirstByteMs, s.value)
	}
	for _, s := range a.ttfts {
		out.TTFTMs = append(out.TTFTMs, s.value)
	}
	return out
}

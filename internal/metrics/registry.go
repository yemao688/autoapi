// Package metrics provides the bounded, process-local target runtime metrics
// registry. It is intentionally not wired into proxy forwarding yet.
package metrics

import (
	"sort"
	"strings"
	"sync"
	"time"

	"autoapi/internal/model"
)

const (
	DefaultCapacity = 1024
	DefaultTTL      = 30 * time.Minute
	defaultSamples  = 64
	routeHorizon    = 10 * time.Minute
)

type sample struct {
	value int64
	at    time.Time
}
type aggregate struct {
	lastUsed, lastSuccess, lastFailure                                                                                             time.Time
	requests, attempts, successes, failures, status429, status5xx, transport, clientAborts, truncated, downstream, conversionLocal int64
	firstBytes, ttfts                                                                                                              []sample
}

type routeSample struct {
	at           time.Time
	outcome      model.AttemptOutcome
	failureClass model.MetricFailureClass
	firstByteMs  int64
	ttftMs       int64
}

type routeAggregate struct {
	samples    []routeSample
	lastIngest time.Time
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func summaryTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func summaryMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// Snapshot is a detached view of one target's counters. Samples are bounded
// observations, not a promise of a percentile or a complete event history.
type Snapshot struct {
	Key             model.TargetMetricKey `json:"key"`
	Requests        int64                 `json:"requests"`
	Attempts        int64                 `json:"attempts"`
	Successes       int64                 `json:"successes"`
	Failures        int64                 `json:"failures"`
	Status429       int64                 `json:"status_429"`
	Status5xx       int64                 `json:"status_5xx"`
	Transport       int64                 `json:"transport"`
	ClientAborts    int64                 `json:"client_aborts"`
	Truncated       int64                 `json:"truncated"`
	Downstream      int64                 `json:"downstream"`
	ConversionLocal int64                 `json:"conversion_local"`
	FirstByteMs     []int64               `json:"first_byte_ms,omitempty"`
	TTFTMs          []int64               `json:"ttft_ms,omitempty"`
	LastUsed        time.Time             `json:"last_used,omitempty"`
	LastSuccess     time.Time             `json:"last_success,omitempty"`
	LastFailure     time.Time             `json:"last_failure,omitempty"`
}

// RouteSnapshot is the bounded recent view for one exact route mode.
type RouteSnapshot struct {
	Key             model.RouteModeKey `json:"key"`
	Attempts        int64              `json:"attempts"`
	Successes       int64              `json:"successes"`
	Failures        int64              `json:"failures"`
	Status429       int64              `json:"status_429"`
	Status5xx       int64              `json:"status_5xx"`
	Transport       int64              `json:"transport"`
	Truncated       int64              `json:"truncated"`
	ConversionLocal int64              `json:"conversion_local"`
	ClientAborts    int64              `json:"client_aborts"`
	Downstream      int64              `json:"downstream"`
	FirstByteMs     []int64            `json:"first_byte_ms,omitempty"`
	TTFTMs          []int64            `json:"ttft_ms,omitempty"`
	LastUsed        time.Time          `json:"last_used,omitempty"`
	LastSuccess     time.Time          `json:"last_success,omitempty"`
	LastFailure     time.Time          `json:"last_failure,omitempty"`
}

type Option func(*Registry)

func WithClock(clock func() time.Time) Option {
	return func(r *Registry) {
		if clock != nil {
			r.clock = clock
		}
	}
}

// Restore replaces the registry with detached cumulative summaries. It is intended
// for startup only; samples are intentionally empty and summaries are not added.
func (r *Registry) Restore(items []model.TargetRuntimeSummary, now time.Time) {
	if now.IsZero() {
		now = r.clock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.After(r.lastIngest) {
		r.lastIngest = now
	}
	r.entries = make(map[model.TargetMetricKey]*aggregate)
	r.routeEntries = make(map[model.RouteModeKey]*routeAggregate)
	// Restore is a complete replace. Duplicate keys are last-write-wins, and
	// capacity is deterministic: retain the first unique keys in input order.
	for _, v := range items {
		k := v.Key.Normalized()
		if v.Validate() != nil || v.LastUsed > now.UnixMilli() || now.Sub(time.UnixMilli(v.LastUsed)) > r.ttl {
			continue
		}
		if _, exists := r.entries[k]; !exists && len(r.entries) >= r.capacity {
			break
		}
		r.entries[k] = &aggregate{requests: v.Requests, attempts: v.Attempts, successes: v.Successes, failures: v.Failures, status429: v.Status429, status5xx: v.Status5xx, transport: v.Transport, clientAborts: v.ClientAborts, truncated: v.Truncated, downstream: v.Downstream, lastUsed: summaryTime(v.LastUsed), lastSuccess: summaryTime(v.LastSuccess), lastFailure: summaryTime(v.LastFailure)}
	}
}

// Registry aggregates events with a fixed key capacity and TTL. Submit is
// safe for concurrent callers; Snapshot never exposes registry-owned slices.
type Registry struct {
	mu           sync.RWMutex
	capacity     int
	ttl          time.Duration
	entries      map[model.TargetMetricKey]*aggregate
	routeEntries map[model.RouteModeKey]*routeAggregate
	clock        func() time.Time
	lastIngest   time.Time
}

func New(capacity int, ttl time.Duration, options ...Option) *Registry {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	r := &Registry{capacity: capacity, ttl: ttl, entries: make(map[model.TargetMetricKey]*aggregate), routeEntries: make(map[model.RouteModeKey]*routeAggregate), clock: func() time.Time { return time.Now().UTC() }}
	for _, option := range options {
		if option != nil {
			option(r)
		}
	}
	return r
}

func (r *Registry) Submit(e model.TargetMetricEvent) bool {
	if !e.Valid() {
		return false
	}
	k := e.Key.Normalized()
	r.mu.Lock()
	defer r.mu.Unlock()
	ingestNow := r.clock()
	if ingestNow.Before(r.lastIngest) {
		ingestNow = r.lastIngest
	} else {
		r.lastIngest = ingestNow
	}
	observationAt := e.At
	if observationAt.IsZero() {
		observationAt = ingestNow
		e.At = observationAt
	}
	r.pruneLocked(ingestNow)
	if e.Kind == model.MetricEventAttempt && e.RouteMode.Valid() {
		r.pruneRouteKeyLocked(e.RouteMode, ingestNow)
	}
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
		r.appendRouteLocked(e, ingestNow)
		a.attempts++
		if e.AttemptOutcome == model.AttemptOutcomeSuccess {
			a.successes++
		} else if e.FailureClass == model.MetricFailure5xx || e.FailureClass == model.MetricFailureTransport || e.FailureClass == model.MetricFailureUpstreamTrunc || e.FailureClass == model.MetricFailureConversionLocal {
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
	} else if e.FailureClass == model.MetricFailureConversionLocal {
		a.conversionLocal++
	}
	if e.Kind == model.MetricEventAttempt {
		if e.FirstByteMs > 0 {
			appendSample(&a.firstBytes, sample{e.FirstByteMs, observationAt})
		}
		if e.TTFTMs > 0 {
			appendSample(&a.ttfts, sample{e.TTFTMs, observationAt})
		}
	}
	a.lastUsed = maxTime(a.lastUsed, observationAt)
	if e.AttemptOutcome == model.AttemptOutcomeSuccess || e.RequestOutcome == model.RequestOutcomeSuccess || e.RequestOutcome == model.RequestOutcomePartial {
		a.lastSuccess = maxTime(a.lastSuccess, observationAt)
	}
	if e.FailureClass != "" && e.FailureClass != model.MetricFailureNone && e.FailureClass != model.MetricFailureClientAbort {
		a.lastFailure = maxTime(a.lastFailure, observationAt)
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

func (r *Registry) pruneRouteKeyLocked(key model.RouteModeKey, now time.Time) {
	key = key.Normalized()
	a := r.routeEntries[key]
	if a == nil {
		return
	}
	cutoff := now.Add(-routeHorizon)
	first := 0
	for first < len(a.samples) && a.samples[first].at.Before(cutoff) {
		first++
	}
	if first == len(a.samples) {
		delete(r.routeEntries, key)
	} else if first > 0 {
		a.samples = append([]routeSample(nil), a.samples[first:]...)
	}
}

func (r *Registry) pruneAllRoutesLocked(now time.Time) {
	for key := range r.routeEntries {
		r.pruneRouteKeyLocked(key, now)
	}
}

func (r *Registry) appendRouteLocked(e model.TargetMetricEvent, now time.Time) {
	if e.Kind != model.MetricEventAttempt || !e.RouteMode.Valid() {
		return
	}
	key := e.RouteMode.Normalized()
	r.pruneRouteKeyLocked(key, now)
	a := r.routeEntries[key]
	if a == nil {
		if len(r.routeEntries) >= r.capacity {
			r.pruneAllRoutesLocked(now)
			if len(r.routeEntries) >= r.capacity {
				r.evictOldestRouteLocked()
			}
		}
		a = &routeAggregate{}
		r.routeEntries[key] = a
	}
	if now.Before(a.lastIngest) {
		now = a.lastIngest
	}
	a.lastIngest = now
	if len(a.samples) >= defaultSamples {
		copy(a.samples, a.samples[1:])
		a.samples = a.samples[:defaultSamples-1]
	}
	a.samples = append(a.samples, routeSample{at: now, outcome: e.AttemptOutcome, failureClass: e.FailureClass.Normalized(), firstByteMs: e.FirstByteMs, ttftMs: e.TTFTMs})
}

func routeKeyLess(a, b model.RouteModeKey) bool {
	if a.TargetID != b.TargetID {
		return a.TargetID < b.TargetID
	}
	if a.InboundProtocol != b.InboundProtocol {
		return a.InboundProtocol < b.InboundProtocol
	}
	return a.UpstreamProtocol < b.UpstreamProtocol
}

func (r *Registry) evictOldestRouteLocked() {
	var key model.RouteModeKey
	var oldest time.Time
	for candidate, a := range r.routeEntries {
		if oldest.IsZero() || a.lastIngest.Before(oldest) || (a.lastIngest.Equal(oldest) && routeKeyLess(candidate, key)) {
			key, oldest = candidate, a.lastIngest
		}
	}
	if !oldest.IsZero() {
		delete(r.routeEntries, key)
	}
}
func (r *Registry) evictOldestLocked() {
	var key model.TargetMetricKey
	var oldest time.Time
	for k, a := range r.entries {
		if oldest.IsZero() || a.lastUsed.Before(oldest) || (a.lastUsed.Equal(oldest) && targetKeyLess(k, key)) {
			key, oldest = k, a.lastUsed
		}
	}
	delete(r.entries, key)
}

func targetKeyLess(a, b model.TargetMetricKey) bool {
	if a.TargetID != b.TargetID {
		return a.TargetID < b.TargetID
	}
	if a.ProviderID != b.ProviderID {
		return a.ProviderID < b.ProviderID
	}
	if a.ModelName != b.ModelName {
		return a.ModelName < b.ModelName
	}
	return a.Endpoint < b.Endpoint
}

func (r *Registry) Snapshot(k model.TargetMetricKey, now time.Time) Snapshot {
	k = k.Normalized()
	if now.IsZero() {
		now = r.clock()
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
	out.Status429, out.Status5xx, out.Transport, out.ClientAborts, out.Truncated, out.ConversionLocal = a.status429, a.status5xx, a.transport, a.clientAborts, a.truncated, a.conversionLocal
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
	return r.Snapshot(k, r.clock())
}

func (r *Registry) CurrentRouteSnapshot(k model.RouteModeKey) RouteSnapshot {
	return r.RouteSnapshot(k, r.clock())
}

// CurrentRouteSnapshots returns detached recent snapshots for all route modes
// belonging to targetID. It applies the same monotonic clock and ten-minute
// horizon as CurrentRouteSnapshot, and returns route modes in stable key order.
func (r *Registry) CurrentRouteSnapshots(targetID string) []RouteSnapshot {
	now := r.clock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.lastIngest) {
		now = r.lastIngest
	}
	targetID = strings.TrimSpace(targetID)
	keys := make([]model.RouteModeKey, 0)
	for key := range r.routeEntries {
		if key.TargetID != targetID {
			continue
		}
		r.pruneRouteKeyLocked(key, now)
		if _, ok := r.routeEntries[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return routeKeyLess(keys[i], keys[j]) })
	out := make([]RouteSnapshot, 0, len(keys))
	for _, key := range keys {
		out = append(out, r.routeSnapshotLocked(key, now))
	}
	return out
}

func (r *Registry) RouteSnapshot(k model.RouteModeKey, now time.Time) RouteSnapshot {
	if now.IsZero() {
		now = r.clock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Before(r.lastIngest) {
		now = r.lastIngest
	}
	r.pruneRouteKeyLocked(k, now)
	return r.routeSnapshotLocked(k.Normalized(), now)
}

func (r *Registry) routeSnapshotLocked(k model.RouteModeKey, now time.Time) RouteSnapshot {
	out := RouteSnapshot{Key: k}
	cutoff := now.Add(-routeHorizon)
	if a := r.routeEntries[k]; a != nil {
		for _, s := range a.samples {
			if s.at.Before(cutoff) {
				continue
			}
			out.Attempts++
			if s.outcome == model.AttemptOutcomeSuccess {
				out.Successes++
			}
			switch s.failureClass {
			case model.MetricFailure429:
				out.Status429++
			case model.MetricFailure5xx:
				out.Status5xx++
			case model.MetricFailureTransport:
				out.Transport++
			case model.MetricFailureUpstreamTrunc:
				out.Truncated++
			case model.MetricFailureConversionLocal:
				out.ConversionLocal++
			case model.MetricFailureClientAbort:
				out.ClientAborts++
			case model.MetricFailureDownstream:
				out.Downstream++
			}
			if s.failureClass == model.MetricFailure5xx || s.failureClass == model.MetricFailureTransport || s.failureClass == model.MetricFailureUpstreamTrunc || s.failureClass == model.MetricFailureConversionLocal {
				out.Failures++
			}
			if s.firstByteMs > 0 {
				out.FirstByteMs = append(out.FirstByteMs, s.firstByteMs)
			}
			if s.ttftMs > 0 {
				out.TTFTMs = append(out.TTFTMs, s.ttftMs)
			}
			if out.LastUsed.Before(s.at) {
				out.LastUsed = s.at
			}
			if s.outcome == model.AttemptOutcomeSuccess && out.LastSuccess.Before(s.at) {
				out.LastSuccess = s.at
			}
			if s.failureClass != "" && s.failureClass != model.MetricFailureNone && s.failureClass != model.MetricFailureClientAbort && out.LastFailure.Before(s.at) {
				out.LastFailure = s.at
			}
		}
	}
	return out
}

// Has reports whether a non-expired entry exists without exposing registry state.
func (r *Registry) Has(k model.TargetMetricKey, now time.Time) bool {
	return r.Snapshot(k, now).LastUsed.After(time.Time{})
}

func (r *Registry) Snapshots(now time.Time) []Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.IsZero() {
		now = r.clock()
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
	out.Status429, out.Status5xx, out.Transport, out.ClientAborts, out.Truncated, out.ConversionLocal = a.status429, a.status5xx, a.transport, a.clientAborts, a.truncated, a.conversionLocal
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

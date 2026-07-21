package metrics

import (
	"autoapi/internal/model"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestRegistryFailureHealthMatrix(t *testing.T) {
	r := New(8, time.Hour)
	k := model.TargetMetricKey{TargetID: "t", ProviderID: "p", ModelName: "m", Endpoint: "/v1/chat/completions"}
	classes := []model.MetricFailureClass{model.MetricFailureHTTPNonRetryable, model.MetricFailureDownstream, model.MetricFailureConversionLocal, model.MetricFailure429, model.MetricFailureClientAbort, model.MetricFailure5xx, model.MetricFailureTransport, model.MetricFailureUpstreamTrunc}
	for _, class := range classes {
		r.Submit(model.TargetMetricEvent{Key: k, RouteMode: model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_chat", UpstreamProtocol: "openai_chat"}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeNonRetryable, FailureClass: class, At: time.Now()})
	}
	s := r.Snapshot(k, time.Now())
	if s.Failures != 4 {
		t.Fatalf("health failures=%d, want 4", s.Failures)
	}
	if s.Downstream != 1 {
		t.Fatalf("downstream=%d, want 1", s.Downstream)
	}
}

func TestRegistryRequestDoesNotAddLatencySamples(t *testing.T) {
	r := New(4, time.Hour)
	k := key("request-only")
	e := ev(k, model.MetricEventRequest, time.Now())
	e.FirstByteMs = 40
	e.TTFTMs = 25
	if !r.Submit(e) {
		t.Fatal("request event rejected")
	}
	s := r.Snapshot(k, time.Now())
	if len(s.FirstByteMs) != 0 || len(s.TTFTMs) != 0 || s.Requests != 1 {
		t.Fatalf("request latency contaminated target samples: %+v", s)
	}
}

func key(id string) model.TargetMetricKey {
	return model.TargetMetricKey{TargetID: id, ProviderID: "p", ModelName: "m", Endpoint: "u"}
}
func ev(k model.TargetMetricKey, kind model.MetricEventKind, at time.Time) model.TargetMetricEvent {
	e := model.TargetMetricEvent{Key: k, Kind: kind, At: at, AttemptOutcome: model.AttemptOutcomeSuccess, RequestOutcome: model.RequestOutcomeSuccess}
	if kind == model.MetricEventAttempt {
		e.RouteMode = model.RouteModeKey{TargetID: k.TargetID, InboundProtocol: "openai_chat", UpstreamProtocol: "openai_chat"}
	}
	return e
}

func TestRegistrySemantics(t *testing.T) {
	k := key("t")
	now := time.Unix(100, 0)
	r := New(10, time.Hour, WithClock(func() time.Time { return now }))
	a := AttributeAttempt(k, ev(k, model.MetricEventAttempt, now).RouteMode, model.AttemptOutcomeRetryable, 429, false, 12, 20)
	a.At = now
	r.Submit(a)
	b := AttributeAttempt(k, ev(k, model.MetricEventAttempt, now).RouteMode, model.AttemptOutcomeClientAbort, 0, true, 0, 0)
	b.At = now
	r.Submit(b)
	c := ev(k, model.MetricEventRequest, now)
	r.Submit(c)
	s := r.Snapshot(k, now)
	if s.Attempts != 2 || s.Requests != 1 || s.Successes != 1 || s.Failures != 0 || s.Status429 != 1 || s.ClientAborts != 1 {
		t.Fatalf("unexpected: %+v", s)
	}
}

func TestRegistryBoundedDetachedTTLAndCapacity(t *testing.T) {
	clock := time.Unix(1, 0)
	r := New(1, time.Second, WithClock(func() time.Time { return clock }))
	k := key("t")
	for i := int64(1); i <= defaultSamples+10; i++ {
		clock = time.Unix(i, 0)
		e := ev(k, model.MetricEventAttempt, time.Unix(i, 0))
		e.FirstByteMs = i
		r.Submit(e)
	}
	s := r.Snapshot(k, time.Unix(74, 0))
	if len(s.FirstByteMs) != defaultSamples || s.FirstByteMs[0] != 11 {
		t.Fatal("bad sample window")
	}
	s.FirstByteMs[0] = 9
	if r.Snapshot(k, time.Unix(74, 0)).FirstByteMs[0] == 9 {
		t.Fatal("slice leaked")
	}
	if r.Snapshot(k, time.Unix(1000, 0)).Attempts != 0 {
		t.Fatal("TTL failed")
	}
	at := time.Unix(2000, 0)
	clock = at
	e := ev(k, model.MetricEventRequest, at)
	r.Submit(e)
	e.Key = key("other")
	r.Submit(e)
	if len(r.Snapshots(at)) != 1 {
		t.Fatal("capacity failed")
	}
}

func TestRegistryConcurrentJSONAndInvalid(t *testing.T) {
	r := New(4, time.Hour)
	k := key("t")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Submit(ev(k, model.MetricEventAttempt, time.Now())) }()
	}
	wg.Wait()
	if r.Snapshot(k, time.Now()).Attempts != 20 {
		t.Fatal("lost writes")
	}
	if _, err := json.Marshal(r.Snapshot(k, time.Now())); err != nil {
		t.Fatal(err)
	}
	if r.Submit(model.TargetMetricEvent{Key: k, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcome("bad")}) || r.Submit(model.TargetMetricEvent{Key: key("x"), Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, TTFTMs: -1}) {
		t.Fatal("invalid accepted")
	}
}

func TestAttributionBranches(t *testing.T) {
	k := key("t")
	tests := []struct {
		name      string
		outcome   model.AttemptOutcome
		status    int
		committed bool
		class     model.MetricFailureClass
	}{
		{"429", model.AttemptOutcomeRetryable, 429, false, model.MetricFailure429},
		{"client abort wins", model.AttemptOutcomeClientAbort, 500, true, model.MetricFailureClientAbort},
		{"truncated committed", model.AttemptOutcomeTruncated, 200, true, model.MetricFailureUpstreamTrunc},
		{"truncated uncommitted", model.AttemptOutcomeTruncated, 0, false, model.MetricFailureUpstreamTrunc},
		{"truncated wins over 5xx", model.AttemptOutcomeTruncated, 503, true, model.MetricFailureUpstreamTrunc},
		{"5xx", model.AttemptOutcomeRetryable, 503, false, model.MetricFailure5xx},
		{"transport", model.AttemptOutcomeRetryable, 0, false, model.MetricFailureTransport},
		{"unknown", model.AttemptOutcomeUnknown, 0, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := AttributeAttempt(k, model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_chat", UpstreamProtocol: "openai_chat"}, tc.outcome, tc.status, tc.committed, 0, 0)
			if e.FailureClass != tc.class {
				t.Fatalf("class=%q want %q", e.FailureClass, tc.class)
			}
			if !e.Valid() {
				t.Fatal("attribution produced invalid event")
			}
		})
	}
	invalid := AttributeAttempt(k, model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_chat", UpstreamProtocol: "openai_chat"}, model.AttemptOutcome("invalid"), 0, false, 0, 0)
	if invalid.Valid() {
		t.Fatal("invalid outcome became valid event")
	}
}

func TestUnknownOutcomesDoNotBecomeFailures(t *testing.T) {
	r := New(2, time.Hour)
	k := key("unknown")
	a := ev(k, model.MetricEventAttempt, time.Now())
	a.AttemptOutcome = model.AttemptOutcomeUnknown
	q := ev(k, model.MetricEventRequest, time.Now())
	q.RequestOutcome = model.RequestOutcomeUnknown
	if !r.Submit(a) || !r.Submit(q) {
		t.Fatal("unknown outcomes should be valid diagnostic events")
	}
	s := r.Snapshot(k, time.Now())
	if s.Attempts != 1 || s.Requests != 1 || s.Successes != 0 || s.Failures != 0 {
		t.Fatalf("unknown outcomes counted as health result: %+v", s)
	}
	for _, class := range []model.MetricFailureClass{"", model.MetricFailureNone, model.MetricFailure429, model.MetricFailureClientAbort} {
		e := ev(k, model.MetricEventAttempt, time.Now())
		e.AttemptOutcome = model.AttemptOutcomeRetryable
		e.FailureClass = class
		if !r.Submit(e) {
			t.Fatalf("class %q rejected", class)
		}
	}
	if got := r.Snapshot(k, time.Now()); got.Failures != 0 {
		t.Fatalf("non-provider classes became failures: %+v", got)
	}
}

func TestFutureEventRejected(t *testing.T) {
	e := ev(key("future"), model.MetricEventAttempt, time.Now().UTC().Add(time.Minute))
	if e.Valid() {
		t.Fatal("future event accepted")
	}
	r := New(1, time.Hour)
	if r.Submit(e) {
		t.Fatal("registry accepted future event")
	}
	zero := ev(key("zero"), model.MetricEventAttempt, time.Time{})
	if !zero.Valid() || !r.Submit(zero) {
		t.Fatal("zero timestamp should be filled by registry")
	}
}

func TestRouteRecentStoreCapHorizonAndClock(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(4, time.Hour, WithClock(func() time.Time { return now }))
	k := model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_chat", UpstreamProtocol: "openai_responses"}
	for i := int64(0); i < defaultSamples+1; i++ {
		e := model.TargetMetricEvent{Key: key("t"), RouteMode: k, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, FirstByteMs: i + 1, At: now.Add(-time.Duration(defaultSamples-i) * time.Second)}
		if !r.Submit(e) {
			t.Fatal("route event rejected")
		}
	}
	s := r.CurrentRouteSnapshot(k)
	if s.Attempts != defaultSamples || len(s.FirstByteMs) != defaultSamples || s.FirstByteMs[0] != 2 {
		t.Fatalf("route cap wrong: %+v", s)
	}
	boundary := model.TargetMetricEvent{Key: key("t"), RouteMode: k, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeRetryable, FailureClass: model.MetricFailure5xx, At: now.Add(-routeHorizon)}
	r.Submit(boundary)
	if got := r.CurrentRouteSnapshot(k); got.Attempts != defaultSamples {
		t.Fatalf("boundary sample was not retained/capped: %+v", got)
	}
	now = now.Add(routeHorizon + time.Nanosecond)
	if got := r.CurrentRouteSnapshot(k); got.Attempts != 0 {
		t.Fatalf("expired route samples remain: %+v", got)
	}
}

func TestRouteRecentStoreIsolationCategoriesRequestAndRestore(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(4, time.Hour, WithClock(func() time.Time { return now }))
	native := model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_chat", UpstreamProtocol: "openai_chat"}
	conversion := model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_chat", UpstreamProtocol: "openai_responses"}
	classes := []model.MetricFailureClass{model.MetricFailure429, model.MetricFailure5xx, model.MetricFailureTransport, model.MetricFailureUpstreamTrunc, model.MetricFailureConversionLocal, model.MetricFailureClientAbort, model.MetricFailureDownstream}
	for _, class := range classes {
		r.Submit(model.TargetMetricEvent{Key: key("t"), RouteMode: conversion, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeRetryable, FailureClass: class, At: now})
	}
	r.Submit(model.TargetMetricEvent{Key: key("t"), RouteMode: native, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: now})
	r.Submit(model.TargetMetricEvent{Key: key("t"), Kind: model.MetricEventRequest, RequestOutcome: model.RequestOutcomeFailure, At: now})
	got := r.CurrentRouteSnapshot(conversion)
	if got.Attempts != int64(len(classes)) || got.Failures != 4 || got.Status429 != 1 || got.Status5xx != 1 || got.Transport != 1 || got.Truncated != 1 || got.ConversionLocal != 1 || got.ClientAborts != 1 || got.Downstream != 1 {
		t.Fatalf("route category accounting wrong: %+v", got)
	}
	if nativeGot := r.CurrentRouteSnapshot(native); nativeGot.Attempts != 1 || nativeGot.Successes != 1 {
		t.Fatalf("native/conversion route modes not isolated: %+v", nativeGot)
	}
	r.Restore([]model.TargetRuntimeSummary{{Key: key("t"), Attempts: 10, Failures: 10, LastUsed: now.UnixMilli(), UpdatedAt: now.UnixMilli()}}, now)
	if got := r.CurrentRouteSnapshot(conversion); got.Attempts != 0 {
		t.Fatalf("restore recreated route recent state: %+v", got)
	}
	if got := r.Snapshot(key("t"), now); got.Attempts != 10 {
		t.Fatalf("restore lost target cumulative summary: %+v", got)
	}
}

func TestRouteCapacityDeterministicEvictionIsIndependentOfTargetCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(2, time.Hour, WithClock(func() time.Time { return now }))
	targetKey := model.TargetMetricKey{TargetID: "t", ProviderID: "p", ModelName: "m", Endpoint: "u"}
	makeEvent := func(upstream string) model.TargetMetricEvent {
		return model.TargetMetricEvent{Key: targetKey, RouteMode: model.RouteModeKey{TargetID: "t", InboundProtocol: "chat", UpstreamProtocol: upstream}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: now}
	}
	r.Submit(makeEvent("a"))
	r.Submit(makeEvent("b"))
	r.Submit(makeEvent("c"))
	if r.CurrentRouteSnapshot(makeEvent("a").RouteMode).Attempts != 0 || r.CurrentRouteSnapshot(makeEvent("b").RouteMode).Attempts != 1 || r.CurrentRouteSnapshot(makeEvent("c").RouteMode).Attempts != 1 {
		t.Fatal("route capacity did not deterministically evict lexicographically oldest key")
	}

	// A target-key eviction must not remove a recent route sample for the same
	// TargetID. The route store has its own capacity and horizon.
	r = New(1, time.Hour, WithClock(func() time.Time { return now }))
	key1 := model.TargetMetricKey{TargetID: "t", ProviderID: "p1", ModelName: "m1", Endpoint: "e1"}
	key2 := model.TargetMetricKey{TargetID: "t", ProviderID: "p2", ModelName: "m2", Endpoint: "e2"}
	r.Submit(model.TargetMetricEvent{Key: key1, RouteMode: model.RouteModeKey{TargetID: "t", InboundProtocol: "chat", UpstreamProtocol: "native1"}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: now})
	r.Submit(model.TargetMetricEvent{Key: key2, Kind: model.MetricEventRequest, RequestOutcome: model.RequestOutcomeSuccess, At: now})
	kept := model.RouteModeKey{TargetID: "t", InboundProtocol: "chat", UpstreamProtocol: "native1"}
	if r.CurrentRouteSnapshot(kept).Attempts != 1 {
		t.Fatal("target capacity eviction incorrectly removed same-TargetID route state")
	}
	r.Submit(model.TargetMetricEvent{Key: key2, RouteMode: model.RouteModeKey{TargetID: "t", InboundProtocol: "chat", UpstreamProtocol: "native2"}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: now})
	if r.CurrentRouteSnapshot(kept).Attempts != 0 {
		t.Fatal("route capacity did not independently evict the oldest route key")
	}

}

func TestTargetTTLEvictionPreservesRecentSameTargetRoute(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(2, time.Second, WithClock(func() time.Time { return now }))
	key1 := model.TargetMetricKey{TargetID: "t", ProviderID: "p1", ModelName: "m1", Endpoint: "e1"}
	key2 := model.TargetMetricKey{TargetID: "t", ProviderID: "p2", ModelName: "m2", Endpoint: "e2"}
	r.Submit(model.TargetMetricEvent{Key: key1, RouteMode: model.RouteModeKey{TargetID: "t", InboundProtocol: "chat", UpstreamProtocol: "native"}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: now.Add(-time.Minute)})
	now = now.Add(2 * time.Second)
	r.Submit(model.TargetMetricEvent{Key: key2, Kind: model.MetricEventRequest, RequestOutcome: model.RequestOutcomeSuccess, At: now})
	if got := r.CurrentRouteSnapshot(model.RouteModeKey{TargetID: "t", InboundProtocol: "chat", UpstreamProtocol: "native"}); got.Attempts != 1 {
		t.Fatalf("target TTL eviction incorrectly removed recent route: %+v", got)
	}
}

func TestRegistryIngestOrderAndTargetTimesAreMonotonic(t *testing.T) {
	clock := time.Unix(1000, 0)
	r := New(1, time.Hour, WithClock(func() time.Time { return clock }))
	k := key("ordered")
	r.Submit(model.TargetMetricEvent{Key: k, RouteMode: model.RouteModeKey{TargetID: "ordered", InboundProtocol: "chat", UpstreamProtocol: "chat"}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: clock})
	clock = clock.Add(time.Second)
	r.Submit(model.TargetMetricEvent{Key: k, RouteMode: model.RouteModeKey{TargetID: "ordered", InboundProtocol: "chat", UpstreamProtocol: "chat"}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeRetryable, FailureClass: model.MetricFailure5xx, At: clock.Add(-time.Hour)})
	s := r.Snapshot(k, clock)
	if !s.LastUsed.Equal(time.Unix(1000, 0)) || !s.LastSuccess.Equal(time.Unix(1000, 0)) || !s.LastFailure.Before(s.LastUsed) {
		t.Fatalf("target timestamps regressed or violated ordering: %+v", s)
	}
	if got := r.CurrentRouteSnapshot(model.RouteModeKey{TargetID: "ordered", InboundProtocol: "chat", UpstreamProtocol: "chat"}); got.Attempts != 2 || !got.LastUsed.Equal(clock) {
		t.Fatalf("route did not use ingest order: %+v", got)
	}
}

func TestCurrentRouteSnapshotsByTargetFiltersSortsPrunesAndDetaches(t *testing.T) {
	now := time.Unix(1000, 0)
	r := New(8, time.Hour, WithClock(func() time.Time { return now }))
	add := func(target, inbound, upstream string, at time.Time, firstByte int64) {
		if !r.Submit(model.TargetMetricEvent{
			Key:       model.TargetMetricKey{TargetID: target, ProviderID: "p", ModelName: "m"},
			RouteMode: model.RouteModeKey{TargetID: target, InboundProtocol: inbound, UpstreamProtocol: upstream},
			Kind:      model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess,
			FirstByteMs: firstByte, At: at,
		}) {
			t.Fatal("route event rejected")
		}
	}
	add("target", "openai_responses", "openai_chat", now, 2)
	add("target", "openai_chat", "openai_chat", now, 1)
	add("target", "anthropic_messages", "openai_responses", now, 3)
	now = now.Add(routeHorizon + time.Nanosecond)
	add("target", "openai_responses", "openai_chat", now, 2)
	add("target", "openai_chat", "openai_chat", now, 1)
	add("other", "openai_chat", "openai_chat", now, 99)

	got := r.CurrentRouteSnapshots("target")
	if len(got) != 2 {
		t.Fatalf("filtered/pruned routes = %#v", got)
	}
	if got[0].Key.InboundProtocol != "openai_chat" || got[1].Key.InboundProtocol != "openai_responses" {
		t.Fatalf("route order = %#v", got)
	}
	if got[0].FirstByteMs[0] != 1 {
		t.Fatalf("first route samples = %#v", got[0].FirstByteMs)
	}
	got[0].FirstByteMs[0] = 999
	again := r.CurrentRouteSnapshots("target")
	if again[0].FirstByteMs[0] != 1 {
		t.Fatalf("returned samples alias registry state: %#v", again[0].FirstByteMs)
	}
	if len(r.CurrentRouteSnapshots("other")) != 1 {
		t.Fatal("target filtering changed other target state")
	}
}

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
	k := model.TargetMetricKey{ProviderID: "p", ModelName: "m", Endpoint: "/v1/chat/completions"}
	classes := []model.MetricFailureClass{model.MetricFailureHTTPNonRetryable, model.MetricFailureDownstream, model.MetricFailure429, model.MetricFailureClientAbort, model.MetricFailure5xx, model.MetricFailureTransport, model.MetricFailureUpstreamTrunc}
	for _, class := range classes {
		r.Submit(model.TargetMetricEvent{Key: k, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeNonRetryable, FailureClass: class, At: time.Now()})
	}
	s := r.Snapshot(k, time.Now())
	if s.Failures != 3 {
		t.Fatalf("health failures=%d, want 3", s.Failures)
	}
	if s.Downstream != 1 {
		t.Fatalf("downstream=%d, want 1", s.Downstream)
	}
}

func key(id string) model.TargetMetricKey {
	return model.TargetMetricKey{TargetID: id, ProviderID: "p", ModelName: "m", Endpoint: "u"}
}
func ev(k model.TargetMetricKey, kind model.MetricEventKind, at time.Time) model.TargetMetricEvent {
	return model.TargetMetricEvent{Key: k, Kind: kind, At: at, AttemptOutcome: model.AttemptOutcomeSuccess, RequestOutcome: model.RequestOutcomeSuccess}
}

func TestRegistrySemantics(t *testing.T) {
	r := New(10, time.Hour)
	k := key("t")
	now := time.Unix(100, 0)
	a := AttributeAttempt(k, model.AttemptOutcomeRetryable, 429, false, 12, 20)
	a.At = now
	r.Submit(a)
	b := AttributeAttempt(k, model.AttemptOutcomeClientAbort, 0, true, 0, 0)
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
	r := New(1, time.Second)
	k := key("t")
	for i := int64(1); i <= defaultSamples+10; i++ {
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
			e := AttributeAttempt(k, tc.outcome, tc.status, tc.committed, 0, 0)
			if e.FailureClass != tc.class {
				t.Fatalf("class=%q want %q", e.FailureClass, tc.class)
			}
			if !e.Valid() {
				t.Fatal("attribution produced invalid event")
			}
		})
	}
	invalid := AttributeAttempt(k, model.AttemptOutcome("invalid"), 0, false, 0, 0)
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

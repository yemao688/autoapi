package proxy

import (
	"sync"
	"testing"

	"autoapi/internal/model"
)

type metricSpy struct {
	mu     sync.Mutex
	events []model.TargetMetricEvent
}

func (s *metricSpy) Events() []model.TargetMetricEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.TargetMetricEvent(nil), s.events...)
}

func (s *metricSpy) Submit(e model.TargetMetricEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return true
}

func TestRequestOutcomeStreamStates(t *testing.T) {
	cases := []struct {
		name string
		log  model.RequestLog
		want model.RequestOutcome
	}{
		{"success", model.RequestLog{IsStream: true, StatusCode: 200}, model.RequestOutcomeSuccess},
		{"truncate", model.RequestLog{IsStream: true, StatusCode: 200, Error: "upstream truncated"}, model.RequestOutcomePartial},
		{"downstream", model.RequestLog{IsStream: true, StatusCode: 200, Error: "write failed"}, model.RequestOutcomePartial},
		{"abort", model.RequestLog{IsStream: true, StatusCode: statusClientClosed, Error: "client disconnected"}, model.RequestOutcomeAborted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestOutcome(&tc.log); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestMetricEmissionIsOneEventPerCall(t *testing.T) {
	spy := &metricSpy{}
	p := &Proxy{metricSink: spy}
	c := candidate{provider: &model.Provider{ID: "provider-1"}, modelName: "model-1", targetID: "target-1"}
	p.emitAttempt(c, "/v1/chat/completions", "request-1", model.AttemptOutcomeRetryable, 429, false, 0, 0)
	p.emitAttempt(c, "/v1/chat/completions", "request-1", model.AttemptOutcomeSuccess, 200, false, 8, 8)
	p.emitRequest(&model.RequestLog{ID: "request-1", ProviderID: "provider-1", Model: "model-1", StatusCode: 200}, "/v1/chat/completions")

	if len(spy.events) != 3 {
		t.Fatalf("got %d metric events, want one per attempt plus one request", len(spy.events))
	}
	if spy.events[0].Kind != model.MetricEventAttempt || spy.events[1].Kind != model.MetricEventAttempt {
		t.Fatalf("first two events should be attempts: %#v", spy.events)
	}
	if spy.events[0].FailureClass != model.MetricFailure429 {
		t.Fatalf("429 attempt failure class = %q", spy.events[0].FailureClass)
	}
	if spy.events[2].Kind != model.MetricEventRequest || spy.events[2].RequestOutcome != model.RequestOutcomeSuccess {
		t.Fatalf("last event should be successful request: %#v", spy.events[2])
	}
}

func TestMetricEmissionDoesNotAttributeClientAbortAsProviderFailure(t *testing.T) {
	spy := &metricSpy{}
	p := &Proxy{metricSink: spy}
	c := candidate{provider: &model.Provider{ID: "provider-1"}, modelName: "model-1", targetID: "target-1"}
	p.emitAttempt(c, "/v1/chat/completions", "request-1", model.AttemptOutcomeClientAbort, statusClientClosed, true, 2, 2)

	if len(spy.events) != 1 || spy.events[0].FailureClass != model.MetricFailureClientAbort {
		t.Fatalf("client abort event = %#v", spy.events)
	}
	if spy.events[0].FailureClass == model.MetricFailure5xx || spy.events[0].FailureClass == model.MetricFailureTransport {
		t.Fatal("client abort was attributed as provider failure")
	}
}

func TestMetricRequestWithoutUpstreamStillEmits(t *testing.T) {
	spy := &metricSpy{}
	p := &Proxy{metricSink: spy}
	p.emitRequest(&model.RequestLog{StatusCode: 401}, "/v1/chat/completions")
	if len(spy.events) != 1 {
		t.Fatalf("got %d events, want one request event", len(spy.events))
	}
	e := spy.events[0]
	if e.Kind != model.MetricEventRequest || e.Key.ProviderID != model.MetricProviderPreflight || e.Key.ModelName != model.MetricProviderClient {
		t.Fatalf("unexpected preflight request event: %#v", e)
	}
}

type panicMetricSink struct{}

func (panicMetricSink) Submit(model.TargetMetricEvent) bool { panic("metric sink failure") }

func TestMetricSinkPanicIsBestEffort(t *testing.T) {
	p := &Proxy{metricSink: panicMetricSink{}}
	p.submitMetric(model.TargetMetricEvent{Kind: model.MetricEventRequest})
}

package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestTargetBreakerRollingWindowAndFailureClasses(t *testing.T) {
	b := &targetBreaker{}
	now := time.Unix(1000, 0)
	threshold, window := targetFailureThreshold, targetFailureWindow
	for i := 0; i < targetFailureThreshold-1; i++ {
		b.recordFailure(now.Add(time.Duration(i)*time.Second), "500", window)
	}
	if !b.allow(now.Add(4*time.Second), threshold, window) {
		t.Fatal("breaker opened before threshold")
	}
	b.recordFailure(now.Add(4*time.Second), "429", window)
	if b.allow(now.Add(5*time.Second), threshold, window) {
		t.Fatal("breaker remained open below threshold")
	}
	if !b.allow(now.Add(window+5*time.Second), threshold, window) {
		t.Fatal("aged failures did not recover")
	}
	if !targetFailure(io.ErrUnexpectedEOF, 0) || !targetFailure(nil, 401) || !targetFailure(nil, 403) || !targetFailure(nil, 429) || !targetFailure(nil, 503) {
		t.Fatal("required failure classes were not counted")
	}
	if targetFailure(context.Canceled, 499) {
		t.Fatal("client abort counted")
	}
}

func TestTargetBreakerStatusIncludesRouteMetadata(t *testing.T) {
	b := &targetBreaker{}
	now := time.Now()
	for i := 0; i < targetFailureThreshold; i++ {
		b.recordFailure(now, "503", targetFailureWindow)
	}
	s := b.status(model.RouteModeKey{TargetID: "target", InboundProtocol: "chat", UpstreamProtocol: "chat"}, "https://upstream.example/v1/chat/completions", 2, targetFailureThreshold, targetFailureWindow, now)
	if s.State != "open" || s.Endpoint == "" || s.Order != 2 || s.RecoveryAtMs == 0 {
		t.Fatalf("status=%+v", s)
	}
}

func TestTargetBreakerSkippedChainAndCurrentConfigStatus(t *testing.T) {
	provider := &model.Provider{ID: "p", Name: "P", BaseURL: "https://old.example", Enabled: true}
	store := &mockStore{providers: map[string]*model.Provider{"p": provider}, rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "m", Enabled: true}}}}}
	p := New(store, &mockService{}, 0, nil)
	defer p.Shutdown()
	c := candidate{provider: provider, targetID: "t", modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions", firstByteBudget: time.Second}
	b := p.targetBreakerFor(c)
	for i := 0; i < targetFailureThreshold; i++ {
		b.recordFailure(time.Now(), "503", targetFailureWindow)
	}
	provider.BaseURL = "https://current.example/api"
	store.rules[0].Targets[0].Tier = 7
	statuses := p.TargetBreakerStatuses()
	if len(statuses) != 1 || statuses[0].State != "open" || statuses[0].Order != 0 || statuses[0].Endpoint != "https://current.example/api/v1/chat/completions" {
		t.Fatalf("statuses=%+v", statuses)
	}
	logEntry := &model.RequestLog{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	p.forwardWithFailover(httptest.NewRecorder(), req, []byte(`{}`), []candidate{c}, false, 0, logEntry)
	if len(logEntry.Chain) != 1 || logEntry.Chain[0].Status != "target_breaker_open" || logEntry.Chain[0].Endpoint != "https://current.example/api/v1/chat/completions" {
		t.Fatalf("chain=%+v", logEntry.Chain)
	}
}

func TestTargetBreakerDoesNotCountUnattemptedCandidateOnExhaustedBudget(t *testing.T) {
	providers := map[string]*model.Provider{
		"p0": {ID: "p0", Name: "P0", BaseURL: "https://p0.example", Enabled: true},
		"p1": {ID: "p1", Name: "P1", BaseURL: "https://p1.example", Enabled: true},
	}
	store := &mockStore{providers: providers}
	p := New(store, &mockService{}, 0, nil)
	defer p.Shutdown()
	first := candidate{provider: providers["p0"], targetID: "t0", modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions", firstByteBudget: -time.Second}
	second := candidate{provider: providers["p1"], targetID: "t1", modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions", firstByteBudget: -time.Second}
	for i := 0; i < targetFailureThreshold; i++ {
		p.targetBreakerFor(first).recordFailure(time.Now(), "503", targetFailureWindow)
	}
	logEntry := &model.RequestLog{}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	p.forwardWithFailover(httptest.NewRecorder(), req, []byte(`{}`), []candidate{first, second}, false, 0, logEntry)
	status := p.TargetBreakerStatuses()
	for _, item := range status {
		if item.TargetID == "t1" && item.FailureCount != 0 {
			t.Fatalf("unattempted candidate was penalized: %+v", item)
		}
	}
}

func TestTargetBreakerUsesConfiguredLimitsAndResetIsolated(t *testing.T) {
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", Enabled: true}}}
	p := New(st, &mockService{}, 0, func() (*model.Settings, error) {
		return &model.Settings{Advanced: model.AdvancedSettings{TargetBreakerThreshold: 2, TargetBreakerWindowSeconds: 30}}, nil
	})
	defer p.Shutdown()
	c := candidate{provider: st.providers["p"], targetID: "t", modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions"}
	b := p.targetBreakerFor(c)
	_, window := p.targetBreakerConfig()
	b.recordFailure(time.Now(), "503", window)
	b.recordFailure(time.Now(), "503", window)
	statuses := p.TargetBreakerStatuses()
	if len(statuses) != 1 || statuses[0].State != "open" || statuses[0].Threshold != 2 || statuses[0].WindowSeconds != 30 {
		t.Fatalf("configured status=%+v", statuses)
	}
	provider := p.breakerFor("p")
	route := p.routeBreakerFor(routeModeKeyForCandidate(c))
	provider.Record(false)
	route.Record(false)
	p.ResetTargetBreakers()
	if len(p.TargetBreakerStatuses()) != 0 || provider.ConsecutiveFailures() != 1 || route.ConsecutiveFailures() != 1 {
		t.Fatalf("reset leaked across breaker classes: target=%+v provider=%d route=%d", p.TargetBreakerStatuses(), provider.ConsecutiveFailures(), route.ConsecutiveFailures())
	}
}

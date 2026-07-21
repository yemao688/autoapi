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
	for i := 0; i < targetFailureThreshold-1; i++ {
		b.recordFailure(now.Add(time.Duration(i)*time.Second), "500")
	}
	if !b.allow(now.Add(4 * time.Second)) {
		t.Fatal("breaker opened before threshold")
	}
	b.recordFailure(now.Add(4*time.Second), "429")
	if b.allow(now.Add(5 * time.Second)) {
		t.Fatal("breaker remained open below threshold")
	}
	if !b.allow(now.Add(targetFailureWindow + 5*time.Second)) {
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
		b.recordFailure(now, "503")
	}
	s := b.status(model.RouteModeKey{TargetID: "target", InboundProtocol: "chat", UpstreamProtocol: "chat"}, "https://upstream.example/v1/chat/completions", 2, now)
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
		b.recordFailure(time.Now(), "503")
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
		p.targetBreakerFor(first).recordFailure(time.Now(), "503")
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

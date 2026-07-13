package api

import (
	"errors"
	"testing"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/scoring"
)

type replayStoreStub struct {
	StoreService
	log       *model.RequestLog
	rule      *model.ModelRule
	providers map[string]*model.Provider
	models    map[string]*model.Model
}

func (s *replayStoreStub) GetRequestLog(string) (*model.RequestLog, error) { return s.log, nil }
func (s *replayStoreStub) GetModelRule(string) (*model.ModelRule, error)   { return s.rule, nil }
func (s *replayStoreStub) ListModelRules() ([]model.ModelRule, error) {
	return []model.ModelRule{*s.rule}, nil
}
func (s *replayStoreStub) GetProvider(id string) (*model.Provider, error) {
	p := s.providers[id]
	if p == nil {
		return nil, errors.New("missing provider")
	}
	return p, nil
}
func (s *replayStoreStub) GetModel(p, n string) (*model.Model, error) {
	m := s.models[p+":"+n]
	if m == nil {
		return nil, errors.New("missing model")
	}
	return m, nil
}

type replayMetricsStub struct{ snapshot metrics.Snapshot }

func (s replayMetricsStub) CurrentSnapshot(k model.TargetMetricKey) metrics.Snapshot {
	out := s.snapshot
	out.Key = k
	return out
}

func TestReplayOutcomeTable(t *testing.T) {
	tests := []struct {
		name, want string
		log        model.RequestLog
	}{
		{"success", "success", model.RequestLog{StatusCode: 200, Chain: []model.RequestLogChainEntry{{Status: "success", TargetID: "t"}}}},
		{"retry failure", "failure", model.RequestLog{Chain: []model.RequestLogChainEntry{{Status: "retryable", TargetID: "t"}}}},
		{"truncated", "partial", model.RequestLog{StatusCode: 200, Chain: []model.RequestLogChainEntry{{Status: "truncated", TargetID: "t"}}}},
		{"downstream", "partial", model.RequestLog{StatusCode: 200, Chain: []model.RequestLogChainEntry{{Status: "downstream_error", TargetID: "t"}}}},
		{"abort", "aborted", model.RequestLog{Chain: []model.RequestLogChainEntry{{Status: "client_abort", TargetID: "t"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeFor(tt.log); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestReplayLogImmutableCostAvailabilityAndMetadata(t *testing.T) {
	rule := &model.ModelRule{ID: "r", Name: "rule", Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "m", Tier: 2, Enabled: true}}}
	log := &model.RequestLog{ID: "log", RouteID: "r", StatusCode: 200, Chain: []model.RequestLogChainEntry{
		{TargetID: "t", ProviderID: "p", ModelName: "m", Status: "success", UpstreamStarted: true, RequestCost: .25, RequestCostAvailable: true},
		{TargetID: "t", ProviderID: "p", ModelName: "m", Status: "retryable", UpstreamStarted: true, RequestCost: 0, RequestCostAvailable: false},
		{TargetID: "t", ProviderID: "p", ModelName: "m", Status: "circuit_open"},
	}}
	s := &replayStoreStub{log: log, rule: rule, providers: map[string]*model.Provider{"p": {ID: "p"}}, models: map[string]*model.Model{"p:m": {Name: "m", RequestPrice: 99}}}
	app := &App{deps: Deps{Store: s, Metrics: replayMetricsStub{snapshot: metrics.Snapshot{Requests: 7, Attempts: 8}}}}
	got, err := app.ReplayLog("log")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 3 || got.Endpoint != "/v1/chat/completions" {
		t.Fatalf("result=%+v", got)
	}
	if !got.Attempts[0].Score.Cost.Available || got.Attempts[0].Score.Cost.Cost != .25 {
		t.Fatalf("known cost=%+v", got.Attempts[0].Score.Cost)
	}
	if got.Attempts[1].Score.Cost.Available || got.Attempts[2].Score.Cost.Available {
		t.Fatalf("availability not distinguished: %+v", got.Attempts)
	}
	if got.Attempts[0].Score.Metrics.Requests != 7 {
		t.Fatalf("metrics=%+v", got.Attempts[0].Score)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected unavailable-price warning")
	}
}

func TestReplayMissingTargetAndProvider(t *testing.T) {
	rule := &model.ModelRule{ID: "r", Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "missing", ModelName: "m"}}}
	log := &model.RequestLog{ID: "l", RouteID: "r", Chain: []model.RequestLogChainEntry{{TargetID: "other", ProviderID: "missing", ModelName: "m", Status: "success", UpstreamStarted: true, RequestCostAvailable: true}}}
	got, err := (&App{deps: Deps{Store: &replayStoreStub{log: log, rule: rule, providers: map[string]*model.Provider{}, models: map[string]*model.Model{}}}}).ReplayLog("l")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Attempts[0].TargetMissing || !got.Attempts[0].ProviderMissing || got.Attempts[0].Score.Availability != scoring.Unavailable {
		t.Fatalf("missing=%+v", got.Attempts[0])
	}
}

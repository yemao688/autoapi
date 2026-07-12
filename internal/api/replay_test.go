package api

import (
	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/scoring"
	"errors"
	"testing"
	"time"
)

type replayStoreStub struct {
	StoreService
	log   *model.RequestLog
	rule  *model.ModelRule
	price *model.Price
}

type replayMetricsStub struct{ snapshot metrics.Snapshot }

func (s *replayMetricsStub) CurrentSnapshot(model.TargetMetricKey) metrics.Snapshot {
	return s.snapshot
}

func (s *replayStoreStub) GetRequestLog(string) (*model.RequestLog, error) { return s.log, nil }
func (s *replayStoreStub) GetModelRule(id string) (*model.ModelRule, error) {
	if id == "bad" {
		return nil, errors.New("missing rule")
	}
	return s.rule, nil
}
func (s *replayStoreStub) ListModelRules() ([]model.ModelRule, error) {
	return []model.ModelRule{*s.rule}, nil
}
func (s *replayStoreStub) GetProvider(id string) (*model.Provider, error) {
	if id == "nil" {
		return nil, nil
	}
	if id == "err" {
		return nil, errors.New("provider error")
	}
	return &model.Provider{ID: id}, nil
}
func (s *replayStoreStub) ResolvePriceAt(string, string, string, int64) (*model.Price, error) {
	return s.price, nil
}

func TestReplayOutcomeAndSelectedTarget(t *testing.T) {
	tests := []struct {
		name, outcome, selected string
		log                     model.RequestLog
	}{
		{"retry success", "success", "b", model.RequestLog{Chain: []model.RequestLogChainEntry{{TargetID: "a", Status: "retryable"}, {TargetID: "b", Status: "success"}}}},
		{"success truncated", "partial", "a", model.RequestLog{StatusCode: 200, Chain: []model.RequestLogChainEntry{{TargetID: "a", Status: "truncated"}}}},
		{"success downstream", "partial", "a", model.RequestLog{StatusCode: 200, Chain: []model.RequestLogChainEntry{{TargetID: "a", Status: "downstream_error"}}}},
		{"client abort", "aborted", "a", model.RequestLog{Chain: []model.RequestLogChainEntry{{TargetID: "a", Status: "client_abort", StatusCode: 499}}}},
		{"failure only", "failure", "", model.RequestLog{Chain: []model.RequestLogChainEntry{{TargetID: "a", Status: "retryable"}}}},
		{"unknown status", "unknown", "", model.RequestLog{Chain: []model.RequestLogChainEntry{{TargetID: "a", Status: "unknown"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcomeFor(tt.log); got != tt.outcome {
				t.Fatalf("outcome=%q want %q", got, tt.outcome)
			}
			if got := selectedTarget(tt.log.Chain); got != tt.selected {
				t.Fatalf("selected=%q want %q", got, tt.selected)
			}
		})
	}
	if got := outcomeFor(model.RequestLog{}); got != "unknown" {
		t.Fatalf("empty outcome=%q", got)
	}
	if got := outcomeFor(model.RequestLog{StatusCode: 499, Chain: []model.RequestLogChainEntry{{TargetID: "a", Status: "success"}}}); got != "aborted" {
		t.Fatalf("top-level 499 outcome=%q", got)
	}
}

func TestAdditionalRetriesForIgnoresPreflightAndCircuit(t *testing.T) {
	chain := []model.RequestLogChainEntry{
		{AttemptOrder: 1, Status: "preflight_error"},
		{AttemptOrder: 2, Status: "circuit_open"},
		{AttemptOrder: 3, Status: "retryable"},
		{AttemptOrder: 4, Status: "success"},
	}
	if got := additionalRetriesFor(chain, chain[0]); got != 0 {
		t.Fatalf("first retries=%d", got)
	}
	if got := additionalRetriesFor(chain, chain[2]); got != 0 {
		t.Fatalf("retryable retries=%d", got)
	}
	if got := additionalRetriesFor(chain, chain[3]); got != 1 {
		t.Fatalf("success retries=%d want 1", got)
	}
}

func TestReplayLogIntegrationFallbackMissingAndRetryCost(t *testing.T) {
	rule := &model.ModelRule{ID: "rule", Name: "fallback", Targets: []model.ModelRuleTarget{{ID: "known", ProviderID: "p", ModelName: "m", Enabled: true}}}
	price := &model.Price{UpstreamModel: "m", BillingMode: model.BillingModeRequest, Currency: "USD", RequestPricePerRequest: 2, Confidence: model.CostConfidenceExact, EffectiveAt: 1}
	log := &model.RequestLog{ID: "replay", Timestamp: 1000, RouteID: "bad", RouteLabel: "fallback", InputTokens: 10, Chain: []model.RequestLogChainEntry{
		{AttemptOrder: 1, TargetID: "missing", ProviderID: "p", ModelName: "m", Status: "preflight_error"},
		{AttemptOrder: 2, TargetID: "known", ProviderID: "p", ModelName: "m", Status: "retryable"},
		{AttemptOrder: 3, TargetID: "known", ProviderID: "p", ModelName: "m", Status: "success"},
	}}
	app := &App{deps: Deps{Store: &replayStoreStub{log: log, rule: rule, price: price}}}
	got, err := app.ReplayLog("replay")
	if err != nil {
		t.Fatal(err)
	}
	if got.RuleName != "fallback" || len(got.Attempts) != 3 {
		t.Fatalf("fallback/attempts: %+v", got)
	}
	if got.Attempts[0].Score.Availability != scoring.Unavailable || got.Attempts[0].Score.Reason != "target_missing" {
		t.Fatalf("missing target score: %+v", got.Attempts[0])
	}
	if got.Attempts[1].Score.EstimatedCost != 2 || got.Attempts[2].Score.EstimatedCost != 4 {
		t.Fatalf("retry costs: %v, %v", got.Attempts[1].Score.EstimatedCost, got.Attempts[2].Score.EstimatedCost)
	}

	log.StatusCode = 499
	log.Chain[0].Status = "success"
	if got, err = app.ReplayLog("replay"); err != nil || got.RequestOutcome != "aborted" {
		t.Fatalf("top-level 499: got=%+v err=%v", got, err)
	}
}

func TestReplayLogProviderNilIsUnavailable(t *testing.T) {
	rule := &model.ModelRule{ID: "rule", Name: "r", Targets: []model.ModelRuleTarget{{ID: "known", ProviderID: "nil", ModelName: "m"}}}
	log := &model.RequestLog{ID: "replay", Timestamp: 1000, RouteID: "rule", Chain: []model.RequestLogChainEntry{{TargetID: "known", ProviderID: "nil", ModelName: "m", Status: "success"}}}
	app := &App{deps: Deps{Store: &replayStoreStub{log: log, rule: rule}}}
	got, err := app.ReplayLog("replay")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Attempts[0].ProviderMissing || got.Attempts[0].Score.Availability != scoring.Unavailable || got.Attempts[0].Score.Reason != "provider_missing" {
		t.Fatalf("provider missing: %+v", got.Attempts[0])
	}
}

func TestReplayLogCopiesMetricsCostAndEndpointMetadata(t *testing.T) {
	rule := &model.ModelRule{ID: "rule", Name: "r", Targets: []model.ModelRuleTarget{{ID: "target", ProviderID: "provider", ModelName: "model", Tier: 2, Enabled: true}}}
	price := &model.Price{Version: "price-v7", UpstreamModel: "model", BillingMode: model.BillingModeRequest, Currency: "USD", RequestPricePerRequest: 2, Confidence: model.CostConfidenceExact, EffectiveAt: 1}
	lastUsed := time.UnixMilli(900)
	log := &model.RequestLog{ID: "replay", Timestamp: 1000, RouteID: "rule", InputTokens: 10, Chain: []model.RequestLogChainEntry{{TargetID: "target", ProviderID: "provider", ModelName: "model", Status: "success"}}}
	snapshot := metrics.Snapshot{Key: model.TargetMetricKey{TargetID: "target", ProviderID: "provider", ModelName: "model", Endpoint: "/v1/chat/completions"}, Requests: 7, Attempts: 8, Successes: 6, Failures: 2, Status429: 1, LastUsed: lastUsed}
	app := &App{deps: Deps{Store: &replayStoreStub{log: log, rule: rule, price: price}, Metrics: &replayMetricsStub{snapshot: snapshot}}}

	got, err := app.ReplayLog("replay")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("attempts=%d", len(got.Attempts))
	}
	score := got.Attempts[0].Score
	if !score.MetricsFresh || score.Metrics.Requests != 7 || score.Metrics.Attempts != 8 || score.Metrics.Successes != 6 || score.Metrics.Failures != 2 || score.Metrics.Status429 != 1 {
		t.Fatalf("metrics not copied: %+v", score.Metrics)
	}
	if !score.Cost.Available || score.Cost.Cost != 2 || score.EstimatedCost != 2 || score.Cost.PriceVersion != "price-v7" || score.PriceVersion != "price-v7" || got.Attempts[0].PriceVersion != "price-v7" || got.Attempts[0].PriceConfidence != model.CostConfidenceExact {
		t.Fatalf("price not copied consistently: score=%+v attempt=%+v", score, got.Attempts[0])
	}
	if score.Endpoint != "/v1/chat/completions" || !score.EndpointAssumed || got.Endpoint != score.Endpoint || !got.EndpointAssumed {
		t.Fatalf("endpoint metadata: %+v result=%+v", score, got)
	}
}

func TestReplayLogUnavailablePriceIsUnknown(t *testing.T) {
	rule := &model.ModelRule{ID: "rule", Name: "r", Targets: []model.ModelRuleTarget{{ID: "target", ProviderID: "provider", ModelName: "model"}}}
	log := &model.RequestLog{ID: "replay", Timestamp: 1000, RouteID: "rule", Chain: []model.RequestLogChainEntry{{TargetID: "target", ProviderID: "provider", ModelName: "model", Status: "success"}}}
	app := &App{deps: Deps{Store: &replayStoreStub{log: log, rule: rule}}}
	got, err := app.ReplayLog("replay")
	if err != nil {
		t.Fatal(err)
	}
	score := got.Attempts[0].Score
	if score.Cost.Available || score.Cost.Confidence != model.CostConfidenceUnavailable || score.Cost.PriceVersion != "" || score.PriceVersion != "" {
		t.Fatalf("unavailable price: %+v", score)
	}
}

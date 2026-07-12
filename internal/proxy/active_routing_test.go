package proxy

import (
	"errors"
	"testing"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/routing"
)

type planningMetricSpy struct {
	calls int
	byID  map[string]metrics.Snapshot
}

func (s *planningMetricSpy) Submit(model.TargetMetricEvent) bool { return true }
func (s *planningMetricSpy) CurrentSnapshot(k model.TargetMetricKey) metrics.Snapshot {
	s.calls++
	if v, ok := s.byID[k.ProviderID]; ok {
		return v
	}
	return s.byID[k.TargetID]
}

type planningPriceStore struct {
	*mockStore
	calls  int
	prices map[string]*model.Price
	err    error
}

func (s *planningPriceStore) ResolvePrice(providerID, modelName, endpointKind string) (*model.Price, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.prices[providerID+":"+modelName], nil
}

func planningCandidates(strategy routing.Strategy) []candidate {
	return []candidate{
		{targetID: "a", provider: &model.Provider{ID: "pa"}, modelName: "ma", tier: 0, strategy: strategy},
		{targetID: "b", provider: &model.Provider{ID: "pb"}, modelName: "mb", tier: 0, strategy: strategy},
		{targetID: "c", provider: &model.Provider{ID: "pc"}, modelName: "mc", tier: 1, strategy: strategy},
	}
}

func planningRequest() *InboundRequest { return &InboundRequest{Endpoint: "/v1/chat/completions"} }

func TestPlanCandidatesPriorityAndEmptyDoNotReadPlanningDependencies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		strategy   routing.Strategy
		candidates []candidate
		wantIDs    []string
	}{
		{name: "priority", strategy: routing.PriorityFirst, candidates: planningCandidates(routing.PriorityFirst), wantIDs: []string{"a", "b", "c"}},
		{name: "empty strategy", strategy: routing.Strategy(""), candidates: planningCandidates(routing.Strategy("")), wantIDs: []string{"a", "b", "c"}},
		{name: "bogus strategy", strategy: routing.Strategy("bogus"), candidates: planningCandidates(routing.Strategy("bogus")), wantIDs: []string{"a", "b", "c"}},
		{name: "empty candidates", strategy: routing.PriorityFirst, candidates: nil, wantIDs: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metricsSpy := &planningMetricSpy{byID: map[string]metrics.Snapshot{}}
			prices := &planningPriceStore{mockStore: &mockStore{}}
			p := &Proxy{metricSink: metricsSpy, store: prices}
			out := p.planCandidates(planningRequest(), tc.candidates)
			if got := ids(out); len(got) != len(tc.wantIDs) {
				t.Fatalf("candidate count=%d, want %d: %v", len(got), len(tc.wantIDs), got)
			}
			for i := range tc.wantIDs {
				if out[i].targetID != tc.wantIDs[i] {
					t.Fatalf("candidate order=%v, want %v", ids(out), tc.wantIDs)
				}
			}
			if metricsSpy.calls != 0 || prices.calls != 0 {
				t.Fatalf("strategy %q read planning dependencies: metrics=%d prices=%d", tc.strategy, metricsSpy.calls, prices.calls)
			}
			if len(tc.candidates) == 0 && out != nil {
				t.Fatalf("empty candidates changed: %#v", out)
			}
			if len(p.breakers) != 0 {
				t.Fatalf("strategy %q created breaker dependencies: %v", tc.strategy, p.breakers)
			}
		})
	}
}

func TestPlanCandidatesScoreWithinTierAndCostFirst(t *testing.T) {
	spy := &planningMetricSpy{byID: map[string]metrics.Snapshot{
		"a": {Attempts: 10, Status5xx: 8}, "b": {Attempts: 10}, "c": {Attempts: 10},
	}}
	p := &Proxy{metricSink: spy}
	out := p.planCandidates(planningRequest(), planningCandidates(routing.ScoreWithinTier))
	if out[0].targetID != "b" || out[1].targetID != "a" || out[2].targetID != "c" {
		t.Fatalf("score crossed tier or did not rank within tier: %v", ids(out))
	}
	prices := &planningPriceStore{mockStore: &mockStore{}, prices: map[string]*model.Price{
		"pa:ma": {UpstreamModel: "ma", BillingMode: model.BillingModeToken, InputPricePerMillion: 10, Currency: "USD", Confidence: model.CostConfidenceExact},
		"pb:mb": {UpstreamModel: "mb", BillingMode: model.BillingModeToken, InputPricePerMillion: 1, Currency: "USD", Confidence: model.CostConfidenceExact},
	}}
	p.store = prices
	out = p.planCandidates(planningRequest(), planningCandidates(routing.CostFirst))
	if out[0].targetID != "b" || out[1].targetID != "a" || out[2].targetID != "c" {
		t.Fatalf("cost order/unknown handling wrong: %v", ids(out))
	}
	if prices.calls != 3 {
		t.Fatalf("price resolver calls=%d, want 3", prices.calls)
	}
}

func TestPlanCandidatesSnapshotsPricesAndFallback(t *testing.T) {
	spy := &planningMetricSpy{byID: map[string]metrics.Snapshot{}}
	prices := &planningPriceStore{mockStore: &mockStore{}, err: errors.New("price unavailable")}
	p := &Proxy{metricSink: spy, store: prices}
	cs := planningCandidates(routing.CostFirst)
	out := p.planCandidates(planningRequest(), cs)
	if spy.calls != 3 || prices.calls != 3 {
		t.Fatalf("calls metrics=%d prices=%d", spy.calls, prices.calls)
	}
	for i := range cs {
		if out[i].targetID != cs[i].targetID {
			t.Fatalf("price error did not fallback: %v", ids(out))
		}
	}

	p = &Proxy{metricSink: spy}
	cb := p.breakerFor("existing")
	cb.state = StateHalfOpen // test package access; planner must only read it.
	before := len(p.breakers)
	p.planCandidates(planningRequest(), cs)
	if len(p.breakers) != before {
		t.Fatal("planner created a breaker")
	}
	if cb.pendingProbe {
		t.Fatal("planner claimed a half-open probe")
	}
	if _, ok := p.breakerState("missing"); ok {
		t.Fatal("missing breaker reported present")
	}
}

func TestPlanCandidatesDuplicateTargetIDUsesOriginalIndex(t *testing.T) {
	spy := &planningMetricSpy{byID: map[string]metrics.Snapshot{
		"p1": {Attempts: 10, Status5xx: 9}, "p2": {Attempts: 10}, "p3": {Attempts: 10, Status5xx: 8},
	}}
	p := &Proxy{metricSink: spy}
	cs := []candidate{
		{targetID: "same", provider: &model.Provider{ID: "p1"}, modelName: "m", tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "same", provider: &model.Provider{ID: "p2"}, modelName: "m", tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "other", provider: &model.Provider{ID: "p3"}, modelName: "m", tier: 0, strategy: routing.ScoreWithinTier},
	}
	out := p.planCandidates(planningRequest(), cs)
	if out[0].provider.ID != "p2" || out[1].provider.ID != "p3" || out[2].provider.ID != "p1" {
		t.Fatalf("duplicate target mapping wrong: %v", ids(out))
	}
}

func ids(cs []candidate) []string {
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].targetID
	}
	return out
}

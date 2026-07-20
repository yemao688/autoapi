package proxy

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/routing"
)

type planningMetricSpy struct {
	calls       int
	byID        map[string]metrics.Snapshot
	routeByCall []metrics.RouteSnapshot
}

func (s *planningMetricSpy) Submit(model.TargetMetricEvent) bool { return true }
func (s *planningMetricSpy) CurrentSnapshot(k model.TargetMetricKey) metrics.Snapshot {
	s.calls++
	if v, ok := s.byID[k.ProviderID]; ok {
		return v
	}
	return s.byID[k.TargetID]
}
func (s *planningMetricSpy) CurrentRouteSnapshot(k model.RouteModeKey) metrics.RouteSnapshot {
	s.calls++
	if len(s.routeByCall) >= s.calls {
		return s.routeByCall[s.calls-1]
	}
	v := s.byID[k.TargetID]
	return metrics.RouteSnapshot{Key: k, Attempts: v.Attempts, Successes: v.Successes, Failures: v.Failures, Status429: v.Status429, Status5xx: v.Status5xx, Transport: v.Transport, Truncated: v.Truncated, ConversionLocal: v.ConversionLocal, FirstByteMs: v.FirstByteMs, TTFTMs: v.TTFTMs, LastUsed: v.LastUsed, LastSuccess: v.LastSuccess, LastFailure: v.LastFailure}
}

type planningPriceStore struct {
	*mockStore
	calls  int
	prices map[string]*model.Model
	err    error
}

type failingKeyService struct{ err error }

func (s failingKeyService) ResolveProviderKey(string) (string, error) { return "", s.err }

func (s *planningPriceStore) GetModel(providerID, modelName string) (*model.Model, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.prices[providerID+":"+modelName], nil
}

func planningCandidates(strategy routing.Strategy) []candidate {
	return []candidate{
		{targetID: "a", provider: &model.Provider{ID: "pa"}, modelName: "ma", tier: 0, strategy: strategy, requestPrice: 10, requestPriceAvailable: true},
		{targetID: "b", provider: &model.Provider{ID: "pb"}, modelName: "mb", tier: 0, strategy: strategy, requestPrice: 1, requestPriceAvailable: true},
		{targetID: "c", provider: &model.Provider{ID: "pc"}, modelName: "mc", tier: 1, strategy: strategy, requestPrice: 0, requestPriceAvailable: true},
	}
}

func planningRequest() *InboundRequest { return &InboundRequest{Endpoint: "/v1/chat/completions"} }

func TestResolveCandidatesSnapshotsPriorityModelPrices(t *testing.T) {
	store := &planningPriceStore{mockStore: &mockStore{
		providers: map[string]*model.Provider{
			"p-zero":    {ID: "p-zero", Name: "zero", Enabled: true},
			"p-missing": {ID: "p-missing", Name: "missing", Enabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "requested", Enabled: true, Strategy: string(routing.PriorityFirst), Targets: []model.ModelRuleTarget{
			{ID: "zero", ProviderID: "p-zero", ModelName: "free", Enabled: true},
			{ID: "missing", ProviderID: "p-missing", ModelName: "gone", Enabled: true},
		}}},
	},
		prices: map[string]*model.Model{"p-zero:free": {ProviderID: "p-zero", Name: "free", RequestPrice: 0}},
	}
	p := &Proxy{store: store, breakers: map[string]*CircuitBreaker{}}
	candidates, err := p.resolveCandidates(&InboundRequest{Model: "requested", Endpoint: "/v1/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || !candidates[0].requestPriceAvailable || candidates[0].requestPrice != 0 {
		t.Fatalf("zero-price snapshot=%+v", candidates)
	}
	if candidates[1].requestPriceAvailable {
		t.Fatalf("missing model marked available: %+v", candidates[1])
	}
	if store.calls != 2 {
		t.Fatalf("GetModel calls=%d, want 2", store.calls)
	}
}

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
	prices := &planningPriceStore{mockStore: &mockStore{}, prices: map[string]*model.Model{
		"pa:ma": {Name: "ma", RequestPrice: 10},
		"pb:mb": {Name: "mb", RequestPrice: 1},
	}}
	p.store = prices
	out = p.planCandidates(planningRequest(), planningCandidates(routing.CostFirst))
	if out[0].targetID != "b" || out[1].targetID != "a" || out[2].targetID != "c" {
		t.Fatalf("cost order/unknown handling wrong: %v", ids(out))
	}
	if prices.calls != 0 {
		t.Fatalf("planning performed model reads=%d", prices.calls)
	}
}

func TestPlanCandidatesScoreUsesHealthWhenPriceMissing(t *testing.T) {
	spy := &planningMetricSpy{byID: map[string]metrics.Snapshot{
		"pa": {Attempts: 20},
		"pb": {Attempts: 20, Status5xx: 19},
	}}
	p := &Proxy{metricSink: spy}
	cs := []candidate{
		{targetID: "a", provider: &model.Provider{ID: "pa"}, modelName: "ma", tier: 0, strategy: routing.ScoreWithinTier, requestPriceAvailable: false},
		{targetID: "b", provider: &model.Provider{ID: "pb"}, modelName: "mb", tier: 0, strategy: routing.ScoreWithinTier, requestPrice: 1, requestPriceAvailable: true},
	}
	out := p.planCandidates(planningRequest(), cs)
	if out[0].targetID != "a" {
		t.Fatalf("healthy missing-price candidate was not scored: %v", ids(out))
	}
}

func TestPlanCandidatesCostFirstUsesNextAttemptPrice(t *testing.T) {
	p := &Proxy{}
	cs := []candidate{
		{targetID: "one", provider: &model.Provider{ID: "p1"}, modelName: "m1", tier: 0, strategy: routing.CostFirst, requestPrice: 1, requestPriceAvailable: true, maxRetries: 3},
		{targetID: "two", provider: &model.Provider{ID: "p2"}, modelName: "m2", tier: 0, strategy: routing.CostFirst, requestPrice: 2, requestPriceAvailable: true},
	}
	out := p.planCandidates(planningRequest(), cs)
	if out[0].targetID != "one" {
		t.Fatalf("max retries changed static cost order: %v", ids(out))
	}
}

func TestPlanCandidatesInvalidPriceIsUnavailableToCostFirst(t *testing.T) {
	p := &Proxy{}
	cs := []candidate{
		{targetID: "invalid", provider: &model.Provider{ID: "pi"}, modelName: "mi", tier: 0, strategy: routing.CostFirst, requestPrice: math.NaN(), requestPriceAvailable: true},
		{targetID: "valid", provider: &model.Provider{ID: "pv"}, modelName: "mv", tier: 0, strategy: routing.CostFirst, requestPrice: 1, requestPriceAvailable: true},
	}
	out := p.planCandidates(planningRequest(), cs)
	if out[0].targetID != "valid" {
		t.Fatalf("invalid price was treated as comparable: %v", ids(out))
	}
}

func TestPlanCandidatesUsesExactRecentRouteModeSnapshot(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := metrics.New(8, time.Hour, metrics.WithClock(func() time.Time { return now }))
	bad := candidate{targetID: "bad", provider: &model.Provider{ID: "p1"}, modelName: "m1", protocol: ProtocolOpenAIChat, convertTo: ProtocolOpenAIResponses, tier: 0, strategy: routing.ScoreWithinTier, requestPriceAvailable: true, requestPrice: 1}
	good := candidate{targetID: "good", provider: &model.Provider{ID: "p2"}, modelName: "m2", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier, requestPriceAvailable: true, requestPrice: 1}
	reg.Submit(model.TargetMetricEvent{Key: model.TargetMetricKey{TargetID: "bad", ProviderID: "p1", ModelName: "m1", Endpoint: "u"}, RouteMode: model.RouteModeKey{TargetID: "bad", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIResponses)}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeRetryable, FailureClass: model.MetricFailureConversionLocal, At: now})
	reg.Submit(model.TargetMetricEvent{Key: model.TargetMetricKey{TargetID: "good", ProviderID: "p2", ModelName: "m2", Endpoint: "u"}, RouteMode: model.RouteModeKey{TargetID: "good", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIChat)}, Kind: model.MetricEventAttempt, AttemptOutcome: model.AttemptOutcomeSuccess, At: now})
	p := &Proxy{metricSink: reg}
	out := p.planCandidates(&InboundRequest{Endpoint: "/v1/chat/completions"}, []candidate{bad, good})
	if out[0].targetID != "good" {
		t.Fatalf("exact route snapshot did not rank healthy native route first: %v", ids(out))
	}
}

func TestScoreExplorationEveryTwentiethQualifiedRequestAndLRU(t *testing.T) {
	now := time.Unix(1000, 0)
	spy := &planningMetricSpy{byID: map[string]metrics.Snapshot{"a": {}, "b": {}}}
	p := &Proxy{metricSink: spy, explorationClock: func() time.Time { return now }, exploration: make(map[explorationKey]*explorationState)}
	var diagnostics []planDiagnostics
	p.planObserver = func(d planDiagnostics) { diagnostics = append(diagnostics, d) }
	cs := []candidate{
		{targetID: "a", ruleID: "r", provider: &model.Provider{ID: "pa"}, modelName: "ma", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "b", ruleID: "r", provider: &model.Provider{ID: "pb"}, modelName: "mb", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
	}
	for i := 0; i < 19; i++ {
		p.planCandidates(planningRequest(), cs)
		if diagnostics[len(diagnostics)-1].ExplorationSelected != "" {
			t.Fatalf("exploration selected before 20th request: %+v", diagnostics[len(diagnostics)-1])
		}
	}
	out := p.planCandidates(planningRequest(), cs)
	if out[0].targetID != "a" || diagnostics[len(diagnostics)-1].ExplorationSelected != "a" {
		t.Fatalf("20th request did not select cold tie by original order: out=%v diag=%+v", ids(out), diagnostics[len(diagnostics)-1])
	}
	if diagnostics[len(diagnostics)-1].OriginalOrder[0] != "a" || diagnostics[len(diagnostics)-1].ScoreSortedOrder[0] != "a" || diagnostics[len(diagnostics)-1].FinalOrder[0] != "a" {
		t.Fatal("diagnostic order sequences incomplete")
	}
	now = now.Add(30 * time.Second)
	spy.byID["a"] = metrics.Snapshot{Attempts: 1, LastUsed: now}
	for i := 0; i < 19; i++ {
		p.planCandidates(planningRequest(), cs)
	}
	out = p.planCandidates(planningRequest(), cs)
	if out[0].targetID != "b" || diagnostics[len(diagnostics)-1].ExplorationSelected != "b" {
		t.Fatalf("next eligible exploration did not choose least recently used cold route: out=%v diag=%+v", ids(out), diagnostics[len(diagnostics)-1])
	}
}

func TestExplorationDoesNotPromoteLowerTierOrRunOtherStrategies(t *testing.T) {
	clock := time.Unix(1000, 0)
	p := &Proxy{explorationClock: func() time.Time { return clock }, exploration: make(map[explorationKey]*explorationState)}
	low := []candidate{
		{targetID: "top", ruleID: "r", provider: &model.Provider{ID: "p1"}, modelName: "m1", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "lower-a", ruleID: "r", provider: &model.Provider{ID: "p2"}, modelName: "m2", protocol: ProtocolOpenAIChat, tier: 1, strategy: routing.ScoreWithinTier},
		{targetID: "lower-b", ruleID: "r", provider: &model.Provider{ID: "p3"}, modelName: "m3", protocol: ProtocolOpenAIChat, tier: 1, strategy: routing.ScoreWithinTier},
	}
	for i := 0; i < 20; i++ {
		out := p.planCandidates(planningRequest(), low)
		if out[0].targetID != "top" {
			t.Fatalf("lower tier was promoted: %v", ids(out))
		}
	}
	if len(p.exploration) != 0 {
		t.Fatalf("insufficient highest-tier candidates created scheduler state: %+v", p.exploration)
	}
	for _, strategy := range []routing.Strategy{routing.PriorityFirst, routing.CostFirst} {
		p = &Proxy{explorationClock: func() time.Time { return clock }, exploration: make(map[explorationKey]*explorationState)}
		cs := low
		for i := range cs {
			cs[i].strategy = strategy
		}
		for i := 0; i < 40; i++ {
			p.planCandidates(planningRequest(), cs)
		}
		if len(p.exploration) != 0 {
			t.Fatalf("strategy %s touched exploration scheduler", strategy)
		}
	}
}

func TestPlanDiagnosticsObserverPanicCannotChangeOrder(t *testing.T) {
	p := &Proxy{planObserver: func(planDiagnostics) { panic("observer") }}
	cs := []candidate{
		{targetID: "a", ruleID: "r", provider: &model.Provider{ID: "pa"}, modelName: "ma", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "b", ruleID: "r", provider: &model.Provider{ID: "pb"}, modelName: "mb", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
	}
	out := p.planCandidates(planningRequest(), cs)
	if len(out) != 2 || out[0].targetID != "a" {
		t.Fatalf("diagnostics observer changed plan: %v", ids(out))
	}
}

func TestPlanDiagnosticsPlannerReasonsAndScoreReasons(t *testing.T) {
	spy := &planningMetricSpy{byID: map[string]metrics.Snapshot{
		"a": {Attempts: 10, Status5xx: 9}, "b": {Attempts: 10},
	}}
	p := &Proxy{metricSink: spy}
	var got planDiagnostics
	p.planObserver = func(d planDiagnostics) { got = d }
	cs := []candidate{
		{targetID: "a", ruleID: "r", provider: &model.Provider{ID: "pa"}, modelName: "ma", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "b", ruleID: "r", provider: &model.Provider{ID: "pb"}, modelName: "mb", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
	}
	p.planCandidates(planningRequest(), cs)
	if got.PlannerReason != "score_reordered" || got.ReorderReason != "score_reordered" || got.FinalReorderReason != "score_reordered" {
		t.Fatalf("score planner reasons=%+v", got)
	}
	if got.Candidates[0].ScoreReason == got.Candidates[0].PlannerReason || !got.Candidates[0].Changed {
		t.Fatalf("candidate score/planner reasons not separated: %+v", got.Candidates[0])
	}

	spy.byID["a"] = metrics.Snapshot{}
	spy.byID["b"] = metrics.Snapshot{}
	p = &Proxy{metricSink: spy}
	p.planObserver = func(d planDiagnostics) { got = d }
	cs[0].strategy, cs[1].strategy = routing.CostFirst, routing.CostFirst
	cs[0].requestPrice, cs[1].requestPrice = 2, 1
	cs[0].requestPriceAvailable, cs[1].requestPriceAvailable = true, true
	p.planCandidates(planningRequest(), cs)
	if got.PlannerReason != "cost_reordered" || got.ReorderReason != "cost_reordered" {
		t.Fatalf("cost planner reasons=%+v", got)
	}
}

func TestExplorationClockClampHasExploredAndZeroScopeCapacity(t *testing.T) {
	clock := time.Unix(100, 0)
	p := &Proxy{explorationClock: func() time.Time { return clock }, exploration: make(map[explorationKey]*explorationState)}
	cs := []candidate{
		{targetID: "a", ruleID: "r", provider: &model.Provider{ID: "pa"}, modelName: "ma", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "b", ruleID: "r", provider: &model.Provider{ID: "pb"}, modelName: "mb", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
	}
	for i := 0; i < 20; i++ {
		p.planCandidates(planningRequest(), cs)
	}
	state := p.exploration[explorationKey{ruleID: "r", tier: 0}]
	if state == nil || !state.hasExplored || !state.lastExploration.Equal(clock) {
		t.Fatalf("zero-time exploration was not recorded explicitly: %+v", state)
	}
	clock = clock.Add(-time.Second)
	for i := 0; i < 20; i++ {
		p.planCandidates(planningRequest(), cs)
	}
	if !state.lastExploration.Equal(time.Unix(100, 0)) {
		t.Fatal("clock rollback bypassed exploration cooldown")
	}

	zero := time.Time{}
	p = &Proxy{explorationClock: func() time.Time { return zero }, exploration: make(map[explorationKey]*explorationState)}
	for i := 0; i < maxExplorationScopes+1; i++ {
		local := cs
		local[0].ruleID = fmt.Sprintf("rule-%04d", i)
		local[1].ruleID = local[0].ruleID
		for j := 0; j < 20; j++ {
			p.planCandidates(planningRequest(), local)
		}
	}
	if len(p.exploration) != maxExplorationScopes {
		t.Fatalf("exploration scope map exceeded bound: %d", len(p.exploration))
	}
	if _, ok := p.exploration[explorationKey{ruleID: "rule-0000", tier: 0}]; ok {
		t.Fatal("zero-touched oldest exploration scope was not evicted")
	}
}

func TestExplorationConcurrentScopeHasSingleOpportunityWinner(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := metrics.New(8, time.Hour, metrics.WithClock(func() time.Time { return now }))
	p := &Proxy{metricSink: reg, explorationClock: func() time.Time { return now }, exploration: make(map[explorationKey]*explorationState)}
	var mu sync.Mutex
	selected := 0
	p.planObserver = func(d planDiagnostics) {
		if d.ExplorationSelected != "" {
			mu.Lock()
			selected++
			mu.Unlock()
		}
	}
	cs := []candidate{
		{targetID: "a", ruleID: "r", provider: &model.Provider{ID: "pa"}, modelName: "ma", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
		{targetID: "b", ruleID: "r", provider: &model.Provider{ID: "pb"}, modelName: "mb", protocol: ProtocolOpenAIChat, tier: 0, strategy: routing.ScoreWithinTier},
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.planCandidates(planningRequest(), cs)
		}()
	}
	wg.Wait()
	mu.Lock()
	got := selected
	mu.Unlock()
	if got != 1 {
		t.Fatalf("concurrent qualified requests selected exploration %d times, want 1 with fixed clock", got)
	}
}

func TestResolveCandidatesAllConversionRoutesFilteredReturnsUnavailable(t *testing.T) {
	for _, strategy := range []routing.Strategy{routing.PriorityFirst, routing.ScoreWithinTier, routing.CostFirst} {
		t.Run(string(strategy), func(t *testing.T) {
			st := &mockStore{
				providers:    map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true}},
				rules:        []model.ModelRule{{ID: "r", Name: "client", Enabled: true, Strategy: string(strategy), Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", Enabled: true}}}},
				capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}},
				apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
			}
			p := New(st, &mockService{}, 0, nil)
			defer p.Shutdown()
			key := model.RouteModeKey{TargetID: "t", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIResponses)}
			cb := p.routeBreakerFor(key)
			for i := 0; i < 3; i++ {
				cb.Record(false)
			}
			_, err := p.resolveCandidates(&InboundRequest{Model: "client", Protocol: ProtocolOpenAIChat})
			if err == nil || !errors.Is(err, errRouteUnavailable) || !strings.Contains(err.Error(), "client") {
				t.Fatalf("strategy=%s err=%v, want route unavailable", strategy, err)
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"client","messages":[]}`))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			p.router.ServeHTTP(rec, req)
			if rec.Code != 503 {
				t.Fatalf("strategy=%s status=%d body=%s, want 503", strategy, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRouteModeBreakerIsolationAndSuccessReset(t *testing.T) {
	p := &Proxy{}
	conversion := candidate{targetID: "tc", provider: &model.Provider{ID: "p"}, protocol: ProtocolOpenAIChat, convertTo: ProtocolOpenAIResponses}
	native := candidate{targetID: "tn", provider: &model.Provider{ID: "p"}, protocol: ProtocolOpenAIChat}
	otherConversion := conversion
	otherConversion.convertTo = ProtocolAnthropicMessages

	cb := p.routeBreakerFor(routeModeKeyForCandidate(conversion))
	cb.Record(false)
	cb.Record(false)
	cb.Record(false)
	if p.routeModeWouldAllow(conversion) {
		t.Fatal("open conversion route remained eligible")
	}
	if !p.routeModeWouldAllow(native) || !p.routeModeWouldAllow(otherConversion) {
		t.Fatal("conversion-local breaker affected unrelated route mode")
	}

	p.resetRouteBreakers()
	cb = p.routeBreakerFor(routeModeKeyForCandidate(conversion))
	cb.Record(false)
	cb.Record(false)
	probe, ok := p.claimRouteProbe(conversion)
	if !ok || probe == nil {
		t.Fatal("closed conversion route did not claim execution")
	}
	probe.success()
	if cb.CurrentState() != StateClosed {
		t.Fatalf("conversion success did not reset route breaker: %v", cb.CurrentState())
	}
}

func TestRouteModeHalfOpenNeutralExecutionTermination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		status int
	}{
		{name: "nonstream_4xx", status: http.StatusBadRequest},
		{name: "nonstream_5xx", status: http.StatusInternalServerError},
		{name: "stream_4xx", stream: true, status: http.StatusBadRequest},
		{name: "stream_5xx", stream: true, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			st := &mockStore{
				providers:    map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true, BaseURL: srv.URL}},
				rules:        []model.ModelRule{{ID: "r", Name: "half-open", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", Enabled: true}}}},
				capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}},
				apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
			}
			p := New(st, &mockService{}, 0, nil)
			defer p.Shutdown()
			key := model.RouteModeKey{TargetID: "t", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIResponses)}
			cb := p.routeBreakerFor(key)
			now := time.Unix(100, 0)
			cb.nowFn = func() time.Time { return now }
			for i := 0; i < 3; i++ {
				cb.Record(false)
			}
			openedAt := cb.openedAt
			now = now.Add(30 * time.Second)
			providerCB := p.breakerFor("p")
			providerCB.nowFn = func() time.Time { return now }
			providerCB.state = StateHalfOpen
			providerCB.pendingProbe = false
			providerOpenedAt := now.Add(-time.Minute)
			providerCB.openedAt = providerOpenedAt
			reqBody := `{"model":"half-open","messages":[]}`
			if tc.stream {
				reqBody = `{"model":"half-open","stream":true,"messages":[]}`
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			p.router.ServeHTTP(rec, req)
			wantStatus := tc.status
			if tc.status >= 500 {
				wantStatus = http.StatusServiceUnavailable
			}
			if rec.Code != wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, wantStatus, rec.Body.String())
			}
			if cb.CurrentState() != StateOpen || cb.pendingProbe || !cb.openedAt.Equal(openedAt) {
				t.Fatalf("neutral termination state=%v pending=%v openedAt=%v want open/%v", cb.CurrentState(), cb.pendingProbe, cb.openedAt, openedAt)
			}
			if providerCB.CurrentState() != StateOpen || providerCB.pendingProbe {
				t.Fatalf("provider probe leaked: state=%v pending=%v", providerCB.CurrentState(), providerCB.pendingProbe)
			}
			if tc.status < 500 && !providerCB.openedAt.Equal(providerOpenedAt) {
				t.Fatalf("neutral provider termination changed openedAt=%v want=%v", providerCB.openedAt, providerOpenedAt)
			}
			if tc.status >= 500 && !providerCB.openedAt.Equal(now) {
				t.Fatalf("provider failure did not refresh openedAt=%v want=%v", providerCB.openedAt, now)
			}
			if !cb.Allow() {
				t.Fatal("neutral termination extended cooldown or stranded probe")
			}
			cb.CancelProbe()
		})
	}
}

func TestProviderHalfOpenPreflightProbeIsCancelled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		service upstreamKeyProvider
		baseURL string
		body    []byte
		convert bool
	}{
		{name: "key", service: failingKeyService{err: errors.New("key failed")}, baseURL: "http://127.0.0.1:1", body: []byte(`{"model":"m","messages":[]}`)},
		{name: "body", service: &mockService{}, baseURL: "http://127.0.0.1:1", body: []byte(`{"model":"m","messages":`), convert: true},
		{name: "url", service: &mockService{}, baseURL: "http://[::1", body: []byte(`{"model":"m","messages":[]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", Enabled: true}}}
			p := New(st, tc.service, 0, nil)
			defer p.Shutdown()
			cb := p.breakerFor("p")
			now := time.Unix(100, 0)
			cb.nowFn = func() time.Time { return now }
			cb.state = StateHalfOpen
			cb.pendingProbe = false
			openedAt := now.Add(-time.Minute)
			cb.openedAt = openedAt
			c := candidate{targetID: "t", provider: st.providers["p"], modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions"}
			if tc.convert {
				c.convertTo = ProtocolOpenAIResponses
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(tc.body)))
			p.forwardWithFailover(httptest.NewRecorder(), req, tc.body, []candidate{c}, false, 0, &model.RequestLog{ID: "request"})
			if cb.CurrentState() != StateOpen || cb.pendingProbe || !cb.openedAt.Equal(openedAt) {
				t.Fatalf("preflight leaked provider probe: state=%v pending=%v openedAt=%v", cb.CurrentState(), cb.pendingProbe, cb.openedAt)
			}
			now = now.Add(time.Minute)
			if !cb.Allow() {
				t.Fatal("preflight cancellation stranded next provider probe")
			}
			cb.CancelProbe()
		})
	}
}

func TestPlanCandidatesSnapshotsPricesAndFallback(t *testing.T) {
	spy := &planningMetricSpy{byID: map[string]metrics.Snapshot{}}
	prices := &planningPriceStore{mockStore: &mockStore{}, err: errors.New("price unavailable")}
	p := &Proxy{metricSink: spy, store: prices}
	cs := planningCandidates(routing.CostFirst)
	for i := range cs {
		cs[i].requestPriceAvailable = false
	}
	out := p.planCandidates(planningRequest(), cs)
	if spy.calls != 3 || prices.calls != 0 {
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
	spy.routeByCall = []metrics.RouteSnapshot{{Attempts: 10, Status5xx: 9}, {Attempts: 10}, {Attempts: 10, Status5xx: 8}}
	p := &Proxy{metricSink: spy}
	cs := []candidate{
		{targetID: "same", provider: &model.Provider{ID: "p1"}, modelName: "m", tier: 0, strategy: routing.ScoreWithinTier, requestPriceAvailable: true, requestPrice: 10},
		{targetID: "same", provider: &model.Provider{ID: "p2"}, modelName: "m", tier: 0, strategy: routing.ScoreWithinTier, requestPriceAvailable: true, requestPrice: 1},
		{targetID: "other", provider: &model.Provider{ID: "p3"}, modelName: "m", tier: 0, strategy: routing.ScoreWithinTier, requestPriceAvailable: true, requestPrice: 2},
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

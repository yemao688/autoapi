package proxy

import (
	"fmt"
	"testing"
	"time"

	"autoapi/internal/model"
)

func makeProvider(id string) *model.Provider {
	return &model.Provider{ID: id, Name: id, BaseURL: "http://" + id}
}

func TestSelectCandidates_SortByTier(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Priority: 1, Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p1", ModelName: "m1", Action: model.RouteActionForward, Tier: 1},
				{ProviderID: "p0", ModelName: "m0", Action: model.RouteActionForward, Tier: 0},
				{ProviderID: "p2", ModelName: "m2", Action: model.RouteActionForward, Tier: 2},
			},
		},
	}
	req := &InboundRequest{Model: "x"}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(req, rules, "", map[string]*CircuitBreaker{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(cands))
	}
	expected := []string{"p0", "p1", "p2"}
	for i, c := range cands {
		if c.provider.ID != expected[i] {
			t.Fatalf("candidate %d: expected %s, got %s", i, expected[i], c.provider.ID)
		}
	}
}

func TestSelectCandidates_FiltersOpenBreaker(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Priority: 1, Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "m0", Action: model.RouteActionForward, Tier: 0},
				{ProviderID: "p1", ModelName: "m1", Action: model.RouteActionForward, Tier: 1},
			},
		},
	}
	open := NewCircuitBreaker()
	for i := 0; i < failureThreshold; i++ {
		open.Record(false)
	}
	breakers := map[string]*CircuitBreaker{"p0": open}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "p1" {
		t.Fatalf("expected only p1, got %+v", cands)
	}
}

func TestSelectCandidates_DefaultFallback(t *testing.T) {
	rules := []model.Route{}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) {
		if id == "default" {
			return makeProvider(id), nil
		}
		return nil, fmt.Errorf("not found")
	}
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "default", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "default" {
		t.Fatalf("expected default provider, got %+v", cands)
	}
}

func TestSelectCandidates_DefaultFallbackWhenAllOpen(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Priority: 1, Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "m0", Action: model.RouteActionForward, Tier: 0},
			},
		},
	}
	open := NewCircuitBreaker()
	for i := 0; i < failureThreshold; i++ {
		open.Record(false)
	}
	breakers := map[string]*CircuitBreaker{"p0": open}
	lookup := func(id string) (*model.Provider, error) {
		return makeProvider(id), nil
	}
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "default", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "default" {
		t.Fatalf("expected default fallback, got %+v", cands)
	}
}

func TestSelectCandidates_NoCandidates(t *testing.T) {
	rules := []model.Route{}
	_, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", map[string]*CircuitBreaker{}, func(string) (*model.Provider, error) { return nil, fmt.Errorf("not found") })
	if err == nil {
		t.Fatal("expected error when no candidates")
	}
}

func TestSelectCandidates_DefaultFallbackPreservesModel(t *testing.T) {
	rules := []model.Route{}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) {
		if id == "default" {
			return makeProvider(id), nil
		}
		return nil, fmt.Errorf("not found")
	}
	cands, err := selectCandidates(&InboundRequest{Model: "user-model"}, rules, "default", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].modelName != "user-model" {
		t.Fatalf("expected default candidate to preserve request model, got %+v", cands)
	}
}

func TestSelectCandidates_EmptyRouteTargetModelPreservesRequestModel(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Priority: 1, Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "", Action: model.RouteActionForward, Tier: 0},
			},
		},
	}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "user-model"}, rules, "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].modelName != "user-model" {
		t.Fatalf("expected empty route target model to fall back to request model, got %+v", cands)
	}
}

func TestSelectCandidates_NonEmptyRouteTargetModelOverrides(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Priority: 1, Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "route-model", Action: model.RouteActionForward, Tier: 0},
			},
		},
	}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "user-model"}, rules, "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].modelName != "route-model" {
		t.Fatalf("expected non-empty route target model to override request model, got %+v", cands)
	}
}

func TestSelectRoute_AllOperators(t *testing.T) {
	baseRoute := func() model.Route {
		return model.Route{
			ID: "r", Priority: 1, Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p", ModelName: "m", Action: model.RouteActionForward, Tier: 0},
			},
		}
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }

	cases := []struct {
		name    string
		cond    model.RouteCondition
		req     *InboundRequest
		matched bool
	}{
		{"matches exact", model.RouteCondition{Field: "model", Operator: model.OpMatches, Value: "gpt-4"}, &InboundRequest{Model: "gpt-4"}, true},
		{"matches wildcard", model.RouteCondition{Field: "model", Operator: model.OpMatches, Value: "gpt-*"}, &InboundRequest{Model: "gpt-4o"}, true},
		{"matches no match", model.RouteCondition{Field: "model", Operator: model.OpMatches, Value: "claude-*"}, &InboundRequest{Model: "gpt-4"}, false},
		{"equals case insensitive", model.RouteCondition{Field: "model", Operator: model.OpEquals, Value: "GPT-4"}, &InboundRequest{Model: "gpt-4"}, true},
		{"lt true", model.RouteCondition{Field: "estimated_tokens", Operator: model.OpLT, Value: "100"}, &InboundRequest{EstimatedTokens: 50}, true},
		{"lt false", model.RouteCondition{Field: "estimated_tokens", Operator: model.OpLT, Value: "100"}, &InboundRequest{EstimatedTokens: 150}, false},
		{"gt true", model.RouteCondition{Field: "estimated_tokens", Operator: model.OpGT, Value: "100"}, &InboundRequest{EstimatedTokens: 150}, true},
		{"gt false", model.RouteCondition{Field: "estimated_tokens", Operator: model.OpGT, Value: "100"}, &InboundRequest{EstimatedTokens: 50}, false},
		{"between inclusive", model.RouteCondition{Field: "estimated_tokens", Operator: model.OpBetween, Value: "10,100"}, &InboundRequest{EstimatedTokens: 50}, true},
		{"between out", model.RouteCondition{Field: "estimated_tokens", Operator: model.OpBetween, Value: "10,100"}, &InboundRequest{EstimatedTokens: 5}, false},
		{"time between wrap", model.RouteCondition{Field: "time.hour", Operator: model.OpBetween, Value: "23,7"}, &InboundRequest{TimeHour: 2}, true},
		{"in set", model.RouteCondition{Field: "model", Operator: model.OpIn, Value: "gpt-4,gpt-4o"}, &InboundRequest{Model: "gpt-4o"}, true},
		{"in set missing", model.RouteCondition{Field: "model", Operator: model.OpIn, Value: "gpt-4,gpt-4o"}, &InboundRequest{Model: "claude-3"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := baseRoute()
			r.Conditions = []model.RouteCondition{tc.cond}
			cands, err := selectCandidates(tc.req, []model.Route{r}, "", map[string]*CircuitBreaker{}, lookup)
			if tc.matched {
				if err != nil || len(cands) != 1 {
					t.Fatalf("expected match, got err=%v cands=%+v", err, cands)
				}
			} else {
				if err == nil && len(cands) != 0 {
					t.Fatalf("expected no match, got cands=%+v", cands)
				}
			}
		})
	}
}

func TestSelectRoute_SkipFallThrough(t *testing.T) {
	rules := []model.Route{
		{
			ID: "skip", Priority: 1, Enabled: true,
			Conditions: []model.RouteCondition{{Field: "model", Operator: model.OpEquals, Value: "x"}},
			Targets:    []model.RouteTarget{{ProviderID: "p0", Action: model.RouteActionSkip, Tier: 0}},
		},
		{
			ID: "forward", Priority: 2, Enabled: true,
			Targets: []model.RouteTarget{{ProviderID: "p1", ModelName: "m", Action: model.RouteActionForward, Tier: 0}},
		},
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", map[string]*CircuitBreaker{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "p1" {
		t.Fatalf("expected skip rule to fall through to forward rule, got %+v", cands)
	}
}

func TestCircuitBreaker_WouldAllowDoesNotClaimProbe(t *testing.T) {
	now := time.Now()
	cb := NewCircuitBreaker()
	cb.nowFn = func() time.Time { return now }
	cb.recoveryTimeout = time.Minute

	for i := 0; i < failureThreshold; i++ {
		cb.Record(false)
	}
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected breaker open, got %v", cb.CurrentState())
	}

	// Before timeout: WouldAllow should still report false.
	if cb.WouldAllow() {
		t.Fatal("expected WouldAllow false before recovery timeout")
	}
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected WouldAllow not to transition state, got %v", cb.CurrentState())
	}

	// After timeout: WouldAllow reports true without claiming the probe.
	now = now.Add(2 * time.Minute)
	if !cb.WouldAllow() {
		t.Fatal("expected WouldAllow to report open→half-open would be allowed")
	}
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected WouldAllow not to transition state, got %v", cb.CurrentState())
	}

	if !cb.Allow() {
		t.Fatal("expected Allow to transition and permit probe")
	}
	if cb.CurrentState() != StateHalfOpen {
		t.Fatalf("expected half-open after Allow, got %v", cb.CurrentState())
	}
	if cb.WouldAllow() {
		t.Fatal("expected WouldAllow false while probe is in flight")
	}
}

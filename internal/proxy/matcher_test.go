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

func TestSelectCandidates_PreservesTargetOrder(t *testing.T) {
	// rules must be pre-sorted (highest priority first); selectRoute no longer sorts.
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "m0", Enabled: true},
				{ProviderID: "p1", ModelName: "m1", Enabled: true},
				{ProviderID: "p2", ModelName: "m2", Enabled: true},
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
	// rules must be pre-sorted (highest priority first); selectRoute no longer sorts.
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "m0", Enabled: true},
				{ProviderID: "p1", ModelName: "m1", Enabled: true},
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
	// rules must be pre-sorted (highest priority first); selectRoute no longer sorts.
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "m0", Enabled: true},
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
	// rules must be pre-sorted (highest priority first); selectRoute no longer sorts.
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "", Enabled: true},
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
	// rules must be pre-sorted (highest priority first); selectRoute no longer sorts.
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p0", ModelName: "route-model", Enabled: true},
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
	// rules must be pre-sorted (highest priority first); selectRoute no longer sorts.
	baseRoute := func() model.Route {
		return model.Route{
			ID: "r", Enabled: true,
			Targets: []model.RouteTarget{
				{ProviderID: "p", ModelName: "m", Enabled: true},
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

// TestSelectCandidates_PopulatesTargetIDAndMaxRetries verifies the candidate
// struct carries the per-target ID and MaxRetries from the underlying
// RouteTarget, so the proxy can address per-target hit/failure counters and
// bound the in-target retry loop.
func TestSelectCandidates_PopulatesTargetIDAndMaxRetries(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
				{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 2, Enabled: true},
				{ID: "t2", ProviderID: "p2", ModelName: "m2", MaxRetries: 5, Enabled: true},
			},
		},
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", map[string]*CircuitBreaker{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(cands))
	}
	expected := []struct {
		id         string
		maxRetries int
	}{
		{"t0", 0},
		{"t1", 2},
		{"t2", 5},
	}
	for i, c := range cands {
		if c.targetID != expected[i].id {
			t.Errorf("candidate %d: expected targetID %q, got %q", i, expected[i].id, c.targetID)
		}
		if c.maxRetries != expected[i].maxRetries {
			t.Errorf("candidate %d: expected maxRetries %d, got %d", i, expected[i].maxRetries, c.maxRetries)
		}
	}
}

// TestSelectCandidates_DefaultFallbackHasEmptyTargetID verifies the synthetic
// default-fallback candidate (used when no route matches or all targets are
// open) has no targetID — this is what guards the proxy's IncrementTargetStats
// call so it isn't issued for a candidate that has no row in route_targets.
func TestSelectCandidates_DefaultFallbackHasEmptyTargetID(t *testing.T) {
	// no route matched → default fallback path
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, nil, "default", map[string]*CircuitBreaker{},
		func(id string) (*model.Provider, error) { return makeProvider(id), nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].targetID != "" || cands[0].maxRetries != 0 {
		t.Fatalf("expected default candidate with empty targetID/maxRetries=0, got %+v", cands)
	}
}

// TestSelectCandidates_SkipsDisabledTargets verifies the Phase-3 per-target
// enable/disable flag: a target with Enabled=false is dropped from the
// candidate list, and the survivors keep their slice (tier) order. If every
// target is disabled and no default is configured, selectCandidates must
// return an error rather than silently forwarding to a disabled provider.
func TestSelectCandidates_SkipsDisabledTargets(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
				{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: false}, // disabled
				{ID: "t2", ProviderID: "p2", ModelName: "m2", Enabled: true},
				{ID: "t3", ProviderID: "p3", ModelName: "m3", Enabled: false}, // disabled
			},
		},
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", map[string]*CircuitBreaker{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates (disabled skipped), got %d: %+v", len(cands), cands)
	}
	if cands[0].provider.ID != "p0" || cands[1].provider.ID != "p2" {
		t.Fatalf("expected candidates [p0, p2] in tier order, got [%s, %s]",
			cands[0].provider.ID, cands[1].provider.ID)
	}
	if cands[0].targetID != "t0" || cands[1].targetID != "t2" {
		t.Fatalf("expected targetID round-trip for survivors, got [%s, %s]",
			cands[0].targetID, cands[1].targetID)
	}
}

// TestSelectCandidates_AllDisabledFallsBackToDefault covers the corner case
// where every routed target is disabled but a default provider is configured:
// the default should be returned (same fallback path used when all circuits
// are open).
func TestSelectCandidates_AllDisabledFallsBackToDefault(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: false},
				{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: false},
			},
		},
	}
	lookup := func(id string) (*model.Provider, error) {
		if id == "default" {
			return makeProvider(id), nil
		}
		return makeProvider(id), nil
	}
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "default", map[string]*CircuitBreaker{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "default" || cands[0].targetID != "" {
		t.Fatalf("expected single default candidate, got %+v", cands)
	}
}

// TestSelectCandidates_AllDisabledNoDefault verifies the error path: every
// target disabled AND no default provider → no available provider.
func TestSelectCandidates_AllDisabledNoDefault(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Enabled: true,
			Targets: []model.RouteTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: false},
			},
		},
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	_, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", map[string]*CircuitBreaker{}, lookup)
	if err == nil {
		t.Fatal("expected error when all targets disabled and no default configured")
	}
}

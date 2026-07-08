package proxy

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"autoapi/internal/model"
)

func makeProvider(id string) *model.Provider {
	return &model.Provider{ID: id, Name: id, BaseURL: "http://" + id}
}

func TestSelectCandidates_PreservesTargetOrder(t *testing.T) {
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
				{ProviderID: "p0", ModelName: "m0", Enabled: true},
				{ProviderID: "p1", ModelName: "m1", Enabled: true},
				{ProviderID: "p2", ModelName: "m2", Enabled: true},
			},
		},
	}
	req := &InboundRequest{Model: "x"}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(req, rules, "", "", map[string]*CircuitBreaker{}, lookup)
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
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
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
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "p1" {
		t.Fatalf("expected only p1, got %+v", cands)
	}
}

func TestSelectCandidates_DefaultFallback(t *testing.T) {
	rules := []model.ModelRule{}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) {
		if id == "default" {
			return makeProvider(id), nil
		}
		return nil, fmt.Errorf("not found")
	}
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "default", "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "default" {
		t.Fatalf("expected default provider, got %+v", cands)
	}
}

func TestSelectCandidates_DefaultFallbackWhenAllOpen(t *testing.T) {
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
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
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "default", "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "default" {
		t.Fatalf("expected default fallback, got %+v", cands)
	}
}

func TestSelectCandidates_NoCandidates(t *testing.T) {
	rules := []model.ModelRule{}
	_, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", "", map[string]*CircuitBreaker{}, func(string) (*model.Provider, error) { return nil, fmt.Errorf("not found") })
	if err == nil {
		t.Fatal("expected error when no candidates")
	}
}

func TestSelectCandidates_DefaultFallbackPreservesModel(t *testing.T) {
	rules := []model.ModelRule{}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) {
		if id == "default" {
			return makeProvider(id), nil
		}
		return nil, fmt.Errorf("not found")
	}
	cands, err := selectCandidates(&InboundRequest{Model: "user-model"}, rules, "default", "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].modelName != "user-model" {
		t.Fatalf("expected default candidate to preserve request model, got %+v", cands)
	}
}

func TestSelectCandidates_EmptyTargetModelPreservesRequestModel(t *testing.T) {
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "user-model", Enabled: true,
			Targets: []model.ModelRuleTarget{
				{ProviderID: "p0", ModelName: "", Enabled: true},
			},
		},
	}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "user-model"}, rules, "", "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].modelName != "user-model" {
		t.Fatalf("expected empty target model to fall back to request model, got %+v", cands)
	}
}

func TestSelectCandidates_NonEmptyTargetModelOverrides(t *testing.T) {
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "user-model", Enabled: true,
			Targets: []model.ModelRuleTarget{
				{ProviderID: "p0", ModelName: "route-model", Enabled: true},
			},
		},
	}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "user-model"}, rules, "", "", breakers, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].modelName != "route-model" {
		t.Fatalf("expected non-empty target model to override request model, got %+v", cands)
	}
}

// TestSelectCandidates_UnknownModelReturnsError verifies the no-match path
// when no default provider is configured: the matcher must surface an
// "unknown model" error instead of silently forwarding to a wrong target.
func TestSelectCandidates_UnknownModelReturnsError(t *testing.T) {
	rules := []model.ModelRule{
		{ID: "r1", Name: "registered-model", Enabled: true},
	}
	breakers := map[string]*CircuitBreaker{}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	_, err := selectCandidates(&InboundRequest{Model: "not-registered"}, rules, "", "", breakers, lookup)
	if err == nil {
		t.Fatal("expected error for unknown model")
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("expected 'unknown model' in error, got: %v", err)
	}
}

func TestFindModelRule_DisabledSkipped(t *testing.T) {
	rules := []model.ModelRule{
		{ID: "r1", Name: "x", Enabled: false},
		{ID: "r2", Name: "x", Enabled: true},
	}
	rule, ok := findModelRule(&InboundRequest{Model: "x"}, rules)
	if !ok || rule.ID != "r2" {
		t.Fatalf("expected to find enabled r2, got %+v ok=%v", rule, ok)
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
// ModelRuleTarget, so the proxy can address per-target hit/failure
// counters and bound the in-target retry loop.
func TestSelectCandidates_PopulatesTargetIDAndMaxRetries(t *testing.T) {
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
				{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 2, Enabled: true},
				{ID: "t2", ProviderID: "p2", ModelName: "m2", MaxRetries: 5, Enabled: true},
			},
		},
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", "", map[string]*CircuitBreaker{}, lookup)
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
// default-fallback candidate (used when no rule matches or all targets are
// open) has no targetID — this is what guards the proxy's IncrementTargetStats
// call so it isn't issued for a candidate that has no row in rule_targets.
func TestSelectCandidates_DefaultFallbackHasEmptyTargetID(t *testing.T) {
	// no rule matched → default fallback path
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, nil, "default", "", map[string]*CircuitBreaker{},
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
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
				{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: false}, // disabled
				{ID: "t2", ProviderID: "p2", ModelName: "m2", Enabled: true},
				{ID: "t3", ProviderID: "p3", ModelName: "m3", Enabled: false}, // disabled
			},
		},
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", "", map[string]*CircuitBreaker{}, lookup)
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
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
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
	cands, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "default", "", map[string]*CircuitBreaker{}, lookup)
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
	rules := []model.ModelRule{
		{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: false},
			},
		},
	}
	lookup := func(id string) (*model.Provider, error) { return makeProvider(id), nil }
	_, err := selectCandidates(&InboundRequest{Model: "x"}, rules, "", "", map[string]*CircuitBreaker{}, lookup)
	if err == nil {
		t.Fatal("expected error when all targets disabled and no default configured")
	}
}


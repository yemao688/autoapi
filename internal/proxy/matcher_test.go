package proxy

import (
	"fmt"
	"testing"

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

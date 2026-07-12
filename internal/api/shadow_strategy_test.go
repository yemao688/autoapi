package api

import (
	"testing"

	"autoapi/internal/model"
	"autoapi/internal/proxy"
	"autoapi/internal/store"
)

func TestShadowStrategyUsesPersistedCostFirstPlanAndStableDuplicateOrder(t *testing.T) {
	s, err := store.New(t.Context(), store.StoreDeps{DSN: t.TempDir() + "/shadow.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	providers := make(map[string]string)
	for _, name := range []string{"p-expensive", "p-cheap"} {
		p, err := s.CreateProvider(model.ProviderInput{Name: name, BaseURL: "https://" + name + ".example"})
		if err != nil {
			t.Fatal(err)
		}
		providers[name] = p.ID
	}
	for _, entry := range [][3]interface{}{{"p-expensive", "expensive", 10.0}, {"p-cheap", "cheap", 1.0}} {
		provider, modelName, price := entry[0].(string), entry[1].(string), entry[2].(float64)
		if _, err := s.UpsertPrice(model.PriceInput{ProviderID: providers[provider], UpstreamModel: modelName, EndpointKind: "chat", BillingMode: model.BillingModeToken, InputPricePerMillion: price, Currency: "USD", Confidence: model.CostConfidenceExact}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.CreateModelRule(model.ModelRuleInput{
		Name: "shadow-cost", Enabled: true, Strategy: "cost_first",
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: providers["p-expensive"], ModelName: "expensive", Tier: shadowIntPtr(1), Enabled: true},
			{ProviderID: providers["p-cheap"], ModelName: "cheap", Tier: shadowIntPtr(1), Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	before := proxy.StateHalfOpen
	out, err := NewApp(Deps{Store: s, Proxy: shadowProxy{statuses: map[string]proxy.BreakerStatus{providers["p-expensive"]: {State: proxy.StateClosed}, providers["p-cheap"]: {State: proxy.StateHalfOpen}}}}).GetModelRuleShadowComparisons()
	if err != nil || len(out) != 1 {
		t.Fatalf("shadow output=%#v err=%v", out, err)
	}
	if out[0].Strategy != "cost_first" || len(out[0].OriginalOrder) != 2 || len(out[0].PlannedOrder) != 2 {
		t.Fatalf("plan=%#v", out[0])
	}
	if out[0].PlannedOrder[0] != out[0].OriginalOrder[1] || !out[0].Changed {
		t.Fatalf("cost plan order/changed=%#v", out[0])
	}
	if out[0].Assumptions[2] != "cooldown_state_assumed_inactive" {
		t.Fatalf("assumptions=%#v", out[0].Assumptions)
	}
	if before != proxy.StateHalfOpen {
		t.Fatal("test breaker state changed")
	}
}

func shadowIntPtr(v int) *int { return &v }

package api

import (
	"autoapi/internal/model"
	"testing"
)

type shadowModelStore struct {
	StoreService
	rule      model.ModelRule
	providers map[string]*model.Provider
	models    map[string]*model.Model
}

func (s *shadowModelStore) ListModelRules() ([]model.ModelRule, error) {
	return []model.ModelRule{s.rule}, nil
}
func (s *shadowModelStore) GetProvider(id string) (*model.Provider, error) {
	return s.providers[id], nil
}
func (s *shadowModelStore) GetModel(p, n string) (*model.Model, error) { return s.models[p+":"+n], nil }

func TestShadowCostUsesModelRequestPrice(t *testing.T) {
	cost := requestCost(&model.Model{RequestPrice: .25}, 3)
	if !cost.IsAvailable() || cost.Cost != .75 {
		t.Fatalf("cost=%+v", cost)
	}
	free := requestCost(&model.Model{RequestPrice: 0}, 1)
	if !free.IsAvailable() || free.Cost != 0 {
		t.Fatalf("free=%+v", free)
	}
}

func TestShadowStrategyUsesPersistedModelsAndDoesNotMutate(t *testing.T) {
	s := &shadowModelStore{rule: model.ModelRule{ID: "r", Name: "r", Enabled: true, Strategy: "cost_first", Targets: []model.ModelRuleTarget{{ID: "e", ProviderID: "p", ModelName: "expensive", Enabled: true}, {ID: "c", ProviderID: "p", ModelName: "cheap", Enabled: true}}}, providers: map[string]*model.Provider{"p": {ID: "p", Enabled: true}}, models: map[string]*model.Model{"p:expensive": {Name: "expensive", RequestPrice: 2}, "p:cheap": {Name: "cheap", RequestPrice: .1}}}
	got, err := (&App{deps: Deps{Store: s}}).GetModelRuleShadowComparisons()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].OriginalOrder[0] != "e" || got[0].PlannedOrder[0] != "c" {
		t.Fatalf("shadow=%+v", got)
	}
	if s.rule.Targets[0].ModelName != "expensive" {
		t.Fatal("shadow mutated persisted rule")
	}
}

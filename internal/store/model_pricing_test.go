package store

import (
	"math"
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestModelRequestPriceDefaultAndRefreshPreserve(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "p", BaseURL: "https://p"})
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "m"}}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetModel(p.ID, "m")
	if m.RequestPrice != .1 {
		t.Fatalf("default price=%v", m.RequestPrice)
	}
	if err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "m", Name: "m", RequestPrice: 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "m", RequestPrice: .9}}); err != nil {
		t.Fatal(err)
	}
	m, _ = s.GetModel(p.ID, "m")
	if m.RequestPrice != 0 {
		t.Fatalf("refresh changed explicit zero price=%v", m.RequestPrice)
	}
}

func TestUpdateProviderModelAtomicIdentity(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "p", BaseURL: "https://p"})
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "old"}, {Name: "new"}}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "rule", Targets: []model.ModelRuleTargetInput{{ProviderID: p.ID, ModelName: "old"}}})
	if err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "old", Name: "renamed", RequestPrice: .25}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetModel(p.ID, "renamed")
	if m.RequestPrice != .25 || m.ID == "" {
		t.Fatalf("updated model=%+v", m)
	}
	got, _ := s.GetModelRule(r.ID)
	if got.Targets[0].ModelName != "renamed" {
		t.Fatalf("target not renamed: %+v", got.Targets)
	}
	for _, price := range []float64{math.NaN(), math.Inf(1), -1} {
		if err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "renamed", Name: "x", RequestPrice: price}); err == nil {
			t.Fatalf("accepted price %v", price)
		}
	}
	if err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "missing", Name: "x", RequestPrice: 0}); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestUpdateProviderModelRenamesRuntimeSummary(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "p", BaseURL: "https://p"})
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "old"}}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Targets: []model.ModelRuleTargetInput{{ProviderID: p.ID, ModelName: "old"}}})
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO target_runtime_summary(target_id,provider_id,model_name,endpoint,requests,updated_at) VALUES('t',?,?,?,?,?)`, p.ID, "old", "/v1/chat", 4, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "old", Name: "new", RequestPrice: .3}); err != nil {
		t.Fatal(err)
	}
	m, _ := s.GetModel(p.ID, "new")
	if m.RequestPrice != .3 {
		t.Fatalf("model=%+v", m)
	}
	rr, _ := s.GetModelRule(r.ID)
	if rr.Targets[0].ModelName != "new" {
		t.Fatalf("target=%+v", rr.Targets)
	}
	var name string
	if err := s.db.QueryRow(`SELECT model_name FROM target_runtime_summary WHERE target_id='t'`).Scan(&name); err != nil || name != "new" {
		t.Fatalf("summary=%q err=%v", name, err)
	}
}

func TestUpdateProviderModelRuntimeSummaryCollisionRollsBack(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProvider(model.ProviderInput{Name: "p", BaseURL: "https://p"})
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "old"}, {Name: "new"}}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Targets: []model.ModelRuleTargetInput{{ProviderID: p.ID, ModelName: "old"}}})
	now := time.Now().UnixMilli()
	for _, target := range []string{"a", "b"} {
		if _, err := s.db.Exec(`INSERT INTO target_runtime_summary(target_id,provider_id,model_name,endpoint,requests,updated_at) VALUES(?,?,?,?,?,?)`, target, p.ID, "old", "/v1/chat", 1, now); err != nil {
			t.Fatal(err)
		}
	}
	// Existing target b/new collides with the identity update for b/old.
	if _, err := s.db.Exec(`UPDATE target_runtime_summary SET model_name='new' WHERE target_id='b'`); err != nil {
		t.Fatal(err)
	}
	err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "old", Name: "new", RequestPrice: .8})
	if err == nil {
		t.Fatal("expected runtime-summary collision")
	}
	m, _ := s.GetModel(p.ID, "old")
	if m == nil || m.RequestPrice == .8 {
		t.Fatalf("model changed: %+v", m)
	}
	rr, _ := s.GetModelRule(r.ID)
	if rr.Targets[0].ModelName != "old" {
		t.Fatalf("target changed: %+v", rr.Targets)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM target_runtime_summary WHERE model_name='old'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("summary rollback count=%d err=%v", count, err)
	}
}

func TestBatchLogAccountingDoesNotLookupModels(t *testing.T) {
	s := newTestStore(t)
	chain := []model.RequestLogChainEntry{{ProviderID: "p", ModelName: "m", Status: "success", UpstreamStarted: true, RequestCost: .25, RequestCostAvailable: true}, {Status: "circuit_open", RequestCost: 9}}
	logs := []model.RequestLog{{ID: "a", Timestamp: time.Now().UnixMilli(), Chain: chain}, {ID: "b", Timestamp: time.Now().UnixMilli(), Chain: chain}}
	done := make(chan error, 1)
	go func() { done <- s.InsertRequestLogsBatch(logs) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("batch insert deadlocked")
	}
	got, err := s.GetRequestLog("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cost != .25 {
		t.Fatalf("cost=%v", got.Cost)
	}
	logs[0].StatusCode = 200
	logs[0].Cost = 0
	done = make(chan error, 1)
	go func() { done <- s.UpdateRequestLogsBatch(logs[:1]) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("batch update deadlocked")
	}
}

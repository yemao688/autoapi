package store

import (
	"autoapi/internal/model"
	"database/sql"
	"strings"
	"testing"
)

func modelCapabilityFixture(t *testing.T) (*Store, *model.Provider) {
	t.Helper()
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "p", BaseURL: "https://p"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "m"}, {Name: "other"}}); err != nil {
		t.Fatal(err)
	}
	return s, p
}

func TestModelCapabilitiesSchemaAndCRUD(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	var sqlText string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='model_capabilities'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"protocol", "PRIMARY KEY", "provider_id"} {
		if !contains(sqlText, want) {
			t.Fatalf("schema missing %q: %s", want, sqlText)
		}
	}
	if err := s.SetModelCapability(p.ID, "m", "openai_responses", "native", false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListModelCapabilities(p.ID, "m")
	if err != nil || len(rows) != 1 || rows[0].Source != "manual" || rows[0].Enabled {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if err := s.SetModelCapability(p.ID, "m", "openai_responses", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModelCapability(p.ID, "m", "openai_responses", "native"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModelCapability(p.ID, "m", "openai_responses", "native"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListModelCapabilities("", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelCapability(p.ID, "missing", "openai", "native", true); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestModelCapabilitiesLifecycleAndBulk(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	if err := s.SetModelCapability(p.ID, "m", "gemini", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "m"}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListModelCapabilities(p.ID, "m")
	if len(rows) != 1 {
		t.Fatal("upsert removed capability")
	}
	got, err := s.GetModelCapabilitiesForModels([]model.ProviderModelRef{{ProviderID: p.ID, ModelName: "m"}, {ProviderID: p.ID, ModelName: "m"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("bulk=%+v err=%v", got, err)
	}
	empty, err := s.GetModelCapabilitiesForModels(nil)
	if err != nil || empty == nil {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
	if err := s.DeleteModel(p.ID, "m"); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListModelCapabilities(p.ID, "m")
	if len(rows) != 0 {
		t.Fatal("delete did not clean capability")
	}
}

func TestModelCapabilityRenameConflictRollsBack(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	if err := s.SetModelCapability(p.ID, "m", "gemini", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelCapability(p.ID, "other", "gemini", "native", false); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "m", Name: "other", RequestPrice: .2}); err == nil {
		t.Fatal("rename conflict accepted")
	}
	if _, err := s.GetModel(p.ID, "m"); err != nil {
		t.Fatal("old model changed")
	}
	rows, _ := s.ListModelCapabilities(p.ID, "m")
	if len(rows) != 1 {
		t.Fatal("capability rename partially applied")
	}
}

func TestDeleteProviderCascadesModelCapabilities(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	if err := s.SetModelCapability(p.ID, "m", "gemini", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProvider(p.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM model_capabilities WHERE provider_id=?`, p.ID).Scan(&n); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("capability cascade failed")
	}
}

func TestModelCapabilitiesFilterAndIsolation(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	p2, _ := s.CreateProvider(model.ProviderInput{Name: "p2", BaseURL: "https://p2"})
	s.UpsertModels(p2.ID, []model.Model{{Name: "m"}})
	if err := s.SetModelCapability(p.ID, "m", "openai_responses", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelCapability(p.ID, "m", "gemini", "native", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetModelCapability(p2.ID, "m", "openai_responses", "native", false); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListModelCapabilities(p.ID, "")
	if len(rows) != 2 {
		t.Fatalf("provider filter failed: %+v", rows)
	}
	rows, _ = s.ListModelCapabilities(p.ID, "m")
	if len(rows) != 2 {
		t.Fatalf("model filter failed: %+v", rows)
	}
	rows, _ = s.ListModelCapabilities(p.ID, "other")
	if len(rows) != 0 {
		t.Fatalf("wrong model leaked: %+v", rows)
	}
	got, _ := s.GetModelCapabilitiesForModels([]model.ProviderModelRef{{ProviderID: p.ID, ModelName: "m"}, {ProviderID: p2.ID, ModelName: "m"}})
	if len(got) != 3 {
		t.Fatalf("bulk isolation: %+v", got)
	}
	got, _ = s.GetModelCapabilitiesForModels([]model.ProviderModelRef{{ProviderID: p.ID, ModelName: "m"}})
	if len(got) != 2 {
		t.Fatalf("bulk single: %+v", got)
	}
}

func TestModelCapabilitiesRenameSuccessAndDeleteModelClearProvider(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	if err := s.SetModelCapability(p.ID, "m", "gemini", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProviderModel(model.ProviderModelUpdate{ProviderID: p.ID, OldName: "m", Name: "renamed", RequestPrice: .2}); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListModelCapabilities(p.ID, "renamed")
	if len(rows) != 1 || rows[0].ModelName != "renamed" {
		t.Fatalf("rename did not move capability: %+v", rows)
	}
	if err := s.ClearProviderModels(p.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListModelCapabilities("", "")
	if len(rows) != 0 {
		t.Fatalf("clear left capabilities: %+v", rows)
	}
}

func TestModelCapabilityInheritAfterDelete(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	if err := s.SetModelCapability(p.ID, "m", "gemini", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModelCapability(p.ID, "m", "gemini", "native"); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListModelCapabilities(p.ID, "m")
	if len(rows) != 0 {
		t.Fatalf("delete failed: %+v", rows)
	}
	if err := s.SetModelCapability(p.ID, "m", "gemini", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModelCapability(p.ID, "m", "gemini", "native"); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListModelCapabilities(p.ID, "m")
	if len(rows) != 0 {
		t.Fatalf("delete idempotency: %+v", rows)
	}
}

func TestMigration027From026PreservesDataAndEnforcesPKCascade(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("026_provider_gemini_capability")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES('p','p','http://p',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,provider_id,name,created_at,updated_at) VALUES('m','p','m',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='model_capabilities'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"protocol", "PRIMARY KEY", "provider_id", "REFERENCES providers"} {
		if !strings.Contains(sqlText, want) {
			t.Fatalf("schema missing %q", want)
		}
	}
	if _, err := db.Exec(`INSERT INTO model_capabilities(provider_id,model_name,protocol,feature,enabled,source,updated_at) VALUES('p','m','gemini','native',1,'manual',1),('p','m','gemini','native',0,'manual',2)`); err == nil {
		t.Fatal("duplicate PK accepted")
	}
	if _, err := db.Exec(`INSERT INTO model_capabilities(provider_id,model_name,protocol,feature,enabled,source,updated_at) VALUES('missing','m','gemini','native',1,'manual',1)`); err == nil {
		t.Fatal("missing provider accepted")
	}
	if _, err := db.Exec(`DELETE FROM providers WHERE id='p'`); err != nil {
		t.Fatalf("provider delete failed: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_capabilities`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade failed: %d", n)
	}
}

func contains(s, w string) bool {
	for i := 0; i+len(w) <= len(s); i++ {
		if s[i:i+len(w)] == w {
			return true
		}
	}
	return false
}

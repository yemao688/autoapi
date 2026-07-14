package store

import (
	"autoapi/internal/model"
	"database/sql"
	"fmt"
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
	for _, want := range []string{"protocol", "PRIMARY KEY", "provider_id", "REFERENCES models", "ON UPDATE CASCADE", "ON DELETE CASCADE"} {
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

func TestModelCapabilitiesDeleteValidation(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	for _, in := range []struct{ p, m, proto, feat string }{
		{"", "m", "openai", "native"},
		{p.ID, "", "openai", "native"},
		{p.ID, "m", "", "native"},
		{p.ID, "m", "openai", ""},
		{"  ", "m", "openai", "native"},
	} {
		if err := s.DeleteModelCapability(in.p, in.m, in.proto, in.feat); err == nil {
			t.Fatalf("missing fields accepted: %+v", in)
		}
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

func TestMigration028From027RebuildsFKAndCleansOrphans(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("027_model_capabilities")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES('p','p','http://p',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,provider_id,name,created_at,updated_at) VALUES('m1','p','kept',1,1),('m2','p','renamed',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_capabilities(provider_id,model_name,protocol,feature,enabled,source,updated_at) VALUES
		('p','kept','gemini','native',1,'manual',1),
		('p','renamed','gemini','native',1,'manual',2),
		('p','orphan','openai_responses','native',0,'manual',3)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='model_capabilities'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"REFERENCES models", "ON UPDATE CASCADE", "ON DELETE CASCADE"} {
		if !strings.Contains(sqlText, want) {
			t.Fatalf("schema missing %q:\n%s", want, sqlText)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_capabilities`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("expected 2 rows after orphan cleanup, got %d", n)
	}
	if _, err := db.Exec(`INSERT INTO model_capabilities(provider_id,model_name,protocol,feature,enabled,source,updated_at) VALUES('p','kept','gemini','native',0,'manual',4)`); err == nil {
		t.Fatal("duplicate PK accepted")
	}
	if _, err := db.Exec(`INSERT INTO model_capabilities(provider_id,model_name,protocol,feature,enabled,source,updated_at) VALUES('p','missing-model','openai','native',1,'manual',1)`); err == nil {
		t.Fatal("missing model accepted")
	}
	if _, err := db.Exec(`DELETE FROM models WHERE name='kept'`); err != nil {
		t.Fatalf("model delete failed: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_capabilities WHERE model_name='kept'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("model delete cascade failed: %d", n)
	}
	if _, err := db.Exec(`UPDATE models SET name='renamed-again' WHERE name='renamed'`); err != nil {
		t.Fatalf("model rename failed: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_capabilities WHERE model_name='renamed-again'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("model rename cascade failed: %d", n)
	}
	if _, err := db.Exec(`DELETE FROM providers WHERE id='p'`); err != nil {
		t.Fatalf("provider delete failed: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_capabilities`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("provider delete cascade failed: %d", n)
	}
}

func TestMigration028DoesNotSkipOnIncompleteFK(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("027_model_capabilities")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES('p','p','http://p',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,provider_id,name,created_at,updated_at) VALUES('m1','p','kept',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_capabilities(provider_id,model_name,protocol,feature,enabled,source,updated_at) VALUES('p','kept','gemini','native',1,'manual',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE model_capabilities_new (
		provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
		model_name TEXT NOT NULL,
		protocol TEXT NOT NULL,
		feature TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		source TEXT NOT NULL DEFAULT 'manual',
		updated_at INTEGER NOT NULL,
		PRIMARY KEY(provider_id, model_name, protocol, feature)
	);
	INSERT INTO model_capabilities_new SELECT * FROM model_capabilities;
	DROP TABLE model_capabilities;
	ALTER TABLE model_capabilities_new RENAME TO model_capabilities;`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='model_capabilities'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "REFERENCES models") || !strings.Contains(sqlText, "model_name") {
		t.Fatalf("migration did not rebuild complete FK:\n%s", sqlText)
	}
	if _, err := db.Exec(`UPDATE models SET name='renamed' WHERE name='kept'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_capabilities WHERE model_name='renamed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("composite FK did not cascade rename: %d", n)
	}
}

func TestModelCapabilitiesBulkChunksNoDuplicates(t *testing.T) {
	s, p := modelCapabilityFixture(t)
	for i := 0; i < 260; i++ {
		name := fmt.Sprintf("m%d", i)
		if err := s.UpsertModels(p.ID, []model.Model{{Name: name}}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetModelCapability(p.ID, name, "openai_responses", "native", i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	refs := []model.ProviderModelRef{{ProviderID: p.ID, ModelName: "m"}}
	for i := 0; i < 270; i++ {
		refs = append(refs, model.ProviderModelRef{ProviderID: p.ID, ModelName: fmt.Sprintf("m%d", i)})
	}
	refs = append(refs, model.ProviderModelRef{ProviderID: p.ID, ModelName: "m0"})
	got, err := s.GetModelCapabilitiesForModels(refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 260 {
		t.Fatalf("expected 260 capabilities, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, c := range got {
		key := c.ModelName + "/" + c.Feature
		if seen[key] {
			t.Fatalf("duplicate %s", key)
		}
		seen[key] = true
	}
	empty, err := s.GetModelCapabilitiesForModels(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty input failed: %v %d", err, len(empty))
	}
}

func TestProviderBulkChunksNoDuplicates(t *testing.T) {
	s := newTestStore(t)
	var ids []string
	for i := 0; i < 450; i++ {
		p, err := s.CreateProvider(model.ProviderInput{Name: fmt.Sprintf("p%d", i), BaseURL: "https://x"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	got, err := s.GetProvidersForIDs(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 450 {
		t.Fatalf("expected 450 providers, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p.ID] {
			t.Fatalf("duplicate provider %s", p.ID)
		}
		seen[p.ID] = true
	}
	caps, err := s.GetProviderCapabilitiesForProviders(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 0 {
		t.Fatalf("expected 0 caps, got %d", len(caps))
	}
	for _, id := range ids {
		if err := s.SetProviderCapability(id, "openai_responses", "native", true); err != nil {
			t.Fatal(err)
		}
	}
	caps, err = s.GetProviderCapabilitiesForProviders(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 450 {
		t.Fatalf("expected 450 caps, got %d", len(caps))
	}
	seen = map[string]bool{}
	for _, c := range caps {
		if seen[c.ProviderID] {
			t.Fatalf("duplicate cap %s", c.ProviderID)
		}
		seen[c.ProviderID] = true
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

func TestTableHasCompositeFKDetectsIncompleteFK(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE models (provider_id TEXT NOT NULL, name TEXT NOT NULL, PRIMARY KEY(provider_id, name));
		CREATE TABLE incomplete (
			provider_id TEXT NOT NULL REFERENCES models(provider_id) ON DELETE CASCADE ON UPDATE CASCADE,
			model_name TEXT NOT NULL,
			PRIMARY KEY(provider_id, model_name)
		);
		CREATE TABLE complete (
			provider_id TEXT NOT NULL,
			model_name TEXT NOT NULL,
			PRIMARY KEY(provider_id, model_name),
			FOREIGN KEY(provider_id, model_name) REFERENCES models(provider_id, name) ON DELETE CASCADE ON UPDATE CASCADE
		);`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	has, err := tableHasCompositeFK(tx, "incomplete", "models", map[string]string{"provider_id": "provider_id", "model_name": "name"}, "CASCADE", "CASCADE")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("incomplete FK incorrectly detected as complete")
	}

	has, err = tableHasCompositeFK(tx, "complete", "models", map[string]string{"provider_id": "provider_id", "model_name": "name"}, "CASCADE", "CASCADE")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("complete FK not detected")
	}

	has, err = tableHasCompositeFK(tx, "complete", "models", map[string]string{"provider_id": "provider_id", "model_name": "name"}, "SET NULL", "CASCADE")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("wrong action accepted")
	}
}

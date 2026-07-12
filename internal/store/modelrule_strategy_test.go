package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"autoapi/internal/model"
	_ "modernc.org/sqlite"
)

func TestMigration020AddsStrategyAndDefaultsLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrateUpTo(db, "019_target_runtime_summary"); err != nil {
		t.Fatalf("migrate to 019: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_rules (id,name,enabled,first_byte_timeout_ms,display_order,created_at,updated_at) VALUES ('legacy','legacy',1,0,0,1,1)`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := migrateUpTo(db, "020_model_rule_strategy"); err != nil {
		t.Fatalf("migrate to 020: %v", err)
	}
	var strategy string
	if err := db.QueryRow(`SELECT strategy FROM model_rules WHERE id='legacy'`).Scan(&strategy); err != nil {
		t.Fatal(err)
	}
	if strategy != "priority_first" {
		t.Fatalf("legacy strategy = %q", strategy)
	}
	if err := migrateUpTo(db, "020_model_rule_strategy"); err != nil {
		t.Fatalf("re-running redundant migration: %v", err)
	}
}

func TestModelRuleStrategyCRUDRoundTripAndUnknownFallback(t *testing.T) {
	s := newTestStore(t)
	for _, strategy := range []string{"priority_first", "score_within_tier", "cost_first", "", "unknown"} {
		r, err := s.CreateModelRule(model.ModelRuleInput{Name: "strategy-" + strategy, Enabled: true, Strategy: strategy})
		if err != nil {
			t.Fatalf("create %q: %v", strategy, err)
		}
		want := strategy
		if want == "" || want == "unknown" {
			want = "priority_first"
		}
		if r.Strategy != want {
			t.Fatalf("create strategy = %q, want %q", r.Strategy, want)
		}
		got, err := s.GetModelRule(r.ID)
		if err != nil || got.Strategy != want {
			t.Fatalf("get strategy = %q, err=%v, want %q", got.Strategy, err, want)
		}
		listed, err := s.ListModelRules()
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range listed {
			if item.ID == r.ID && item.Strategy != want {
				t.Fatalf("list strategy = %q, want %q", item.Strategy, want)
			}
		}
	}
	rules, err := s.ListModelRules()
	if err != nil || len(rules) == 0 {
		t.Fatalf("list: %v", err)
	}
	r := rules[0]
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{Name: r.Name, Enabled: true, Strategy: "score_within_tier"})
	if err != nil || updated.Strategy != "score_within_tier" {
		t.Fatalf("update strategy = %q, err=%v", updated.Strategy, err)
	}
	if _, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{Name: r.Name, Enabled: true, Strategy: "not-a-strategy"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetModelRule(r.ID)
	if err != nil || got.Strategy != "priority_first" {
		t.Fatalf("unknown update strategy = %q, err=%v", got.Strategy, err)
	}
	if _, err := s.db.Exec(`UPDATE model_rules SET strategy='database_unknown' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetModelRule(r.ID)
	if err != nil || got.Strategy != "priority_first" {
		t.Fatalf("unknown database strategy = %q, err=%v", got.Strategy, err)
	}
}

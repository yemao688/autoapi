package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"autoapi/internal/model"
)

// TestModelRuleTarget_TierExplicitZeroRoundTrip verifies that an explicit tier
// of 0 (not first position) is preserved through create and update.
func TestModelRuleTarget_TierExplicitZeroRoundTrip(t *testing.T) {
	s := newTestStore(t)

	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "explicit-zero",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p1", ModelName: "m1", Tier: intPtr(1)},
			{ProviderID: "p2", ModelName: "m2", Tier: intPtr(0)},
			{ProviderID: "p3", ModelName: "m3", Tier: intPtr(2)},
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	if len(r.Targets) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(r.Targets))
	}
	// Create returns targets in input order (regression check).
	for i, want := range []struct {
		model string
		tier  int
	}{
		{"m1", 1},
		{"m2", 0},
		{"m3", 2},
	} {
		if r.Targets[i].ModelName != want.model || r.Targets[i].Tier != want.tier {
			t.Fatalf("create target[%d] got (%q, %d), want (%q, %d)",
				i, r.Targets[i].ModelName, r.Targets[i].Tier, want.model, want.tier)
		}
	}

	// GetModelRule returns targets sorted by tier ascending.
	got, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule: %v", err)
	}
	for i, want := range []int{0, 1, 2} {
		if got.Targets[i].Tier != want {
			t.Fatalf("get target[%d] tier = %d, want %d", i, got.Targets[i].Tier, want)
		}
	}

	// Update: keep m2 at explicit tier 0, move m1 to tier 0 as well, m3 to tier 5.
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "explicit-zero",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ID: got.Targets[0].ID, ProviderID: "p2", ModelName: "m2", Tier: intPtr(0)},
			{ID: got.Targets[1].ID, ProviderID: "p1", ModelName: "m1", Tier: intPtr(0)},
			{ID: got.Targets[2].ID, ProviderID: "p3", ModelName: "m3", Tier: intPtr(5)},
		},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule: %v", err)
	}
	// Stored order is tier 0 (m1 first, m2 second by rowid), then tier 5.
	want := []struct {
		model string
		tier  int
	}{
		{"m1", 0},
		{"m2", 0},
		{"m3", 5},
	}
	for i, w := range want {
		if updated.Targets[i].ModelName != w.model || updated.Targets[i].Tier != w.tier {
			t.Fatalf("updated target[%d] got (%q, %d), want (%q, %d)",
				i, updated.Targets[i].ModelName, updated.Targets[i].Tier, w.model, w.tier)
		}
	}

	// Re-read from DB to ensure the value persisted.
	got, err = s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule: %v", err)
	}
	for i, w := range want {
		if got.Targets[i].ModelName != w.model || got.Targets[i].Tier != w.tier {
			t.Fatalf("get target[%d] got (%q, %d), want (%q, %d)",
				i, got.Targets[i].ModelName, got.Targets[i].Tier, w.model, w.tier)
		}
	}
}

// TestModelRuleTarget_TierDefaultKeepsPosition verifies that a missing tier
// (nil) falls back to the slice index, preserving legacy positional ordering.
func TestModelRuleTarget_TierDefaultKeepsPosition(t *testing.T) {
	s := newTestStore(t)

	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "default-positional",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p1", ModelName: "m1"},
			{ProviderID: "p2", ModelName: "m2"},
			{ProviderID: "p3", ModelName: "m3"},
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	for i, want := range []int{0, 1, 2} {
		if r.Targets[i].Tier != want {
			t.Fatalf("create target[%d] tier = %d, want %d", i, r.Targets[i].Tier, want)
		}
	}

	// Reorder via update without explicit tiers: new positions become new tiers.
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "default-positional",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ID: r.Targets[2].ID, ProviderID: "p3", ModelName: "m3"},
			{ID: r.Targets[0].ID, ProviderID: "p1", ModelName: "m1"},
			{ID: r.Targets[1].ID, ProviderID: "p2", ModelName: "m2"},
		},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule: %v", err)
	}
	for i, want := range []int{0, 1, 2} {
		if updated.Targets[i].Tier != want {
			t.Fatalf("updated target[%d] tier = %d, want %d", i, updated.Targets[i].Tier, want)
		}
	}
	wantModels := []string{"m3", "m1", "m2"}
	for i, want := range wantModels {
		if updated.Targets[i].ModelName != want {
			t.Fatalf("updated target[%d] model = %q, want %q", i, updated.Targets[i].ModelName, want)
		}
	}
}

// TestModelRuleTarget_SameTierStableOrder verifies that targets sharing the
// same explicit tier keep their insertion order (the legacy tie-breaker).
func TestModelRuleTarget_SameTierStableOrder(t *testing.T) {
	s := newTestStore(t)

	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "same-tier",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p1", ModelName: "a", Tier: intPtr(1)},
			{ProviderID: "p2", ModelName: "b", Tier: intPtr(1)},
			{ProviderID: "p3", ModelName: "c", Tier: intPtr(1)},
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if r.Targets[i].ModelName != w {
			t.Fatalf("create target[%d] model = %q, want %q", i, r.Targets[i].ModelName, w)
		}
	}

	// List must return the same order.
	rules, err := s.ListModelRules()
	if err != nil {
		t.Fatalf("ListModelRules: %v", err)
	}
	var got *model.ModelRule
	for i := range rules {
		if rules[i].Name == "same-tier" {
			got = &rules[i]
			break
		}
	}
	if got == nil {
		t.Fatal("rule not found in list")
	}
	for i, w := range want {
		if got.Targets[i].ModelName != w {
			t.Fatalf("list target[%d] model = %q, want %q", i, got.Targets[i].ModelName, w)
		}
	}
}

// TestModelRuleTarget_ExportIncludesTier verifies that the exported JSON
// contains the tier field so future import can reconstruct it. (There is no full
// Import implementation in this repository yet, so this is an export-only check.)
func TestModelRuleTarget_ExportIncludesTier(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "export-tier",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m", Tier: intPtr(2)}},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	data, _, err := s.Export(model.ExportAllJSON)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(string(data), `"tier"`) {
		t.Fatal("export JSON is missing tier field")
	}
	if !strings.Contains(string(data), `"tier": 2`) {
		t.Fatal("export JSON did not include the expected tier value")
	}
}

// TestModelRuleTarget_MigrationAllTierZeroPreservesInsertionOrder verifies that
// an old route_targets table where every row has tier=0 keeps its insertion order
// after migration to rule_targets.
func TestModelRuleTarget_MigrationAllTierZeroPreservesInsertionOrder(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(p); err != nil {
			t.Fatalf("pragma %q: %v", p, err)
		}
	}
	if err := migrateUpTo(db, "010_route_target_enabled"); err != nil {
		t.Fatalf("migrate up to 010: %v", err)
	}

	now := nowMs()
	ruleID := makeID()
	if _, err := db.Exec(`
		INSERT INTO routes (id, name, description, priority, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ruleID, "legacy-zero", "", 0, 1, now, now); err != nil {
		t.Fatalf("insert route: %v", err)
	}
	for i, name := range []string{"m1", "m2", "m3"} {
		if _, err := db.Exec(`
			INSERT INTO route_targets (id, route_id, provider_id, model_name, tier, enabled)
			VALUES (?, ?, ?, ?, ?, ?)`,
			makeID(), ruleID, fmt.Sprintf("p%d", i+1), name, 0, 1); err != nil {
			t.Fatalf("insert route target %d: %v", i, err)
		}
	}

	if err := migrateUpTo(db, "011_route_to_model_rule"); err != nil {
		t.Fatalf("migrate to 011: %v", err)
	}

	rows, err := db.Query(`
		SELECT model_name, tier FROM rule_targets WHERE rule_id = ? ORDER BY tier ASC, rowid ASC`, ruleID)
	if err != nil {
		t.Fatalf("query rule_targets: %v", err)
	}
	defer rows.Close()
	want := []struct {
		model string
		tier  int
	}{
		{"m1", 0},
		{"m2", 0},
		{"m3", 0},
	}
	for i, w := range want {
		if !rows.Next() {
			t.Fatalf("expected %d rows, got %d", len(want), i)
		}
		var modelName string
		var tier int
		if err := rows.Scan(&modelName, &tier); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if modelName != w.model || tier != w.tier {
			t.Fatalf("row %d got (%q, %d), want (%q, %d)", i, modelName, tier, w.model, w.tier)
		}
	}
	if rows.Next() {
		t.Fatal("unexpected extra rows")
	}
}

// TestModelRuleTarget_MigrationFromRouteTargets verifies that tier ordering is
// preserved when the pre-011 route_targets table is migrated to rule_targets.
func TestModelRuleTarget_MigrationFromRouteTargets(t *testing.T) {
	// Bootstrap a database up to the schema just before the route→model-rule
	// rename, insert route_targets with explicit tiers, then run migration 011.
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(p); err != nil {
			t.Fatalf("pragma %q: %v", p, err)
		}
	}
	if err := migrateUpTo(db, "010_route_target_enabled"); err != nil {
		t.Fatalf("migrate up to 010: %v", err)
	}

	now := nowMs()
	ruleID := makeID()
	if _, err := db.Exec(`
		INSERT INTO routes (id, name, description, priority, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ruleID, "legacy", "", 0, 1, now, now); err != nil {
		t.Fatalf("insert route: %v", err)
	}
	for i, tier := range []int{2, 0, 1} {
		if _, err := db.Exec(`
			INSERT INTO route_targets (id, route_id, provider_id, model_name, tier, enabled)
			VALUES (?, ?, ?, ?, ?, ?)`,
			makeID(), ruleID, fmt.Sprintf("p%d", i+1), fmt.Sprintf("m%d", i+1), tier, 1); err != nil {
			t.Fatalf("insert route target %d: %v", i, err)
		}
	}

	if err := migrateUpTo(db, "011_route_to_model_rule"); err != nil {
		t.Fatalf("migrate to 011: %v", err)
	}

	rows, err := db.Query(`
		SELECT model_name, tier FROM rule_targets WHERE rule_id = ? ORDER BY tier ASC, rowid ASC`, ruleID)
	if err != nil {
		t.Fatalf("query rule_targets: %v", err)
	}
	defer rows.Close()
	want := []struct {
		model string
		tier  int
	}{
		{"m2", 0},
		{"m3", 1},
		{"m1", 2},
	}
	for i, w := range want {
		if !rows.Next() {
			t.Fatalf("expected %d rows, got %d", len(want), i)
		}
		var modelName string
		var tier int
		if err := rows.Scan(&modelName, &tier); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		if modelName != w.model || tier != w.tier {
			t.Fatalf("row %d got (%q, %d), want (%q, %d)", i, modelName, tier, w.model, w.tier)
		}
	}
	if rows.Next() {
		t.Fatal("unexpected extra rows")
	}
}

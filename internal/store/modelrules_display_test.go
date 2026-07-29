package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"autoapi/internal/model"
)

// bootstrapPre017Schema creates a fresh database with migrations up to and
// including 016_provider_enabled, i.e. the schema that existed before the
// model_rules.display_order migration. Callers can then insert rules directly
// and reopen with New to exercise the display_order migration on a genuine
// pre-017 fixture.
func bootstrapPre017Schema(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	defer db.Close()
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
	if err := migrateUpTo(db, "016_provider_enabled"); err != nil {
		t.Fatalf("migrate up to 016: %v", err)
	}
}

func TestModelRuleDisplayOrder_MigrationBackfill(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")

	// Bootstrap a genuine pre-017 schema and insert rules directly into it.
	bootstrapPre017Schema(t, dsn)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open pre-017 db: %v", err)
	}

	base := time.Now().Add(-24 * time.Hour).UnixMilli()

	oldID := makeID()
	equal1ID := makeID()
	equal2ID := makeID()
	midID := makeID()
	newestID := makeID()

	ruleRows := []struct {
		id        string
		name      string
		createdAt int64
	}{
		{oldID, "old", base},
		{equal1ID, "equal1", base + 1000},
		{equal2ID, "equal2", base + 1000},
		{midID, "mid", base + 2000},
		{newestID, "newest", base + 3000},
	}
	for _, r := range ruleRows {
		if _, err := db.Exec(`
			INSERT INTO model_rules (id, name, enabled, first_byte_timeout_ms, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			r.id, r.name, 1, 0, r.createdAt, r.createdAt); err != nil {
			t.Fatalf("insert rule %s: %v", r.name, err)
		}
	}
	db.Close()

	// Reopen: migration 017 adds display_order and backfills it from the
	// existing visible order (created_at DESC, then id DESC), newest at 0.
	s, err := New(context.Background(), StoreDeps{DSN: dsn})
	if err != nil {
		t.Fatalf("New (post-017): %v", err)
	}
	defer s.Close()

	// The two equal-created_at rules must be ordered by id DESC.
	var largerEqualID, smallerEqualID string
	if equal1ID > equal2ID {
		largerEqualID, smallerEqualID = equal1ID, equal2ID
	} else {
		largerEqualID, smallerEqualID = equal2ID, equal1ID
	}
	wantOrder := []string{newestID, midID, largerEqualID, smallerEqualID, oldID}
	wantDisplayOrder := map[string]int64{
		newestID:       0,
		midID:          1,
		largerEqualID:  2,
		smallerEqualID: 3,
		oldID:          4,
	}

	for id, want := range wantDisplayOrder {
		var got int64
		if err := s.db.QueryRow(`SELECT display_order FROM model_rules WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read display_order for %s: %v", id, err)
		}
		if got != want {
			t.Fatalf("display_order for %s: got %d, want %d", id, got, want)
		}
	}

	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 5 {
		t.Fatalf("expected 5 rules, got %d", len(rules))
	}
	for i, id := range wantOrder {
		if rules[i].ID != id {
			t.Fatalf("display order[%d]: got %s, want %s", i, rules[i].ID, id)
		}
	}

	// Routing order remains driven by created_at DESC and is not affected by
	// the new display_order column.
	routing, err := s.ListModelRules()
	if err != nil {
		t.Fatalf("ListModelRules: %v", err)
	}
	if len(routing) != 5 {
		t.Fatalf("expected 5 routing rules, got %d", len(routing))
	}
	if routing[0].ID != newestID || routing[1].ID != midID || routing[4].ID != oldID {
		t.Fatalf("routing order boundaries got [%s, %s, ..., %s], want [%s, %s, ..., %s]",
			routing[0].ID, routing[1].ID, routing[4].ID, newestID, midID, oldID)
	}
	middle := map[string]bool{routing[2].ID: true, routing[3].ID: true}
	if !middle[equal1ID] || !middle[equal2ID] {
		t.Fatalf("routing middle got [%s, %s], want the two equal-created_at rules", routing[2].ID, routing[3].ID)
	}

	// Assert that the 017 migration was recorded as applied.
	var seen017 int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE id = '017_model_rule_display_order'`).Scan(&seen017); err != nil {
		t.Fatalf("count 017 migration row: %v", err)
	}
	if seen017 != 1 {
		t.Fatalf("expected 017 migration row to be recorded, got %d", seen017)
	}
}

func TestModelRuleDisplayOrder_NormalizesPreExistingInvalidColumn(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")

	// Bootstrap a genuine pre-017 schema, add the display_order column
	// manually (as if it were created by another branch/build), and insert
	// rules with all-zero display_order values.
	bootstrapPre017Schema(t, dsn)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open pre-017 db: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE model_rules ADD COLUMN display_order INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("add display_order: %v", err)
	}

	base := time.Now().Add(-24 * time.Hour).UnixMilli()
	oldID := makeID()
	newID := makeID()
	if _, err := db.Exec(`INSERT INTO model_rules (id, name, enabled, first_byte_timeout_ms, created_at, updated_at, display_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, oldID, "old", 1, 0, base, base, 0); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO model_rules (id, name, enabled, first_byte_timeout_ms, created_at, updated_at, display_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, newID, "new", 1, 0, base+1000, base+1000, 0); err != nil {
		t.Fatalf("insert new: %v", err)
	}
	db.Close()

	// Reopen: migration 017 sees the column exists but values are invalid
	// (all-zero), so its Hook normalizes them from the existing order.
	s, err := New(context.Background(), StoreDeps{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	want := map[string]int64{newID: 0, oldID: 1}
	for id, ord := range want {
		var got int64
		if err := s.db.QueryRow(`SELECT display_order FROM model_rules WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatalf("read display_order for %s: %v", id, err)
		}
		if got != ord {
			t.Fatalf("display_order for %s: got %d, want %d", id, got, ord)
		}
	}

	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 2 || rules[0].ID != newID || rules[1].ID != oldID {
		t.Fatalf("display order got [%s, %s], want [%s, %s]", rules[0].ID, rules[1].ID, newID, oldID)
	}
}

func TestModelRuleCreate_InsertsAtTop(t *testing.T) {
	s := newTestStore(t)

	a, err := s.CreateModelRule(model.ModelRuleInput{Name: "a", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := s.CreateModelRule(model.ModelRuleInput{Name: "b", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}

	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].ID != b.ID || rules[1].ID != a.ID {
		t.Fatalf("display order got [%s, %s], want [%s, %s]", rules[0].ID, rules[1].ID, b.ID, a.ID)
	}
}

func TestModelRuleDelete_DoesNotRearrange(t *testing.T) {
	s := newTestStore(t)

	a, err := s.CreateModelRule(model.ModelRuleInput{Name: "a", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	b, err := s.CreateModelRule(model.ModelRuleInput{Name: "b", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := s.DeleteModelRule(a.ID); err != nil {
		t.Fatalf("delete a: %v", err)
	}
	c, err := s.CreateModelRule(model.ModelRuleInput{Name: "c", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	if err != nil {
		t.Fatalf("create c: %v", err)
	}

	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].ID != c.ID || rules[1].ID != b.ID {
		t.Fatalf("display order got [%s, %s], want [%s, %s]", rules[0].ID, rules[1].ID, c.ID, b.ID)
	}
}

func TestModelRule_ReorderSuccess(t *testing.T) {
	s := newTestStore(t)

	a, _ := s.CreateModelRule(model.ModelRuleInput{Name: "a", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	b, _ := s.CreateModelRule(model.ModelRuleInput{Name: "b", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	c, _ := s.CreateModelRule(model.ModelRuleInput{Name: "c", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	if err := s.ReorderModelRules([]string{b.ID, a.ID, c.ID}); err != nil {
		t.Fatalf("ReorderModelRules: %v", err)
	}

	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	want := []string{b.ID, a.ID, c.ID}
	for i, id := range want {
		if rules[i].ID != id {
			t.Fatalf("order[%d]: got %s, want %s", i, rules[i].ID, id)
		}
	}
}

func TestModelRule_ReorderConflicts(t *testing.T) {
	s := newTestStore(t)

	a, _ := s.CreateModelRule(model.ModelRuleInput{Name: "a", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	b, _ := s.CreateModelRule(model.ModelRuleInput{Name: "b", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	// Empty list.
	if err := s.ReorderModelRules([]string{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("empty list: expected ErrConflict, got %v", err)
	}
	// Duplicate.
	if err := s.ReorderModelRules([]string{a.ID, a.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: expected ErrConflict, got %v", err)
	}
	// Unknown.
	if err := s.ReorderModelRules([]string{"unknown", a.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unknown: expected ErrConflict, got %v", err)
	}
	// Missing.
	if err := s.ReorderModelRules([]string{a.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("missing: expected ErrConflict, got %v", err)
	}
	// Count mismatch.
	if err := s.ReorderModelRules([]string{a.ID, b.ID, "extra"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("count mismatch: expected ErrConflict, got %v", err)
	}
}

func TestModelRule_ReorderSameSetTwiceIsLastWriteWins(t *testing.T) {
	s := newTestStore(t)

	a, _ := s.CreateModelRule(model.ModelRuleInput{Name: "a", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	b, _ := s.CreateModelRule(model.ModelRuleInput{Name: "b", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	// First valid reorder.
	if err := s.ReorderModelRules([]string{b.ID, a.ID}); err != nil {
		t.Fatalf("first reorder: %v", err)
	}
	// Second valid reorder with the same set (different order) must also succeed.
	if err := s.ReorderModelRules([]string{a.ID, b.ID}); err != nil {
		t.Fatalf("second reorder: %v", err)
	}

	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].ID != a.ID || rules[1].ID != b.ID {
		t.Fatalf("final order got [%s, %s], want [%s, %s]", rules[0].ID, rules[1].ID, a.ID, b.ID)
	}
}

func TestModelRule_ReorderPersistsAfterCloseAndReopen(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")

	s, err := New(context.Background(), StoreDeps{DSN: dsn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	a, _ := s.CreateModelRule(model.ModelRuleInput{Name: "a", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	b, _ := s.CreateModelRule(model.ModelRuleInput{Name: "b", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	c, _ := s.CreateModelRule(model.ModelRuleInput{Name: "c", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	if err := s.ReorderModelRules([]string{b.ID, a.ID, c.ID}); err != nil {
		t.Fatalf("ReorderModelRules: %v", err)
	}
	s.Close()

	s2, err := New(context.Background(), StoreDeps{DSN: dsn})
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	defer s2.Close()

	rules, err := s2.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	want := []string{b.ID, a.ID, c.ID}
	for i, id := range want {
		if rules[i].ID != id {
			t.Fatalf("order[%d] after reopen: got %s, want %s", i, rules[i].ID, id)
		}
	}
}

func TestModelRuleTarget_ReorderIsolation(t *testing.T) {
	s := newTestStore(t)

	r1, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "r1",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p", ModelName: "m1"},
			{ProviderID: "p", ModelName: "m2"},
		},
	})
	if err != nil {
		t.Fatalf("create r1: %v", err)
	}
	r2, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "r2",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p", ModelName: "n1"},
			{ProviderID: "p", ModelName: "n2"},
		},
	})
	if err != nil {
		t.Fatalf("create r2: %v", err)
	}

	// Reorder r1's targets in reverse.
	wantR1 := []string{r1.Targets[1].ID, r1.Targets[0].ID}
	if err := s.ReorderModelRuleTargets(r1.ID, wantR1); err != nil {
		t.Fatalf("ReorderModelRuleTargets r1: %v", err)
	}

	gotR1, err := s.GetModelRule(r1.ID)
	if err != nil {
		t.Fatalf("GetModelRule r1: %v", err)
	}
	if len(gotR1.Targets) != 2 || gotR1.Targets[0].ID != wantR1[0] || gotR1.Targets[1].ID != wantR1[1] {
		t.Fatalf("r1 targets got [%v], want [%v]", gotR1.Targets, wantR1)
	}

	gotR2, err := s.GetModelRule(r2.ID)
	if err != nil {
		t.Fatalf("GetModelRule r2: %v", err)
	}
	if len(gotR2.Targets) != 2 || gotR2.Targets[0].ID != r2.Targets[0].ID || gotR2.Targets[1].ID != r2.Targets[1].ID {
		t.Fatalf("r2 targets changed: got [%v], want [%v]", gotR2.Targets, []string{r2.Targets[0].ID, r2.Targets[1].ID})
	}

	// Display order of r1/r2 should not have moved.
	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 2 || rules[0].ID != r2.ID || rules[1].ID != r1.ID {
		t.Fatalf("display order changed: got [%s, %s], want [%s, %s]", rules[0].ID, rules[1].ID, r2.ID, r1.ID)
	}
}

func TestModelRule_RoutingOrderUnchangedAfterDisplayReorder(t *testing.T) {
	s := newTestStore(t)

	a, _ := s.CreateModelRule(model.ModelRuleInput{Name: "a", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	b, _ := s.CreateModelRule(model.ModelRuleInput{Name: "b", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	c, _ := s.CreateModelRule(model.ModelRuleInput{Name: "c", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	base := time.Now().Add(-24 * time.Hour).UnixMilli()
	if _, err := s.db.Exec(`UPDATE model_rules SET created_at = ? WHERE id = ?`, base, a.ID); err != nil {
		t.Fatalf("set a created_at: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE model_rules SET created_at = ? WHERE id = ?`, base+1000, b.ID); err != nil {
		t.Fatalf("set b created_at: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE model_rules SET created_at = ? WHERE id = ?`, base+2000, c.ID); err != nil {
		t.Fatalf("set c created_at: %v", err)
	}

	// Display order becomes [b, a, c]; routing order stays newest-first by created_at.
	if err := s.ReorderModelRules([]string{b.ID, a.ID, c.ID}); err != nil {
		t.Fatalf("ReorderModelRules: %v", err)
	}

	routing, err := s.ListModelRules()
	if err != nil {
		t.Fatalf("ListModelRules: %v", err)
	}
	routingWant := []string{c.ID, b.ID, a.ID}
	for i, id := range routingWant {
		if routing[i].ID != id {
			t.Fatalf("routing order[%d]: got %s, want %s", i, routing[i].ID, id)
		}
	}

	display, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	displayWant := []string{b.ID, a.ID, c.ID}
	for i, id := range displayWant {
		if display[i].ID != id {
			t.Fatalf("display order[%d]: got %s, want %s", i, display[i].ID, id)
		}
	}
}

func insertRequestLog(t *testing.T, s *Store, l model.RequestLog) {
	t.Helper()
	if err := s.InsertRequestLog(l); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
	}
}

func TestModelRuleTodayStats_NoData(t *testing.T) {
	s := newTestStore(t)
	_, _ = s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 1 {
		t.Fatal("expected 1 rule")
	}
	if rules[0].TodayRequestCount != 0 {
		t.Fatalf("expected 0 requests, got %d", rules[0].TodayRequestCount)
	}
	if rules[0].TodaySuccessRate != nil {
		t.Fatalf("expected nil rate, got %v", *rules[0].TodaySuccessRate)
	}
}

func TestModelRuleTodayStats_RealZero(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	insertRequestLog(t, s, model.RequestLog{ID: "l1", Timestamp: time.Now().UnixMilli(), StatusCode: 500, RouteID: r.ID})
	insertRequestLog(t, s, model.RequestLog{ID: "l2", Timestamp: time.Now().UnixMilli(), StatusCode: 400, RouteID: r.ID})

	rules, _ := s.ListModelRulesForDisplay()
	if len(rules) != 1 {
		t.Fatal("expected 1 rule")
	}
	if rules[0].TodayRequestCount != 2 {
		t.Fatalf("expected 2 requests, got %d", rules[0].TodayRequestCount)
	}
	if rules[0].TodaySuccessRate == nil || *rules[0].TodaySuccessRate != 0 {
		t.Fatalf("expected 0%% rate, got %v", rules[0].TodaySuccessRate)
	}
}

func TestModelRuleTodayStats_2xxWithErrorNotSuccess(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	insertRequestLog(t, s, model.RequestLog{ID: "l1", Timestamp: time.Now().UnixMilli(), StatusCode: 200, RouteID: r.ID})
	insertRequestLog(t, s, model.RequestLog{ID: "l2", Timestamp: time.Now().UnixMilli(), StatusCode: 200, RouteID: r.ID, Error: "truncated"})

	rules, _ := s.ListModelRulesForDisplay()
	if len(rules) != 1 {
		t.Fatal("expected 1 rule")
	}
	if rules[0].TodayRequestCount != 2 {
		t.Fatalf("expected 2 requests, got %d", rules[0].TodayRequestCount)
	}
	if rules[0].TodaySuccessRate == nil || *rules[0].TodaySuccessRate != 50 {
		t.Fatalf("expected 50%% rate, got %v", rules[0].TodaySuccessRate)
	}
}

func TestModelRuleTodayStats_Non2xxNotSuccess(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	insertRequestLog(t, s, model.RequestLog{ID: "l1", Timestamp: time.Now().UnixMilli(), StatusCode: 200, RouteID: r.ID})
	insertRequestLog(t, s, model.RequestLog{ID: "l2", Timestamp: time.Now().UnixMilli(), StatusCode: 422, RouteID: r.ID})

	rules, _ := s.ListModelRulesForDisplay()
	if rules[0].TodaySuccessRate == nil || *rules[0].TodaySuccessRate != 50 {
		t.Fatalf("expected 50%% rate, got %v", rules[0].TodaySuccessRate)
	}
}

func TestModelRuleTodayStats_StatusCodeZeroNotCounted(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	insertRequestLog(t, s, model.RequestLog{ID: "l1", Timestamp: time.Now().UnixMilli(), StatusCode: 200, RouteID: r.ID})
	insertRequestLog(t, s, model.RequestLog{ID: "l2", Timestamp: time.Now().UnixMilli(), StatusCode: 0, RouteID: r.ID, Error: "client abort"})

	rules, _ := s.ListModelRulesForDisplay()
	if rules[0].TodayRequestCount != 1 {
		t.Fatalf("expected 1 request, got %d", rules[0].TodayRequestCount)
	}
	if rules[0].TodaySuccessRate == nil || *rules[0].TodaySuccessRate != 100 {
		t.Fatalf("expected 100%% rate, got %v", rules[0].TodaySuccessRate)
	}
}

func TestModelRuleTodayStats_CrossRuleAggregation(t *testing.T) {
	s := newTestStore(t)
	r1, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r1", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	r2, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r2", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	insertRequestLog(t, s, model.RequestLog{ID: "l1", Timestamp: time.Now().UnixMilli(), StatusCode: 200, RouteID: r1.ID})
	insertRequestLog(t, s, model.RequestLog{ID: "l2", Timestamp: time.Now().UnixMilli(), StatusCode: 500, RouteID: r1.ID})
	insertRequestLog(t, s, model.RequestLog{ID: "l3", Timestamp: time.Now().UnixMilli(), StatusCode: 200, RouteID: r2.ID})

	rules, _ := s.ListModelRulesForDisplay()
	if len(rules) != 2 {
		t.Fatal("expected 2 rules")
	}
	byID := make(map[string]model.ModelRule)
	for _, r := range rules {
		byID[r.ID] = r
	}
	if byID[r1.ID].TodayRequestCount != 2 || byID[r1.ID].TodaySuccessRate == nil || *byID[r1.ID].TodaySuccessRate != 50 {
		t.Fatalf("r1 stats got %+v", byID[r1.ID])
	}
	if byID[r2.ID].TodayRequestCount != 1 || byID[r2.ID].TodaySuccessRate == nil || *byID[r2.ID].TodaySuccessRate != 100 {
		t.Fatalf("r2 stats got %+v", byID[r2.ID])
	}
}

func TestModelRuleTodayStats_LocalMidnightBoundary(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})

	now := time.Now()
	loc := now.Location()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	justBefore := startOfDay.Add(-1 * time.Millisecond)
	justAfter := startOfDay.Add(1 * time.Millisecond)

	insertRequestLog(t, s, model.RequestLog{ID: "l1", Timestamp: justBefore.UnixMilli(), StatusCode: 200, RouteID: r.ID})
	insertRequestLog(t, s, model.RequestLog{ID: "l2", Timestamp: justAfter.UnixMilli(), StatusCode: 200, RouteID: r.ID})

	rules, _ := s.ListModelRulesForDisplay()
	if len(rules) != 1 {
		t.Fatal("expected 1 rule")
	}
	if rules[0].TodayRequestCount != 1 {
		t.Fatalf("expected 1 request today, got %d", rules[0].TodayRequestCount)
	}
	if rules[0].TodaySuccessRate == nil || *rules[0].TodaySuccessRate != 100 {
		t.Fatalf("expected 100%% rate, got %v", rules[0].TodaySuccessRate)
	}
}

func TestModelRuleTodayStats_RulesWithoutTargetsStillHydrated(t *testing.T) {
	s := newTestStore(t)
	r, _ := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{}})
	insertRequestLog(t, s, model.RequestLog{ID: "l1", Timestamp: time.Now().UnixMilli(), StatusCode: 200, RouteID: r.ID})

	rules, _ := s.ListModelRulesForDisplay()
	if len(rules) != 1 || len(rules[0].Targets) != 0 {
		t.Fatal("expected 1 rule with 0 targets")
	}
	if rules[0].TodayRequestCount != 1 || rules[0].TodaySuccessRate == nil || *rules[0].TodaySuccessRate != 100 {
		t.Fatalf("unexpected stats: %+v", rules[0])
	}
}

func TestModelRuleExport_DoesNotIncludeDeprecatedFields(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}

	data, _, err := s.Export(model.ExportAllJSON)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("exported data is empty")
	}
	str := string(data)
	for _, key := range []string{"monthly_hits", "monthly_savings", "display_order"} {
		if strings.Contains(str, "\""+key+"\"") {
			t.Fatalf("exported JSON contains forbidden key %q", key)
		}
	}
}

// mkTargetAttemptLog builds a completed request log whose chain holds a
// single attempt against targetID. Attempt timestamps come from the log row.
func mkTargetAttemptLog(id string, ts time.Time, targetID, status string, started bool) model.RequestLog {
	return model.RequestLog{
		ID:         id,
		Timestamp:  ts.UnixMilli(),
		StatusCode: 200,
		Chain: []model.RequestLogChainEntry{{
			AttemptOrder:    1,
			TargetID:        targetID,
			Status:          status,
			UpstreamStarted: started,
		}},
	}
}

// createRuleWithTargetID returns the persisted target ID of a single-target rule.
func createRuleWithTargetID(t *testing.T, s *Store) string {
	t.Helper()
	r, err := s.CreateModelRule(model.ModelRuleInput{Name: "r", Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}}})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	full, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule: %v", err)
	}
	if len(full.Targets) != 1 || full.Targets[0].ID == "" {
		t.Fatalf("expected 1 persisted target, got %+v", full.Targets)
	}
	return full.Targets[0].ID
}

func displayTargetRates(t *testing.T, s *Store) (*float64, *float64) {
	t.Helper()
	rules, err := s.ListModelRulesForDisplay()
	if err != nil {
		t.Fatalf("ListModelRulesForDisplay: %v", err)
	}
	if len(rules) != 1 || len(rules[0].Targets) != 1 {
		t.Fatalf("expected 1 rule with 1 target, got %+v", rules)
	}
	return rules[0].Targets[0].SuccessRateRecent100, rules[0].Targets[0].SuccessRateHour
}

func TestModelRuleTargetSuccessRates_NilWithoutAttempts(t *testing.T) {
	s := newTestStore(t)
	createRuleWithTargetID(t, s)

	recent, hour := displayTargetRates(t, s)
	if recent != nil || hour != nil {
		t.Fatalf("expected nil rates, got recent=%v hour=%v", recent, hour)
	}
}

func TestModelRuleTargetSuccessRates_CountsOnlyRealAttempts(t *testing.T) {
	s := newTestStore(t)
	tid := createRuleWithTargetID(t, s)
	now := time.Now()

	// One request whose chain holds a real success, a real failure, and a
	// circuit-open rejection that never reached the upstream. Only the two
	// real attempts count: 1/2 = 50%.
	insertRequestLog(t, s, model.RequestLog{
		ID:         "l1",
		Timestamp:  now.UnixMilli(),
		StatusCode: 200,
		Chain: []model.RequestLogChainEntry{
			{AttemptOrder: 1, TargetID: tid, Status: "success", UpstreamStarted: true},
			{AttemptOrder: 2, TargetID: tid, Status: "retryable", UpstreamStarted: true},
			{AttemptOrder: 3, TargetID: tid, Status: "circuit_open", UpstreamStarted: false},
		},
	})

	recent, hour := displayTargetRates(t, s)
	if recent == nil || *recent != 50 {
		t.Fatalf("expected recent 50, got %v", recent)
	}
	if hour == nil || *hour != 50 {
		t.Fatalf("expected hour 50, got %v", hour)
	}
}

func TestModelRuleTargetSuccessRates_HourWindowExcludesOldAttempts(t *testing.T) {
	s := newTestStore(t)
	tid := createRuleWithTargetID(t, s)
	now := time.Now()

	insertRequestLog(t, s, mkTargetAttemptLog("old", now.Add(-2*time.Hour), tid, "success", true))
	insertRequestLog(t, s, mkTargetAttemptLog("new", now.Add(-30*time.Minute), tid, "retryable", true))

	recent, hour := displayTargetRates(t, s)
	if recent == nil || *recent != 50 {
		t.Fatalf("expected recent 50, got %v", recent)
	}
	// The hour window holds only the failure: a real 0%, not nil.
	if hour == nil || *hour != 0 {
		t.Fatalf("expected hour 0, got %v", hour)
	}
}

func TestModelRuleTargetSuccessRates_Recent100UsesLatest100(t *testing.T) {
	s := newTestStore(t)
	tid := createRuleWithTargetID(t, s)
	now := time.Now()

	// 101 attempts inside the hour: the oldest is a failure, the 100 newest
	// are successes. recent-100 drops the oldest; the hour window keeps all.
	insertRequestLog(t, s, mkTargetAttemptLog("oldest", now.Add(-59*time.Minute), tid, "retryable", true))
	for i := 0; i < 100; i++ {
		insertRequestLog(t, s, mkTargetAttemptLog("s"+strconv.Itoa(i), now.Add(-58*time.Minute).Add(time.Duration(i)*time.Second), tid, "success", true))
	}

	recent, hour := displayTargetRates(t, s)
	if recent == nil || *recent != 100 {
		t.Fatalf("expected recent 100, got %v", recent)
	}
	wantHour := 100.0 / 101.0 * 100
	if hour == nil || *hour < wantHour-1e-9 || *hour > wantHour+1e-9 {
		t.Fatalf("expected hour %.6f, got %v", wantHour, hour)
	}
}

func TestModelRuleTargetSuccessRates_ScanBoundExcludesVeryOldAttempts(t *testing.T) {
	s := newTestStore(t)
	tid := createRuleWithTargetID(t, s)

	insertRequestLog(t, s, mkTargetAttemptLog("ancient", time.Now().Add(-31*24*time.Hour), tid, "success", true))

	recent, hour := displayTargetRates(t, s)
	if recent != nil || hour != nil {
		t.Fatalf("expected nil rates beyond scan bound, got recent=%v hour=%v", recent, hour)
	}
}

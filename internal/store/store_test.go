package store

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"

	"autoapi/internal/model"
)

func TestModelRuleTargetTimeoutPersistenceAndLegacyConversion(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		name    string
		seconds int
	}{{"2000ms", 2}, {"1500ms", 1}, {"999ms", 0}} {
		rule, err := s.CreateModelRule(model.ModelRuleInput{Name: tc.name, Enabled: true, Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m", FirstTokenTimeoutSeconds: tc.seconds, Enabled: true}}})
		if err != nil {
			t.Fatalf("create %s: %v", tc.name, err)
		}
		got, err := s.GetModelRule(rule.ID)
		if err != nil || got.Targets[0].FirstTokenTimeoutSeconds != tc.seconds {
			t.Fatalf("round trip %s: got=%+v err=%v", tc.name, got.Targets, err)
		}
		list, err := s.ListModelRules()
		if err != nil {
			t.Fatalf("list %s: %v", tc.name, err)
		}
		var listed *model.ModelRule
		for i := range list {
			if list[i].Name == tc.name {
				listed = &list[i]
				break
			}
		}
		if listed == nil || len(listed.Targets) != 1 || listed.Targets[0].FirstTokenTimeoutSeconds != tc.seconds {
			t.Fatalf("list timeout: got %+v want %d", listed, tc.seconds)
		}
	}
}

func TestModelRuleTargetTimeoutValidationIsTransactional(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateModelRule(model.ModelRuleInput{Name: "negative", Targets: []model.ModelRuleTargetInput{{ProviderID: "p", FirstTokenTimeoutSeconds: -1}}}); err == nil {
		t.Fatal("expected negative timeout rejection")
	}
	rules, err := s.ListModelRules()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.Name == "negative" {
			t.Fatal("negative timeout partially committed rule")
		}
	}
	max := int(math.MaxInt64 / 1000)
	if _, err := s.CreateModelRule(model.ModelRuleInput{Name: "overflow", Targets: []model.ModelRuleTargetInput{{ProviderID: "p", FirstTokenTimeoutSeconds: max + 1}}}); err == nil {
		t.Fatal("expected overflow timeout rejection")
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "autoapi-store-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s, err := New(context.Background(), StoreDeps{DSN: dir + "/test.db"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ---------------------------------------------------------------------------
//  Provider CRUD
// ---------------------------------------------------------------------------

func TestProviderCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create
	p, err := s.CreateProvider(model.ProviderInput{
		Name:             "Test Provider",
		BaseURL:          "https://test.example.com",
		UpstreamKey:      "sk-test-abc123",
		ResponsesEnabled: true,
		MessagesEnabled:  true,
		GeminiEnabled:    true,
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if p.Name != "Test Provider" {
		t.Fatalf("expected 'Test Provider', got %q", p.Name)
	}
	if p.Status != model.ProviderStatusUnknown {
		t.Fatalf("expected status 'unknown', got %q", p.Status)
	}
	if !p.ResponsesEnabled {
		t.Fatal("expected Responses capability enabled after create")
	}
	if !p.MessagesEnabled {
		t.Fatal("expected Messages capability enabled after create")
	}
	if !p.GeminiEnabled {
		t.Fatal("expected Gemini capability enabled after create")
	}
	// The store no longer stores the upstream key; key columns should be empty.
	if len(p.KeyCiphertext) != 0 || p.KeyMasked != "" {
		t.Fatalf("expected empty key columns on create, got ciphertext=%q masked=%q", p.KeyCiphertext, p.KeyMasked)
	}

	// The App layer encrypts and stores the upstream key separately.
	if err := s.UpdateProviderKeyCiphertext(p.ID, []byte("dummy-ciphertext"), []byte("dummy-nonce"), "sk-test-****c123"); err != nil {
		t.Fatalf("UpdateProviderKeyCiphertext: %v", err)
	}

	// Get
	got, err := s.GetProvider(p.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.ID != p.ID || got.Name != p.Name || got.BaseURL != p.BaseURL {
		t.Fatalf("GetProvider mismatch: got %+v, want %+v", got, p)
	}
	if !got.ResponsesEnabled {
		t.Fatal("expected Responses capability persisted by Get")
	}
	if !got.MessagesEnabled {
		t.Fatal("expected Messages capability persisted by Get")
	}
	if !got.GeminiEnabled {
		t.Fatal("expected Gemini capability persisted by Get")
	}
	if string(got.KeyCiphertext) != "dummy-ciphertext" {
		t.Fatalf("expected ciphertext set by helper, got %q", got.KeyCiphertext)
	}
	if got.KeyMasked != "sk-test-****c123" {
		t.Fatalf("expected masked key 'sk-test-****c123', got %q", got.KeyMasked)
	}

	// Update provider body; key columns should be preserved.
	updated, err := s.UpdateProvider(p.ID, model.ProviderInput{
		Name:             "Updated Provider",
		BaseURL:          "https://updated.example.com",
		UpstreamKey:      "sk-updated-xyz789",
		ResponsesEnabled: false,
		MessagesEnabled:  false,
		GeminiEnabled:    false,
	})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.Name != "Updated Provider" {
		t.Fatalf("expected 'Updated Provider', got %q", updated.Name)
	}
	if updated.ResponsesEnabled {
		t.Fatal("expected Responses capability persisted by Update")
	}
	if updated.MessagesEnabled {
		t.Fatal("expected Messages capability persisted by Update")
	}
	if updated.GeminiEnabled {
		t.Fatal("expected Gemini capability persisted by Update")
	}
	if string(updated.KeyCiphertext) != "dummy-ciphertext" {
		t.Fatalf("expected key columns preserved, got %q", updated.KeyCiphertext)
	}
	if updated.KeyMasked != "sk-test-****c123" {
		t.Fatalf("expected masked key preserved, got %q", updated.KeyMasked)
	}

	// List
	list, err := s.ListProviders()
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(list))
	}

	// Delete
	if err := s.DeleteProvider(p.ID); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	_, err = s.GetProvider(p.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// ---------------------------------------------------------------------------
//  Model CRUD
// ---------------------------------------------------------------------------

func TestModels(t *testing.T) {
	s := newTestStore(t)

	p, _ := s.CreateProvider(model.ProviderInput{Name: "M", BaseURL: "https://m.example.com"})

	if err := s.UpsertModels(p.ID, []model.Model{{Name: "model-a"}, {Name: "model-b"}}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}

	models, err := s.ListModels(p.ID)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	// Upsert again (should preserve count, new models added, existing active preserved)
	if err := s.UpsertModels(p.ID, []model.Model{{Name: "model-c"}}); err != nil {
		t.Fatalf("UpsertModels (2): %v", err)
	}
	models, _ = s.ListModels(p.ID)
	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}
}

func TestClearProviderModels(t *testing.T) {
	s := newTestStore(t)

	p, _ := s.CreateProvider(model.ProviderInput{Name: "Clear Me", BaseURL: "https://clear.example.com"})
	other, _ := s.CreateProvider(model.ProviderInput{Name: "Other", BaseURL: "https://other.example.com"})

	if err := s.UpsertModels(p.ID, []model.Model{{Name: "m1"}, {Name: "m2"}, {Name: "m3"}}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	if err := s.UpsertModels(other.ID, []model.Model{{Name: "o1"}}); err != nil {
		t.Fatalf("UpsertModels other: %v", err)
	}

	if err := s.ClearProviderModels(p.ID); err != nil {
		t.Fatalf("ClearProviderModels: %v", err)
	}

	models, err := s.ListModels(p.ID)
	if err != nil {
		t.Fatalf("ListModels after clear: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected 0 models, got %d", len(models))
	}

	got, err := s.GetProvider(p.ID)
	if err != nil {
		t.Fatalf("GetProvider after clear: %v", err)
	}
	if got.ModelsCount != 0 {
		t.Fatalf("expected models_count 0, got %d", got.ModelsCount)
	}

	// Idempotent second call.
	if err := s.ClearProviderModels(p.ID); err != nil {
		t.Fatalf("ClearProviderModels idempotent: %v", err)
	}

	otherModels, err := s.ListModels(other.ID)
	if err != nil {
		t.Fatalf("ListModels other: %v", err)
	}
	if len(otherModels) != 1 {
		t.Fatalf("expected other provider to keep 1 model, got %d", len(otherModels))
	}
}

// ---------------------------------------------------------------------------
//  API Key CRUD (simple access token model)
// ---------------------------------------------------------------------------

func TestAPIKeyCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create
	in := model.ApiKeyInput{
		Name:      "Test Token",
		ExpiresAt: 1893456000000, // 2030-01-01
	}
	k, err := s.CreateAPIKey(in)
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if k.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if k.Name != "Test Token" {
		t.Fatalf("expected name 'Test Token', got %q", k.Name)
	}
	if k.ExpiresAt != in.ExpiresAt {
		t.Fatalf("expected expires_at %d, got %d", in.ExpiresAt, k.ExpiresAt)
	}

	// List
	keys, err := s.ListAPIKeys()
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].ID != k.ID || keys[0].Name != k.Name {
		t.Fatalf("list mismatch: got %+v", keys[0])
	}

	// Update
	updated, err := s.UpdateAPIKey(k.ID, model.ApiKeyInput{
		Name:      "Updated Token",
		ExpiresAt: 1924992000000, // 2031-01-01
	})
	if err != nil {
		t.Fatalf("UpdateAPIKey: %v", err)
	}
	if updated.Name != "Updated Token" {
		t.Fatalf("expected 'Updated Token', got %q", updated.Name)
	}
	if updated.ExpiresAt != 1924992000000 {
		t.Fatalf("expected updated expiry 1924992000000, got %d", updated.ExpiresAt)
	}

	// Delete
	if err := s.DeleteAPIKey(k.ID); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	keys, _ = s.ListAPIKeys()
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys after delete, got %d", len(keys))
	}

	// Recreate to verify the simple path works end-to-end
	created, err := s.CreateAPIKey(model.ApiKeyInput{Name: "Another Token"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	if created.ID == "" || created.Name != "Another Token" {
		t.Fatalf("unexpected recreated key: %+v", created)
	}
}

// ---------------------------------------------------------------------------
//  ModelRule CRUD
// ---------------------------------------------------------------------------

func TestModelRuleCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create with targets
	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "Test Model",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p01", ModelName: "gpt-4o"},
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	if len(r.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(r.Targets))
	}

	// Get
	got, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule: %v", err)
	}
	if got.Name != "Test Model" {
		t.Fatalf("expected 'Test Model', got %q", got.Name)
	}

	// List
	list, err := s.ListModelRules()
	if err != nil {
		t.Fatalf("ListModelRules: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 model rule, got %d", len(list))
	}

	// Update
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "Updated Model",
		Enabled: false,
		Targets: []model.ModelRuleTargetInput{},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule: %v", err)
	}
	if updated.Name != "Updated Model" {
		t.Fatalf("expected 'Updated Model', got %q", updated.Name)
	}
	if len(updated.Targets) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(updated.Targets))
	}

	// Reorder (no-op but must still succeed for API compatibility)
	if err := s.ReorderModelRules([]string{r.ID}); err != nil {
		t.Fatalf("ReorderModelRules: %v", err)
	}

	// Delete
	if err := s.DeleteModelRule(r.ID); err != nil {
		t.Fatalf("DeleteModelRule: %v", err)
	}
	_, err = s.GetModelRule(r.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestModelRule_NameUniqueness verifies that two rules cannot share the
// same Name (the client-facing model identifier). The second Create/Update
// with a duplicate name must return an error.
func TestModelRule_NameUniqueness(t *testing.T) {
	s := newTestStore(t)

	// First rule lands cleanly.
	if _, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "shared-name",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{{ProviderID: "p01", ModelName: "m"}},
	}); err != nil {
		t.Fatalf("CreateModelRule (first): %v", err)
	}

	// Second rule with the same name must fail.
	if _, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "shared-name",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{{ProviderID: "p02", ModelName: "m"}},
	}); err == nil {
		t.Fatal("expected error for duplicate name, got nil")
	}

	// A different name should still work.
	if _, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "different-name",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{{ProviderID: "p03", ModelName: "m"}},
	}); err != nil {
		t.Fatalf("CreateModelRule (different name): %v", err)
	}
}

func TestModelRule_NameUniquenessDatabaseConstraint(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateModelRule(model.ModelRuleInput{Name: "database-guard"}); err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	_, err := s.db.Exec(`INSERT INTO model_rules
		(id, name, enabled, first_byte_timeout_ms, strategy, display_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "direct-duplicate", "database-guard", 1, 0, "priority_first", 0, nowMs(), nowMs())
	if err == nil {
		t.Fatal("expected database unique constraint to reject duplicate model rule name")
	}
}

// TestModelRule_UpdateRenameRejectsDuplicate verifies that renaming a rule
// to an already-taken name is rejected by the store.
func TestModelRule_UpdateRenameRejectsDuplicate(t *testing.T) {
	s := newTestStore(t)

	r1, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "alpha",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{{ProviderID: "p01", ModelName: "m"}},
	})
	if err != nil {
		t.Fatalf("CreateModelRule (r1): %v", err)
	}
	if _, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "beta",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{{ProviderID: "p02", ModelName: "m"}},
	}); err != nil {
		t.Fatalf("CreateModelRule (r2): %v", err)
	}

	// Try to rename alpha → beta; should fail.
	if _, err := s.UpdateModelRule(r1.ID, model.ModelRuleInput{
		Name:    "beta",
		Enabled: true,
		Targets: targetsToInput(r1.Targets),
	}); err == nil {
		t.Fatal("expected rename-to-duplicate error, got nil")
	}

	// Rename alpha → alpha (no-op rename) should still succeed.
	if _, err := s.UpdateModelRule(r1.ID, model.ModelRuleInput{
		Name:    "alpha",
		Enabled: true,
		Targets: targetsToInput(r1.Targets),
	}); err != nil {
		t.Fatalf("UpdateModelRule (same name): %v", err)
	}
}

// TestModelRuleUpdatePreservesTargetCounters verifies the round-trip fix: editing
// a rule must NOT reset per-target hit_count/failure_count on targets that
// were kept (same ID round-trips). It also covers reorder (tier rewritten from
// slice index while ID is preserved), add (new row, counters default 0), and
// remove (deleted).
func TestModelRuleUpdatePreservesTargetCounters(t *testing.T) {
	s := newTestStore(t)

	// Create with three targets.
	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "Counter Test",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p01", ModelName: "m1"}, // tier 0
			{ProviderID: "p01", ModelName: "m2"}, // tier 1
			{ProviderID: "p02", ModelName: "m3"}, // tier 2
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	if len(r.Targets) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(r.Targets))
	}
	m1, m2, m3 := r.Targets[0], r.Targets[1], r.Targets[2]

	// Set distinct counters on each target so we can prove preservation.
	if err := s.IncrementTargetStats(m1.ID, 5, 2); err != nil {
		t.Fatalf("IncrementTargetStats[m1]: %v", err)
	}
	if err := s.IncrementTargetStats(m2.ID, 3, 7); err != nil {
		t.Fatalf("IncrementTargetStats[m2]: %v", err)
	}
	if err := s.IncrementTargetStats(m3.ID, 1, 0); err != nil {
		t.Fatalf("IncrementTargetStats[m3]: %v", err)
	}

	// Sanity: counters reflected before update.
	pre, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule (pre): %v", err)
	}
	want := []struct {
		id        string
		model     string
		hit, fail int64
	}{
		{m1.ID, "m1", 5, 2},
		{m2.ID, "m2", 3, 7},
		{m3.ID, "m3", 1, 0},
	}
	for i, w := range want {
		if pre.Targets[i].ID != w.id || pre.Targets[i].ModelName != w.model ||
			pre.Targets[i].HitCount != w.hit || pre.Targets[i].FailureCount != w.fail {
			t.Fatalf("pre[%d]: got %+v, want id=%s model=%s hit=%d fail=%d",
				i, pre.Targets[i], w.id, w.model, w.hit, w.fail)
		}
	}

	// Update: reorder (m3 first, m1 second), drop m2, add a brand-new m4.
	//   - m3 and m1 keep their IDs → counters must round-trip.
	//   - m2 omitted → DELETEd.
	//   - m4 has empty ID → INSERTed fresh; counters default to 0.
	//   - MaxRetries change on m1 must persist via UPDATE.
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "Counter Test (renamed)",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ID: m3.ID, ProviderID: "p02", ModelName: "m3"},
			{ID: m1.ID, ProviderID: "p01", ModelName: "m1", MaxRetries: 4},
			{ProviderID: "p03", ModelName: "m4"}, // new, empty ID
		},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule: %v", err)
	}

	if len(updated.Targets) != 3 {
		t.Fatalf("expected 3 targets after update, got %d", len(updated.Targets))
	}

	// Slice order after update: [m3, m1, m4] (m3 promoted to top).
	if updated.Targets[0].ModelName != "m3" {
		t.Fatalf("expected tier 0 = m3, got %q", updated.Targets[0].ModelName)
	}
	if updated.Targets[0].ID != m3.ID {
		t.Fatalf("m3 ID not preserved: got %q, want %q", updated.Targets[0].ID, m3.ID)
	}
	if updated.Targets[0].HitCount != 1 || updated.Targets[0].FailureCount != 0 {
		t.Fatalf("m3 counters lost: expected 1/0, got %d/%d",
			updated.Targets[0].HitCount, updated.Targets[0].FailureCount)
	}

	if updated.Targets[1].ModelName != "m1" {
		t.Fatalf("expected tier 1 = m1, got %q", updated.Targets[1].ModelName)
	}
	if updated.Targets[1].ID != m1.ID {
		t.Fatalf("m1 ID not preserved: got %q, want %q", updated.Targets[1].ID, m1.ID)
	}
	if updated.Targets[1].HitCount != 5 || updated.Targets[1].FailureCount != 2 {
		t.Fatalf("m1 counters lost: expected 5/2, got %d/%d",
			updated.Targets[1].HitCount, updated.Targets[1].FailureCount)
	}
	if updated.Targets[1].MaxRetries != 4 {
		t.Fatalf("m1 MaxRetries not updated: expected 4, got %d", updated.Targets[1].MaxRetries)
	}

	// m4 must be a fresh row (non-empty ID, default counters).
	if updated.Targets[2].ModelName != "m4" {
		t.Fatalf("expected tier 2 = m4, got %q", updated.Targets[2].ModelName)
	}
	if updated.Targets[2].ID == "" {
		t.Fatal("new target m4 should have a generated ID")
	}
	if updated.Targets[2].ID == m1.ID || updated.Targets[2].ID == m2.ID || updated.Targets[2].ID == m3.ID {
		t.Fatalf("new target m4 must not reuse an existing ID; got %q", updated.Targets[2].ID)
	}
	if updated.Targets[2].HitCount != 0 || updated.Targets[2].FailureCount != 0 {
		t.Fatalf("m4 counters must default to 0, got %d/%d",
			updated.Targets[2].HitCount, updated.Targets[2].FailureCount)
	}

	// m2 must be gone.
	for _, tg := range updated.Targets {
		if tg.ID == m2.ID {
			t.Fatalf("expected removed target m2 (ID %q) to be deleted, but it still exists", m2.ID)
		}
	}

	// Confirm the preserved target is still writable (proves the row is real,
	// not just echoed from the in-memory slice).
	if err := s.IncrementTargetStats(updated.Targets[1].ID, 1, 0); err != nil {
		t.Fatalf("IncrementTargetStats after update: %v", err)
	}
	post, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule (post): %v", err)
	}
	for _, tg := range post.Targets {
		if tg.ID == m1.ID {
			if tg.HitCount != 6 || tg.FailureCount != 2 {
				t.Fatalf("m1 after extra +1 hit: expected 6/2, got %d/%d", tg.HitCount, tg.FailureCount)
			}
		}
	}

	// Edge case: updating with an empty Targets slice deletes all targets
	// cleanly (no incoming IDs → DELETE WHERE route_id = ?).
	cleared, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "Counter Test (renamed)",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule (clear): %v", err)
	}
	if len(cleared.Targets) != 0 {
		t.Fatalf("expected 0 targets after clearing, got %d", len(cleared.Targets))
	}
}

// TestModelRuleClampsNegativeMaxRetries verifies Fix 2: a negative max_retries
// coming from the API is clamped to 0 rather than rejected (friendlier, and
// prevents the silent-skip footgun in the retry loop).
func TestModelRuleClampsNegativeMaxRetries(t *testing.T) {
	s := newTestStore(t)

	// CreateModelRule clamps too.
	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "Clamp Test",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p01", ModelName: "m1", MaxRetries: -7},
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	if r.Targets[0].MaxRetries != 0 {
		t.Fatalf("CreateModelRule: expected MaxRetries clamped to 0, got %d", r.Targets[0].MaxRetries)
	}

	// UpdateModelRule (upsert path) clamps as well.
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "Clamp Test",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ID: r.Targets[0].ID, ProviderID: "p01", ModelName: "m1", MaxRetries: -1},
		},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule: %v", err)
	}
	if len(updated.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(updated.Targets))
	}
	if updated.Targets[0].MaxRetries != 0 {
		t.Fatalf("UpdateModelRule: expected MaxRetries clamped to 0, got %d", updated.Targets[0].MaxRetries)
	}
	// And the existing target ID was preserved (not a new row).
	if updated.Targets[0].ID != r.Targets[0].ID {
		t.Fatalf("expected target ID preserved (%q), got %q", r.Targets[0].ID, updated.Targets[0].ID)
	}
}

// TestReorderModelRuleTargets verifies the non-destructive reorder API: only the
// tier column changes, IDs are preserved, and counters are untouched. It also
// rejects malformed inputs (count mismatch, unknown ID, duplicate ID).
func TestReorderModelRuleTargets(t *testing.T) {
	s := newTestStore(t)

	// Create a rule with three targets.
	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "Reorder Target Test",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p01", ModelName: "m1"}, // tier 0
			{ProviderID: "p01", ModelName: "m2"}, // tier 1
			{ProviderID: "p02", ModelName: "m3"}, // tier 2
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	if len(r.Targets) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(r.Targets))
	}
	m1, m2, m3 := r.Targets[0], r.Targets[1], r.Targets[2]

	// Set distinct counters so we can prove preservation.
	if err := s.IncrementTargetStats(m1.ID, 5, 2); err != nil {
		t.Fatalf("IncrementTargetStats[m1]: %v", err)
	}
	if err := s.IncrementTargetStats(m2.ID, 3, 7); err != nil {
		t.Fatalf("IncrementTargetStats[m2]: %v", err)
	}
	if err := s.IncrementTargetStats(m3.ID, 1, 0); err != nil {
		t.Fatalf("IncrementTargetStats[m3]: %v", err)
	}

	// Reorder: swap first and last, so the new order is [m3, m2, m1].
	if err := s.ReorderModelRuleTargets(r.ID, []string{m3.ID, m2.ID, m1.ID}); err != nil {
		t.Fatalf("ReorderModelRuleTargets: %v", err)
	}

	post, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule (post): %v", err)
	}
	if len(post.Targets) != 3 {
		t.Fatalf("expected 3 targets after reorder, got %d", len(post.Targets))
	}

	// Verify order and ID preservation.
	want := []struct {
		id    string
		model string
		hit   int64
		fail  int64
	}{
		{m3.ID, "m3", 1, 0},
		{m2.ID, "m2", 3, 7},
		{m1.ID, "m1", 5, 2},
	}
	for i, w := range want {
		if post.Targets[i].ID != w.id {
			t.Fatalf("post[%d]: id got %q, want %q", i, post.Targets[i].ID, w.id)
		}
		if post.Targets[i].ModelName != w.model {
			t.Fatalf("post[%d]: model got %q, want %q", i, post.Targets[i].ModelName, w.model)
		}
		if post.Targets[i].HitCount != w.hit || post.Targets[i].FailureCount != w.fail {
			t.Fatalf("post[%d]: counters got %d/%d, want %d/%d", i,
				post.Targets[i].HitCount, post.Targets[i].FailureCount, w.hit, w.fail)
		}
	}

	// Wrong number of IDs is a detectable target-set conflict.
	if err := s.ReorderModelRuleTargets(r.ID, []string{m1.ID, m2.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for wrong ID count, got %v", err)
	}

	// Unknown ID (same count, bad id) must fail.
	if err := s.ReorderModelRuleTargets(r.ID, []string{m1.ID, m2.ID, "unknown-id"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for unknown ID, got %v", err)
	}

	// Duplicate ID must fail.
	if err := s.ReorderModelRuleTargets(r.ID, []string{m1.ID, m1.ID, m2.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict for duplicate ID, got %v", err)
	}
}

// TestModelRuleTarget_EnabledDefaultsToTrueOnInsert verifies the Phase-3 spec
// "default to true when a target is created without an explicit value":
// a target constructed without setting `Enabled` must be stored as enabled.
// This guards the assumption that a brand-new target is usable immediately
// without the frontend having to opt in.
func TestModelRuleTarget_EnabledDefaultsToTrueOnInsert(t *testing.T) {
	s := newTestStore(t)

	// CreateModelRule path.
	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "Default-enabled",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p01", ModelName: "m1"}, // Enabled unset (zero value)
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	if !r.Targets[0].Enabled {
		t.Fatalf("CreateModelRule: expected Enabled default-true, got false")
	}

	// Round-trip through the store to confirm persistence, not just an in-memory echo.
	got, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule: %v", err)
	}
	if !got.Targets[0].Enabled {
		t.Fatalf("GetModelRule: expected persisted Enabled=true, got false")
	}

	// UpdateModelRule path: adding a new target (empty ID) to an existing route
	// must also default-true.
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "Default-enabled",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ID: r.Targets[0].ID, ProviderID: "p01", ModelName: "m1", Enabled: true},
			{ProviderID: "p02", ModelName: "m2"}, // new target, Enabled unset
		},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule: %v", err)
	}
	if !updated.Targets[1].Enabled {
		t.Fatalf("UpdateModelRule (new target): expected Enabled default-true, got false")
	}
}

// TestModelRuleTarget_EnabledToggleRoundTrip verifies the per-target enable/disable
// toggle flows through CreateModelRule, UpdateModelRule, and the read path without
// disturbing other fields or the per-target counters.
func TestModelRuleTarget_EnabledToggleRoundTrip(t *testing.T) {
	s := newTestStore(t)

	r, err := s.CreateModelRule(model.ModelRuleInput{
		Name:    "Toggle",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ProviderID: "p01", ModelName: "m1", Enabled: true},
			{ProviderID: "p02", ModelName: "m2", Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("CreateModelRule: %v", err)
	}
	t0, t1 := r.Targets[0], r.Targets[1]

	// Bump counters so we can prove the UPDATE doesn't reset them.
	if err := s.IncrementTargetStats(t0.ID, 1, 0); err != nil {
		t.Fatalf("IncrementTargetStats: %v", err)
	}

	// Flip t0 off, t1 stays on.
	updated, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "Toggle",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ID: t0.ID, ProviderID: "p01", ModelName: "m1", Enabled: false},
			{ID: t1.ID, ProviderID: "p02", ModelName: "m2", Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule: %v", err)
	}

	var got0, got1 model.ModelRuleTarget
	for _, tg := range updated.Targets {
		switch tg.ID {
		case t0.ID:
			got0 = tg
		case t1.ID:
			got1 = tg
		}
	}
	if got0.ID == "" || got1.ID == "" {
		t.Fatalf("expected both targets to round-trip, got %+v", updated.Targets)
	}
	if got0.Enabled {
		t.Fatalf("t0: expected Enabled=false after toggle off, got true")
	}
	if !got1.Enabled {
		t.Fatalf("t1: expected Enabled=true (unchanged), got false")
	}
	if got0.HitCount != 1 {
		t.Fatalf("t0: expected HitCount=1 (counters preserved across toggle), got %d", got0.HitCount)
	}

	// Re-read to confirm the change is persisted, not just echoed.
	reread, err := s.GetModelRule(r.ID)
	if err != nil {
		t.Fatalf("GetModelRule: %v", err)
	}
	for _, tg := range reread.Targets {
		if tg.ID == t0.ID && tg.Enabled {
			t.Fatalf("persisted t0 should be disabled, got Enabled=true")
		}
		if tg.ID == t1.ID && !tg.Enabled {
			t.Fatalf("persisted t1 should remain enabled, got Enabled=false")
		}
	}

	// Flip t0 back on.
	reEnabled, err := s.UpdateModelRule(r.ID, model.ModelRuleInput{
		Name:    "Toggle",
		Enabled: true,
		Targets: []model.ModelRuleTargetInput{
			{ID: t0.ID, ProviderID: "p01", ModelName: "m1", Enabled: true},
			{ID: t1.ID, ProviderID: "p02", ModelName: "m2", Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("UpdateModelRule (re-enable): %v", err)
	}
	if !reEnabled.Targets[0].Enabled {
		t.Fatalf("t0: expected Enabled=true after re-enable, got false")
	}
}

// ---------------------------------------------------------------------------
//  Settings
// ---------------------------------------------------------------------------

func TestSettings(t *testing.T) {
	s := newTestStore(t)

	settings, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings (default): %v", err)
	}
	if settings.Server.Port != 8344 {
		t.Fatalf("expected default port 8344, got %d", settings.Server.Port)
	}
	if settings.General.StartupAction != "show_window" {
		t.Fatalf("expected default startup action show_window, got %q", settings.General.StartupAction)
	}
	if settings.Advanced.FeatureCapabilityEnforcement != model.FeatureCapabilityEnforcementObserve {
		t.Fatalf("expected default feature enforcement observe, got %q", settings.Advanced.FeatureCapabilityEnforcement)
	}

	settings.Server.Port = 9090
	settings.Appearance.Theme = "dark"
	if err := s.SaveSettings(*settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	loaded, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings (after save): %v", err)
	}
	if loaded.Server.Port != 9090 {
		t.Fatalf("expected port 9090, got %d", loaded.Server.Port)
	}
	if loaded.Appearance.Theme != "dark" {
		t.Fatalf("expected theme 'dark', got %q", loaded.Appearance.Theme)
	}
}

func TestSettingsInjectedDefaultsAndPersistedMerge(t *testing.T) {
	dir := t.TempDir()
	s, err := New(context.Background(), StoreDeps{DSN: dir + "/settings.db", DefaultPort: 18344})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	settings, err := s.GetSettings()
	if err != nil || settings.Server.Port != 18344 {
		t.Fatalf("injected default: settings=%+v err=%v", settings, err)
	}
	if _, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES ('server', '{"bind_address":"127.0.0.1"}')`); err != nil {
		t.Fatalf("insert partial settings: %v", err)
	}
	settings, err = s.GetSettings()
	if err != nil || settings.Server.Port != 18344 || settings.Server.BindAddress != "127.0.0.1" {
		t.Fatalf("partial server merge: settings=%+v err=%v", settings, err)
	}
	if _, err := s.db.Exec(`UPDATE settings SET value = '{"port":19090}' WHERE key = 'server'`); err != nil {
		t.Fatalf("persist explicit port: %v", err)
	}
	settings, err = s.GetSettings()
	if err != nil || settings.Server.Port != 19090 || settings.Server.BindAddress != "0.0.0.0" {
		t.Fatalf("explicit persisted port: settings=%+v err=%v", settings, err)
	}
	settings, err = s.ResetSettings()
	if err != nil || settings.Server.Port != 18344 {
		t.Fatalf("reset settings: settings=%+v err=%v", settings, err)
	}
	settings, err = s.GetSettings()
	if err != nil || settings.Server.Port != 18344 {
		t.Fatalf("persisted reset settings: settings=%+v err=%v", settings, err)
	}
}

func TestSettingsZeroDefaultPortFallsBack(t *testing.T) {
	s := newTestStore(t)
	settings, err := s.GetSettings()
	if err != nil || settings.Server.Port != 8344 {
		t.Fatalf("zero default port fallback: settings=%+v err=%v", settings, err)
	}
}

func TestFeatureCapabilityEnforcementNormalization(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES ('advanced', '{"feature_capability_enforcement":"bad"}')`); err != nil {
		t.Fatal(err)
	}
	settings, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Advanced.FeatureCapabilityEnforcement != model.FeatureCapabilityEnforcementObserve {
		t.Fatalf("expected invalid value normalized to observe, got %q", settings.Advanced.FeatureCapabilityEnforcement)
	}

	settings.Advanced.FeatureCapabilityEnforcement = model.FeatureCapabilityEnforcementEnforce
	if err := s.SaveSettings(*settings); err != nil {
		t.Fatal(err)
	}
	settings, err = s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.Advanced.FeatureCapabilityEnforcement != model.FeatureCapabilityEnforcementEnforce {
		t.Fatalf("expected enforce roundtrip, got %q", settings.Advanced.FeatureCapabilityEnforcement)
	}
}

// ---------------------------------------------------------------------------
//  Endpoints
// ---------------------------------------------------------------------------

func TestListEndpoints(t *testing.T) {
	s := newTestStore(t)
	eps, err := s.ListEndpoints()
	if err != nil {
		t.Fatalf("ListEndpoints: %v", err)
	}
	if len(eps) != 7 {
		t.Fatalf("expected 7 endpoints, got %d", len(eps))
	}
}

// ---------------------------------------------------------------------------
//  Request logs + purge
// ---------------------------------------------------------------------------

func TestRequestLogs(t *testing.T) {
	s := newTestStore(t)

	// Insert a log
	l := model.RequestLog{
		ID:           "log-1",
		Timestamp:    1000,
		StatusCode:   200,
		ProviderID:   "p01",
		ProviderName: "OpenAI",
		Model:        "gpt-4o",
		InputTokens:  100,
		OutputTokens: 50,
		LatencyMs:    200,
	}
	if err := s.InsertRequestLog(l); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
	}

	// Query
	logs, total, err := s.QueryLogs(model.LogQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total 1, got %d", total)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}

	// Purge (0 days = delete everything older than today)
	_, err = s.PurgeLogs(0)
	if err != nil {
		t.Fatalf("PurgeLogs: %v", err)
	}
	logs, total, _ = s.QueryLogs(model.LogQuery{Page: 1, PageSize: 10})
	if total != 0 {
		t.Fatalf("expected 0 logs after purge, got %d", total)
	}
}

// ---------------------------------------------------------------------------
//  Dashboard + Usage
// ---------------------------------------------------------------------------

func TestDashboardAndUsage(t *testing.T) {
	s := newTestStore(t)

	// Dashboard on empty store should not crash
	d, err := s.Dashboard()
	if err != nil {
		t.Fatalf("Dashboard (empty): %v", err)
	}
	if d == nil {
		t.Fatal("Dashboard returned nil")
	}

	u, err := s.UsageStats(model.LogQuery{})
	if err != nil {
		t.Fatalf("UsageStats (empty): %v", err)
	}
	if u == nil {
		t.Fatal("UsageStats returned nil")
	}
}

// ---------------------------------------------------------------------------
//  Export
// ---------------------------------------------------------------------------

func TestExport(t *testing.T) {
	s := newTestStore(t)

	data, filename, err := s.Export(model.ExportSettingsJSON)
	if err != nil {
		t.Fatalf("Export settings: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("exported data is empty")
	}
	if filename != "autoapi-settings.json" {
		t.Fatalf("expected 'autoapi-settings.json', got %q", filename)
	}

	// All JSON
	_, _, err = s.Export(model.ExportAllJSON)
	if err != nil {
		t.Fatalf("Export all: %v", err)
	}

	// CSV exports
	_, _, err = s.Export(model.ExportTokensCSV)
	if err != nil {
		t.Fatalf("Export tokens csv: %v", err)
	}
	_, _, err = s.Export(model.ExportLogsCSV)
	if err != nil {
		t.Fatalf("Export logs csv: %v", err)
	}
}

// ---------------------------------------------------------------------------
//  Provider test update helpers
// ---------------------------------------------------------------------------

func TestProviderTestHelpers(t *testing.T) {
	s := newTestStore(t)

	p, _ := s.CreateProvider(model.ProviderInput{Name: "Test", BaseURL: "https://test.example.com"})

	if err := s.UpdateProviderTestResult(p.ID, model.ProviderStatusConnected, 150, ""); err != nil {
		t.Fatalf("UpdateProviderTestResult: %v", err)
	}

	got, _ := s.GetProvider(p.ID)
	if got.Status != model.ProviderStatusConnected {
		t.Fatalf("expected status 'connected', got %q", got.Status)
	}
	if got.AvgLatencyMs != 150 {
		t.Fatalf("expected avg_latency_ms 150, got %d", got.AvgLatencyMs)
	}
	// models_count should NOT be updated by UpdateProviderTestResult.
	if got.ModelsCount != 0 {
		t.Fatalf("expected models_count unchanged (0), got %d", got.ModelsCount)
	}
}

// TestRuleTargets_MigrationRenameIdempotency covers the Phase-3 oracle fix:
// the per-target `enabled` column was originally added under migration
// "009_route_target_enabled" and is now "010_route_target_enabled". A
// pre-existing DB will have the column AND a _migrations row with the old
// ID, so the renamed entry must:
//   - not crash with "duplicate column name", and
//   - be recorded under its new ID in _migrations.
func TestRuleTargets_MigrationRenameIdempotency(t *testing.T) {
	dir, err := os.MkdirTemp("", "autoapi-mig-rename-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Open the DB and apply the full migration list as it stands today
	// (which includes the renamed 010_ entry, not 009_). The fresh DB will
	// pick up the 010_ migration cleanly.
	s, err := New(context.Background(), StoreDeps{DSN: dir + "/fresh.db"})
	if err != nil {
		t.Fatalf("New (fresh): %v", err)
	}
	s.Close()

	// Reopen — the 010_ entry is already in _migrations so the loop should
	// just continue past it.
	s2, err := New(context.Background(), StoreDeps{DSN: dir + "/fresh.db"})
	if err != nil {
		t.Fatalf("New (reopen): %v", err)
	}
	s2.Close()

	// On a DB where the column was created under the OLD 009_ ID, the rename
	// to 010_ should be detected as a no-op and the new ID recorded without
	// the duplicate-column crash. Simulate that by:
	//   1. creating a fresh DB,
	//   2. manually rewriting the 010_ tracking row to 009_ to mimic the
	//      pre-rename world,
	//   3. reopening it through New() so the renamed migration runs again.
	dir2, err := os.MkdirTemp("", "autoapi-mig-rename-old-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir2)

	s3, err := New(context.Background(), StoreDeps{DSN: dir2 + "/old.db"})
	if err != nil {
		t.Fatalf("New (old-bootstrap): %v", err)
	}
	if _, err := s3.db.Exec(`UPDATE _migrations SET id = '009_route_target_enabled' WHERE id = '010_route_target_enabled'`); err != nil {
		t.Fatalf("simulate pre-rename: rewrite 010->009: %v", err)
	}
	s3.Close()

	// Reopen — the 010_ entry will look pending, the SkipIfRedundant
	// predicate will detect the column is already present, and the migration
	// runner will record the 010_ ID without re-running the SQL.
	s4, err := New(context.Background(), StoreDeps{DSN: dir2 + "/old.db"})
	if err != nil {
		t.Fatalf("New (reopen with old ID): %v", err)
	}
	defer s4.Close()

	var seen009, seen010 int
	if err := s4.db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE id = '009_route_target_enabled'`).Scan(&seen009); err != nil {
		t.Fatalf("count 009: %v", err)
	}
	if err := s4.db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE id = '010_route_target_enabled'`).Scan(&seen010); err != nil {
		t.Fatalf("count 010: %v", err)
	}
	if seen009 != 1 {
		t.Fatalf("expected legacy 009_ row to still be present, got %d", seen009)
	}
	if seen010 != 1 {
		t.Fatalf("expected new 010_ row to be recorded after rename, got %d", seen010)
	}
}

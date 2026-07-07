package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"autoapi/internal/model"
)

func init() {
	// Disable dev seeding in tests so test data isn't polluted.
	initDev = func(*Store) {}
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
		Name:        "Test Provider",
		BaseURL:     "https://test.example.com",
		UpstreamKey: "sk-test-abc123",
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
	if string(got.KeyCiphertext) != "dummy-ciphertext" {
		t.Fatalf("expected ciphertext set by helper, got %q", got.KeyCiphertext)
	}
	if got.KeyMasked != "sk-test-****c123" {
		t.Fatalf("expected masked key 'sk-test-****c123', got %q", got.KeyMasked)
	}

	// Update provider body; key columns should be preserved.
	updated, err := s.UpdateProvider(p.ID, model.ProviderInput{
		Name:        "Updated Provider",
		BaseURL:     "https://updated.example.com",
		UpstreamKey: "sk-updated-xyz789",
	})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.Name != "Updated Provider" {
		t.Fatalf("expected 'Updated Provider', got %q", updated.Name)
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
//  Route CRUD
// ---------------------------------------------------------------------------

func TestRouteCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create with conditions and targets
	r, err := s.CreateRoute(model.RouteInput{
		Name:        "Test Route",
		Description: "A test route",
		Enabled:     true,
		Conditions: []model.RouteCondition{
			{Field: "model", Operator: model.OpMatches, Value: "gpt-*"},
		},
		Targets: []model.RouteTarget{
			{ProviderID: "p01", ModelName: "gpt-4o"},
		},
	})
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}
	if len(r.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(r.Conditions))
	}
	if len(r.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(r.Targets))
	}

	// Get
	got, err := s.GetRoute(r.ID)
	if err != nil {
		t.Fatalf("GetRoute: %v", err)
	}
	if got.Name != "Test Route" {
		t.Fatalf("expected 'Test Route', got %q", got.Name)
	}

	// List
	list, err := s.ListRoutes()
	if err != nil {
		t.Fatalf("ListRoutes: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 route, got %d", len(list))
	}

	// Update
	updated, err := s.UpdateRoute(r.ID, model.RouteInput{
		Name:        "Updated Route",
		Description: "Updated",
		Enabled:     false,
		Conditions:  []model.RouteCondition{},
		Targets:     []model.RouteTarget{},
	})
	if err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}
	if updated.Name != "Updated Route" {
		t.Fatalf("expected 'Updated Route', got %q", updated.Name)
	}
	if len(updated.Conditions) != 0 {
		t.Fatalf("expected 0 conditions, got %d", len(updated.Conditions))
	}

	// Reorder
	if err := s.ReorderRoutes([]string{r.ID}); err != nil {
		t.Fatalf("ReorderRoutes: %v", err)
	}

	// Delete
	if err := s.DeleteRoute(r.ID); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	_, err = s.GetRoute(r.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestRouteUpdatePreservesTargetCounters verifies the round-trip fix: editing
// a route must NOT reset per-target hit_count/failure_count on targets that
// were kept (same ID round-trips). It also covers reorder (tier rewritten from
// slice index while ID is preserved), add (new row, counters default 0), and
// remove (deleted).
func TestRouteUpdatePreservesTargetCounters(t *testing.T) {
	s := newTestStore(t)

	// Create with three targets.
	r, err := s.CreateRoute(model.RouteInput{
		Name:    "Counter Test",
		Enabled: true,
		Targets: []model.RouteTarget{
			{ProviderID: "p01", ModelName: "m1"}, // tier 0
			{ProviderID: "p01", ModelName: "m2"}, // tier 1
			{ProviderID: "p02", ModelName: "m3"}, // tier 2
		},
	})
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
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
	pre, err := s.GetRoute(r.ID)
	if err != nil {
		t.Fatalf("GetRoute (pre): %v", err)
	}
	want := []struct {
		id              string
		model           string
		hit, fail       int64
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
	updated, err := s.UpdateRoute(r.ID, model.RouteInput{
		Name:    "Counter Test (renamed)",
		Enabled: true,
		Targets: []model.RouteTarget{
			{ID: m3.ID, ProviderID: "p02", ModelName: "m3"},
			{ID: m1.ID, ProviderID: "p01", ModelName: "m1", MaxRetries: 4},
			{ProviderID: "p03", ModelName: "m4"}, // new, empty ID
		},
	})
	if err != nil {
		t.Fatalf("UpdateRoute: %v", err)
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
	post, err := s.GetRoute(r.ID)
	if err != nil {
		t.Fatalf("GetRoute (post): %v", err)
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
	cleared, err := s.UpdateRoute(r.ID, model.RouteInput{
		Name:    "Counter Test (renamed)",
		Enabled: true,
		Targets: []model.RouteTarget{},
	})
	if err != nil {
		t.Fatalf("UpdateRoute (clear): %v", err)
	}
	if len(cleared.Targets) != 0 {
		t.Fatalf("expected 0 targets after clearing, got %d", len(cleared.Targets))
	}
}

// TestRouteClampsNegativeMaxRetries verifies Fix 2: a negative max_retries
// coming from the API is clamped to 0 rather than rejected (friendlier, and
// prevents the silent-skip footgun in the retry loop).
func TestRouteClampsNegativeMaxRetries(t *testing.T) {
	s := newTestStore(t)

	// CreateRoute clamps too.
	r, err := s.CreateRoute(model.RouteInput{
		Name:    "Clamp Test",
		Enabled: true,
		Targets: []model.RouteTarget{
			{ProviderID: "p01", ModelName: "m1", MaxRetries: -7},
		},
	})
	if err != nil {
		t.Fatalf("CreateRoute: %v", err)
	}
	if r.Targets[0].MaxRetries != 0 {
		t.Fatalf("CreateRoute: expected MaxRetries clamped to 0, got %d", r.Targets[0].MaxRetries)
	}

	// UpdateRoute (upsert path) clamps as well.
	updated, err := s.UpdateRoute(r.ID, model.RouteInput{
		Name:    "Clamp Test",
		Enabled: true,
		Targets: []model.RouteTarget{
			{ID: r.Targets[0].ID, ProviderID: "p01", ModelName: "m1", MaxRetries: -1},
		},
	})
	if err != nil {
		t.Fatalf("UpdateRoute: %v", err)
	}
	if len(updated.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(updated.Targets))
	}
	if updated.Targets[0].MaxRetries != 0 {
		t.Fatalf("UpdateRoute: expected MaxRetries clamped to 0, got %d", updated.Targets[0].MaxRetries)
	}
	// And the existing target ID was preserved (not a new row).
	if updated.Targets[0].ID != r.Targets[0].ID {
		t.Fatalf("expected target ID preserved (%q), got %q", r.Targets[0].ID, updated.Targets[0].ID)
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

	u, err := s.UsageStats()
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

	if err := s.UpdateProviderTestResult(p.ID, model.ProviderStatusConnected, 3, 150, ""); err != nil {
		t.Fatalf("UpdateProviderTestResult: %v", err)
	}

	got, _ := s.GetProvider(p.ID)
	if got.Status != model.ProviderStatusConnected {
		t.Fatalf("expected status 'connected', got %q", got.Status)
	}
	if got.ModelsCount != 3 {
		t.Fatalf("expected models_count 3, got %d", got.ModelsCount)
	}
}

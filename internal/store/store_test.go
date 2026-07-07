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

	if err := s.UpsertModels(p.ID, []string{"model-a", "model-b"}); err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}

	models, err := s.ListModels(p.ID)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}

	// Upsert again (should replace)
	if err := s.UpsertModels(p.ID, []string{"model-c"}); err != nil {
		t.Fatalf("UpsertModels (2): %v", err)
	}
	models, _ = s.ListModels(p.ID)
	if len(models) != 1 || models[0].Name != "model-c" {
		t.Fatalf("expected 1 model 'model-c', got %+v", models)
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
		Priority:    1,
		Enabled:     true,
		Conditions: []model.RouteCondition{
			{Field: "model", Operator: model.OpMatches, Value: "gpt-*"},
		},
		Targets: []model.RouteTarget{
			{ProviderID: "p01", ModelName: "gpt-4o", Action: model.RouteActionForward},
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
		Priority:    2,
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
	if len(eps) != 4 {
		t.Fatalf("expected 4 endpoints, got %d", len(eps))
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

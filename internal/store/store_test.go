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
		Name:    "Test Provider",
		BaseURL: "https://test.example.com",
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

	// Get
	got, err := s.GetProvider(p.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.ID != p.ID || got.Name != p.Name || got.BaseURL != p.BaseURL {
		t.Fatalf("GetProvider mismatch: got %+v, want %+v", got, p)
	}

	// Update
	updated, err := s.UpdateProvider(p.ID, model.ProviderInput{
		Name:    "Updated Provider",
		BaseURL: "https://updated.example.com",
	})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.Name != "Updated Provider" {
		t.Fatalf("expected 'Updated Provider', got %q", updated.Name)
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
//  API Key CRUD (ciphertext variant)
// ---------------------------------------------------------------------------

func TestAPIKeyCRUD(t *testing.T) {
	s := newTestStore(t)

	// Create via ciphertext path
	in := model.ApiKeyInput{
		Name:        "Test Key",
		Key:         "sk-test-abcdefghijklmnop",
		Permission:  model.KeyPermissionReadWrite,
		Environment: model.KeyEnvProduction,
	}
	k, err := s.CreateAPIKeyCiphertext(in, []byte("ciphertext_data"), []byte("nonce_data"))
	if err != nil {
		t.Fatalf("CreateAPIKeyCiphertext: %v", err)
	}
	if k.KeyPrefix != "sk-test-abcd" {
		t.Fatalf("expected key_prefix 'sk-test-abcd', got %q", k.KeyPrefix)
	}
	if k.KeySuffix != "mnop" {
		t.Fatalf("expected key_suffix 'mnop', got %q", k.KeySuffix)
	}

	// List
	keys, err := s.ListAPIKeys()
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].KeyMasked != "sk-test-abcd****mnop" {
		t.Fatalf("unexpected masked: %q", keys[0].KeyMasked)
	}

	// Get ciphertext
	ct, nonce, pid, err := s.GetAPIKeyCiphertext(k.ID)
	if err != nil {
		t.Fatalf("GetAPIKeyCiphertext: %v", err)
	}
	if string(ct) != "ciphertext_data" {
		t.Fatalf("unexpected ciphertext: %q", ct)
	}
	if string(nonce) != "nonce_data" {
		t.Fatalf("unexpected nonce: %q", nonce)
	}
	if pid != "" {
		t.Fatalf("expected empty provider_id, got %q", pid)
	}

	// Update
	in2 := model.ApiKeyInput{
		Name:        "Updated Key",
		Key:         "sk-new-abcdefghijklmnop",
		Permission:  model.KeyPermissionReadOnly,
		Environment: model.KeyEnvTest,
	}
	_, err = s.UpdateAPIKeyCiphertext(k.ID, in2, []byte("new_ct"), []byte("new_nonce"))
	if err != nil {
		t.Fatalf("UpdateAPIKeyCiphertext: %v", err)
	}

	// Delete
	if err := s.DeleteAPIKey(k.ID); err != nil {
		t.Fatalf("DeleteAPIKey: %v", err)
	}
	keys, _ = s.ListAPIKeys()
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys after delete, got %d", len(keys))
	}

	// CreateAPIKey should return ErrCryptoRequired
	_, err = s.CreateAPIKey(in)
	if !errors.Is(err, ErrCryptoRequired) {
		t.Fatalf("expected ErrCryptoRequired, got %v", err)
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

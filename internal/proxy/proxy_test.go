package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoapi/internal/model"
)

type mockStore struct {
	providers map[string]*model.Provider
	routes    []model.Route
	apiKeys   []model.ApiKey
	settings  *model.Settings
}

func (m *mockStore) ListProviders() ([]model.Provider, error) {
	var out []model.Provider
	for _, p := range m.providers {
		out = append(out, *p)
	}
	return out, nil
}

func (m *mockStore) ListRoutes() ([]model.Route, error) { return m.routes, nil }

func (m *mockStore) GetProvider(id string) (*model.Provider, error) {
	p, ok := m.providers[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}

func (m *mockStore) ListAPIKeys() ([]model.ApiKey, error) { return m.apiKeys, nil }

func (m *mockStore) GetAPIKeyCiphertext(id string) (ciphertext, nonce []byte, providerID string, err error) {
	return nil, nil, "", nil
}

func (m *mockStore) InsertRequestLog(l model.RequestLog) error { return nil }

func (m *mockStore) InsertRequestLogsBatch(logs []model.RequestLog) error { return nil }

func (m *mockStore) ListModels(providerID string) ([]model.Model, error) { return nil, nil }

func (m *mockStore) GetSettings() (*model.Settings, error) {
	if m.settings != nil {
		return m.settings, nil
	}
	return &model.Settings{}, nil
}

func (m *mockStore) Dashboard() (*model.DashboardData, error) { return &model.DashboardData{}, nil }

func (m *mockStore) UpdateProviderHealth(id string, status model.ProviderStatus, errorMessage string) error {
	if p, ok := m.providers[id]; ok {
		p.Status = status
		p.ErrorMessage = errorMessage
	}
	return nil
}

type mockService struct{}

func (m *mockService) ResolveAPIKey(keyID string) (string, error) { return "secret", nil }

func TestFailover_P0FailsP1Succeeds(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "p0 failed"})
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "chatcmpl-p1",
			"object": "chat.completion",
			"model":  "m1",
			"usage": map[string]interface{}{
				"prompt_tokens":     3,
				"completion_tokens": 4,
			},
		})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", APIKeyID: "k0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", APIKeyID: "k1"},
		},
		routes: []model.Route{
			{
				ID: "r1", Priority: 1, Enabled: true,
				Targets: []model.RouteTarget{
					{ProviderID: "p0", ModelName: "m0", Action: model.RouteActionForward, Tier: 0},
					{ProviderID: "p1", ModelName: "m1", Action: model.RouteActionForward, Tier: 1},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-p1") {
		t.Fatalf("expected response from P1, got %s", rec.Body.String())
	}
	if p0Hits != 1 {
		t.Fatalf("expected P0 hit once, got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once, got %d", p1Hits)
	}
}

func TestFailover_OpensCircuitAfterThreshold(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "p0 failed"})
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "chatcmpl-p1",
			"object": "chat.completion",
			"model":  "m1",
			"usage":  map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", APIKeyID: "k0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", APIKeyID: "k1"},
		},
		routes: []model.Route{
			{
				ID: "r1", Priority: 1, Enabled: true,
				Targets: []model.RouteTarget{
					{ProviderID: "p0", ModelName: "m0", Action: model.RouteActionForward, Tier: 0},
					{ProviderID: "p1", ModelName: "m1", Action: model.RouteActionForward, Tier: 1},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })

	for i := 0; i < failureThreshold; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	if p0Hits != failureThreshold {
		t.Fatalf("expected P0 hits %d, got %d", failureThreshold, p0Hits)
	}
	if p1Hits != failureThreshold {
		t.Fatalf("expected P1 hits %d, got %d", failureThreshold, p1Hits)
	}

	// Next request should skip P0 because its circuit is open.
	p0HitsBefore := p0Hits
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after breaker open, got %d: %s", rec.Code, rec.Body.String())
	}
	if p0Hits != p0HitsBefore {
		t.Fatalf("expected P0 to be skipped after breaker open, got %d hits", p0Hits)
	}
	if p1Hits != failureThreshold+1 {
		t.Fatalf("expected P1 hits %d, got %d", failureThreshold+1, p1Hits)
	}
}

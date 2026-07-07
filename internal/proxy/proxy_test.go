package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoapi/internal/model"
	"autoapi/internal/store"
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
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (m *mockStore) ListAPIKeys() ([]model.ApiKey, error) { return m.apiKeys, nil }

func (m *mockStore) GetProviderKeyCiphertext(providerID string) (ciphertext, nonce []byte, err error) {
	return nil, nil, nil
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

func (m *mockService) ResolveProviderKey(providerID string) (string, error) { return "secret", nil }

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1"},
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
	defer p.Stop()
	defer p.Stop()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1"},
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
	defer p.Stop()

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

func TestFailover_NonRetryableStopsLoop(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "bad request"})
			return
		}
		p1Hits++
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "chatcmpl-p1"})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1"},
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
	defer p.Stop()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 to stop failover, got %d: %s", rec.Code, rec.Body.String())
	}
	if p0Hits != 1 {
		t.Fatalf("expected P0 hit once, got %d", p0Hits)
	}
	if p1Hits != 0 {
		t.Fatalf("expected P1 not hit, got %d", p1Hits)
	}
}

func TestFailover_AllCandidatesFail(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		p1Hits++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1"},
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
	defer p.Stop()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when all candidates fail, got %d: %s", rec.Code, rec.Body.String())
	}
	if p0Hits != 1 || p1Hits != 1 {
		t.Fatalf("expected both providers tried once, got p0=%d p1=%d", p0Hits, p1Hits)
	}
}

func TestFailover_HalfOpenProbeNotStarved(t *testing.T) {
	var p0Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p0Hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "chatcmpl-ok"})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
		},
		routes: []model.Route{
			{
				ID: "r1", Priority: 1, Enabled: true,
				Targets: []model.RouteTarget{
					{ProviderID: "p0", ModelName: "m0", Action: model.RouteActionForward, Tier: 0},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	// Open the breaker.
	cb := p.breakerFor("p0")
	cb.recoveryTimeout = 0
	for i := 0; i < failureThreshold; i++ {
		cb.Record(false)
	}
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected breaker open, got %v", cb.CurrentState())
	}

	// WouldAllow (via matcher) should report the provider is available without
	// consuming the probe. Allow (via forwardWithFailover) then claims the probe
	// and succeeds, closing the breaker.
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected half-open probe to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if p0Hits != 1 {
		t.Fatalf("expected exactly one probe hit, got %d", p0Hits)
	}
	if cb.CurrentState() != StateClosed {
		t.Fatalf("expected breaker closed after successful probe, got %v", cb.CurrentState())
	}
}

func TestFailover_DefaultProviderPreservesModel(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "chatcmpl-ok"})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"default": {ID: "default", Name: "Default", BaseURL: srv.URL},
		},
		routes:  []model.Route{},
		apiKeys: []model.ApiKey{{ID: "key1"}},
		settings: &model.Settings{
			Routing: model.RoutingSettings{DefaultProviderID: "default"},
		},
	}
	p := New(store, &mockService{}, func() *model.Settings { return store.settings })
	defer p.Stop()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"user-requested-model","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(seenBody, `"model":"user-requested-model"`) {
		t.Fatalf("expected upstream body to preserve request model, got %s", seenBody)
	}
}

func TestTokenStatsRequiresAuth(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	req := httptest.NewRequest("GET", "/v1/stats/tokens", nil)
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth, got %d", rec.Code)
	}

	req = httptest.NewRequest("GET", "/v1/stats/tokens", nil)
	req.Header.Set("Authorization", "Bearer key1")
	rec = httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with auth, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestGenericOpenAI_ImagesRoute(t *testing.T) {
	var seenPath, seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"created": 1,
			"data":    []map[string]interface{}{{"url": "http://example.com/img.png"}},
		})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		routes: []model.Route{
			{
				ID: "r1", Priority: 1, Enabled: true,
				Targets: []model.RouteTarget{
					{ProviderID: "p0", ModelName: "dall-e-3", Action: model.RouteActionForward, Tier: 0},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	req := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"dall-e-3","prompt":"a cat"}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if seenPath != "/v1/images/generations" {
		t.Fatalf("expected upstream path /v1/images/generations, got %s", seenPath)
	}
	if !strings.Contains(seenBody, `"model":"dall-e-3"`) {
		t.Fatalf("expected upstream body to contain model, got %s", seenBody)
	}
}

func TestStreaming_PassThrough(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c2\"}\n\n"))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		routes: []model.Route{
			{
				ID: "r1", Priority: 1, Enabled: true,
				Targets: []model.RouteTarget{
					{ProviderID: "p0", ModelName: "gpt-4o", Action: model.RouteActionForward, Tier: 0},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "c1") || !strings.Contains(rec.Body.String(), "c2") {
		t.Fatalf("expected SSE chunks in body, got %s", rec.Body.String())
	}
	if !strings.Contains(seenBody, `"stream":true`) {
		t.Fatalf("expected upstream body to preserve stream flag, got %s", seenBody)
	}
}


package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/store"
)

type mockStore struct {
	providers map[string]*model.Provider
	rules     []model.ModelRule
	apiKeys   []model.ApiKey
	settings  *model.Settings

	mu          sync.Mutex
	statsDeltas map[string]struct {
		hit  int64
		fail int64
	}
	lastLog model.RequestLog
	hasLog  bool
}

func (m *mockStore) LastLog() (model.RequestLog, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastLog, m.hasLog
}

func (m *mockStore) ListProviders() ([]model.Provider, error) {
	var out []model.Provider
	for _, p := range m.providers {
		out = append(out, *p)
	}
	return out, nil
}

func (m *mockStore) ListModelRules() ([]model.ModelRule, error) { return m.rules, nil }

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

func (m *mockStore) InsertRequestLogsBatch(logs []model.RequestLog) error {
	if len(logs) == 0 {
		return nil
	}
	m.mu.Lock()
	m.lastLog = logs[len(logs)-1]
	m.hasLog = true
	m.mu.Unlock()
	return nil
}

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

func (m *mockStore) IncrementTargetStats(targetID string, hitDelta, failDelta int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statsDeltas == nil {
		m.statsDeltas = map[string]struct {
			hit  int64
			fail int64
		}{}
	}
	d := m.statsDeltas[targetID]
	d.hit += hitDelta
	d.fail += failDelta
	m.statsDeltas[targetID] = d
	return nil
}

func (m *mockStore) statsFor(targetID string) (int64, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statsDeltas == nil {
		return 0, 0
	}
	d := m.statsDeltas[targetID]
	return d.hit, d.fail
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
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ProviderID: "p0", ModelName: "m0", Enabled: true},
					{ProviderID: "p1", ModelName: "m1", Enabled: true},
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
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ProviderID: "p0", ModelName: "m0", Enabled: true},
					{ProviderID: "p1", ModelName: "m1", Enabled: true},
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
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ProviderID: "p0", ModelName: "m0", Enabled: true},
					{ProviderID: "p1", ModelName: "m1", Enabled: true},
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
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ProviderID: "p0", ModelName: "m0", Enabled: true},
					{ProviderID: "p1", ModelName: "m1", Enabled: true},
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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ProviderID: "p0", ModelName: "m0", Enabled: true},
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
		rules:   []model.ModelRule{},
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
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "dall-e-3", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ProviderID: "p0", ModelName: "dall-e-3", Enabled: true},
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
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "gpt-4o", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ProviderID: "p0", ModelName: "gpt-4o", Enabled: true},
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

// TestFailover_RetryBoundedSucceedsWithinBudget verifies that a target with
// maxRetries=2 is retried on CategoryRetryable errors and that a successful
// attempt within the retry budget produces hit_count=1, failure_count=(failed
// attempts), and does NOT fall through to the next candidate.
func TestFailover_RetryBoundedSucceedsWithinBudget(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			if p0Hits <= 2 {
				// First two attempts fail with 500 (CategoryRetryable).
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "transient"})
				return
			}
			// Third attempt succeeds.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":    "chatcmpl-p0",
				"model": "m0",
				"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1},
			})
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
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 2, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-p0") {
		t.Fatalf("expected response from P0, got %s", rec.Body.String())
	}
	if p0Hits != 3 {
		t.Fatalf("expected P0 hit 3 times (2 fails + 1 success), got %d", p0Hits)
	}
	if p1Hits != 0 {
		t.Fatalf("expected P1 not hit (P0 succeeded within retry budget), got %d", p1Hits)
	}
	hits, fails := store.statsFor("t0")
	if hits != 1 {
		t.Fatalf("expected t0 hit_count=1, got %d", hits)
	}
	if fails != 2 {
		t.Fatalf("expected t0 failure_count=2 (one per failed attempt), got %d", fails)
	}
	hits, fails = store.statsFor("t1")
	if hits != 0 || fails != 0 {
		t.Fatalf("expected t1 untouched, got hit=%d fail=%d", hits, fails)
	}
}

// TestFailover_RetryBoundedExhaustedFallsThrough verifies that when a target
// exhausts its retry budget on CategoryRetryable errors, the proxy falls
// through to the next candidate AND records failure_count for ALL attempts
// (no hit_count increment).
func TestFailover_RetryBoundedExhaustedFallsThrough(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "p0 always fails"})
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "chatcmpl-p1",
			"model": "m1",
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1"},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 2, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-p1") {
		t.Fatalf("expected response from P1 after P0 exhaustion, got %s", rec.Body.String())
	}
	// MaxRetries=2 → 1 initial + 2 retries = 3 attempts on P0.
	if p0Hits != 3 {
		t.Fatalf("expected P0 hit 3 times (1 + maxRetries), got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after P0 exhaustion, got %d", p1Hits)
	}
	hits, fails := store.statsFor("t0")
	if hits != 0 {
		t.Fatalf("expected t0 hit_count=0 (all attempts failed), got %d", hits)
	}
	if fails != 3 {
		t.Fatalf("expected t0 failure_count=3 (one per attempt), got %d", fails)
	}
	hits, fails = store.statsFor("t1")
	if hits != 1 {
		t.Fatalf("expected t1 hit_count=1, got %d", hits)
	}
	if fails != 0 {
		t.Fatalf("expected t1 failure_count=0, got %d", fails)
	}
}

// TestStreaming_CapturesTTFTAndStatus verifies the core streaming logging fix:
// StatusCode must come from the upstream HTTP response (200) not from
// ww.Status() which could be 0, and FirstTokenMs/IsStream must be set.
func TestStreaming_CapturesTTFTAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Delay the first chunk so TTFT is non-trivial and measurable.
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "gpt-4o", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "gpt-4o", Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Read the full response so the stream completes cleanly.
	_, _ = io.ReadAll(resp.Body)

	// Poll for the log entry to be flushed before stopping the writer.
	deadline, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for {
		log, ok := store.LastLog()
		if ok && log.StatusCode != 0 {
			break
		}
		select {
		case <-deadline.Done():
			log, _ := store.LastLog()
			t.Fatalf("timed out waiting for log entry; status=%d", log.StatusCode)
		case <-time.After(50 * time.Millisecond):
		}
	}

	log, _ := store.LastLog()
	if log.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d (err=%q)", log.StatusCode, log.Error)
	}
	if !log.IsStream {
		t.Fatalf("expected IsStream=true")
	}
	if log.FirstTokenMs < 20 {
		t.Fatalf("expected TTFT >= 20ms (30ms upstream sleep), got %d", log.FirstTokenMs)
	}
	if log.ProviderID != "p0" || log.Model != "gpt-4o" {
		t.Fatalf("expected provider/model in log, got provider=%q model=%q", log.ProviderID, log.Model)
	}
}

// TestStreaming_ClientDisconnect_BreakerNotTripped verifies that a client
// disconnecting during a stream does not trip the provider circuit breaker.
func TestStreaming_ClientDisconnect_BreakerNotTripped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"c2\"}\n\n"))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "gpt-4o", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "gpt-4o", Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST",
		proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 16)
	_, _ = resp.Body.Read(buf) // read at least one chunk
	cancel()                   // disconnect

	time.Sleep(500 * time.Millisecond)
	p.Stop() // drain the async writer

	if !p.breakerFor("p0").WouldAllow() {
		log, _ := store.LastLog()
		t.Fatalf("circuit breaker tripped after client disconnect — provider should not be penalized. log status=%d err=%q",
			log.StatusCode, log.Error)
	}
}

// TestIsClientDisconnect verifies the error classifier that guards the circuit
// breaker from client-side failures.
func TestIsClientDisconnect(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, true},
		{"context deadline", context.DeadlineExceeded, false},
		{"write broken pipe", &net.OpError{Op: "write", Err: os.NewSyscallError("write", syscall.EPIPE)}, true},
		{"read connection refused", &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNREFUSED)}, false},
		{"plain error", io.ErrUnexpectedEOF, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClientDisconnect(tt.err); got != tt.want {
				t.Errorf("isClientDisconnect(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestStreaming_RetriesOnRetryable5xxBeforeFirstByte verifies the core fix
// for the bug described in the proxy changelog: a streaming request to a
// candidate that returns a retryable 5xx must NOT commit any bytes to
// the client (no 500 leaked). Instead the proxy must fall through to the
// next candidate and stream from there. The chain should record the
// failed attempt and the successful one.
func TestStreaming_RetriesOnRetryable5xxBeforeFirstByte(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"transient"}`))
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1"},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 (failover from p0 500 to p1 200), got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected SSE chunks from P1, got %q", body)
	}

	if p0Hits != 1 {
		t.Fatalf("expected P0 hit once, got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after failover, got %d", p1Hits)
	}

	// Stats: P0 should show one failure, P1 should show one hit.
	h0, f0 := store.statsFor("t0")
	if f0 != 1 || h0 != 0 {
		t.Fatalf("expected t0 hit=0 fail=1, got hit=%d fail=%d", h0, f0)
	}
	h1, f1 := store.statsFor("t1")
	if h1 != 1 || f1 != 0 {
		t.Fatalf("expected t1 hit=1 fail=0, got hit=%d fail=%d", h1, f1)
	}
}

// TestStreaming_RetriesPerTargetBeforeFailover verifies the per-target
// retry budget: a candidate with MaxRetries=2 that returns 500 on every
// attempt is retried, then the proxy falls through to the next candidate
// which succeeds. No bytes should reach the client from the failing
// target.
func TestStreaming_RetriesPerTargetBeforeFailover(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"transient"}`))
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0"},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1"},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 2, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	_, _ = io.ReadAll(resp.Body)

	// 1 initial + 2 retries = 3 attempts on P0, then P1 succeeds.
	if p0Hits != 3 {
		t.Fatalf("expected P0 hit 3 times (1 + MaxRetries), got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after P0 exhausted, got %d", p1Hits)
	}
	h0, f0 := store.statsFor("t0")
	if h0 != 0 || f0 != 3 {
		t.Fatalf("expected t0 hit=0 fail=3, got hit=%d fail=%d", h0, f0)
	}
	h1, f1 := store.statsFor("t1")
	if h1 != 1 || f1 != 0 {
		t.Fatalf("expected t1 hit=1 fail=0, got hit=%d fail=%d", h1, f1)
	}
}

// TestStreaming_FailoverOnNetworkError verifies that a candidate whose
// upstream is unreachable (connection refused) triggers failover to the
// next candidate before any bytes are sent to the client.
func TestStreaming_FailoverOnNetworkError(t *testing.T) {
	// Start and immediately close a server so its listener port becomes
	// available for "connection refused" on the next dial. Using
	// httptest.NewUnstartedServer + Close gives us a guaranteed-closed
	// listener with a real port number.
	closedSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedAddr := closedSrv.Listener.Addr().String()
	closedSrv.Close()

	// Use the closed address as P0's base URL — every request will fail
	// with "connection refused", a retryable transport error.
	var p1Hits int
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p1Hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
	}))
	defer okSrv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: "http://" + closedAddr},
			"p1": {ID: "p1", Name: "P1", BaseURL: okSrv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after network-error failover, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected SSE chunks from P1, got %q", body)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after network-error failover, got %d", p1Hits)
	}
}

// TestStreaming_FailoverOnFirstByteTimeout verifies the per-target
// first-byte timeout: a candidate that accepts the connection but never
// sends the first byte should not block the streaming request until the
// client times out. The proxy's per-candidate first-byte timeout (set
// via ModelRuleTarget.TimeoutMs) must fire, the attempt must be
// classified as retryable, and the request must fail over to the next
// candidate.
func TestStreaming_FailoverOnFirstByteTimeout(t *testing.T) {
	// hangCh lets the hang-server release the blocked goroutine when
	// the test finishes, otherwise the test leaks a goroutine.
	// IMPORTANT: declare this defer AFTER hangSrv.Close so it runs
	// FIRST (LIFO). Otherwise hangSrv.Close blocks waiting for the
	// handler, which is blocked on hangCh, and the test deadlocks.
	hangCh := make(chan struct{})
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hangCh:
		case <-r.Context().Done():
		}
	}))
	defer hangSrv.Close()
	defer close(hangCh)

	var p1Hits int
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p1Hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
	}))
	defer okSrv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: hangSrv.URL},
			"p1": {ID: "p1", Name: "P1", BaseURL: okSrv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					// Per-target first-byte timeout: 200ms.
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, TimeoutMs: 200, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after first-byte-timeout failover, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected SSE chunks from P1, got %q", body)
	}
	elapsed := time.Since(start)
	// The whole request should complete well under the default 60s
	// first-byte timeout — fail fast is the whole point.
	if elapsed > 5*time.Second {
		t.Fatalf("request took %v; first-byte-timeout failover should fail fast", elapsed)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after first-byte-timeout failover, got %d", p1Hits)
	}
}

// TestStreaming_TruePassThrough verifies the Phase 3 core: chunks
// arrive at the client incrementally (not all at once after the body
// is fully buffered), and TTFT is meaningfully smaller than total
// latency. Three chunks are emitted ~50ms apart; the client must
// receive the LAST chunk at least ~80ms after the FIRST chunk, and
// the log must record TTFT < total stream time. The proxy wraps the
// ResponseWriter so SetReadDeadline isn't available, so we use
// timing-based verification instead.
func TestStreaming_TruePassThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// First chunk: TTFT is recorded at this point.
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"id\":\"c2\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "gpt-4o", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "gpt-4o", Enabled: true},
				},
			},
		},
		apiKeys:  []model.ApiKey{{ID: "key1"}},
		settings: &model.Settings{},
	}
	p := New(store, &mockService{}, func() *model.Settings { return store.settings })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Read incrementally using small buffers to force multiple Read
	// calls. Record the wall-clock time at each read; if the proxy
	// were buffering the full body before forwarding, the first read
	// would have all chunks and subsequent reads would return
	// (0, io.EOF) immediately.
	type readResult struct {
		data []byte
		at   time.Duration
	}
	results := []readResult{}
	readBuf := make([]byte, 64) // small buffer to force multiple Reads
	for {
		n, rerr := resp.Body.Read(readBuf)
		if n > 0 {
			results = append(results, readResult{data: append([]byte(nil), readBuf[:n]...), at: time.Since(start)})
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			t.Fatalf("read failed mid-stream: %v", rerr)
		}
	}

	// Combine all chunks and verify content.
	combined := ""
	for _, r := range results {
		combined += string(r.data)
	}
	if !strings.Contains(combined, `"id":"c1"`) {
		t.Fatalf("expected c1 chunk, got %q", combined)
	}
	if !strings.Contains(combined, `"id":"c2"`) {
		t.Fatalf("expected c2 chunk, got %q", combined)
	}
	if !strings.Contains(combined, "[DONE]") {
		t.Fatalf("expected [DONE] marker, got %q", combined)
	}
	// Pass-through evidence: the last chunk must arrive well after
	// the first chunk. With 50ms sleeps between writes, the span
	// between first and last should be >= 80ms (50ms + 50ms,
	// minus jitter). If the proxy buffers the full body, the first
	// read contains all chunks and the span is ~0ms.
	if len(results) < 2 {
		t.Fatalf("expected multiple incremental reads, got %d", len(results))
	}
	span := results[len(results)-1].at - results[0].at
	if span < 80*time.Millisecond {
		t.Fatalf("expected chunk span >= 80ms (proves pass-through, not full-buffer), got %v (reads=%d)",
			span, len(results))
	}

	// Wait for the log to be flushed.
	deadline, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for {
		log, ok := store.LastLog()
		if ok && log.StatusCode != 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for log entry")
		case <-time.After(50 * time.Millisecond):
		}
	}
	log, _ := store.LastLog()
	if !log.IsStream {
		t.Fatalf("expected IsStream=true")
	}
	// The success chain entry's FirstTokenMs should be < total LatencyMs.
	if len(log.Chain) == 0 {
		t.Fatalf("expected at least one chain entry")
	}
	last := log.Chain[len(log.Chain)-1]
	if last.Status != "success" {
		t.Fatalf("expected last chain status=success, got %q", last.Status)
	}
	if last.LatencyMs < 100 {
		t.Fatalf("expected success chain LatencyMs >= 100ms (3 chunks at 50ms intervals), got %d", last.LatencyMs)
	}
	// Top-level FirstTokenMs should equal the success chain
	// FirstTokenMs when there are no prior failed attempts.
	if log.FirstTokenMs != last.FirstTokenMs {
		t.Fatalf("top-level FirstTokenMs (%d) should equal success chain FirstTokenMs (%d) when no prior failures",
			log.FirstTokenMs, last.FirstTokenMs)
	}
}

// TestStreaming_FirstByteTimeoutFailover verifies that a target with a
// per-target TimeoutMs that never sends its first byte causes the
// request to fail over to the next target, and the chain records the
// failed attempt.
func TestStreaming_FirstByteTimeoutFailover(t *testing.T) {
	// hangCh lets the hang-server release the blocked goroutine when
	// the test finishes, otherwise the test leaks a goroutine.
	hangCh := make(chan struct{})
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the test releases us. This makes the
		// upstream never send the first byte, so the per-target
		// first-byte timeout fires.
		select {
		case <-hangCh:
		case <-r.Context().Done():
		}
	}))
	defer hangSrv.Close()
	defer close(hangCh)

	var p1Hits int
	p1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p1Hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Small sleep before first byte so TTFT is non-trivial.
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer p1Srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: hangSrv.URL},
			"p1": {ID: "p1", Name: "P1", BaseURL: p1Srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					// Per-target first-byte timeout: 200ms.
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, TimeoutMs: 200, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after first-byte-timeout failover, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected SSE chunks from P1, got %q", body)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once, got %d", p1Hits)
	}

	// Wait for log.
	deadline, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for {
		log, ok := store.LastLog()
		if ok && log.StatusCode != 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for log entry")
		case <-time.After(50 * time.Millisecond):
		}
	}
	log, _ := store.LastLog()
	if len(log.Chain) != 2 {
		t.Fatalf("expected 2 chain entries (p0 failure + p1 success), got %d", len(log.Chain))
	}
	// First entry: p0 retryable (first-byte timeout).
	if log.Chain[0].ProviderID != "p0" {
		t.Fatalf("expected chain[0] provider=p0, got %q", log.Chain[0].ProviderID)
	}
	if log.Chain[0].Status != "retryable" {
		t.Fatalf("expected chain[0] status=retryable, got %q", log.Chain[0].Status)
	}
	// Second entry: p1 success.
	if log.Chain[1].ProviderID != "p1" {
		t.Fatalf("expected chain[1] provider=p1, got %q", log.Chain[1].ProviderID)
	}
	if log.Chain[1].Status != "success" {
		t.Fatalf("expected chain[1] status=success, got %q", log.Chain[1].Status)
	}
	// Top-level FirstTokenMs ≈ p0 latency + p1 first byte. The p0
	// failure latency is bounded by the 200ms first-byte timeout
	// (plus a small tolerance); p1 first byte is ~20ms after the
	// request hits P1. We allow ±150ms slack.
	expectedLow := 200 + 0
	expectedHigh := 200 + 250
	if log.FirstTokenMs < expectedLow || log.FirstTokenMs > expectedHigh {
		t.Fatalf("expected top-level FirstTokenMs in [%d, %d] (p0 ~200ms + p1 ~20ms), got %d",
			expectedLow, expectedHigh, log.FirstTokenMs)
	}
}

// TestStreaming_CumulativeLatency verifies that 2 failing candidates +
// 1 succeeding candidate produce a top-level FirstTokenMs that equals
// the sum of failed chain latencies + the success chain FirstTokenMs.
func TestStreaming_CumulativeLatency(t *testing.T) {
	failHandler := func(w http.ResponseWriter, r *http.Request) {
		// Each failing candidate takes ~100ms then returns 500.
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"transient"}`))
	}
	p0Srv := httptest.NewServer(http.HandlerFunc(failHandler))
	defer p0Srv.Close()
	p1Srv := httptest.NewServer(http.HandlerFunc(failHandler))
	defer p1Srv.Close()

	var p2Hits int
	p2Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p2Hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// First byte at ~50ms after the request hits P2.
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(100 * time.Millisecond) // simulate generation
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer p2Srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: p0Srv.URL},
			"p1": {ID: "p1", Name: "P1", BaseURL: p1Srv.URL},
			"p2": {ID: "p2", Name: "P2", BaseURL: p2Srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
					{ID: "t2", ProviderID: "p2", ModelName: "m2", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 after cumulative-failure failover, got %d: %s", resp.StatusCode, body)
	}
	_, _ = io.ReadAll(resp.Body)
	if p2Hits != 1 {
		t.Fatalf("expected P2 hit once, got %d", p2Hits)
	}

	// Wait for log.
	deadline, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for {
		log, ok := store.LastLog()
		if ok && log.StatusCode != 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for log entry")
		case <-time.After(50 * time.Millisecond):
		}
	}
	log, _ := store.LastLog()
	if len(log.Chain) != 3 {
		t.Fatalf("expected 3 chain entries (p0 + p1 failures + p2 success), got %d", len(log.Chain))
	}
	if log.Chain[0].ProviderID != "p0" || log.Chain[0].Status != "retryable" {
		t.Fatalf("expected chain[0] = p0/retryable, got %s/%s", log.Chain[0].ProviderID, log.Chain[0].Status)
	}
	if log.Chain[1].ProviderID != "p1" || log.Chain[1].Status != "retryable" {
		t.Fatalf("expected chain[1] = p1/retryable, got %s/%s", log.Chain[1].ProviderID, log.Chain[1].Status)
	}
	if log.Chain[2].ProviderID != "p2" || log.Chain[2].Status != "success" {
		t.Fatalf("expected chain[2] = p2/success, got %s/%s", log.Chain[2].ProviderID, log.Chain[2].Status)
	}
	// Success chain entry FirstTokenMs ≈ 50ms (P2 sleeps 50ms before
	// first byte). Allow 20-200ms tolerance for CI noise.
	if log.Chain[2].FirstTokenMs < 20 || log.Chain[2].FirstTokenMs > 200 {
		t.Fatalf("expected success chain FirstTokenMs in [20, 200]ms, got %d", log.Chain[2].FirstTokenMs)
	}
	// Top-level FirstTokenMs = Σ failed chain latencies + success
	// chain FirstTokenMs. Each failed candidate's latency is ~100ms,
	// so we expect roughly 100 + 100 + 50 = 250ms. Allow ±100ms
	// tolerance.
	expected := log.Chain[0].LatencyMs + log.Chain[1].LatencyMs + log.Chain[2].FirstTokenMs
	if log.FirstTokenMs < expected-50 || log.FirstTokenMs > expected+100 {
		t.Fatalf("top-level FirstTokenMs = %d, but expected ≈ %d (Σ=%d + success_ttft=%d)",
			log.FirstTokenMs, expected, log.Chain[0].LatencyMs+log.Chain[1].LatencyMs, log.Chain[2].FirstTokenMs)
	}
}

// TestStreaming_UsageParsedFromStream verifies that SSE usage fields
// are extracted from the stream in real time (no buffered body) and
// stored on the RequestLog. Anthropic dialect is used: input_tokens
// in the message_start event, output_tokens + cache_read_input_tokens
// in the message_delta event.
func TestStreaming_UsageParsedFromStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Anthropic-style stream with usage in message_start and
		// message_delta. Last seen wins, so the final usage values
		// are what the proxy stores.
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":50}}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hello\"}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":50,\"cache_read_input_tokens\":200}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	_, _ = io.ReadAll(resp.Body)

	// Wait for log.
	deadline, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for {
		log, ok := store.LastLog()
		if ok && log.StatusCode != 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for log entry")
		case <-time.After(50 * time.Millisecond):
		}
	}
	log, _ := store.LastLog()
	// Last-seen-wins: input_tokens=100 (only in message_start),
	// output_tokens=50 (only in message_delta), cache_read=200
	// (overwritten from 50 → 200 in message_delta).
	if log.InputTokens != 100 {
		t.Fatalf("expected InputTokens=100, got %d", log.InputTokens)
	}
	if log.OutputTokens != 50 {
		t.Fatalf("expected OutputTokens=50, got %d", log.OutputTokens)
	}
	if log.CacheHit != 200 {
		t.Fatalf("expected CacheHit=200, got %d", log.CacheHit)
	}
}

// TestStreaming_MidStreamFailureBreaker verifies that when the upstream
// closes the body before [DONE] (mid-stream failure), the breaker is
// penalized (Record(false)) and the partial chunk already sent to the
// client is preserved.
func TestStreaming_MidStreamFailureBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Send one chunk, then abruptly close (no [DONE] marker).
		// Hijack to force a connection close so the client sees an
		// error rather than a clean EOF.
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Give the client time to receive the first chunk, then
		// close the connection abruptly.
		time.Sleep(50 * time.Millisecond)
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
			}
		}
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	breaker := p.breakerFor("p0")
	beforeSuccess := breaker.consecutiveFailures

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	// Status may be 200 (headers already sent) or the connection may
	// be reset before the response status is observed. Either way,
	// the first chunk must have been forwarded to the client.
	if resp.StatusCode != http.StatusOK {
		t.Logf("non-200 status (acceptable for connection reset): %d", resp.StatusCode)
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Logf("read error (expected for abrupt close): %v", readErr)
	}
	// Partial chunk must be present in whatever we did receive.
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected partial chunk in body, got %q", string(body))
	}

	// Wait a moment for the proxy to observe the close and penalize
	// the breaker, then drain the async writer.
	time.Sleep(200 * time.Millisecond)
	p.Stop() // drain async writer

	// The breaker should have recorded a failure (since the stream
	// ended without [DONE]). Use the exported accessors.
	afterState := breaker.CurrentState()
	afterSuccess := breaker.consecutiveFailures
	if afterSuccess <= beforeSuccess {
		t.Fatalf("expected breaker consecutiveFailures to increase after mid-stream failure (before=%d, after=%d, state=%v)",
			beforeSuccess, afterSuccess, afterState)
	}
	// Provider health should also reflect the error.
	prov, _ := store.GetProvider("p0")
	if prov.Status != model.ProviderStatusError {
		t.Fatalf("expected provider status=error after mid-stream failure, got %q (err=%q)",
			prov.Status, prov.ErrorMessage)
	}
}

// TestStreaming_LongBodyNotKilledByFirstByteTimeout is a regression test
// for the BLOCKING bug found in Phase 3: an earlier draft used
// context.WithTimeout on the request, which causes Go's http.Client to
// cancel body reads as soon as the deadline expires. The correct
// approach is to use Transport.ResponseHeaderTimeout, which only
// bounds headers arrival.
//
// Setup:
//   - Target TimeoutMs: 300 (300ms first-byte budget).
//   - Upstream: send headers + first chunk IMMEDIATELY, then sleep
//     800ms (well beyond the 300ms timeout), then send the remaining
//     chunks + [DONE].
//
// Expectations (would fail with the buggy context-based approach):
//   - Client receives ALL chunks, including those after the 800ms sleep.
//   - Stream is NOT truncated.
//   - Status is 200; chain has 1 success entry.
//   - Breaker is NOT penalized (provider health = Connected).
//   - Total stream latency > 800ms (proves body read was not killed).
func TestStreaming_LongBodyNotKilledByFirstByteTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// First chunk arrives immediately. TTFT is captured here;
		// well within the 300ms first-byte budget.
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Sleep 800ms — well beyond the 300ms first-byte timeout.
		// With the buggy context-based approach, this would cause
		// the body read to be killed mid-stream. With the
		// ResponseHeaderTimeout fix, body reads are unbounded
		// once headers have arrived.
		time.Sleep(800 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"id\":\"c2\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					// 300ms first-byte timeout. Headers + first
					// chunk arrive immediately, but body streaming
					// continues for 800ms+. The body must NOT be
					// killed by the 300ms deadline.
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, TimeoutMs: 300, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	elapsed := time.Since(start)
	// ALL chunks must be present. With the buggy code, only c1
	// would arrive (or nothing at all) and c2 + [DONE] would be
	// truncated by the deadline.
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected c1 chunk, got %q", body)
	}
	if !strings.Contains(string(body), `"id":"c2"`) {
		t.Fatalf("expected c2 chunk (proves body was not killed by first-byte timeout), got %q", body)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("expected [DONE] marker, got %q", body)
	}
	// Total stream latency must exceed 800ms (the upstream sleep).
	// If the body was killed at 300ms, elapsed would be ~300ms.
	if elapsed < 700*time.Millisecond {
		t.Fatalf("expected total latency >= 700ms (proves body was not killed at 300ms), got %v", elapsed)
	}

	// Wait for the log to be flushed.
	deadline, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for {
		log, ok := store.LastLog()
		if ok && log.StatusCode != 0 {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for log entry")
		case <-time.After(50 * time.Millisecond):
		}
	}
	log, _ := store.LastLog()
	// Chain should have exactly one success entry; no failures
	// recorded.
	if len(log.Chain) != 1 {
		t.Fatalf("expected 1 chain entry (success), got %d: %+v", len(log.Chain), log.Chain)
	}
	if log.Chain[0].Status != "success" {
		t.Fatalf("expected chain status=success, got %q", log.Chain[0].Status)
	}

	// Provider health must NOT be penalized — the stream ended
	// cleanly with [DONE], so the mid-stream-failure path must not
	// fire. Use a short wait + Stop to drain the async log writer.
	time.Sleep(200 * time.Millisecond)
	p.Stop()
	prov, _ := store.GetProvider("p0")
	if prov.Status == model.ProviderStatusError {
		t.Fatalf("expected provider status != error after clean long stream, got %q (err=%q)",
			prov.Status, prov.ErrorMessage)
	}
}

// TestStreaming_ClientDisconnectDuringBodyReadNoBreakerPenalty is a
// regression test for the BLOCKING bug found in Phase 3 oracle final
// review: when the client cancels its request mid-stream (during
// resp.Body.Read), the proxy must NOT penalize the provider's
// circuit breaker. The previous code conflated upstream mid-stream
// failures with client-initiated disconnects.
//
// Setup:
//   - Upstream streams a chunk then BLOCKS indefinitely (waits on a
//     channel that the test releases only at the end, after the
//     client has disconnected).
//   - Client reads the first chunk, then cancels its request context.
//   - The proxy's resp.Body.Read then returns a context.Canceled
//     error.
//
// Expectations:
//   - Breaker is NOT penalized (provider health stays Connected, not
//     Error).
//   - The chain entry for the attempt has Status == "client_abort".
func TestStreaming_ClientDisconnectDuringBodyReadNoBreakerPenalty(t *testing.T) {
	// hangCh lets the upstream release the blocked goroutine when
	// the test finishes, otherwise the test leaks a goroutine.
	// IMPORTANT: declare this defer AFTER hangSrv.Close so it runs
	// FIRST (LIFO). Otherwise hangSrv.Close blocks waiting for the
	// handler, which is blocked on hangCh, and the test deadlocks.
	hangCh := make(chan struct{})
	var upstreamHits int
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// First chunk: TTFT recorded here.
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Block until the test releases us. This means the
		// upstream will NOT send [DONE] (or close the body) before
		// the client disconnects. The proxy's body Read will then
		// return a context.Canceled error when the client cancels.
		select {
		case <-hangCh:
		case <-r.Context().Done():
		}
	}))
	defer hangSrv.Close()
	defer close(hangCh)

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: hangSrv.URL},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()

	req, _ := http.NewRequestWithContext(clientCtx, "POST",
		proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Read the first chunk to confirm the stream is in progress.
	readBuf := make([]byte, 256)
	n, rerr := resp.Body.Read(readBuf)
	if rerr != nil && rerr != io.EOF {
		t.Fatalf("first read failed: %v", rerr)
	}
	if n == 0 {
		t.Fatalf("first read returned 0 bytes")
	}
	if !strings.Contains(string(readBuf[:n]), `"id":"c1"`) {
		t.Fatalf("expected c1 chunk, got %q", string(readBuf[:n]))
	}

	// Disconnect the client. This cancels clientCtx, which
	// propagates into the proxy's request context, which the
	// http.Transport honors by closing the upstream connection. The
	// proxy's resp.Body.Read will then return an error wrapping
	// context.Canceled.
	clientCancel()
	_ = resp.Body.Close()

	// Give the proxy time to observe the disconnect, then drain
	// the async log writer.
	time.Sleep(300 * time.Millisecond)
	p.Stop()

	if upstreamHits != 1 {
		t.Fatalf("expected exactly 1 upstream hit, got %d", upstreamHits)
	}

	// Breaker must NOT be penalized. The breaker's
	// consecutiveFailures must remain 0 (initial state).
	breaker := p.breakerFor("p0")
	if breaker.consecutiveFailures != 0 {
		t.Fatalf("expected breaker consecutiveFailures=0 after client disconnect, got %d",
			breaker.consecutiveFailures)
	}
	if breaker.CurrentState() != StateClosed {
		t.Fatalf("expected breaker state=closed after client disconnect, got %v",
			breaker.CurrentState())
	}
	// Provider health must stay Connected.
	prov, _ := store.GetProvider("p0")
	if prov.Status == model.ProviderStatusError {
		t.Fatalf("expected provider status != error after client disconnect, got %q (err=%q)",
			prov.Status, prov.ErrorMessage)
	}

	// Inspect the chain entry — it should record the abort as
	// "client_abort" (not as a generic stream error), and the
	// FirstTokenMs should still be set to the legitimate TTFT.
	log, ok := store.LastLog()
	if !ok {
		t.Fatalf("expected log entry")
	}
	if len(log.Chain) != 1 {
		t.Fatalf("expected 1 chain entry, got %d: %+v", len(log.Chain), log.Chain)
	}
	if log.Chain[0].Status != "client_abort" {
		t.Fatalf("expected chain status=client_abort, got %q (err=%q)",
			log.Chain[0].Status, log.Chain[0].Error)
	}
	if !strings.Contains(strings.ToLower(log.Chain[0].Error), "client disconnect") {
		t.Fatalf("expected chain error to mention 'client disconnect', got %q", log.Chain[0].Error)
	}
}

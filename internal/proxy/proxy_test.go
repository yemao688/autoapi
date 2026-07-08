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

// TestStreaming_FailoverOnHeaderTimeout verifies the upstream header
// timeout fix: a candidate that accepts the connection but never sends
// headers should not block the streaming request until the client
// times out. The proxy's ResponseHeaderTimeout must fire, the attempt
// must be classified as retryable, and the request must fail over to
// the next candidate.
func TestStreaming_FailoverOnHeaderTimeout(t *testing.T) {
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
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, func() *model.Settings { return &model.Settings{} })
	defer p.Stop()

	// Override the proxy's transport with a short header timeout so the
	// test runs in well under a second. We rebuild the transport from
	// the existing one to preserve defaults (keep-alive, etc.).
	if t1, ok := p.transport.(*http.Transport); ok {
		clone := t1.Clone()
		clone.ResponseHeaderTimeout = 200 * time.Millisecond
		p.transport = clone
	}

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
		t.Fatalf("expected 200 after header-timeout failover, got %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected SSE chunks from P1, got %q", body)
	}
	elapsed := time.Since(start)
	// The whole request should complete well under the default 30s
	// ResponseHeaderTimeout — fail fast is the whole point of the fix.
	if elapsed > 5*time.Second {
		t.Fatalf("request took %v; header-timeout failover should fail fast", elapsed)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after header-timeout failover, got %d", p1Hits)
	}
}

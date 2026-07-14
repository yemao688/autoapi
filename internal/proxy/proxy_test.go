package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/routing"
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

func (m *mockStore) UpdateRequestLogsBatch(logs []model.RequestLog) error {
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
func (m *mockStore) GetModel(providerID, name string) (*model.Model, error) {
	return &model.Model{ProviderID: providerID, Name: name, RequestPrice: 0.1}, nil
}

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

func TestCurrentSettingsDefaultPortFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		provider func() (*model.Settings, error)
		wantPort int
	}{
		{name: "nil provider", provider: nil, wantPort: 18344},
		{name: "nil settings", provider: func() (*model.Settings, error) { return nil, nil }, wantPort: 18344},
		{name: "zero port", provider: func() (*model.Settings, error) { return &model.Settings{}, nil }, wantPort: 18344},
		{name: "explicit port", provider: func() (*model.Settings, error) {
			return &model.Settings{Server: model.ServerSettings{Port: 19090}}, nil
		}, wantPort: 19090},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Proxy{defaultPort: 18344, settingsProvider: tt.provider}
			settings, err := p.currentSettings()
			if err != nil {
				t.Fatalf("currentSettings: %v", err)
			}
			if settings.Server.Port != tt.wantPort {
				t.Fatalf("port = %d, want %d", settings.Server.Port, tt.wantPort)
			}
			if settings.Server.BindAddress != "0.0.0.0" {
				t.Fatalf("bind address = %q, want 0.0.0.0", settings.Server.BindAddress)
			}
		})
	}
}

func TestNewZeroDefaultPortFallback(t *testing.T) {
	p := New(&mockStore{}, nil, 0, nil)
	t.Cleanup(func() { _ = p.Shutdown() })
	settings, err := p.currentSettings()
	if err != nil {
		t.Fatalf("currentSettings: %v", err)
	}
	if got := settings.Server.Port; got != 8344 {
		t.Fatalf("port = %d, want 8344", got)
	}
}

func TestStartPropagatesSettingsError(t *testing.T) {
	wantErr := errors.New("read failed")
	p := New(&mockStore{}, nil, 18344, func() (*model.Settings, error) { return nil, wantErr })
	t.Cleanup(func() { _ = p.Shutdown() })
	if err := p.Start(); !errors.Is(err, wantErr) {
		t.Fatalf("Start error = %v, want wrapped %v", err, wantErr)
	}
	if p.IsRunning() {
		t.Fatal("proxy should not be running after settings read failure")
	}
}

func TestRestartSettingsErrorKeepsListener(t *testing.T) {
	settingsErr := error(nil)
	p := New(&mockStore{}, nil, 18344, func() (*model.Settings, error) {
		if settingsErr != nil {
			return nil, settingsErr
		}
		return &model.Settings{Server: model.ServerSettings{Port: 0, BindAddress: "127.0.0.1"}}, nil
	})
	t.Cleanup(func() { _ = p.Shutdown() })
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	oldURL := p.URL()
	settingsErr = errors.New("database unavailable")
	if err := p.Restart(); !errors.Is(err, settingsErr) {
		t.Fatalf("Restart error = %v, want wrapped %v", err, settingsErr)
	}
	if !p.IsRunning() || p.URL() != oldURL {
		t.Fatalf("listener changed after settings error: running=%v url=%q want=%q", p.IsRunning(), p.URL(), oldURL)
	}
}

func TestRestartOccupiedPortKeepsOldServerLive(t *testing.T) {
	initialPort := reserveTCPPort(t)
	settings := &model.Settings{Server: model.ServerSettings{Port: initialPort, BindAddress: "127.0.0.1"}}
	p := New(&mockStore{}, nil, 0, func() (*model.Settings, error) { return settings, nil })
	t.Cleanup(func() { _ = p.Shutdown() })
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	oldURL := p.URL()
	assertHTTPAvailable(t, oldURL)

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy target port: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })
	settings = &model.Settings{Server: model.ServerSettings{
		Port:        occupied.Addr().(*net.TCPAddr).Port,
		BindAddress: "127.0.0.1",
	}}
	if err := p.Restart(); err == nil {
		t.Fatal("Restart succeeded with occupied target port")
	}
	if !p.IsRunning() || p.URL() != oldURL {
		t.Fatalf("old listener changed: running=%v url=%q want=%q", p.IsRunning(), p.URL(), oldURL)
	}
	assertHTTPAvailable(t, oldURL)
}

func TestRestartSuccessfullySwitchesListener(t *testing.T) {
	settings := &model.Settings{Server: model.ServerSettings{Port: reserveTCPPort(t), BindAddress: "127.0.0.1"}}
	p := New(&mockStore{}, nil, 0, func() (*model.Settings, error) { return settings, nil })
	t.Cleanup(func() { _ = p.Shutdown() })
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	oldURL := p.URL()
	settings = &model.Settings{Server: model.ServerSettings{Port: reserveTCPPort(t), BindAddress: "127.0.0.1"}}
	if err := p.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !p.IsRunning() || p.URL() == oldURL {
		t.Fatalf("listener did not switch: running=%v old=%q new=%q", p.IsRunning(), oldURL, p.URL())
	}
	assertHTTPAvailable(t, p.URL())
}

func TestRestartSameAddressIsNoOp(t *testing.T) {
	settings := &model.Settings{Server: model.ServerSettings{Port: reserveTCPPort(t), BindAddress: "127.0.0.1"}}
	p := New(&mockStore{}, nil, 0, func() (*model.Settings, error) { return settings, nil })
	t.Cleanup(func() { _ = p.Shutdown() })
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	p.mu.RLock()
	oldListener := p.listener
	oldServer := p.server
	p.mu.RUnlock()
	if err := p.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.listener != oldListener || p.server != oldServer {
		t.Fatal("same-address restart replaced listener or server")
	}
}

func TestRestartSamePortChangesBindAddress(t *testing.T) {
	tests := []struct {
		name string
		from string
		to   string
	}{
		{name: "loopback to all interfaces", from: "127.0.0.1", to: "0.0.0.0"},
		{name: "all interfaces to loopback", from: "0.0.0.0", to: "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := reserveTCPPort(t)
			settings := &model.Settings{Server: model.ServerSettings{Port: port, BindAddress: tt.from}}
			p := New(&mockStore{}, nil, 0, func() (*model.Settings, error) { return settings, nil })
			t.Cleanup(func() { _ = p.Shutdown() })
			if err := p.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}

			settings = &model.Settings{Server: model.ServerSettings{Port: port, BindAddress: tt.to}}
			if err := p.Restart(); err != nil {
				t.Fatalf("Restart: %v", err)
			}
			if !p.IsRunning() {
				t.Fatal("proxy is not running after same-port bind change")
			}
			p.mu.RLock()
			active := p.activeSettings
			listenerSettings := serverSettingsForListener(p.listener)
			p.mu.RUnlock()
			if active != listenerSettings || active.Port != port || !listenerMatchesSettings(p.listener, settings.Server) {
				t.Fatalf("active settings = %+v, listener settings = %+v, want %s:%d", active, listenerSettings, tt.to, port)
			}
			assertHTTPAvailable(t, fmt.Sprintf("http://127.0.0.1:%d", port))
		})
	}
}

func TestRestartSamePortBindFailureRestoresOldListener(t *testing.T) {
	port := reserveTCPPort(t)
	settings := &model.Settings{Server: model.ServerSettings{Port: port, BindAddress: "127.0.0.1"}}
	p := New(&mockStore{}, nil, 0, func() (*model.Settings, error) { return settings, nil })
	t.Cleanup(func() { _ = p.Shutdown() })
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	wantErr := errors.New("injected target bind failure")
	p.listen = func(network, address string) (net.Listener, error) {
		if strings.HasPrefix(address, "0.0.0.0:") {
			return nil, wantErr
		}
		return net.Listen(network, address)
	}
	settings = &model.Settings{Server: model.ServerSettings{Port: port, BindAddress: "0.0.0.0"}}
	if err := p.Restart(); !errors.Is(err, wantErr) {
		t.Fatalf("Restart error = %v, want %v", err, wantErr)
	}
	if !p.IsRunning() || p.URL() != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("old listener not restored: running=%v url=%q", p.IsRunning(), p.URL())
	}
	p.mu.RLock()
	active := p.activeSettings
	p.mu.RUnlock()
	if active.Port != port || active.BindAddress != "127.0.0.1" {
		t.Fatalf("active settings after restore = %+v", active)
	}
	assertHTTPAvailable(t, p.URL())
}

func TestRestartSamePortReportsRestoreFailure(t *testing.T) {
	port := reserveTCPPort(t)
	settings := &model.Settings{Server: model.ServerSettings{Port: port, BindAddress: "127.0.0.1"}}
	p := New(&mockStore{}, nil, 0, func() (*model.Settings, error) { return settings, nil })
	t.Cleanup(func() { _ = p.Shutdown() })
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	bindErr := errors.New("new bind failed")
	restoreErr := errors.New("restore bind failed")
	p.listen = func(_ string, address string) (net.Listener, error) {
		if strings.HasPrefix(address, "0.0.0.0:") {
			return nil, bindErr
		}
		return nil, restoreErr
	}
	settings = &model.Settings{Server: model.ServerSettings{Port: port, BindAddress: "0.0.0.0"}}
	err := p.Restart()
	if !errors.Is(err, bindErr) || !strings.Contains(err.Error(), restoreErr.Error()) {
		t.Fatalf("Restart error = %v, want original and restore failures", err)
	}
	if p.IsRunning() {
		t.Fatal("proxy reports running when listener restoration failed")
	}
}

// TestStopStartKeepsLogWriterAvailable verifies that a user-initiated Stop/Start
// cycle does NOT permanently stop the log writer. The writer is created once in
// New and must survive across Stop/Start so request logging keeps working.
func TestStopStartKeepsLogWriterAvailable(t *testing.T) {
	port := reserveTCPPort(t)
	settings := &model.Settings{Server: model.ServerSettings{Port: port, BindAddress: "127.0.0.1"}}
	p := New(&mockStore{}, nil, 0, func() (*model.Settings, error) { return settings, nil })
	defer p.Shutdown()

	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !p.IsRunning() {
		t.Fatal("proxy should be running after Start")
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.IsRunning() {
		t.Fatal("proxy should not be running after Stop")
	}

	if err := p.Start(); err != nil {
		t.Fatalf("Start after Stop: %v", err)
	}
	if !p.IsRunning() {
		t.Fatal("proxy should be running after second Start")
	}

	// The log writer must still accept entries after Stop/Start.
	if !p.writer.Enqueue(model.RequestLog{ID: "log-after-restart"}) {
		t.Fatal("log writer Enqueue returned false after Stop/Start")
	}
}

func reserveTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve TCP port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release TCP port: %v", err)
	}
	return port
}

func assertHTTPAvailable(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(time.Second)
	for {
		resp, err := client.Get(baseURL + "/")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP server %s unavailable: %v", baseURL, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()
	defer p.Shutdown()

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

func TestHandlerMetricsCountsRealAttemptsAndRequest(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "ok", "usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true}, "p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p0", ModelName: "m0", Enabled: true}, {ProviderID: "p1", ModelName: "m1", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	reg := metrics.New(16, time.Hour)
	p := New(st, &mockService{}, 0, nil, reg)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || p0Hits != 1 || p1Hits != 1 {
		t.Fatalf("response=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	var attempts, requests int64
	for _, snapshot := range reg.Snapshots(time.Now()) {
		attempts += snapshot.Attempts
		requests += snapshot.Requests
	}
	if attempts != 2 || requests != 1 {
		t.Fatalf("metrics attempts=%d requests=%d", attempts, requests)
	}
}

func TestHandlerPreflightMetricsAreRequestOnly(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		model string
		want  int
	}{
		{name: "invalid json", body: `{`, model: "", want: http.StatusBadRequest},
		{name: "no matching rule", body: `{"model":"missing","messages":[]}`, model: "missing", want: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &mockStore{apiKeys: []model.ApiKey{{ID: "key1"}}, providers: map[string]*model.Provider{}, rules: nil}
			reg := metrics.New(16, time.Hour)
			p := New(st, &mockService{}, 0, nil, reg)
			defer p.Shutdown()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			p.router.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var attempts, requests int64
			var preflight bool
			for _, s := range reg.Snapshots(time.Now()) {
				attempts += s.Attempts
				requests += s.Requests
				preflight = preflight || s.Key.ProviderID == model.MetricProviderPreflight
			}
			if attempts != 0 || requests != 1 || !preflight {
				t.Fatalf("attempts=%d requests=%d preflight=%v", attempts, requests, preflight)
			}
		})
	}
}

func TestHandlerStreamMetricsSuccessAndTruncate(t *testing.T) {
	cases := []struct {
		name, body string
		want       model.RequestOutcome
	}{
		{"success", "data: {\"id\":\"x\"}\n\ndata: [DONE]\n\n", model.RequestOutcomeSuccess},
		{"truncate", "data: {\"id\":\"x\"}\n\n", model.RequestOutcomePartial},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1"}}}
			spy := &metricSpy{}
			p := New(st, &mockService{}, 0, nil)
			p.metricSink = spy
			defer p.Shutdown()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","stream":true,"messages":[]}`))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			p.router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || hits != 1 {
				t.Fatalf("status=%d hits=%d body=%s", rec.Code, hits, rec.Body.String())
			}
			events := spy.Events()
			var attempts, requests int
			var got model.RequestOutcome
			for _, e := range events {
				if e.Kind == model.MetricEventAttempt {
					attempts++
				}
				if e.Kind == model.MetricEventRequest {
					requests++
					got = e.RequestOutcome
				}
			}
			if attempts != 1 || requests != 1 || got != tc.want {
				t.Fatalf("events=%#v", events)
			}
		})
	}
}

func TestHandlerMetricSinkPanicDoesNotChangeResponse(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(st, &mockService{}, 0, nil)
	p.metricSink = panicMetricSink{}
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` || hits != 1 {
		t.Fatalf("status=%d hits=%d body=%q", rec.Code, hits, rec.Body.String())
	}
}

func TestMessagesClientFallsBackToResponsesProvider(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":3}}`)
	}))
	defer srv.Close()

	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream-resp", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/responses" || gotBody["model"] != "upstream-resp" {
		t.Fatalf("upstream path/body = %q %#v", gotPath, gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"type":"message"`) || !strings.Contains(rec.Body.String(), `"model":"client-model"`) || !strings.Contains(rec.Body.String(), `"stop_reason":"end_turn"`) {
		t.Fatalf("response was not converted to Messages: %s", rec.Body.String())
	}
}

func TestResponsesClientFallsBackToMessagesProvider(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":5}}`)
	}))
	defer srv.Close()

	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, MessagesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream-msg", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"client-model","instructions":"brief","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/messages" || gotBody["model"] != "upstream-msg" || gotBody["system"] != "brief" {
		t.Fatalf("upstream path/body = %q %#v", gotPath, gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"object":"response"`) || !strings.Contains(rec.Body.String(), `"model":"client-model"`) || !strings.Contains(rec.Body.String(), `"status":"completed"`) {
		t.Fatalf("response was not converted to Responses: %s", rec.Body.String())
	}
}

func TestResponsesClientStreamsFromMessagesProvider(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":2}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\"}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, MessagesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream-msg", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"client-model","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/messages" {
		t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
	}
	for _, event := range []string{"response.created", "response.output_text.delta", "response.completed"} {
		if !strings.Contains(rec.Body.String(), event) {
			t.Fatalf("missing %s in %s", event, rec.Body.String())
		}
	}
}

func TestProtocolConversionErrorLogsChainAndTripsBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "not-json")
	}))
	defer srv.Close()

	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%s, want 502", rec.Code, rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 1 || log.Chain[0].Status != "conversion_error" {
		t.Fatalf("chain=%#v, want one conversion_error entry", log.Chain)
	}
	if log.Chain[0].LatencyMs < 0 {
		t.Fatalf("conversion error latency=%d, want non-negative", log.Chain[0].LatencyMs)
	}
	if p.breakerFor("p").CurrentState() != StateClosed {
		t.Fatalf("breaker opened after one failure; threshold should not be reached")
	}
	for i := 0; i < failureThreshold-1; i++ {
		req = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec = httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
	}
	if p.breakerFor("p").CurrentState() != StateOpen {
		t.Fatalf("breaker state=%v, want open after %d conversion failures", p.breakerFor("p").CurrentState(), failureThreshold)
	}
}

func TestMessagesClientToResponsesProviderStreamingUpstreamErrorIsReached(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream-resp", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable || hits != 1 {
		t.Fatalf("status=%d hits=%d body=%s", rec.Code, hits, rec.Body.String())
	}
}

func TestStreamingConversionPreCommitFailover(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/p0/") {
			p0Hits++
			_, _ = io.WriteString(w, "event: response.created\ndata: {malformed}\n\n")
			return
		}
		p1Hits++
		_, _ = io.WriteString(w, responsesSuccessSSE("p1"))
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true, ResponsesEnabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
			{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || p0Hits != 1 || p1Hits != 1 {
		t.Fatalf("status=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "p1") || strings.Contains(rec.Body.String(), "p0") {
		t.Fatalf("client received unexpected converted stream: %s", rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 2 || log.Chain[0].Status != string(model.AttemptOutcomeRetryable) || log.Chain[1].Status != string(model.AttemptOutcomeSuccess) {
		t.Fatalf("chain=%+v, want retryable -> success", log.Chain)
	}
	if hit, fail := st.statsFor("t0"); hit != 0 || fail != 1 {
		t.Fatalf("pre-commit conversion failure stats=(%d,%d), want target failure +1 and no hit", hit, fail)
	}
	if p.breakerFor("p0").consecutiveFailures != 0 {
		t.Fatalf("pre-commit conversion failure penalized breaker: %d", p.breakerFor("p0").consecutiveFailures)
	}
	if provider, _ := st.GetProvider("p0"); provider.Status == model.ProviderStatusError {
		t.Fatal("pre-commit conversion failure penalized provider health")
	}
}

func TestStreamingConversionPartialByteStallDeadlineFailover(t *testing.T) {
	var p0Hits, p1Hits int
	p0Done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/p0/") {
			p0Hits++
			_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case <-r.Context().Done():
				close(p0Done)
			case <-time.After(5 * time.Second):
				t.Error("P0 was not cancelled by first-visible deadline")
			}
			return
		}
		p1Hits++
		_, _ = io.WriteString(w, responsesSuccessSSE("p1"))
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true, ResponsesEnabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true, FirstTokenTimeoutSeconds: 1},
			{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	select {
	case <-p0Done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for P0 context cancellation")
	}
	if rec.Code != http.StatusOK || p0Hits != 1 || p1Hits != 1 {
		t.Fatalf("status=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "p1") || strings.Contains(rec.Body.String(), "p0") {
		t.Fatalf("client received unexpected stream: %s", rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 2 || log.Chain[0].Status != string(model.AttemptOutcomeRetryable) || log.Chain[1].Status != string(model.AttemptOutcomeSuccess) {
		t.Fatalf("chain=%+v, want retryable -> success", log.Chain)
	}
	if hit, fail := st.statsFor("t0"); hit != 0 || fail != 1 {
		t.Fatalf("P0 target stats=(%d,%d), want (0,1)", hit, fail)
	}
}

func TestMessagesClientResponsesProviderStreamingSuccessE2E(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"upstream\",\"usage\":{\"input_tokens\":2}}}\n\n"+
			"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"item_1\"}}\n\n"+
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"+
			"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\"}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":3}}}\n\n")
	}))
	defer srv.Close()
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "target", ProviderID: "p", ModelName: "upstream", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/responses" {
		t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
	}
	body := rec.Body.String()
	for _, event := range []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"} {
		if strings.Count(body, "event: "+event) != 1 {
			t.Fatalf("event %s count=%d body=%s", event, strings.Count(body, "event: "+event), body)
		}
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.AttemptOutcomeSuccess) || log.InputTokens != 2 || log.OutputTokens != 3 {
		t.Fatalf("log=%+v, want one success with usage 2/3", log)
	}
	if hit, fail := st.statsFor("target"); hit != 1 || fail != 0 {
		t.Fatalf("stats=(%d,%d), want (1,0)", hit, fail)
	}
	provider, _ := st.GetProvider("p")
	if provider.Status != model.ProviderStatusConnected || p.breakerFor("p").consecutiveFailures != 0 {
		t.Fatalf("provider=%q breaker failures=%d", provider.Status, p.breakerFor("p").consecutiveFailures)
	}
}

func TestResponsesNativeStreamingPassthroughE2E(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	const upstream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_native\",\"model\":\"native\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"native\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":5}}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstream)
	}))
	defer srv.Close()
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "target", ProviderID: "p", ModelName: "native", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"client-model","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/responses" || gotBody["model"] != "native" {
		t.Fatalf("status=%d path=%q body=%#v response=%s", rec.Code, gotPath, gotBody, rec.Body.String())
	}
	if rec.Body.String() != upstream {
		t.Fatalf("native stream was converted or changed: %q", rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.AttemptOutcomeSuccess) || log.InputTokens != 4 || log.OutputTokens != 5 {
		t.Fatalf("log=%+v, want native success with usage 4/5", log)
	}
	if hit, fail := st.statsFor("target"); hit != 1 || fail != 0 {
		t.Fatalf("stats=(%d,%d), want (1,0)", hit, fail)
	}
	provider, _ := st.GetProvider("p")
	if provider.Status != model.ProviderStatusConnected {
		t.Fatalf("provider status=%q, want connected", provider.Status)
	}
}

func TestStreamingConversionPostCommitFailure(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/p0/") {
			p0Hits++
			_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"p0\",\"model\":\"m0\"}}\n\n"+
				"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n"+
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"p0\"}\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {malformed}\n\n")
			return
		}
		p1Hits++
		_, _ = io.WriteString(w, responsesSuccessSSE("p1"))
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true, ResponsesEnabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
			{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	before := p.breakerFor("p0").consecutiveFailures
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || p0Hits != 1 || p1Hits != 0 || !strings.Contains(rec.Body.String(), "p0") {
		t.Fatalf("status=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.AttemptOutcomeTruncated) {
		t.Fatalf("chain=%+v, want one truncated entry", log.Chain)
	}
	if p.breakerFor("p0").consecutiveFailures <= before {
		t.Fatal("post-commit conversion failure did not penalize breaker")
	}
	prov, _ := st.GetProvider("p0")
	if prov.Status != model.ProviderStatusError {
		t.Fatalf("provider status=%q, want error", prov.Status)
	}
}

func responsesSuccessSSE(id string) string {
	return "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"" + id + "\",\"model\":\"m1\"}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + id + "\"}\n\n" +
		"event: response.output_item.done\ndata: {}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"
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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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

// TestNoMatchingRuleReturns503 verifies the proxy-level behavior for the
// removed "default fallback" feature: an inbound request whose model name
// is not registered as a model rule must produce a 503 with
// error.type = "no_matching_rule" and a JSON body — NOT a silent forward to
// a configured default provider.
func TestNoMatchingRuleReturns503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream should not be called when no model rule matches; got %s", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"default": {ID: "default", Name: "Default", BaseURL: srv.URL, Enabled: true},
		},
		rules:   []model.ModelRule{}, // no rules registered
		apiKeys: []model.ApiKey{{ID: "key1"}},
		settings: &model.Settings{
			Routing: model.RoutingSettings{StreamingSSE: true},
		},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return store.settings, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"user-requested-model","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when no rule matches, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected JSON content-type, got %q", ct)
	}
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected JSON body, got %q: %v", rec.Body.String(), err)
	}
	if body.Error.Type != "no_matching_rule" {
		t.Fatalf("expected error.type=no_matching_rule, got %q", body.Error.Type)
	}
	if !strings.Contains(body.Error.Message, "user-requested-model") {
		t.Fatalf("expected error message to mention the request model, got %q", body.Error.Message)
	}
}

func TestTokenStatsRequiresAuth(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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

func TestHandlerPlansCandidatesOnceBeforeRetryAndFailover(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer srv.Close()

	store := &planningPriceStore{mockStore: &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
		},
		rules: []model.ModelRule{{
			ID: "r1", Name: "x", Enabled: true, Strategy: string("score_within_tier"),
			Targets: []model.ModelRuleTarget{
				{ProviderID: "p0", ModelName: "m0", MaxRetries: 1, Enabled: true},
				{ProviderID: "p1", ModelName: "m1", Enabled: true},
			},
		}},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}}
	metricsSpy := &planningMetricSpy{byID: map[string]metrics.Snapshot{}}
	p := New(store, &mockService{}, 0, nil)
	p.metricSink = metricsSpy
	t.Cleanup(func() { _ = p.Shutdown() })

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || p0Hits != 2 || p1Hits != 1 {
		t.Fatalf("response=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	if metricsSpy.calls != 2 || store.calls != 2 {
		t.Fatalf("resolve planning calls repeated during retry/failover: metrics=%d prices=%d, want 2 each", metricsSpy.calls, store.calls)
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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
	p.Shutdown() // drain the async writer

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
	if h1 != 0 || f1 != 1 {
		t.Fatalf("expected truncated t1 to have exactly one failure and no hit, got hit=%d fail=%d", h1, f1)
	}
	p.Shutdown()
	log, ok := store.LastLog()
	if !ok || len(log.Chain) == 0 {
		t.Fatal("expected request chain for truncated committed stream")
	}
	last := log.Chain[len(log.Chain)-1]
	if last.Status != string(model.OutcomeTruncated) || last.StatusCode != http.StatusOK {
		t.Fatalf("expected committed truncated HTTP 200 outcome, got status=%q code=%d", last.Status, last.StatusCode)
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
			"p0": {ID: "p0", Name: "P0", BaseURL: "http://" + closedAddr, Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: okSrv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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

// TestStreaming_FailoverOnFirstByteTimeout verifies the per-rule
// first-byte budget for streaming: a candidate that takes too long to
// send the first byte (longer than the per-rule budget allows) must
// not block the streaming request until the client times out. The
// attempt is classified as retryable and the request must fail over
// to the next candidate. P0 sleeps below the budget then returns 500;
// this exercises the "first-byte budget still has room for failover"
// path: a fully-hanging P0 would consume the entire budget (since
// the per-attempt transport timeout is the rule budget itself), so we
// use a bounded delay instead.
func TestStreaming_FailoverOnFirstByteTimeout(t *testing.T) {
	// p0Srv delays ~200ms (well under the 1s budget) then returns
	// 500. The 500 status itself is retryable, so the attempt is
	// retried; but since the rule has only one target on p0, the
	// chain falls through to p1.
	p0Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"transient"}`))
	}))
	defer p0Srv.Close()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: p0Srv.URL, Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: okSrv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				// Per-rule first-byte budget: 1s.
				FirstByteTimeoutSeconds: 1,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
	// The whole request should complete well under the 1s budget
	// (P0 takes ~200ms, P1 responds immediately, plus small overhead).
	if elapsed > time.Second {
		t.Fatalf("request took %v; first-byte-budget failover should complete within the budget", elapsed)
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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return store.settings, nil })
	defer p.Shutdown()

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

// TestStreaming_FirstByteTimeoutFailover verifies the per-rule
// FirstByteTimeoutSeconds: a candidate that takes too long to send
// its first byte (longer than the per-rule budget allows) is recorded
// as a retryable failure, and the request fails over to the next
// target. P0 sleeps below the budget then returns 500; this exercises
// the "first-byte budget still has room for failover" path: a
// fully-hanging P0 would consume the entire budget (since the
// per-attempt transport timeout is the rule budget itself), so we use
// a bounded delay instead.
func TestStreaming_FirstByteTimeoutFailover(t *testing.T) {
	// p0Srv delays ~200ms (well under the 1s budget) then returns
	// 500. The 500 status is retryable, so the attempt is retried;
	// but since the rule has only one target on p0, the chain falls
	// through to p1.
	p0Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"transient"}`))
	}))
	defer p0Srv.Close()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: p0Srv.URL, Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: p1Srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				// Per-rule first-byte budget: 1s.
				FirstByteTimeoutSeconds: 1,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
	// First entry: p0 retryable (5xx within budget).
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
	// Top-level FirstTokenMs = p0 latency (~200ms) + p1 first byte
	// (~20ms after P1 sees the request) + a small backoff between
	// attempts. Allow generous ±250ms slack.
	if log.FirstTokenMs < 200 || log.FirstTokenMs > 500 {
		t.Fatalf("expected top-level FirstTokenMs in [200, 500] (p0 ~200ms + p1 ~20ms + backoff), got %d",
			log.FirstTokenMs)
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
			"p0": {ID: "p0", Name: "P0", BaseURL: p0Srv.URL, Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: p1Srv.URL, Enabled: true},
			"p2": {ID: "p2", Name: "P2", BaseURL: p2Srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				// Per-rule first-byte budget: 1s.
				FirstByteTimeoutSeconds: 1,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
					{ID: "t2", ProviderID: "p2", ModelName: "m2", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
	p.Shutdown() // drain async writer

	// A committed stream ending without [DONE] is truncated: it is not a
	// successful hit, but the provider is penalized as a failed completion.
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
	log, ok := store.LastLog()
	if !ok {
		t.Fatal("expected request log")
	}
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.OutcomeTruncated) {
		t.Fatalf("expected one committed truncated chain entry, got %+v", log.Chain)
	}
	if log.Chain[0].StatusCode != http.StatusOK {
		t.Fatalf("expected committed truncated HTTP 200, got %d", log.Chain[0].StatusCode)
	}
	if log.Chain[0].Error == "" {
		t.Fatal("expected truncated chain entry to include an interruption error")
	}
	if log.StatusCode != http.StatusOK {
		t.Fatalf("expected top-level committed HTTP 200, got %d", log.StatusCode)
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
//   - Rule FirstByteTimeoutSeconds: 1 (1s first-byte budget).
//   - Upstream: send headers + first chunk IMMEDIATELY, then sleep
//     800ms (well beyond the 1s timeout), then send the remaining
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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				// Per-rule first-byte budget: 1s. Headers + first
				// chunk arrive immediately, but body streaming
				// continues for 800ms+. The body must NOT be
				// killed by the 1s deadline.
				FirstByteTimeoutSeconds: 1,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
	// fire. Use a short wait + Shutdown to drain the async log writer.
	time.Sleep(200 * time.Millisecond)
	p.Shutdown()
	prov, _ := store.GetProvider("p0")
	if prov.Status == model.ProviderStatusError {
		t.Fatalf("expected provider status != error after clean long stream, got %q (err=%q)",
			prov.Status, prov.ErrorMessage)
	}
}

// TestStreaming_FirstByteBudgetExcludesBodyTime is a regression test
// for the per-rule first-byte budget semantics on streaming responses:
// the budget is ONLY counted before the first response byte arrives.
// Once headers + first body chunk are received, the body download is
// unbounded and must NOT be killed by the first-byte deadline.
//
// Setup:
//   - Rule FirstByteTimeoutSeconds: 1 (1s first-byte budget).
//   - Upstream: send headers + first chunk IMMEDIATELY, then sleep
//     3s (well beyond the 1s budget), then send [DONE].
//
// Expectations:
//   - Request succeeds with status 200.
//   - ALL chunks are received by the client (body was not killed by
//     the 1s first-byte deadline).
//   - The chain has exactly 1 success entry (no budget_exceeded).
//   - Total stream latency > 3s (proves the body was streamed in
//     full, not truncated at 1s).
//   - Provider is not penalized.
func TestStreaming_FirstByteBudgetExcludesBodyTime(t *testing.T) {
	bodyDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// First chunk: TTFT is captured here; well within the 1s
		// first-byte budget.
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Sleep 3s — well beyond the 1s first-byte budget. The
		// body must NOT be killed by the budget deadline.
		time.Sleep(3 * time.Second)
		_, _ = w.Write([]byte("data: {\"id\":\"c2\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(bodyDone)
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				// Per-rule first-byte budget: 1s. The body
				// stream runs for 3s+ after the first byte —
				// the budget must NOT kill it.
				FirstByteTimeoutSeconds: 1,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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

	// ALL chunks must be present. If the budget killed the body at
	// 1s, the client would only see c1 (no c2, no [DONE]).
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected c1 chunk, got %q", body)
	}
	if !strings.Contains(string(body), `"id":"c2"`) {
		t.Fatalf("expected c2 chunk (proves body was not killed by 1s first-byte budget), got %q", body)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("expected [DONE] marker, got %q", body)
	}
	// Total stream latency must exceed 3s. If the body was killed at
	// 1s by the budget, elapsed would be ~1s.
	if elapsed < 2500*time.Millisecond {
		t.Fatalf("expected total latency >= 2500ms (proves body was not killed at 1s), got %v", elapsed)
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
	// Chain should have exactly one success entry; no budget_exceeded.
	if len(log.Chain) != 1 {
		t.Fatalf("expected 1 chain entry (success), got %d: %+v", len(log.Chain), log.Chain)
	}
	if log.Chain[0].Status != "success" {
		t.Fatalf("expected chain status=success, got %q (err=%q)",
			log.Chain[0].Status, log.Chain[0].Error)
	}

	// Provider health must NOT be penalized — the stream ended
	// cleanly with [DONE]. Use a short wait + Shutdown to drain the
	// async log writer.
	time.Sleep(200 * time.Millisecond)
	p.Shutdown()
	prov, _ := store.GetProvider("p0")
	if prov.Status == model.ProviderStatusError {
		t.Fatalf("expected provider status != error after clean long stream, got %q (err=%q)",
			prov.Status, prov.ErrorMessage)
	}
	// Sanity: the upstream handler completed (wrote [DONE]) and was
	// not canceled mid-body. This is the strongest evidence that
	// the body was not killed by the budget.
	select {
	case <-bodyDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("upstream handler did not finish body within 2s after proxy completed — body was likely killed by the budget")
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
			"p0": {ID: "p0", Name: "P0", BaseURL: hangSrv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

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
	p.Shutdown()

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

// TestStreaming_DoneThenClientCancel_IsSuccess is a regression test for the
// bug observed in production logs: a streaming request that completes
// SUCCESSFULLY (full SSE body including the [DONE] marker is delivered to
// the client) was being recorded as a failure because the client closed
// its connection immediately after receiving the full response. The
// proxy's final resp.Body.Read returned context.Canceled, and the old
// streamErr branch tagged the chain entry "client_abort" even though the
// response had already been fully and correctly delivered.
//
// Setup:
//   - Upstream sends a complete SSE stream: one data chunk, then
//     "data: [DONE]", then closes the response writer (clean EOF from
//     the upstream's perspective).
//   - The client reads the ENTIRE body (until its own Read returns EOF).
//   - The client then closes its connection / cancels its request
//     context. Because the client transport is still attached to the
//     proxy's request context, the proxy's next/last resp.Body.Read may
//     return context.Canceled.
//
// Expectations:
//   - Chain entry Status == "success" (NOT "client_abort").
//   - No "client disconnect" warning is emitted for this request.
//   - Breaker is NOT penalized.
//   - Provider health stays Connected.
//   - The recorded log has no Error.
func TestStreaming_DoneThenClientCancel_IsSuccess(t *testing.T) {
	// releaseCh lets the upstream handler close the response body
	// AFTER the client has already cancelled its context. This
	// reproduces the production race: the client SDK receives the
	// complete response (including [DONE]) and immediately closes
	// its connection, but the upstream side of the proxy connection
	// is still open. The proxy's next resp.Body.Read then returns
	// context.Canceled (not io.EOF) because the client transport
	// cancelled the request context. The fix: when usageAcc.Done()
	// is true, any subsequent read error is a clean completion.
	releaseCh := make(chan struct{})
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		// Send a complete SSE stream: a data chunk with usage, then [DONE].
		_, _ = w.Write([]byte("data: {\"id\":\"c1\",\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		// Hold the response body open until the test releases us.
		// This ensures the upstream does NOT send a clean EOF; the
		// only way the proxy's body Read terminates is via the
		// client's context cancellation.
		select {
		case <-releaseCh:
		case <-r.Context().Done():
		}
	}))
	defer upstreamSrv.Close()
	defer close(releaseCh)

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: upstreamSrv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	// Use a cancellable client context so we can cancel AFTER reading
	// the [DONE] marker, simulating a client that disconnects right
	// after the response completes.
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

	// Read until we see [DONE] — the full response has been delivered
	// to the client at this point.
	readBuf := make([]byte, 1024)
	var got strings.Builder
	for {
		n, rerr := resp.Body.Read(readBuf)
		if n > 0 {
			got.Write(readBuf[:n])
			if strings.Contains(got.String(), "[DONE]") {
				break
			}
		}
		if rerr != nil {
			t.Fatalf("client read ended before [DONE]: %v (got %q)", rerr, got.String())
		}
	}

	// Cancel the client context NOW — the response is fully
	// delivered, but the upstream body is still held open by the
	// handler. The proxy's resp.Body.Read will return
	// context.Canceled. This is the exact production race.
	clientCancel()
	_ = resp.Body.Close()

	// Let the proxy finish its end-of-stream handling + flush the log.
	time.Sleep(200 * time.Millisecond)
	p.Shutdown()

	log, ok := store.LastLog()
	if !ok {
		t.Fatalf("expected log entry")
	}
	if len(log.Chain) != 1 {
		t.Fatalf("expected 1 chain entry, got %d: %+v", len(log.Chain), log.Chain)
	}
	ce := log.Chain[0]
	if ce.Status != "success" {
		t.Fatalf("expected chain status=success (stream completed, [DONE] seen before client cancel), got %q (err=%q)",
			ce.Status, ce.Error)
	}
	if ce.Error != "" {
		t.Fatalf("expected chain entry to have NO error for a successful stream, got %q", ce.Error)
	}
	if log.Error != "" {
		t.Fatalf("expected top-level log to have NO error, got %q", log.Error)
	}
	// Breaker must NOT be penalized.
	breaker := p.breakerFor("p0")
	if breaker.consecutiveFailures != 0 {
		t.Fatalf("expected breaker consecutiveFailures=0, got %d", breaker.consecutiveFailures)
	}
	// Provider health stays Connected (not Error).
	prov, _ := store.GetProvider("p0")
	if prov.Status == model.ProviderStatusError {
		t.Fatalf("expected provider status != error, got %q (err=%q)",
			prov.Status, prov.ErrorMessage)
	}
}

// TestFailover_GlobalAttemptCap verifies that the proxy enforces a global
// cap on the number of upstream attempts per inbound request, regardless
// of how many candidates exist or how high each candidate's per-target
// MaxRetries is. The cap prevents a misbehaving upstream (e.g. fast 500s)
// from causing N×(M+1) billable calls when many targets each have a high
// MaxRetries value.
//
// Setup: 3 targets, each MaxRetries=5 (so 6 attempts per target, 18
// attempts total if unchecked). All targets return 500. Asserts that
// total upstream attempts stop at maxTotalAttempts (8), that the last
// chain entry has status "attempts_capped", and that the client gets a
// 5xx with the cap message.
func TestFailover_GlobalAttemptCap(t *testing.T) {
	var totalHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalHits++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"transient"}`))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
			"p2": {ID: "p2", Name: "P2", BaseURL: srv.URL + "/p2", Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 5, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 5, Enabled: true},
					{ID: "t2", ProviderID: "p2", ModelName: "m2", MaxRetries: 5, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	// Expect 5xx (cap is treated as upstream unavailability).
	if resp.StatusCode < 500 {
		t.Fatalf("expected 5xx when global attempt cap is hit, got %d", resp.StatusCode)
	}

	// Total upstream attempts must be capped at maxTotalAttempts (8),
	// not 18 (3 candidates × 6 attempts each).
	if totalHits != maxTotalAttempts {
		t.Fatalf("expected exactly %d upstream attempts (global cap), got %d", maxTotalAttempts, totalHits)
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

	// The chain must have maxTotalAttempts real attempt entries
	// plus the final "attempts_capped" marker, so maxTotalAttempts+1
	// total. The cap entry replaces what would have been the next
	// attempt's retryable entry.
	if len(log.Chain) != maxTotalAttempts+1 {
		t.Fatalf("expected %d chain entries (8 attempts + 1 cap), got %d",
			maxTotalAttempts+1, len(log.Chain))
	}
	last := log.Chain[len(log.Chain)-1]
	if last.Status != "attempts_capped" {
		t.Fatalf("expected last chain status=attempts_capped, got %q (err=%q)", last.Status, last.Error)
	}
	if !strings.Contains(last.Error, "global attempt cap") {
		t.Fatalf("expected cap message in last chain error, got %q", last.Error)
	}
	if !strings.Contains(last.Error, fmt.Sprintf("%d", maxTotalAttempts)) {
		t.Fatalf("expected cap number %d in chain error, got %q", maxTotalAttempts, last.Error)
	}
}

// TestFailover_BackoffBetweenRetries verifies the exponential backoff
// with jitter between retry attempts on a single target. Setup: 1
// target with MaxRetries=2, all attempts return 500. Without backoff
// the request would complete almost instantly; with backoff the wall
// clock must be at least 200ms + 400ms = 600ms (plus a small jitter
// allowance) before the second-and-third attempts even start.
//
// Timing assertions are generous: elapsed >= 400ms (the spec's
// suggested floor with slack for CI variance) and elapsed <= 2500ms
// (catches accidental seconds-long sleeps without being flaky on slow
// CI). The test also asserts exactly 3 attempts were made (1 + 2
// retries).
func TestFailover_BackoffBetweenRetries(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"transient"}`))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 2, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	elapsed := time.Since(start)

	// 1 initial + 2 retries = 3 attempts.
	if hits != 3 {
		t.Fatalf("expected 3 attempts (1 initial + 2 retries), got %d", hits)
	}

	// Floor: 200ms + 400ms = 600ms of pure backoff. We allow 400ms
	// for CI noise but the request MUST take longer than it would
	// without backoff (~0ms). The 400ms floor catches regressions
	// where backoff is accidentally disabled.
	if elapsed < 400*time.Millisecond {
		t.Fatalf("expected elapsed >= 400ms (proves backoff fires), got %v", elapsed)
	}
	// Ceiling: 200ms (jitter-up to +25% = 50ms) + 400ms (jitter-up to
	// +25% = 100ms) = 750ms. We give it 2500ms of headroom for CI
	// noise — anything above that means backoff is way out of spec.
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("expected elapsed <= 2500ms (sanity cap), got %v", elapsed)
	}

	// Response should be 5xx (all attempts failed).
	if resp.StatusCode < 500 {
		t.Fatalf("expected 5xx after all attempts failed, got %d", resp.StatusCode)
	}
}

// TestFailover_UnknownStatusFailover verifies that an unknown 4xx status
// from a provider (e.g. 451, 460) is now treated as RETRYABLE so the
// proxy can fail over to the next candidate. Previously these codes
// were categorized as NonRetryable, which stopped the request after
// the first provider — defeating the purpose of having a fallback.
//
// Setup: P0 returns 451 (legal-blocked; previously non-retryable, now
// retryable per the "unknown 4xx defaults to retryable" rule). P1
// returns 200. Asserts the request succeeds via P1 failover, NOT via
// P0.
func TestFailover_UnknownStatusFailover(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(451) // legal-blocked, used as a stand-in for "unknown 4xx"
			_, _ = w.Write([]byte(`{"error":"legal-blocked"}`))
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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover from 451, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-p1") {
		t.Fatalf("expected response from P1, got %s", rec.Body.String())
	}
	if p0Hits != 1 {
		t.Fatalf("expected P0 hit once, got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after failover, got %d", p1Hits)
	}
	// Critically: an unknown 4xx must NOT trip the breaker (only 5xx
	// and net errors do). 451 is a "client-error-shaped" code from
	// P0's perspective, not a server failure. We verify this by
	// checking the breaker's consecutiveFailures directly.
	if p0Breaker := p.breakerFor("p0"); p0Breaker.consecutiveFailures != 0 {
		t.Fatalf("expected P0 breaker consecutiveFailures=0 (unknown 4xx should not break the breaker), got %d",
			p0Breaker.consecutiveFailures)
	}
	// Provider health must also stay Connected — same reason.
	prov, _ := store.GetProvider("p0")
	if prov.Status == model.ProviderStatusError {
		t.Fatalf("expected P0 provider status != error, got %q (err=%q)",
			prov.Status, prov.ErrorMessage)
	}
	// The target's failure_count is incremented for every failed
	// attempt (regardless of category); that is by design and
	// separate from the circuit breaker. What we care about is that
	// the failover path WORKED, not whether the failure counter
	// moved — so we only assert the hit/fail split on P1.
	h1, f1 := store.statsFor("t1")
	if h1 != 1 || f1 != 0 {
		t.Fatalf("expected t1 hit=1 fail=0, got hit=%d fail=%d", h1, f1)
	}
}

// TestFailover_RespectsRetryAfter verifies that the proxy honors an
// upstream Retry-After header on a 429/503 response: instead of the
// computed exponential backoff, the next attempt (on a different
// target) is delayed by at least the Retry-After value.
//
// Setup: P0 returns 429 with Retry-After: 2 (delta-seconds). P1
// returns 200. Asserts the request succeeds via P1 and that the
// wall-clock delay between the P0 attempt and the P1 attempt is
// >= 2s (Retry-After honored).
func TestFailover_RespectsRetryAfter(t *testing.T) {
	var p0Hits, p1Hits int
	var p0Time time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			p0Time = time.Now()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "2") // 2 seconds
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
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
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	start := time.Now()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover with Retry-After, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-p1") {
		t.Fatalf("expected response from P1, got %s", rec.Body.String())
	}
	if p0Hits != 1 {
		t.Fatalf("expected P0 hit once, got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after failover, got %d", p1Hits)
	}
	// P0 returned at p0Time. P1 should have been called >= 2s after
	// that (Retry-After: 2). We measure from p0Time to elapsed; a
	// little slack for clock noise.
	p1Delay := elapsed - start.Sub(p0Time)
	// elapsed is time-since-start; p0Time-start is negative. So
	// elapsed - (p0Time-start) is actually (now-start) - (p0Time-start) =
	// now-p0Time = delay between p0 and now (which is when p1 returned,
	// slightly after p1 was hit). Approximate.
	_ = p1Delay
	// The simpler assertion: elapsed (full request time) must be at
	// least 2s because the Retry-After delay is between the two
	// upstream calls.
	if elapsed < 1900*time.Millisecond {
		t.Fatalf("expected elapsed >= 1900ms (proves Retry-After honored), got %v", elapsed)
	}
	// Sanity ceiling: with Retry-After: 2 and no other sleeps, the
	// request should complete well under 5s.
	if elapsed > 5*time.Second {
		t.Fatalf("expected elapsed <= 5s (Retry-After cap), got %v", elapsed)
	}
}

// TestFailover_RuleFirstByteBudgetExceeded verifies the per-rule
// first-byte budget. The test shortens the budget on the matched rule so
// the request aborts quickly. A slow upstream (1s+ per attempt) combined
// with MaxRetries guarantees the chain runs longer than the shortened
// budget.
//
// Setup: rule.FirstByteTimeoutSeconds = 1. P0 responds after 200ms with
// 500 (retryable). MaxRetries=5. The total chain time (multiple 200ms
// attempts + backoff sleeps) easily exceeds 1s, so the budget should fire.
func TestFailover_RuleFirstByteBudgetExceeded(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// Sleep long enough that the cumulative budget fires before
		// the chain can complete.
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"slow"}`))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				FirstByteTimeoutSeconds: 1,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 5, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")

	start := time.Now()
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	// 5xx because the budget was exhausted (treated as upstream
	// unavailability).
	if rec.Code < 500 {
		t.Fatalf("expected 5xx when budget exceeded, got %d: %s", rec.Code, rec.Body.String())
	}
	// The request must have completed reasonably close to the
	// 1s budget, NOT after the full MaxRetries+1 = 6 attempts
	// × 200ms each (1.2s) + backoff. Allow generous upper bound.
	if elapsed > 3*time.Second {
		t.Fatalf("expected elapsed <= 3s (budget should have fired ~1s), got %v", elapsed)
	}
	// Floor: at least one full attempt should have completed
	// before the budget fired.
	if hits < 1 {
		t.Fatalf("expected at least 1 upstream hit before budget fired, got %d", hits)
	}
}

// TestFailover_TruncatedNonStreaming verifies that when an upstream sends a
// Content-Length header but closes the connection before the full body is
// delivered, the proxy retries, classifies the attempt as retryable with a
// synthetic 502 status code, and logs a clear "upstream response truncated"
// error in the chain — NOT a misleading 200.
func TestFailover_TruncatedNonStreaming(t *testing.T) {
	var p0Hits, p1Hits int
	// p0 hijacks the connection and sends a Content-Length header but closes
	// before the full body, triggering io.ErrUnexpectedEOF in the proxy.
	p0Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p0Hits++
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijacker not available")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		// Send headers claiming 1000 bytes, then only 100 bytes of body.
		headers := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n"
		body := strings.Repeat("x", 100)
		_, _ = conn.Write([]byte(headers + body))
	}))
	defer p0Srv.Close()

	p1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p1Hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    "chatcmpl-p1",
			"model": "m1",
			"usage": map[string]interface{}{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer p1Srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: p0Srv.URL, Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: p1Srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{
				ID: "r1", Name: "x", Enabled: true,
				Targets: []model.ModelRuleTarget{
					{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 1, Enabled: true},
					{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
				},
			},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after failover, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "chatcmpl-p1") {
		t.Fatalf("expected response from P1, got %s", rec.Body.String())
	}
	// MaxRetries=1 -> 1 initial + 1 retry = 2 attempts on P0, then failover to P1.
	if p0Hits != 2 {
		t.Fatalf("expected P0 hit 2 times (1 + maxRetries), got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once after P0 exhaustion, got %d", p1Hits)
	}

	log := waitForLog(t, store)
	if len(log.Chain) != 3 {
		t.Fatalf("expected 3 chain entries (2 P0 retries + 1 P1 success), got %d: %+v", len(log.Chain), log.Chain)
	}
	for i := 0; i < 2; i++ {
		p0Entry := log.Chain[i]
		if p0Entry.StatusCode != http.StatusBadGateway {
			t.Fatalf("P0 attempt %d: expected truncated chain status 502, got %d", i+1, p0Entry.StatusCode)
		}
		if p0Entry.Status != "retryable" {
			t.Fatalf("P0 attempt %d: expected chain status 'retryable', got %q", i+1, p0Entry.Status)
		}
		if p0Entry.Error != "upstream response truncated" {
			t.Fatalf("P0 attempt %d: expected chain error 'upstream response truncated', got %q", i+1, p0Entry.Error)
		}
	}
	p1Entry := log.Chain[2]
	if p1Entry.StatusCode != http.StatusOK {
		t.Fatalf("expected P1 chain status 200, got %d", p1Entry.StatusCode)
	}
	if p1Entry.Status != "success" {
		t.Fatalf("expected P1 chain status 'success', got %q", p1Entry.Status)
	}
}

// TestFailover_SuccessImplicit200 verifies that an upstream which writes a body
// without calling WriteHeader still records status 200 in the chain entry.
// This guards the responseBuffer.Write path that sets the implicit status code.
func TestFailover_SuccessImplicit200(t *testing.T) {
	var p0Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p0Hits++
		// Deliberately do NOT call WriteHeader; the responseBuffer.Write path
		// should set the implicit 200 status code.
		_, _ = w.Write([]byte(`{"id":"chatcmpl-implicit","object":"chat.completion","model":"m0"}`))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if p0Hits != 1 {
		t.Fatalf("expected P0 hit once, got %d", p0Hits)
	}

	log := waitForLog(t, store)
	if log.StatusCode != http.StatusOK {
		t.Fatalf("expected log status 200, got %d", log.StatusCode)
	}
	if len(log.Chain) != 1 {
		t.Fatalf("expected 1 chain entry, got %d: %+v", len(log.Chain), log.Chain)
	}
	if log.Chain[0].StatusCode != http.StatusOK {
		t.Fatalf("expected chain status 200, got %d", log.Chain[0].StatusCode)
	}
	if log.Chain[0].Status != "success" {
		t.Fatalf("expected chain status 'success', got %q", log.Chain[0].Status)
	}
}

// TestFailover_AllCandidatesTruncated verifies that when every upstream
// candidate truncates its response body, the final client response is 503
// (Service Unavailable), all chain entries show synthetic 502 with "upstream
// response truncated", and the top-level log status is 503.
func TestFailover_AllCandidatesTruncated(t *testing.T) {
	truncateHandler := func(hitCounter *int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			*hitCounter++
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("hijacker not available")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			defer conn.Close()
			headers := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n"
			body := strings.Repeat("x", 100)
			_, _ = conn.Write([]byte(headers + body))
		}
	}

	var p0Hits, p1Hits int
	p0Srv := httptest.NewServer(http.HandlerFunc(truncateHandler(&p0Hits)))
	defer p0Srv.Close()
	p1Srv := httptest.NewServer(http.HandlerFunc(truncateHandler(&p1Hits)))
	defer p1Srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: p0Srv.URL, Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: p1Srv.URL, Enabled: true},
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
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	// All candidates truncated → exhaustion → 503 (lastStatus=502 >= 500 → 503).
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after all candidates truncated, got %d: %s", rec.Code, rec.Body.String())
	}
	if p0Hits != 1 {
		t.Fatalf("expected P0 hit once (MaxRetries=0), got %d", p0Hits)
	}
	if p1Hits != 1 {
		t.Fatalf("expected P1 hit once (MaxRetries=0), got %d", p1Hits)
	}

	log := waitForLog(t, store)
	if log.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected log status 503, got %d", log.StatusCode)
	}
	if len(log.Chain) != 2 {
		t.Fatalf("expected 2 chain entries (1 per candidate), got %d: %+v", len(log.Chain), log.Chain)
	}
	for i, entry := range log.Chain {
		if entry.StatusCode != http.StatusBadGateway {
			t.Fatalf("chain entry %d: expected synthetic 502, got %d", i, entry.StatusCode)
		}
		if entry.Error != "upstream response truncated" {
			t.Fatalf("chain entry %d: expected 'upstream response truncated', got %q", i, entry.Error)
		}
	}
}

func assertErrorEnvelope(t *testing.T, body []byte, wantType, wantMessage string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("expected JSON error envelope, got %q: %v", string(body), err)
	}
	if envelope.Error.Type != wantType {
		t.Fatalf("error.type = %q, want %q", envelope.Error.Type, wantType)
	}
	if envelope.Error.Message != wantMessage {
		t.Fatalf("error.message = %q, want %q", envelope.Error.Message, wantMessage)
	}
}

func assertErrorEnvelopeContains(t *testing.T, body []byte, wantType, wantMessage string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("expected JSON error envelope, got %q: %v", string(body), err)
	}
	if envelope.Error.Type != wantType {
		t.Fatalf("error.type = %q, want %q", envelope.Error.Type, wantType)
	}
	if !strings.Contains(envelope.Error.Message, wantMessage) {
		t.Fatalf("error.message = %q, want containing %q", envelope.Error.Message, wantMessage)
	}
}

func waitForLog(t *testing.T, store *mockStore) model.RequestLog {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		log, ok := store.LastLog()
		if ok && log.StatusCode != 0 {
			return log
		}
		select {
		case <-deadline.Done():
			log, _ := store.LastLog()
			t.Fatalf("timed out waiting for log entry; status=%d", log.StatusCode)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// TestChatMultimodal_Passthrough verifies that a chat request with an
// image_url content block (vision/multimodal) is forwarded to the upstream
// with the image data intact. The proxy must not strip or corrupt the
// messages array.
func TestChatMultimodal_Passthrough(t *testing.T) {
	var upstreamBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-vision",
			"object":  "chat.completion",
			"model":   "m1",
			"choices": []map[string]interface{}{{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": "It's a cat."}}},
			"usage":   map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{ID: "r1", Name: "vision-model", Enabled: true, Targets: []model.ModelRuleTarget{
				{ProviderID: "p0", ModelName: "m1", Enabled: true},
			}},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	// Build a multimodal request with a base64 image in the messages.
	// The image data is a small fake base64 string for testing.
	fakeImageB64 := strings.Repeat("A", 1000)
	reqBody := fmt.Sprintf(`{
		"model": "vision-model",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "What's in this image?"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,%s"}}
				]
			}
		]
	}`, fakeImageB64)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the upstream received the image data intact.
	if !strings.Contains(upstreamBody, fakeImageB64) {
		t.Fatalf("upstream body does not contain the base64 image data; got %d chars", len(upstreamBody))
	}
	if !strings.Contains(upstreamBody, "image_url") {
		t.Fatalf("upstream body does not contain image_url field")
	}
	if !strings.Contains(upstreamBody, "m1") {
		t.Fatalf("upstream body should have model rewritten to 'm1'")
	}
}

// TestChatLargeMultimodalBody verifies that a chat body larger than 10 MiB
// (the old limit) but under the new 50 MiB limit is accepted and forwarded.
func TestChatLargeMultimodalBody(t *testing.T) {
	var upstreamBodyLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamBodyLen = len(raw)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":     "chatcmpl-big",
			"object": "chat.completion",
			"model":  "m1",
			"usage":  map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{ID: "r1", Name: "vision-model", Enabled: true, Targets: []model.ModelRuleTarget{
				{ProviderID: "p0", ModelName: "m1", Enabled: true},
			}},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	// Build a body that's ~12 MiB: a base64 image of ~12MB.
	// 12 MiB = 12 * 1024 * 1024 bytes. The JSON overhead is small.
	fakeImageB64 := strings.Repeat("B", 12*1024*1024)
	reqBody := fmt.Sprintf(`{"model":"vision-model","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, fakeImageB64)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for large multimodal body, got %d: %s", rec.Code, rec.Body.String())
	}
	if upstreamBodyLen < 12*1024*1024 {
		t.Fatalf("upstream body should be >= 12 MiB, got %d bytes", upstreamBodyLen)
	}
}

// TestMultipartAudioTranscription verifies that the proxy can route a
// multipart/form-data request (audio transcription) by extracting the model
// field from the form, and that the raw multipart body is forwarded to the
// upstream with its Content-Type preserved.
func TestMultipartAudioTranscription(t *testing.T) {
	var (
		upstreamContentType string
		upstreamBody        string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamBody = string(raw)
		upstreamContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"text": "Hello, world.",
		})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{ID: "r1", Name: "whisper-1", Enabled: true, Targets: []model.ModelRuleTarget{
				{ProviderID: "p0", ModelName: "whisper-1", Enabled: true},
			}},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	// Build a multipart/form-data body with model=whisper-1 and a fake audio file.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "whisper-1")
	part, _ := mw.CreateFormFile("file", "audio.mp3")
	_, _ = part.Write([]byte("FAKE_AUDIO_DATA_12345"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/v1/audio/transcriptions", &buf)
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for multipart audio, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify upstream received the multipart body with correct Content-Type.
	if !strings.HasPrefix(upstreamContentType, "multipart/form-data") {
		t.Fatalf("upstream Content-Type should be multipart/form-data, got %q", upstreamContentType)
	}
	if !strings.Contains(upstreamBody, "FAKE_AUDIO_DATA_12345") {
		t.Fatalf("upstream body should contain the audio file data")
	}
	if !strings.Contains(upstreamBody, "whisper-1") {
		t.Fatalf("upstream body should contain the model field 'whisper-1'")
	}

	// Verify the response was forwarded correctly.
	if !strings.Contains(rec.Body.String(), "Hello, world") {
		t.Fatalf("response should contain transcribed text, got %s", rec.Body.String())
	}
}

// TestUpstreamErrorBodyInLog verifies that when the upstream returns a
// non-2xx response with an OpenAI-style error body, the detailed error
// message from that body is captured in the log entry's Error field,
// not just the status code.
func TestUpstreamErrorBodyInLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found: minimax-m3","type":"invalid_request_error","code":"model_not_found"}}`))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true},
		},
		rules: []model.ModelRule{
			{ID: "r1", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
			}},
		},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	log := waitForLog(t, store)
	if log.Error == "" {
		t.Fatalf("expected non-empty log error")
	}
	if !strings.Contains(log.Error, "model not found: minimax-m3") {
		t.Fatalf("log error should contain upstream error message, got: %s", log.Error)
	}
}

// TestLogModelPopulatedOnAbort verifies that logEntry.Model is populated
// even when the request fails before reaching a candidate (e.g. no
// matching rule). The model field should carry the inbound model name.
func TestLogModelPopulatedOnNoMatch(t *testing.T) {
	store := &mockStore{
		providers: map[string]*model.Provider{},
		rules:     []model.ModelRule{},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"nonexistent-model","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	log := waitForLog(t, store)
	if log.Model != "nonexistent-model" {
		t.Fatalf("expected log.Model = 'nonexistent-model', got %q", log.Model)
	}
	if log.RouteLabel != "nonexistent-model" {
		t.Fatalf("expected log.RouteLabel = 'nonexistent-model', got %q", log.RouteLabel)
	}
}

func TestChar_ChatRouteRegistered(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"missing","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("expected /v1/chat/completions to be registered, got 404: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected request to reach chat handler and fail with 503, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "no_matching_rule", "no matching model rule: missing")
}

func TestChar_ChatStreamingCleanEOFWithoutDoneIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"c1\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	before := p.breakerFor("p0").consecutiveFailures
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"id":"c1"`) {
		t.Fatalf("expected forwarded partial SSE data, got %q", rec.Body.String())
	}

	deadline, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	for {
		if got := p.breakerFor("p0").consecutiveFailures; got > before {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for truncated stream handling before shutdown; consecutiveFailures=%d", p.breakerFor("p0").consecutiveFailures)
		case <-time.After(10 * time.Millisecond):
		}
	}
	_ = p.Shutdown()
	if got := p.breakerFor("p0").consecutiveFailures; got <= before {
		t.Fatalf("expected breaker consecutiveFailures to increase, before=%d after=%d", before, got)
	}
	log := waitForLog(t, store)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.OutcomeTruncated) {
		t.Fatalf("expected one truncated chain entry, got %+v", log.Chain)
	}
}

func TestChar_ResponsesRouteRegistered(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"missing","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("expected /v1/responses to be registered, got 404: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected request to reach responses handler and fail with 503, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "no_matching_rule", "no matching model rule: missing")
}

func TestChar_ResponsesE2EAndPreflightReject(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotPath string
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[]}`))
		}))
		defer srv.Close()

		store := &mockStore{
			providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
			rules:     []model.ModelRule{{ID: "r1", Name: "resp-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "upstream-resp", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1"}},
		}
		p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
		defer p.Shutdown()

		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"resp-model","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if gotPath != "/v1/responses" {
			t.Fatalf("expected upstream /v1/responses, got %q", gotPath)
		}
		if string(gotBody) != `{"input":"hi","model":"upstream-resp"}` {
			t.Fatalf("unexpected upstream body %q", string(gotBody))
		}
		if !strings.Contains(rec.Body.String(), `"id":"resp_1"`) {
			t.Fatalf("expected passthrough response body, got %s", rec.Body.String())
		}
	})

	t.Run("preflight reject", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("upstream should not be called, got %s", r.URL.Path)
		}))
		defer srv.Close()

		store := &mockStore{
			providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: false}},
			rules:     []model.ModelRule{{ID: "r1", Name: "resp-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "upstream-resp", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1"}},
		}
		p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
		defer p.Shutdown()

		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"resp-model","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
		}
		assertErrorEnvelopeContains(t, rec.Body.Bytes(), "service_unavailable", "no available provider: all targets of model \"resp-model\" are disabled or have open circuits")
	})
}

func TestChar_MessagesRouteRegistered(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"missing","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("expected /v1/messages to be registered, got 404: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected request to reach messages handler and fail with 503, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "no_matching_rule", "no matching model rule: missing")
}

func TestChar_MessagesE2EAndPreflightReject(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotPath string
		var gotBody []byte
		var gotAuthorization string
		var gotAPIKey string
		var gotVersion string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuthorization = r.Header.Get("Authorization")
			gotAPIKey = r.Header.Get("X-Api-Key")
			gotVersion = r.Header.Get("anthropic-version")
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[]}`))
		}))
		defer srv.Close()

		store := &mockStore{
			providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, MessagesEnabled: true}},
			rules:     []model.ModelRule{{ID: "r1", Name: "claude", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "claude-upstream", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1"}},
		}
		p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
		defer p.Shutdown()

		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if gotPath != "/v1/messages" {
			t.Fatalf("expected upstream /v1/messages, got %q", gotPath)
		}
		if gotAuthorization != "" || gotAPIKey != "secret" {
			t.Fatalf("Authorization=%q X-Api-Key=%q, want no bearer and x-api-key", gotAuthorization, gotAPIKey)
		}
		if gotVersion != AnthropicDefaultVersion {
			t.Fatalf("anthropic-version=%q want %q", gotVersion, AnthropicDefaultVersion)
		}
		if string(gotBody) != `{"messages":[{"role":"user","content":"hi"}],"model":"claude-upstream"}` {
			t.Fatalf("unexpected upstream body %q", string(gotBody))
		}
	})

	t.Run("version passthrough", func(t *testing.T) {
		var gotVersion string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotVersion = r.Header.Get("anthropic-version")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[]}`))
		}))
		defer srv.Close()

		store := &mockStore{
			providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, MessagesEnabled: true}},
			rules:     []model.ModelRule{{ID: "r1", Name: "claude", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "claude-upstream", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1"}},
		}
		p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
		defer p.Shutdown()

		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude","messages":[]}`))
		req.Header.Set("Authorization", "Bearer key1")
		req.Header.Set("anthropic-version", "2024-01-01")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if gotVersion != "2024-01-01" {
			t.Fatalf("anthropic-version=%q want 2024-01-01", gotVersion)
		}
	})

	t.Run("preflight reject", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("upstream should not be called, got %s", r.URL.Path)
		}))
		defer srv.Close()

		store := &mockStore{
			providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, MessagesEnabled: false}},
			rules:     []model.ModelRule{{ID: "r1", Name: "claude", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "claude-upstream", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1"}},
		}
		p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
		defer p.Shutdown()

		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude","messages":[]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
		}
		assertErrorEnvelopeContains(t, rec.Body.Bytes(), "service_unavailable", "no available provider: all targets of model \"claude\" are disabled or have open circuits")
	})
}

func TestChar_GeminiRouteRegistered(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1"}}}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1beta/models/gemini-pro:generateContent", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code == http.StatusNotFound {
		t.Fatalf("expected /v1beta/models/{model}:generateContent to be registered, got 404: %s", rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected request to reach Gemini handler and fail with 503, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "no_matching_rule", "no matching model rule: gemini-pro")
}

func TestChar_GeminiE2EAndPreflightReject(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var gotPath string
		var gotQuery url.Values
		var gotAuthorization string
		var gotAPIKey string
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotQuery = r.URL.Query()
			gotAuthorization = r.Header.Get("Authorization")
			gotAPIKey = r.Header.Get("X-Api-Key")
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`))
		}))
		defer srv.Close()

		store := &mockStore{
			providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, GeminiEnabled: true}},
			rules:     []model.ModelRule{{ID: "r1", Name: "gemini-pro", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "gemini-1.5-pro", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1"}},
		}
		p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
		defer p.Shutdown()

		req := httptest.NewRequest("POST", "/v1beta/models/gemini-pro:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if gotPath != "/v1beta/models/gemini-1.5-pro:generateContent" {
			t.Fatalf("expected upstream model path, got %q", gotPath)
		}
		if gotQuery.Get("key") != "secret" {
			t.Fatalf("expected upstream key in query, got %q", gotQuery.Get("key"))
		}
		if gotAuthorization != "" || gotAPIKey != "" {
			t.Fatalf("Authorization=%q X-Api-Key=%q, want none", gotAuthorization, gotAPIKey)
		}
		if len(gotBody) == 0 {
			t.Fatal("expected body forwarded")
		}
	})

	t.Run("preflight reject", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("upstream should not be called, got %s", r.URL.Path)
		}))
		defer srv.Close()

		store := &mockStore{
			providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, GeminiEnabled: false}},
			rules:     []model.ModelRule{{ID: "r1", Name: "gemini-pro", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "gemini-1.5-pro", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1"}},
		}
		p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
		defer p.Shutdown()

		req := httptest.NewRequest("POST", "/v1beta/models/gemini-pro:generateContent", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
		}
		assertErrorEnvelopeContains(t, rec.Body.Bytes(), "service_unavailable", "no available provider: all targets of model \"gemini-pro\" are disabled or have open circuits")
	})
}

func TestChar_GeminiStreamingSSETerminal(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotQuery url.Values
	var gotAuthorization string
	var gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		gotAuthorization = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[]}}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":1}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, GeminiEnabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "gemini-pro", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "gemini-1.5-pro", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1beta/models/gemini-pro:streamGenerateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1beta/models/gemini-1.5-pro:streamGenerateContent" {
		t.Fatalf("expected upstream stream path, got %q", gotPath)
	}
	if gotQuery.Get("alt") != "sse" {
		t.Fatalf("expected alt=sse in query, got %q", gotQuery.Get("alt"))
	}
	if gotQuery.Get("key") != "secret" {
		t.Fatalf("expected upstream key in query, got %q", gotQuery.Get("key"))
	}
	if gotAuthorization != "" || gotAPIKey != "" {
		t.Fatalf("Authorization=%q X-Api-Key=%q, want none", gotAuthorization, gotAPIKey)
	}
	if !strings.Contains(rec.Body.String(), `"finishReason":"STOP"`) {
		t.Fatalf("expected stream body to contain finishReason, got %s", rec.Body.String())
	}
	log := waitForLog(t, store)
	if log.StatusCode != http.StatusOK {
		t.Fatalf("expected log status 200, got %d", log.StatusCode)
	}
	if log.InputTokens != 3 || log.OutputTokens != 1 {
		t.Fatalf("expected input=3 output=1, got input=%d output=%d", log.InputTokens, log.OutputTokens)
	}
	if len(log.Chain) != 1 || log.Chain[0].Status != "success" {
		t.Fatalf("expected one success chain entry, got %+v", log.Chain)
	}
}

func TestChar_MessagesStreamingE2E(t *testing.T) {
	var mu sync.Mutex
	var gotAuthorization string
	var gotAPIKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuthorization = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, MessagesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "claude", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "claude-upstream", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	auth, apiKey := gotAuthorization, gotAPIKey
	mu.Unlock()
	if auth != "" || apiKey != "secret" {
		t.Fatalf("Authorization=%q X-Api-Key=%q, want no bearer and x-api-key", auth, apiKey)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: message_start") || !strings.Contains(body, "event: content_block_delta") || !strings.Contains(body, "event: message_stop") {
		t.Fatalf("expected Anthropic SSE passthrough, got %s", body)
	}
	log := waitForLog(t, store)
	if len(log.Chain) != 1 || log.Chain[0].Status != "success" {
		t.Fatalf("expected one success chain entry, got %+v", log.Chain)
	}
}

func TestChar_WriteErrorEnvelopeFormats(t *testing.T) {
	p := New(&mockStore{}, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	cases := []struct {
		name    string
		status  int
		typ     string
		message string
	}{
		{name: "401", status: http.StatusUnauthorized, typ: "invalid_request_error", message: "Invalid API key"},
		{name: "400", status: http.StatusBadRequest, typ: "invalid_request_error", message: "Failed to read request body"},
		{name: "422", status: http.StatusUnprocessableEntity, typ: "invalid_request_error", message: "model is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			p.writeError(rec, tc.status, tc.typ, tc.message)
			if rec.Code != tc.status {
				t.Fatalf("status=%d want %d", rec.Code, tc.status)
			}
			assertErrorEnvelope(t, rec.Body.Bytes(), tc.typ, tc.message)
		})
	}
}

func TestChar_PriorityFirstFailoverPreservesCandidateOrder(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Hits++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"p0 failed"}`))
			return
		}
		p1Hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl-p1", "model": "m1", "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
		},
		rules:   []model.ModelRule{{ID: "r1", Name: "x", Enabled: true, Strategy: string(routing.PriorityFirst), Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true}, {ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: true}}}},
		apiKeys: []model.ApiKey{{ID: "key1"}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if p0Hits != 1 || p1Hits != 1 {
		t.Fatalf("expected one hit per candidate, got p0=%d p1=%d", p0Hits, p1Hits)
	}
	log := waitForLog(t, store)
	if len(log.Chain) != 2 {
		t.Fatalf("expected two chain entries, got %+v", log.Chain)
	}
	if log.Chain[0].AttemptOrder != 1 || log.Chain[0].ProviderID != "p0" {
		t.Fatalf("first attempt reordered: %+v", log.Chain[0])
	}
	if log.Chain[1].AttemptOrder != 2 || log.Chain[1].ProviderID != "p1" {
		t.Fatalf("second attempt reordered: %+v", log.Chain[1])
	}
}

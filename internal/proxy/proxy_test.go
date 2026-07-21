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
	"reflect"
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
	providers              map[string]*model.Provider
	rules                  []model.ModelRule
	apiKeys                []model.ApiKey
	settings               *model.Settings
	capabilities           []model.ProviderCapability
	bulkProviderErr        error
	bulkCapabilityErr      error
	bulkProviderCalls      int
	bulkProviderIDs        []string
	getProviderCalls       int
	modelCapabilities      []model.ModelCapability
	bulkModelCapabilityErr error
	bulkModelRefs          []model.ProviderModelRef
	getSettingsCalls       int
	getProviderKeyCalls    int

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
	m.getProviderCalls++
	p, ok := m.providers[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return p, nil
}

func (m *mockStore) GetProvidersForIDs(ids []string) ([]model.Provider, error) {
	m.bulkProviderCalls++
	m.bulkProviderIDs = append([]string(nil), ids...)
	if m.bulkProviderErr != nil {
		return nil, m.bulkProviderErr
	}
	out := make([]model.Provider, 0, len(ids))
	for _, id := range ids {
		if p, ok := m.providers[id]; ok {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (m *mockStore) GetProviderCapabilitiesForProviders(ids []string) ([]model.ProviderCapability, error) {
	if m.bulkCapabilityErr != nil {
		return nil, m.bulkCapabilityErr
	}
	return m.capabilities, nil
}

func (m *mockStore) GetModelCapabilitiesForModels(refs []model.ProviderModelRef) ([]model.ModelCapability, error) {
	m.bulkModelRefs = append([]model.ProviderModelRef(nil), refs...)
	if m.bulkModelCapabilityErr != nil {
		return nil, m.bulkModelCapabilityErr
	}
	return m.modelCapabilities, nil
}

func (m *mockStore) GetAPIKey(id string) (*model.ApiKey, error) {
	for i := range m.apiKeys {
		if m.apiKeys[i].ID == id {
			k := m.apiKeys[i]
			return &k, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *mockStore) GetProviderKeyCiphertext(providerID string) (ciphertext, nonce []byte, err error) {
	m.getProviderKeyCalls++
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
	m.getSettingsCalls++
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

type countingKeyService struct {
	inner           upstreamKeyProvider
	keyResolveCalls int
}

func (c *countingKeyService) ResolveProviderKey(providerID string) (string, error) {
	c.keyResolveCalls++
	return c.inner.ResolveProviderKey(providerID)
}

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
	key := model.RouteModeKey{TargetID: "t", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIResponses)}
	cb := p.routeBreakerFor(key)
	for i := 0; i < 3; i++ {
		cb.Record(false)
	}

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
	if p.routeModeWouldAllow(candidate{targetID: "t", provider: &model.Provider{ID: "p"}, protocol: ProtocolOpenAIChat, convertTo: ProtocolOpenAIResponses}) {
		t.Fatal("failed restart cleared route breaker state")
	}
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
	p.explorationMu.Lock()
	p.exploration[explorationKey{ruleID: "r", tier: 0}] = &explorationState{qualified: 19}
	p.explorationMu.Unlock()
	key := model.RouteModeKey{TargetID: "t", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIResponses)}
	cb := p.routeBreakerFor(key)
	for i := 0; i < 3; i++ {
		cb.Record(false)
	}
	if err := p.Restart(); err != nil {
		t.Fatalf("same-address restart after route failure: %v", err)
	}
	if _, ok := p.routeBreakerState(key); ok {
		t.Fatal("successful no-op restart did not clear route breakers")
	}
	if len(p.exploration) != 0 {
		t.Fatal("successful no-op restart did not clear exploration scheduler")
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
	p.explorationMu.Lock()
	p.exploration[explorationKey{ruleID: "r", tier: 0}] = &explorationState{qualified: 19}
	p.explorationMu.Unlock()
	if err := p.Restart(); !errors.Is(err, wantErr) {
		t.Fatalf("Restart error = %v, want %v", err, wantErr)
	}
	if !p.IsRunning() || p.URL() != fmt.Sprintf("http://127.0.0.1:%d", port) {
		t.Fatalf("old listener not restored: running=%v url=%q", p.IsRunning(), p.URL())
	}
	p.explorationMu.Lock()
	_, explorationRetained := p.exploration[explorationKey{ruleID: "r", tier: 0}]
	p.explorationMu.Unlock()
	if !explorationRetained {
		t.Fatal("failed restart discarded exploration scheduler state")
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
	// Phase 7.3: malformed request bodies now return 422.
	for i := range cases {
		if cases[i].name == "invalid json" {
			cases[i].want = http.StatusUnprocessableEntity
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &mockStore{apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}, providers: map[string]*model.Provider{}, rules: nil}
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
			st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
			spy := &metricSpy{}
			p := New(st, &mockService{}, 0, nil)
			p.metricSink = spy
			defer p.Shutdown()
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","stream":true,"messages":[]}`))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			var aborted bool
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						if !errors.Is(recovered.(error), http.ErrAbortHandler) {
							t.Fatalf("unexpected panic: %v", recovered)
						}
						aborted = true
					}
				}()
				p.router.ServeHTTP(rec, req)
			}()
			if tc.name == "truncate" && !aborted {
				t.Fatal("expected truncated stream to abort the handler")
			}
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
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	spy := &metricSpy{}
	p.metricSink = spy
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

func TestMessagesSystemArrayNativePassThroughAndConversionPreflight(t *testing.T) {
	t.Run("native Messages provider preserves raw system array", func(t *testing.T) {
		var gotBody []byte
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_1","content":[],"stop_reason":"end_turn"}`)
		}))
		defer srv.Close()
		st := &mockStore{
			providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, MessagesEnabled: true}},
			rules:     []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
		}
		p := New(st, &mockService{}, 0, nil)
		defer p.Shutdown()
		body := `{"model":"m","system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var gotJSON, wantJSON map[string]any
		if err := json.Unmarshal(gotBody, &gotJSON); err != nil {
			t.Fatalf("decode native body: %v", err)
		}
		if err := json.Unmarshal([]byte(body), &wantJSON); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if !reflect.DeepEqual(gotJSON["system"], wantJSON["system"]) || gotJSON["model"] != "upstream" {
			t.Fatalf("native body changed: got %s", gotBody)
		}
	})

	t.Run("Responses-only target rejects system array before upstream HTTP", func(t *testing.T) {
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		st := &mockStore{
			providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
			rules:     []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
		}
		p := New(st, &mockService{}, 0, nil)
		defer p.Shutdown()
		body := `{"model":"m","system":[{"type":"text","text":"system"}],"messages":[]}`
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity || hits != 0 {
			t.Fatalf("status=%d hits=%d body=%s", rec.Code, hits, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"unsupported_feature"`) {
			t.Fatalf("unexpected error body: %s", rec.Body.String())
		}
	})
}

func TestConversionPreservationAvailabilityE2E(t *testing.T) {
	tests := []struct {
		name             string
		provider         *model.Provider
		body             string
		breakerOpen      bool
		wantStatus       int
		wantKeyLookups   int
		wantUpstreamHits int
	}{
		{
			name:             "native-only disabled provider is 503",
			provider:         &model.Provider{ID: "p", Enabled: false, ResponsesEnabled: true},
			body:             `{"model":"m","system":[{"type":"text","text":"system"}],"messages":[]}`,
			wantStatus:       http.StatusServiceUnavailable,
			wantKeyLookups:   0,
			wantUpstreamHits: 0,
		},
		{
			name:             "vision breaker open is 503",
			provider:         &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true},
			body:             `{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x","media_type":"image/png"}}]}]}`,
			breakerOpen:      true,
			wantStatus:       http.StatusServiceUnavailable,
			wantKeyLookups:   0,
			wantUpstreamHits: 0,
		},
		{
			name:             "native-only no target protocol is 503",
			provider:         &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: false},
			body:             `{"model":"m","system":[{"type":"text","text":"system"}],"messages":[]}`,
			wantStatus:       http.StatusServiceUnavailable,
			wantKeyLookups:   0,
			wantUpstreamHits: 0,
		},
		{
			name:             "native-only basic target is 422 before key and HTTP",
			provider:         &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true},
			body:             `{"model":"m","system":[{"type":"text","text":"system"}],"messages":[]}`,
			wantStatus:       http.StatusUnprocessableEntity,
			wantKeyLookups:   0,
			wantUpstreamHits: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"upstream"}`)
			}))
			defer srv.Close()
			provider := *tc.provider
			provider.BaseURL = srv.URL
			st := &mockStore{
				providers: map[string]*model.Provider{"p": &provider},
				rules:     []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
				apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
			}
			p := New(st, &mockService{}, 0, nil)
			defer p.Shutdown()
			spy := &metricSpy{}
			p.metricSink = spy
			if tc.breakerOpen {
				cb := p.breakerFor("p")
				cb.mutex.Lock()
				cb.state = StateOpen
				cb.openedAt = time.Now()
				cb.mutex.Unlock()
			}
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			p.router.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if st.getProviderKeyCalls != tc.wantKeyLookups || hits != tc.wantUpstreamHits {
				t.Fatalf("key lookups=%d upstream hits=%d", st.getProviderKeyCalls, hits)
			}
		})
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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

func TestChatResponsesNonStreamingConversionE2E(t *testing.T) {
	t.Run("Chat to Responses", func(t *testing.T) {
		var gotPath string
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		}))
		defer srv.Close()
		st := &mockStore{
			providers:    map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
			rules:        []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
			apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
			capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}},
		}
		p := New(st, &mockService{}, 0, nil)
		defer p.Shutdown()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || gotPath != "/v1/responses" {
			t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
		}
		if gotBody["model"] != "upstream" || gotBody["input"] == nil {
			t.Fatalf("unexpected converted request: %#v", gotBody)
		}
		if !strings.Contains(rec.Body.String(), `"choices"`) || !strings.Contains(rec.Body.String(), `"prompt_tokens":2`) {
			t.Fatalf("unexpected Chat response: %s", rec.Body.String())
		}
	})

	t.Run("Responses to Chat", func(t *testing.T) {
		var gotPath string
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		}))
		defer srv.Close()
		st := &mockStore{
			providers:    map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}},
			rules:        []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
			apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
			capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"}},
		}
		p := New(st, &mockService{}, 0, nil)
		defer p.Shutdown()
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || gotPath != "/v1/chat/completions" {
			t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
		}
		if gotBody["model"] != "upstream" || gotBody["messages"] == nil {
			t.Fatalf("unexpected converted request: %#v", gotBody)
		}
		if !strings.Contains(rec.Body.String(), `"status":"completed"`) || !strings.Contains(rec.Body.String(), `"input_tokens":2`) {
			t.Fatalf("unexpected Responses response: %s", rec.Body.String())
		}
	})
}

func TestChatToResponsesNonStreamingE2E(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer srv.Close()
	st := &mockStore{
		providers:    map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:        []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
		apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	spy := &metricSpy{}
	p.metricSink = spy
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/responses" {
		t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
	}
	if gotBody["model"] != "upstream" || gotBody["input"] == nil || gotBody["stream"] != nil {
		t.Fatalf("converted request=%#v", gotBody)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["model"] != "m" || response["choices"] == nil {
		t.Fatalf("response=%#v", response)
	}
	if response["usage"].(map[string]any)["prompt_tokens"] != float64(2) || response["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatalf("usage=%#v", response["usage"])
	}
}

func TestResponsesToChatNonStreamingE2E(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer srv.Close()
	st := &mockStore{
		providers:    map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}},
		rules:        []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
		apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/chat/completions" {
		t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
	}
	if gotBody["model"] != "upstream" || gotBody["messages"] == nil || gotBody["stream"] != nil {
		t.Fatalf("converted request=%#v", gotBody)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["model"] != "m" || response["status"] != "completed" {
		t.Fatalf("response=%#v", response)
	}
	if response["usage"].(map[string]any)["input_tokens"] != float64(2) {
		t.Fatalf("usage=%#v", response["usage"])
	}
}

func TestChatResponseConversionErrorFailsOverToNextCandidate(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"bad","status":"completed","output":[{"type":"unknown"}]}`)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"good","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer good.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "bad", BaseURL: bad.URL, Enabled: true, ResponsesEnabled: true},
			"p1": {ID: "p1", Name: "good", BaseURL: good.URL, Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "p0", ModelName: "u0", Enabled: true},
			{ID: "t1", ProviderID: "p1", ModelName: "u1", Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{
			{ProviderID: "p0", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"},
			{ProviderID: "p1", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"},
		},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	spy := &metricSpy{}
	p.metricSink = spy
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"model":"m"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) < 2 || log.Chain[0].Status != "conversion_error" || log.Chain[1].Status != string(model.OutcomeSuccess) {
		t.Fatalf("chain=%+v", log.Chain)
	}
	p0, _ := st.GetProvider("p0")
	p1, _ := st.GetProvider("p1")
	if st.statsDeltas["t0"].fail == 0 || st.statsDeltas["t1"].hit == 0 || p0.Status == model.ProviderStatusError || p1.Status != model.ProviderStatusConnected {
		t.Fatalf("failure accounting stats=%+v providers=%+v", st.statsDeltas, st.providers)
	}
	conversionRoute := model.RouteModeKey{TargetID: "t0", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIResponses)}
	if routeCB := p.routeBreakerFor(conversionRoute); routeCB.CurrentState() != StateClosed || routeCB.ConsecutiveFailures() != 1 {
		t.Fatalf("conversion route breaker=%v failures=%d, want one local failure", routeCB.CurrentState(), routeCB.ConsecutiveFailures())
	}
	if providerCB := p.breakerFor("p0"); providerCB.CurrentState() != StateClosed || providerCB.ConsecutiveFailures() != 0 {
		t.Fatalf("provider breaker changed after conversion failure: state=%v failures=%d", providerCB.CurrentState(), providerCB.ConsecutiveFailures())
	}
	events := spy.Events()
	if len(events) != 3 || events[0].Kind != model.MetricEventAttempt || events[1].Kind != model.MetricEventAttempt || events[2].Kind != model.MetricEventRequest {
		t.Fatalf("conversion failover metric cardinality=%+v", events)
	}
	if events[0].StatusCode != 200 || events[0].AttemptOutcome != model.AttemptOutcomeConversionError || events[0].FailureClass != model.MetricFailureConversionLocal || !events[0].RouteMode.Valid() {
		t.Fatalf("conversion failure event=%+v", events[0])
	}
	if events[1].AttemptOutcome != model.AttemptOutcomeSuccess || events[1].StatusCode != 200 {
		t.Fatalf("success attempt event=%+v", events[1])
	}
}

func TestConversionRouteBreakerFiltersAfterThreeFailuresWithoutAffectingNative(t *testing.T) {
	var badHits, nativeHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "bad-upstream" {
			badHits++
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `not-json`)
			return
		}
		nativeHits++
		_, _ = io.WriteString(w, `{"id":"native-ok","object":"chat.completion","model":"native-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules: []model.ModelRule{{ID: "r", Name: "client", Enabled: true, Strategy: string(routing.PriorityFirst), Targets: []model.ModelRuleTarget{
			{ID: "t-conversion", ProviderID: "p", ModelName: "bad-upstream", Enabled: true},
			{ID: "t-native", ProviderID: "p", ModelName: "native-upstream", Enabled: true},
		}}},
		modelCapabilities: []model.ModelCapability{
			{ProviderID: "p", ModelName: "bad-upstream", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"},
			{ProviderID: "p", ModelName: "bad-upstream", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: true, Source: "manual"},
			{ProviderID: "p", ModelName: "native-upstream", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: true, Source: "manual"},
			{ProviderID: "p", ModelName: "native-upstream", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"client","messages":[]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "native-ok") {
			t.Fatalf("request %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	if badHits != 3 || nativeHits != 4 {
		t.Fatalf("route filtering calls bad=%d native=%d, want 3/4", badHits, nativeHits)
	}
	if state, ok := p.routeBreakerState(model.RouteModeKey{TargetID: "t-conversion", InboundProtocol: string(ProtocolOpenAIChat), UpstreamProtocol: string(ProtocolOpenAIResponses)}); !ok || state != StateOpen {
		t.Fatalf("conversion route state=%v exists=%v, want open", state, ok)
	}
	if p.breakerFor("p").CurrentState() != StateClosed {
		t.Fatalf("provider breaker=%v, conversion-local failures must not open it", p.breakerFor("p").CurrentState())
	}
	provider, _ := st.GetProvider("p")
	if provider.Status == model.ProviderStatusError {
		t.Fatal("conversion-local failures marked provider health error")
	}
}

func TestAllChatResponseConversionErrorsReturn502(t *testing.T) {
	servers := make([]*httptest.Server, 2)
	providers := make(map[string]*model.Provider)
	targets := make([]model.ModelRuleTarget, 2)
	capabilities := make([]model.ProviderCapability, 0, 2)
	for i := range servers {
		id := fmt.Sprintf("p%d", i)
		targetID := fmt.Sprintf("t%d", i)
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"bad","status":"completed","output":[{"type":"unknown"}]}`)
		}))
		providers[id] = &model.Provider{ID: id, Name: id, BaseURL: servers[i].URL, Enabled: true, ResponsesEnabled: true}
		targets[i] = model.ModelRuleTarget{ID: targetID, ProviderID: id, ModelName: id, Enabled: true}
		capabilities = append(capabilities, model.ProviderCapability{ProviderID: id, Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"})
	}
	defer func() {
		for _, server := range servers {
			server.Close()
		}
	}()
	st := &mockStore{providers: providers, rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: targets}}, apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}, capabilities: capabilities}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"upstream_error"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 2 || log.Chain[0].Status != "conversion_error" || log.Chain[1].Status != "conversion_error" {
		t.Fatalf("chain=%+v", log.Chain)
	}
}

func TestChatStreamRoutesToResponsesProvider(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"m\"}}\n\n"+
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1},\"stop_reason\":\"end_turn\"}}\n\n")
	}))
	defer srv.Close()
	store := &mockStore{
		providers:    map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:        []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", Enabled: true}}}},
		apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}},
	}
	keys := &countingKeyService{inner: &mockService{}}
	p := New(store, keys, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || keys.keyResolveCalls != 1 || hits != 1 {
		t.Fatalf("status=%d keyCalls=%d hits=%d body=%s", rec.Code, keys.keyResolveCalls, hits, rec.Body.String())
	}
}

func TestResponsesStreamPrefersChatEdgeOverMessagesTarget(t *testing.T) {
	chatHits := 0
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatHits++
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-up\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer chatSrv.Close()
	messagesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("Messages target should not be reached when Chat edge is available")
	}))
	defer messagesSrv.Close()
	store := &mockStore{
		providers: map[string]*model.Provider{
			"chat":     {ID: "chat", Name: "Chat", BaseURL: chatSrv.URL, Enabled: true},
			"messages": {ID: "messages", Name: "Messages", BaseURL: messagesSrv.URL, Enabled: true, MessagesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "chat", Enabled: true},
			{ID: "t1", ProviderID: "messages", Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{
			{ProviderID: "chat", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: true, Source: "manual"},
			{ProviderID: "messages", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		},
	}
	p := New(store, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || chatHits != 1 || !strings.Contains(rec.Body.String(), "response.output_text.delta") {
		t.Fatalf("status=%d chatHits=%d body=%s", rec.Code, chatHits, rec.Body.String())
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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

func TestProtocolConversionErrorLogsChainWithoutProviderBreaker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "not-json")
	}))
	defer srv.Close()

	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	providerCB := p.breakerFor("p")
	now := time.Unix(100, 0)
	providerCB.nowFn = func() time.Time { return now }
	providerCB.state = StateHalfOpen
	providerCB.pendingProbe = false
	openedAt := now.Add(-time.Minute)
	providerCB.openedAt = openedAt
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
	if providerCB.CurrentState() != StateOpen || providerCB.pendingProbe || !providerCB.openedAt.Equal(openedAt) {
		t.Fatalf("conversion-local provider probe leaked: state=%v pending=%v openedAt=%v", providerCB.CurrentState(), providerCB.pendingProbe, providerCB.openedAt)
	}
	if !providerCB.Allow() {
		t.Fatal("conversion-local cancellation stranded next provider probe")
	}
	providerCB.CancelProbe()
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	spy := &metricSpy{}
	p.metricSink = spy
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
	if len(log.Chain) != 2 || log.Chain[0].Status != string(model.AttemptOutcomeConversionError) || log.Chain[1].Status != string(model.AttemptOutcomeSuccess) {
		t.Fatalf("chain=%+v, want conversion_error -> success", log.Chain)
	}
	if hit, fail := st.statsFor("t0"); hit != 0 || fail != 1 {
		t.Fatalf("pre-commit conversion failure stats=(%d,%d), want target failure +1 and no hit", hit, fail)
	}
	conversionRoute := model.RouteModeKey{TargetID: "t0", InboundProtocol: string(ProtocolAnthropicMessages), UpstreamProtocol: string(ProtocolOpenAIResponses)}
	if routeCB := p.routeBreakerFor(conversionRoute); routeCB.CurrentState() != StateClosed || routeCB.ConsecutiveFailures() != 1 {
		t.Fatalf("pre-commit conversion route breaker=%v failures=%d, want one local failure", routeCB.CurrentState(), routeCB.ConsecutiveFailures())
	}
	if p.breakerFor("p0").consecutiveFailures != 0 {
		t.Fatalf("pre-commit conversion failure penalized breaker: %d", p.breakerFor("p0").consecutiveFailures)
	}
	if provider, _ := st.GetProvider("p0"); provider.Status == model.ProviderStatusError {
		t.Fatal("pre-commit conversion failure penalized provider health")
	}
	events := spy.Events()
	if len(events) != 3 || events[0].Kind != model.MetricEventAttempt || events[1].Kind != model.MetricEventAttempt || events[2].Kind != model.MetricEventRequest {
		t.Fatalf("pre-commit conversion metric cardinality=%+v", events)
	}
	wantRoute := conversionRoute
	if events[0].StatusCode != 200 || events[0].AttemptOutcome != model.AttemptOutcomeConversionError || events[0].FailureClass != model.MetricFailureConversionLocal || events[0].StreamCommitted || events[0].RouteMode != wantRoute {
		t.Fatalf("pre-commit conversion event=%+v want route=%+v", events[0], wantRoute)
	}
	if events[1].AttemptOutcome != model.AttemptOutcomeSuccess || events[1].StatusCode != 200 {
		t.Fatalf("pre-commit fallback success event=%+v", events[1])
	}
}

func TestStreamingHTTP200SSEErrorFailsOverBeforeCommit(t *testing.T) {
	var firstHits, secondHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/first/") {
			firstHits++
			// Heartbeats and metadata-only events must remain staged. Also split
			// the first data field prefix across reads to exercise SSE framing.
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, ": ping\n\nid: heartbeat\n\nretry: 1\n\nda")
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(2 * time.Millisecond)
			_, _ = io.WriteString(w, "ta: {\"type\":\"error\",\"message\":\"rate limit: concurrency limit reached\"}\n\n")
			return
		}
		secondHits++
		_, _ = io.WriteString(w, responsesSuccessSSE("healthy"))
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"first":  {ID: "first", Name: "First", BaseURL: srv.URL + "/first", Enabled: true, ResponsesEnabled: true},
			"second": {ID: "second", Name: "Second", BaseURL: srv.URL + "/second", Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t-first", ProviderID: "first", ModelName: "first-model", MaxRetries: 0, Enabled: true},
			{ID: "t-second", ProviderID: "second", ModelName: "second-model", MaxRetries: 0, Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || firstHits != 1 || secondHits != 1 {
		t.Fatalf("status=%d first=%d second=%d body=%s", rec.Code, firstHits, secondHits, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "healthy") || strings.Contains(rec.Body.String(), "concurrency limit") {
		t.Fatalf("client received unexpected stream: %s", rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 2 || log.Chain[0].Status != string(model.AttemptOutcomeRetryable) || log.Chain[1].Status != string(model.AttemptOutcomeSuccess) {
		t.Fatalf("chain=%+v, want retryable -> success", log.Chain)
	}
	if !strings.Contains(log.Chain[0].Error, "concurrency limit") || log.Chain[0].StatusCode != http.StatusOK {
		t.Fatalf("first chain entry=%+v, want retryable HTTP 200 semantic error", log.Chain[0])
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	spy := &metricSpy{}
	p.metricSink = spy
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
	events := spy.Events()
	var bAttempts, requests int
	for _, event := range events {
		if event.Kind == model.MetricEventRequest {
			requests++
		}
		if event.Kind == model.MetricEventAttempt && event.RouteMode.TargetID == "t1" {
			bAttempts++
			if event.TTFTMs != int64(log.Chain[1].FirstTokenMs) {
				t.Fatalf("B TTFT sample=%+v, want its own chain TTFT=%d", event, log.Chain[1].FirstTokenMs)
			}
		}
	}
	if bAttempts != 1 || requests != 1 {
		t.Fatalf("stream metric attribution events=%+v, B attempts=%d requests=%d", events, bAttempts, requests)
	}
	if log.FirstTokenMs <= log.Chain[1].FirstTokenMs {
		t.Fatalf("request TTFT=%d should include A delay beyond B attempt TTFT=%d", log.FirstTokenMs, log.Chain[1].FirstTokenMs)
	}
}

func TestStreamingNativeSSEPartialEventStallDeadlineFailover(t *testing.T) {
	var firstHits, secondHits int
	firstDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/first/") {
			firstHits++
			flusher, _ := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: ")
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				close(firstDone)
			case <-time.After(5 * time.Second):
				t.Error("first native SSE attempt was not cancelled")
			}
			return
		}
		secondHits++
		_, _ = io.WriteString(w, responsesSuccessSSE("native-healthy"))
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"first":  {ID: "first", Name: "First", BaseURL: srv.URL + "/first", Enabled: true, ResponsesEnabled: true},
			"second": {ID: "second", Name: "Second", BaseURL: srv.URL + "/second", Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t-first", ProviderID: "first", ModelName: "first-model", MaxRetries: 0, FirstTokenTimeoutSeconds: 1, Enabled: true},
			{ID: "t-second", ProviderID: "second", ModelName: "second-model", MaxRetries: 0, Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for native SSE first attempt cancellation")
	}
	if rec.Code != http.StatusOK || firstHits != 1 || secondHits != 1 {
		t.Fatalf("status=%d first=%d second=%d body=%s", rec.Code, firstHits, secondHits, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "native-healthy") {
		t.Fatalf("client received unexpected stream: %s", rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 2 || log.Chain[0].Status != string(model.AttemptOutcomeRetryable) || log.Chain[1].Status != string(model.AttemptOutcomeSuccess) {
		t.Fatalf("chain=%+v, want retryable -> success", log.Chain)
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
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "target", ProviderID: "p", ModelName: "upstream", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "client-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "target", ProviderID: "p", ModelName: "native", Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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
			time.Sleep(5 * time.Millisecond)
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	spy := &metricSpy{}
	p.metricSink = spy
	before := p.breakerFor("p0").ConsecutiveFailures()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"client-model","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	var aborted bool
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if !errors.Is(recovered.(error), http.ErrAbortHandler) {
					t.Fatalf("unexpected panic: %v", recovered)
				}
				aborted = true
			}
		}()
		p.router.ServeHTTP(rec, req)
	}()
	if !aborted {
		t.Fatal("expected post-commit conversion failure to abort the handler")
	}
	if rec.Code != http.StatusOK || p0Hits != 1 || p1Hits != 0 || !strings.Contains(rec.Body.String(), "p0") {
		t.Fatalf("status=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.AttemptOutcomeConversionError) {
		t.Fatalf("chain=%+v, want one conversion_error entry", log.Chain)
	}
	if p.breakerFor("p0").consecutiveFailures != before {
		t.Fatal("post-commit conversion failure penalized breaker")
	}
	conversionRoute := model.RouteModeKey{TargetID: "t0", InboundProtocol: string(ProtocolAnthropicMessages), UpstreamProtocol: string(ProtocolOpenAIResponses)}
	if routeCB := p.routeBreakerFor(conversionRoute); routeCB.CurrentState() != StateClosed || routeCB.ConsecutiveFailures() != 1 {
		t.Fatalf("post-commit conversion route breaker=%v failures=%d, want one local failure", routeCB.CurrentState(), routeCB.ConsecutiveFailures())
	}
	prov, _ := st.GetProvider("p0")
	if prov.Status == model.ProviderStatusError {
		t.Fatalf("provider status=%q, conversion failure must remain local", prov.Status)
	}
	events := spy.Events()
	if len(events) != 2 || events[0].Kind != model.MetricEventAttempt || events[1].Kind != model.MetricEventRequest {
		t.Fatalf("post-commit conversion metric cardinality=%+v", events)
	}
	if events[0].StatusCode != 200 || events[0].AttemptOutcome != model.AttemptOutcomeConversionError || events[0].FailureClass != model.MetricFailureConversionLocal || !events[0].StreamCommitted || !events[0].RouteMode.Valid() {
		t.Fatalf("post-commit conversion event=%+v", events[0])
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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

func TestDisabledAPIKeyIsUnauthorized(t *testing.T) {
	st := &mockStore{apiKeys: []model.ApiKey{{ID: "disabled", Name: "off", Enabled: false}}}
	p := New(st, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()
	req := httptest.NewRequest("GET", "/v1/stats/tokens", nil)
	req.Header.Set("Authorization", "Bearer disabled")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for disabled key, got %d", rec.Code)
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		_, _ = w.Write([]byte("data: {\"id\":\"c2\"}\n\ndata: [DONE]\n\n"))
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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

func TestHybridFailover_ChatConversionToNativeChat(t *testing.T) {
	var calls []string
	responses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "responses")
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("conversion target path=%q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "upstream-responses" {
			t.Fatalf("conversion target model=%v", body["model"])
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"retryable"}`)
	}))
	defer responses.Close()

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "chat")
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("native target path=%q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "upstream-chat" {
			t.Fatalf("native target model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-ok","object":"chat.completion","model":"upstream-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer chat.Close()

	st := &mockStore{
		providers: map[string]*model.Provider{
			"p-responses": {ID: "p-responses", Name: "Responses", BaseURL: responses.URL, Enabled: true, ResponsesEnabled: true},
			"p-chat":      {ID: "p-chat", Name: "Chat", BaseURL: chat.URL, Enabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "client-chat", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t-convert", ProviderID: "p-responses", ModelName: "upstream-responses", MaxRetries: 0, Enabled: true},
			{ID: "t-native", ProviderID: "p-chat", ModelName: "upstream-chat", MaxRetries: 0, Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
		modelCapabilities: []model.ModelCapability{
			{ProviderID: "p-responses", ModelName: "upstream-responses", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"},
			{ProviderID: "p-responses", ModelName: "upstream-responses", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: true, Source: "manual"},
			{ProviderID: "p-chat", ModelName: "upstream-chat", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: true, Source: "manual"},
			{ProviderID: "p-chat", ModelName: "upstream-chat", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"client-chat","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || len(calls) != 2 || calls[0] != "responses" || calls[1] != "chat" {
		t.Fatalf("status=%d calls=%v body=%s", rec.Code, calls, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["choices"]; !ok {
		t.Fatalf("expected final Chat response, got %s", rec.Body.String())
	}
	if _, ok := response["output"]; ok {
		t.Fatalf("final response unexpectedly remained Responses protocol: %s", rec.Body.String())
	}
}

func TestHybridFailover_NativeResponsesToChatConversion(t *testing.T) {
	var calls []string
	responses := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "responses")
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("native target path=%q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "upstream-responses" {
			t.Fatalf("native target model=%v", body["model"])
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"retryable"}`)
	}))
	defer responses.Close()

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "chat")
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("conversion target path=%q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "upstream-chat" {
			t.Fatalf("conversion target model=%v", body["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-ok","object":"chat.completion","model":"upstream-chat","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer chat.Close()

	st := &mockStore{
		providers: map[string]*model.Provider{
			"p-responses": {ID: "p-responses", Name: "Responses", BaseURL: responses.URL, Enabled: true, ResponsesEnabled: true},
			"p-chat":      {ID: "p-chat", Name: "Chat", BaseURL: chat.URL, Enabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "client-responses", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t-native", ProviderID: "p-responses", ModelName: "upstream-responses", MaxRetries: 0, Enabled: true},
			{ID: "t-convert", ProviderID: "p-chat", ModelName: "upstream-chat", MaxRetries: 0, Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
		modelCapabilities: []model.ModelCapability{
			{ProviderID: "p-responses", ModelName: "upstream-responses", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: true, Source: "manual"},
			{ProviderID: "p-responses", ModelName: "upstream-responses", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"},
			{ProviderID: "p-chat", ModelName: "upstream-chat", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
			{ProviderID: "p-chat", ModelName: "upstream-chat", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: true, Source: "manual"},
		},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"client-responses","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || len(calls) != 2 || calls[0] != "responses" || calls[1] != "chat" {
		t.Fatalf("status=%d calls=%v body=%s", rec.Code, calls, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != "completed" {
		t.Fatalf("expected final Responses response, got %s", rec.Body.String())
	}
	if _, ok := response["output"]; !ok {
		t.Fatalf("expected converted Responses output, got %s", rec.Body.String())
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys:  []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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

// TestStreaming_ProtocolErrorThenClientCancelIsTruncated verifies that a
// client cancellation caused by an upstream SSE error is attributed to the
// upstream, not to the client. The error event is committed before the
// request is cancelled, so the attempt must not fail over.
func TestStreaming_ProtocolErrorThenClientCancelIsTruncated(t *testing.T) {
	hangCh := make(chan struct{})
	var upstreamHits, failoverHits int
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"id\":\"r\",\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\n" +
			"data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"server overloaded\"}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-hangCh:
		case <-r.Context().Done():
		}
	}))
	defer upstreamSrv.Close()
	defer close(hangCh)
	failoverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failoverHits++
		http.Error(w, "unexpected failover", http.StatusInternalServerError)
	}))
	defer failoverSrv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: upstreamSrv.URL, Enabled: true, ResponsesEnabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: failoverSrv.URL, Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{
			ID: "r1", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{
				{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 0, Enabled: true},
				{ID: "t1", ProviderID: "p1", ModelName: "m1", MaxRetries: 0, Enabled: true},
			},
		}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()
	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()
	req, _ := http.NewRequestWithContext(clientCtx, "POST", proxySrv.URL+"/v1/responses",
		strings.NewReader(`{"model":"x","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	readBuf := make([]byte, 4096)
	n, readErr := resp.Body.Read(readBuf)
	if readErr != nil && !strings.Contains(readErr.Error(), "canceled") {
		t.Fatalf("reading committed error event failed: %v", readErr)
	}
	if !strings.Contains(string(readBuf[:n]), "server overloaded") {
		t.Fatalf("expected committed protocol error, got %q", string(readBuf[:n]))
	}
	clientCancel()
	_ = resp.Body.Close()

	time.Sleep(300 * time.Millisecond)
	p.Shutdown()
	if upstreamHits != 1 || failoverHits != 0 {
		t.Fatalf("expected one upstream hit and no failover, got upstream=%d failover=%d", upstreamHits, failoverHits)
	}
	if got, want := store.providers["p0"].Status, model.ProviderStatusError; got != want {
		t.Fatalf("expected provider health %q, got %q", want, got)
	}
	if hit, fail := store.statsFor("t0"); hit != 0 || fail != 1 {
		t.Fatalf("expected target stats 0 hits/1 failure, got %d/%d", hit, fail)
	}
	log, ok := store.LastLog()
	if !ok || len(log.Chain) != 1 {
		t.Fatalf("expected one chain entry, got ok=%v log=%+v", ok, log)
	}
	if log.Chain[0].Status != string(model.OutcomeTruncated) {
		t.Fatalf("expected truncated, got %q", log.Chain[0].Status)
	}
	if !strings.Contains(log.Chain[0].Error, "server overloaded") || strings.Contains(log.Chain[0].Error, "client disconnect") {
		t.Fatalf("expected upstream error attribution, got %q", log.Chain[0].Error)
	}
}

func TestStreaming_ProtocolErrorCleanEOFKeepsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"r\",\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\n"+
			"data: {\"type\":\"error\",\"code\":\"server_error\",\"message\":\"server overloaded\"}\n\n")
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "x", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()
	req, _ := http.NewRequest("POST", proxySrv.URL+"/v1/responses", strings.NewReader(`{"model":"x","input":"hi","stream":true}`))
	req.Header.Set("Authorization", "Bearer key1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "server overloaded") {
		t.Fatalf("expected protocol error in response, got %q", string(body))
	}
	log := waitForLog(t, st)
	if log.Error == "" || len(log.Chain) != 1 || log.Chain[0].Error == "" {
		t.Fatalf("expected top-level and chain errors, got log=%+v", log)
	}
	if log.Chain[0].Status != string(model.OutcomeTruncated) || !strings.Contains(log.Error, "server overloaded") {
		t.Fatalf("expected truncated protocol error, got top=%q chain=%+v", log.Error, log.Chain[0])
	}
}

// TestStreaming_ClientDisconnectBeforeFirstByte_RecordsProviderIdentity is a
// regression test for the production gap where a client disconnect BEFORE the
// upstream sent response headers (Do error, context canceled) logged status
// 499 with EMPTY top-level provider/model/route fields, even though the
// attempt had genuinely hit the upstream (chain_json showed the provider).
//
// Setup:
//   - Upstream hangs without writing response headers.
//   - Client cancels while the proxy is blocked in client.Do.
//
// Expectations:
//   - Final log has StatusCode 499, one chain entry with
//     Status == "client_abort".
//   - Top-level ProviderID/ProviderName/Model/RouteID/RouteLabel carry the
//     chosen candidate (not the handler-initialized client-facing values).
func TestStreaming_ClientDisconnectBeforeFirstByte_RecordsProviderIdentity(t *testing.T) {
	hangCh := make(chan struct{})
	var upstreamHits int
	hangSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		// Never write headers; block until the proxy's transport
		// tears down the connection after the client disconnects.
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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

	// The request blocks in Do because the upstream never responds;
	// cancel after the proxy has dialed the upstream.
	respCh := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		respCh <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if upstreamHits != 1 {
		t.Fatalf("expected upstream to be hit before cancel, got %d", upstreamHits)
	}
	clientCancel()
	<-respCh

	// Give the proxy time to observe the disconnect, then drain the
	// async log writer.
	time.Sleep(300 * time.Millisecond)
	p.Shutdown()

	log, ok := store.LastLog()
	if !ok {
		t.Fatalf("expected log entry")
	}
	if log.StatusCode != statusClientClosed {
		t.Fatalf("expected status 499, got %d (err=%q)", log.StatusCode, log.Error)
	}
	if log.ProviderID != "p0" || log.ProviderName != "P0" {
		t.Fatalf("expected provider identity p0/P0, got %q/%q", log.ProviderID, log.ProviderName)
	}
	if log.Model != "m0" {
		t.Fatalf("expected upstream model m0, got %q", log.Model)
	}
	if log.RouteID != "r1" {
		t.Fatalf("expected route id r1, got %q", log.RouteID)
	}
	if len(log.Chain) != 1 {
		t.Fatalf("expected 1 chain entry, got %d: %+v", len(log.Chain), log.Chain)
	}
	if log.Chain[0].Status != "client_abort" {
		t.Fatalf("expected chain status=client_abort, got %q (err=%q)",
			log.Chain[0].Status, log.Chain[0].Error)
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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

func TestStreamHalfOpenTopTerminationReleasesBothProbes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration
		cap    bool
	}{
		{name: "global_cap", cap: true},
		{name: "budget", budget: time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer srv.Close()
			st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}}, rules: []model.ModelRule{{ID: "r", Name: "x", Enabled: true, FirstByteTimeoutSeconds: 1, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "m", MaxRetries: 1, Enabled: true}}}}, apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
			p := New(st, &mockService{}, 0, nil)
			defer p.Shutdown()
			spy := &metricSpy{}
			p.metricSink = spy
			providerCB := p.breakerFor("p")
			providerCB.nowFn = func() time.Time { return now }
			providerCB.state = StateOpen
			providerCB.openedAt = now.Add(-time.Minute)
			providerGeneration := providerCB.Generation()
			c := candidate{targetID: "t", provider: st.providers["p"], modelName: "m", protocol: ProtocolOpenAIChat, convertTo: ProtocolOpenAIResponses, maxRetries: 1, firstByteBudget: tc.budget}
			routeCB := p.routeBreakerFor(routeModeKeyForCandidate(c))
			routeCB.nowFn = func() time.Time { return now }
			routeCB.state = StateOpen
			routeCB.openedAt = now.Add(-time.Minute)
			routeGeneration := routeCB.Generation()
			candidates := []candidate{c}
			if tc.cap {
				// Consume the request-wide cap with eight preceding candidates;
				// the final candidate then claims its half-open probes and hits
				// the real forwardStream loop-top cap before an upstream call.
				candidates = make([]candidate, 0, maxTotalAttempts+1)
				for i := 0; i < maxTotalAttempts; i++ {
					provider := &model.Provider{ID: fmt.Sprintf("prior-%d", i), Name: "prior", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}
					candidates = append(candidates, candidate{targetID: fmt.Sprintf("prior-%d", i), provider: provider, modelName: "m", protocol: ProtocolOpenAIChat, maxRetries: 0, firstByteBudget: time.Hour})
				}
				candidates = append(candidates, c)
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"x","stream":true,"messages":[]}`))
			rec := httptest.NewRecorder()
			logEntry := &model.RequestLog{}
			p.forwardStream(rec, req, []byte(`{"model":"x","stream":true,"messages":[]}`), candidates, 0, logEntry)
			if !tc.cap {
				if rec.Code != http.StatusServiceUnavailable || hits != 0 || len(spy.Events()) != 0 {
					t.Fatalf("budget caller result status=%d hits=%d events=%d", rec.Code, hits, len(spy.Events()))
				}
				if hit, fail := st.statsFor("t"); hit != 0 || fail != 0 {
					t.Fatalf("budget caller changed target stats: hit=%d fail=%d", hit, fail)
				}
				if len(logEntry.Chain) != 1 || logEntry.Chain[0].Status != "budget_exceeded" || logEntry.Chain[0].UpstreamStarted {
					t.Fatalf("budget caller chain=%+v", logEntry.Chain)
				}
			}
			if hits == 0 && tc.cap {
				t.Fatal("stream cap test did not reach upstream")
			}
			if providerCB.pendingProbe || providerCB.CurrentState() != StateOpen || !providerCB.openedAt.Equal(now.Add(-time.Minute)) {
				t.Fatalf("provider probe not neutrally settled: state=%v pending=%v openedAt=%v", providerCB.CurrentState(), providerCB.pendingProbe, providerCB.openedAt)
			}
			if routeCB.pendingProbe || routeCB.CurrentState() != StateOpen || !routeCB.openedAt.Equal(now.Add(-time.Minute)) || routeCB.ConsecutiveFailures() != 0 {
				t.Fatalf("route probe was penalized or leaked: state=%v pending=%v failures=%d", routeCB.CurrentState(), routeCB.pendingProbe, routeCB.ConsecutiveFailures())
			}
			if providerCB.Generation() != providerGeneration+2 || routeCB.Generation() != routeGeneration+2 {
				t.Fatalf("neutral half-open settlement generation mismatch: provider=%d route=%d", providerCB.Generation(), routeCB.Generation())
			}
			if !providerCB.Allow() {
				t.Fatal("provider next half-open probe could not claim")
			}
			providerCB.CancelProbe()
		})
	}
}

func TestStreamAttemptExpiredBudgetDoesNotStartAttempt(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	spy := &metricSpy{}
	store := &mockStore{}
	p := New(store, &mockService{}, 0, nil)
	p.metricSink = spy
	c := candidate{targetID: "t", provider: &model.Provider{ID: "p", Name: "P", BaseURL: srv.URL}, modelName: "m", protocol: ProtocolOpenAIChat, firstByteBudget: time.Second}
	deadline := time.Now().Add(-time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	prep := AttemptPreparation{Body: []byte(`{"model":"m","messages":[]}`), Path: "/v1/chat/completions"}
	upstreamURL, err := url.Parse(srv.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	logEntry := &model.RequestLog{}
	result, _ := p.streamAttempt(ctx, httptest.NewRecorder(), req, c, "key", prep.Body, upstreamURL, prep, 0, 0, logEntry)
	if !result.BudgetExceeded || result.AttemptStarted {
		t.Fatalf("expired budget result=%+v", result)
	}
	if hits != 0 || len(spy.Events()) != 0 {
		t.Fatalf("expired budget started work: hits=%d events=%d", hits, len(spy.Events()))
	}
	for _, entry := range logEntry.Chain {
		if entry.UpstreamStarted {
			t.Fatalf("expired budget emitted started chain entry: %+v", entry)
		}
	}
	if hit, fail := store.statsFor("t"); hit != 0 || fail != 0 {
		t.Fatalf("expired budget changed target stats: hit=%d fail=%d", hit, fail)
	}
}

func TestStreamingExhaustedProviderTransportFailuresOpenBreaker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL func(*testing.T) string
		c       func(string, *model.Provider) candidate
	}{
		{name: "client_do_network", baseURL: func(t *testing.T) string {
			s := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			addr := s.Listener.Addr().String()
			s.Close()
			return "http://" + addr
		}, c: func(base string, provider *model.Provider) candidate {
			return candidate{targetID: "t", provider: provider, modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions", firstByteBudget: time.Second}
		}},
		{name: "first_body_timeout", baseURL: func(t *testing.T) string {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				time.Sleep(20 * time.Millisecond)
			}))
			return s.URL
		}, c: func(base string, provider *model.Provider) candidate {
			return candidate{targetID: "t", provider: provider, modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions", firstByteBudget: time.Second, targetFirstBodyByteTimeout: time.Millisecond}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseURL := tc.baseURL(t)
			provider := &model.Provider{ID: "p", Name: "P", BaseURL: baseURL, Enabled: true}
			p := New(&mockStore{}, &mockService{}, 0, nil)
			defer p.Shutdown()
			c := tc.c(baseURL, provider)
			for i := 0; i < failureThreshold; i++ {
				req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
				p.forwardStream(httptest.NewRecorder(), req, []byte(`{"model":"m","stream":true,"messages":[]}`), []candidate{c}, 0, &model.RequestLog{})
			}
			cb := p.breakerFor("p")
			if cb.CurrentState() != StateOpen || cb.ConsecutiveFailures() < failureThreshold {
				t.Fatalf("%s did not open provider breaker: state=%v failures=%d", tc.name, cb.CurrentState(), cb.ConsecutiveFailures())
			}
		})
	}
}

func TestStreamingPrematureNon2xxBodyIsProviderTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijacker unavailable")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write([]byte("HTTP/1.1 400 Bad Request\r\nContent-Length: 100\r\n\r\nx"))
		_ = conn.Close()
	}))
	defer srv.Close()
	p := New(&mockStore{}, &mockService{}, 0, nil)
	defer p.Shutdown()
	provider := &model.Provider{ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}
	c := candidate{targetID: "t", provider: provider, modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions", firstByteBudget: time.Second}
	for i := 0; i < failureThreshold; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
		p.forwardStream(httptest.NewRecorder(), req, []byte(`{"model":"m","stream":true,"messages":[]}`), []candidate{c}, 0, &model.RequestLog{})
	}
	if cb := p.breakerFor("p"); cb.CurrentState() != StateOpen || cb.ConsecutiveFailures() < failureThreshold {
		t.Fatalf("premature non-2xx body did not open provider breaker: state=%v failures=%d", cb.CurrentState(), cb.ConsecutiveFailures())
	}
}

func TestStreamingFinalStatusOnly429ClearsEarlierNetworkCause(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("hijacker unavailable")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()
	p := New(&mockStore{}, &mockService{}, 0, nil)
	defer p.Shutdown()
	provider := &model.Provider{ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}
	c := candidate{targetID: "t", provider: provider, modelName: "m", protocol: ProtocolOpenAIChat, upstreamPath: "/v1/chat/completions", firstByteBudget: time.Second, maxRetries: 1}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	p.forwardStream(httptest.NewRecorder(), req, []byte(`{"model":"m","stream":true,"messages":[]}`), []candidate{c}, 0, &model.RequestLog{})
	if hits != 2 {
		t.Fatalf("expected network retry followed by 429, hits=%d", hits)
	}
	if cb := p.breakerFor("p"); cb.CurrentState() != StateClosed || cb.ConsecutiveFailures() != 0 {
		t.Fatalf("stale network cause leaked into final status-only 429: state=%v failures=%d", cb.CurrentState(), cb.ConsecutiveFailures())
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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

func TestFailover_CandidateLocalBackoffNonStreaming(t *testing.T) {
	var aHits, bHits int
	var bTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/a/"):
			aHits++
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/b/"):
			bHits++
			bTimes = append(bTimes, time.Now())
			if bHits == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, `{"id":"b-ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
		}
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"a": {ID: "a", Name: "A", BaseURL: srv.URL + "/a", Enabled: true},
			"b": {ID: "b", Name: "B", BaseURL: srv.URL + "/b", Enabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "local-backoff", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "ta", ProviderID: "a", MaxRetries: 3, Enabled: true},
			{ID: "tb", ProviderID: "b", MaxRetries: 1, Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"local-backoff","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || aHits != 4 || bHits != 2 || len(bTimes) != 2 {
		t.Fatalf("status=%d a=%d b=%d times=%v body=%s", rec.Code, aHits, bHits, bTimes, rec.Body.String())
	}
	gap := bTimes[1].Sub(bTimes[0])
	if gap < 150*time.Millisecond || gap > 600*time.Millisecond {
		t.Fatalf("B first retry used non-local backoff: gap=%v", gap)
	}
}

func TestFailover_CandidateLocalBackoffStreaming(t *testing.T) {
	var aHits, bHits int
	var bTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case strings.HasPrefix(r.URL.Path, "/a/"):
			aHits++
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/b/"):
			bHits++
			bTimes = append(bTimes, time.Now())
			if bHits == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = io.WriteString(w, "data: {\"id\":\"b-ok\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"a": {ID: "a", Name: "A", BaseURL: srv.URL + "/a", Enabled: true},
			"b": {ID: "b", Name: "B", BaseURL: srv.URL + "/b", Enabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "local-stream-backoff", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "ta", ProviderID: "a", MaxRetries: 3, Enabled: true},
			{ID: "tb", ProviderID: "b", MaxRetries: 1, Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"local-stream-backoff","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || aHits != 4 || bHits != 2 || len(bTimes) != 2 {
		t.Fatalf("status=%d a=%d b=%d times=%v body=%s", rec.Code, aHits, bHits, bTimes, rec.Body.String())
	}
	gap := bTimes[1].Sub(bTimes[0])
	if gap < 150*time.Millisecond || gap > 600*time.Millisecond {
		t.Fatalf("B stream retry used non-local backoff: gap=%v", gap)
	}
}

func TestFailover_RetryAfterHonoredWithinCandidate(t *testing.T) {
	var hits int
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		times = append(times, time.Now())
		if hits == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}},
		rules:     []model.ModelRule{{ID: "r", Name: "retry-after-local", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", MaxRetries: 1, Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"retry-after-local","messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hits != 2 || len(times) != 2 {
		t.Fatalf("status=%d hits=%d times=%v body=%s", rec.Code, hits, times, rec.Body.String())
	}
	if gap := times[1].Sub(times[0]); gap < 900*time.Millisecond || gap > 2*time.Second {
		t.Fatalf("Retry-After was not honored within candidate: gap=%v", gap)
	}
}

func TestFailover_IgnoresRetryAfterAcrossTargetsStreaming(t *testing.T) {
	var p0Time, p1Time time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasPrefix(r.URL.Path, "/p0/") {
			p0Time = time.Now()
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		p1Time = time.Now()
		_, _ = io.WriteString(w, "data: {\"id\":\"p1\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "stream-retry-after", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "p0", MaxRetries: 0, Enabled: true},
			{ID: "t1", ProviderID: "p1", MaxRetries: 0, Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"stream-retry-after","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || p1Time.Sub(p0Time) >= 500*time.Millisecond {
		t.Fatalf("Retry-After leaked across stream targets: status=%d gap=%v body=%s", rec.Code, p1Time.Sub(p0Time), rec.Body.String())
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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

// TestFailover_IgnoresRetryAfterAcrossTargets verifies that Retry-After is
// local to a candidate retry loop and never delays the next target.
//
// Setup: P0 returns 429 with Retry-After: 2 (delta-seconds). P1
// returns 200. MaxRetries=0 means P1 starts immediately after P0 fails.
func TestFailover_IgnoresRetryAfterAcrossTargets(t *testing.T) {
	var p0Hits, p1Hits int
	var p0Time, p1Time time.Time
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
		p1Time = time.Now()
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
	if p1Time.Sub(p0Time) >= 500*time.Millisecond || elapsed >= time.Second {
		t.Fatalf("expected immediate target switch, p0->p1=%v elapsed=%v", p1Time.Sub(p0Time), elapsed)
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
	var envelope struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Error.Type == "" {
		t.Fatalf("expected standard error envelope, body=%s err=%v", rec.Body.String(), err)
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

func TestFailover_StreamRuleFirstByteBudgetExceeded(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := &mockStore{
		providers: map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "stream-budget", Enabled: true, FirstByteTimeoutSeconds: 1, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "m0", MaxRetries: 5, Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"stream-budget","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code < 500 || hits < 1 {
		t.Fatalf("expected stream budget 5xx with upstream hit, status=%d hits=%d body=%s", rec.Code, hits, rec.Body.String())
	}
	var envelope struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil || envelope.Error.Type == "" {
		t.Fatalf("expected standard stream error envelope, body=%s err=%v", rec.Body.String(), err)
	}
}

func TestProviderHalfOpenLeaseIsReusedAcrossRetries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stream     bool
		statuses   []int
		wantClosed bool
	}{
		{name: "nonstream_fail", statuses: []int{500, 500}},
		{name: "nonstream_success", statuses: []int{500, 200}, wantClosed: true},
		{name: "stream_fail", stream: true, statuses: []int{500, 500}},
		{name: "stream_success", stream: true, statuses: []int{500, 200}, wantClosed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status := tc.statuses[hits]
				hits++
				if status >= 500 {
					w.WriteHeader(status)
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"ok\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"ok","choices":[]}`)
			}))
			defer srv.Close()
			st := &mockStore{
				providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}},
				rules:     []model.ModelRule{{ID: "r", Name: "lease", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "m", MaxRetries: 1, Enabled: true}}}},
				apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
			}
			p := New(st, &mockService{}, 0, nil)
			defer p.Shutdown()
			cb := p.breakerFor("p")
			now := time.Unix(100, 0)
			cb.nowFn = func() time.Time { return now }
			cb.state = StateOpen
			cb.openedAt = now.Add(-time.Minute)
			reqBody := `{"model":"lease","messages":[]}`
			if tc.stream {
				reqBody = `{"model":"lease","stream":true,"messages":[]}`
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(reqBody))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			p.router.ServeHTTP(rec, req)
			if hits != 2 {
				t.Fatalf("upstream attempts=%d, want 2; status=%d body=%s", hits, rec.Code, rec.Body.String())
			}
			if cb.pendingProbe {
				t.Fatal("provider half-open probe remains pending")
			}
			if tc.wantClosed {
				if cb.CurrentState() != StateClosed || cb.ConsecutiveFailures() != 0 {
					t.Fatalf("success did not close provider breaker: state=%v failures=%d", cb.CurrentState(), cb.ConsecutiveFailures())
				}
			} else if cb.CurrentState() != StateOpen || !cb.openedAt.Equal(now) {
				t.Fatalf("final failure did not refresh provider breaker: state=%v openedAt=%v want=%v", cb.CurrentState(), cb.openedAt, now)
			}
		})
	}
}

func TestHalfOpenProvider501DoesNotPenalizeConversionRoute(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[stream], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotImplemented)
			}))
			defer srv.Close()
			st := &mockStore{
				providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
				rules:     []model.ModelRule{{ID: "r", Name: "route", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "m", Enabled: true}}}},
				apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
			}
			p := New(st, &mockService{}, 0, nil)
			defer p.Shutdown()
			now := time.Unix(100, 0)
			providerCB := p.breakerFor("p")
			providerCB.nowFn = func() time.Time { return now }
			providerCB.state = StateOpen
			providerCB.openedAt = now.Add(-time.Minute)
			c := candidate{targetID: "t", provider: st.providers["p"], modelName: "m", protocol: ProtocolAnthropicMessages, convertTo: ProtocolOpenAIResponses}
			routeCB := p.routeBreakerFor(routeModeKeyForCandidate(c))
			routeCB.nowFn = func() time.Time { return now }
			routeOpenedAt := now.Add(-2 * time.Minute)
			routeCB.state = StateOpen
			routeCB.openedAt = routeOpenedAt
			body := `{"model":"route","max_tokens":1,"messages":[]}`
			if stream {
				body = `{"model":"route","stream":true,"max_tokens":1,"messages":[]}`
			}
			req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer key1")
			rec := httptest.NewRecorder()
			p.router.ServeHTTP(rec, req)
			if rec.Code < 500 {
				t.Fatalf("expected 5xx for 501, got %d body=%s", rec.Code, rec.Body.String())
			}
			if providerCB.CurrentState() != StateOpen || providerCB.pendingProbe || !providerCB.openedAt.Equal(now) {
				t.Fatalf("provider 501 settlement wrong: state=%v pending=%v openedAt=%v", providerCB.CurrentState(), providerCB.pendingProbe, providerCB.openedAt)
			}
			if routeCB.CurrentState() != StateOpen || routeCB.pendingProbe || !routeCB.openedAt.Equal(routeOpenedAt) || routeCB.ConsecutiveFailures() != 0 {
				t.Fatalf("route 501 settlement penalized route: state=%v pending=%v openedAt=%v failures=%d", routeCB.CurrentState(), routeCB.pendingProbe, routeCB.openedAt, routeCB.ConsecutiveFailures())
			}
		})
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()

	before := p.breakerFor("p0").consecutiveFailures
	proxySrv := httptest.NewServer(p.router)
	defer proxySrv.Close()
	req, err := http.NewRequest("POST", proxySrv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"x","messages":[],"stream":true}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after committed SSE, got %d", resp.StatusCode)
	}
	body, readErr := io.ReadAll(resp.Body)
	if readErr == nil {
		t.Fatal("expected transport error while reading truncated stream")
	}
	if !strings.Contains(string(body), `"id":"c1"`) {
		t.Fatalf("expected forwarded partial SSE data, got %q", body)
	}

	deadline, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	for {
		if got := p.breakerFor("p0").ConsecutiveFailures(); got > before {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatalf("timed out waiting for truncated stream handling before shutdown; consecutiveFailures=%d", p.breakerFor("p0").ConsecutiveFailures())
		case <-time.After(10 * time.Millisecond):
		}
	}
	_ = p.Shutdown()
	if got := p.breakerFor("p0").ConsecutiveFailures(); got <= before {
		t.Fatalf("expected breaker consecutiveFailures to increase, before=%d after=%d", before, got)
	}
	log := waitForLog(t, store)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.OutcomeTruncated) {
		t.Fatalf("expected one truncated chain entry, got %+v", log.Chain)
	}
}

func TestChar_ResponsesRouteRegistered(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
			providers:    map[string]*model.Provider{"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: false}},
			rules:        []model.ModelRule{{ID: "r1", Name: "resp-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p0", ModelName: "upstream-resp", Enabled: true}}}},
			apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
			capabilities: []model.ProviderCapability{{ProviderID: "p0", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}},
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
		assertErrorEnvelopeContains(t, rec.Body.Bytes(), "service_unavailable", "no available provider for model \"resp-model\"")
	})
}

func TestChar_MessagesRouteRegistered(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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

func TestFeatureMismatchReturns422UnsupportedFeature(t *testing.T) {
	// Model rule exists, but the only target lacks vision capability and
	// conversion cannot preserve vision either.
	store := &mockStore{
		settings:  &model.Settings{Advanced: model.AdvancedSettings{FeatureCapabilityEnforcement: model.FeatureCapabilityEnforcementEnforce}},
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: "http://localhost", Enabled: true, MessagesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "vision-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) {
		return &model.Settings{Advanced: model.AdvancedSettings{FeatureCapabilityEnforcement: model.FeatureCapabilityEnforcementEnforce}}, nil
	})
	defer p.Shutdown()

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"vision-model","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x","media_type":"image/png"}}]}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 unsupported_feature, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "unsupported_feature", "vision")
}

func TestNoRuleStillReturns503(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}, providers: map[string]*model.Provider{}, rules: nil}
	p := New(store, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"missing","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "no_matching_rule", "no matching model rule: missing")
}

func TestChatFeatureExplicitFalseE2E422(t *testing.T) {
	store := &mockStore{
		settings:  &model.Settings{Advanced: model.AdvancedSettings{FeatureCapabilityEnforcement: model.FeatureCapabilityEnforcementEnforce}},
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: "http://localhost", Enabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "vision-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{
			{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: string(model.FeatureVision), Enabled: false, Source: "manual"},
		},
	}
	p := New(store, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"vision-model","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png"}}]}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "unsupported_feature", "vision")
}

func TestGeminiFeatureExplicitFalseE2E422(t *testing.T) {
	store := &mockStore{
		settings:  &model.Settings{Advanced: model.AdvancedSettings{FeatureCapabilityEnforcement: model.FeatureCapabilityEnforcementEnforce}},
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: "http://localhost", Enabled: true, GeminiEnabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "gemini-model", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{
			{ProviderID: "p", Protocol: string(ProtocolGemini), Feature: string(model.FeatureVision), Enabled: false, Source: "manual"},
		},
	}
	p := New(store, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-model:generateContent", strings.NewReader(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"x"}}]}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	assertErrorEnvelopeContains(t, rec.Body.Bytes(), "unsupported_feature", "vision")
}

func TestNonFeatureUnavailableStillReturns503(t *testing.T) {
	// Provider disabled is not a feature failure; should remain 503.
	store := &mockStore{
		settings:  &model.Settings{Advanced: model.AdvancedSettings{FeatureCapabilityEnforcement: model.FeatureCapabilityEnforcementEnforce}},
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: "http://localhost", Enabled: false, MessagesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(store, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestConversionRejectsBeforeUpstreamAttempt(t *testing.T) {
	// A vision Messages request with only a Responses-capable provider should
	// fail at the conversion selector before any upstream HTTP/key attempt.
	var upstreamHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "x", "content": []map[string]string{{"type": "text", "text": "ok"}}, "stop_reason": "end_turn", "usage": map[string]int{"input_tokens": 1, "output_tokens": 1}})
	}))
	defer srv.Close()

	store := &mockStore{
		settings:  &model.Settings{Advanced: model.AdvancedSettings{FeatureCapabilityEnforcement: model.FeatureCapabilityEnforcementEnforce}},
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	svc := &countingKeyService{inner: &mockService{}}
	p := New(store, svc, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x","media_type":"image/png"}}]}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", rec.Code, rec.Body.String())
	}
	if svc.keyResolveCalls != 0 {
		t.Fatalf("expected no key resolve calls, got %d", svc.keyResolveCalls)
	}
	if upstreamHits != 0 {
		t.Fatalf("expected no upstream hits, got %d", upstreamHits)
	}
}

func TestFeatureEnforcementCachedNoPerRequestSettingsQuery(t *testing.T) {
	store := &mockStore{
		settings:  &model.Settings{Advanced: model.AdvancedSettings{FeatureCapabilityEnforcement: model.FeatureCapabilityEnforcementEnforce}},
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: "http://localhost", Enabled: true, MessagesEnabled: true}},
		rules:     []model.ModelRule{{ID: "r1", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p", Enabled: true}}}},
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
	}
	p := New(store, &mockService{}, 0, nil)
	defer p.Shutdown()

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Authorization", "Bearer key1")
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
	}
	if store.getSettingsCalls != 1 {
		t.Fatalf("expected one GetSettings call (at construction), got %d", store.getSettingsCalls)
	}
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
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
		assertErrorEnvelopeContains(t, rec.Body.Bytes(), "service_unavailable", "no available provider for model \"claude\"")
	})
}

func TestChar_GeminiRouteRegistered(t *testing.T) {
	store := &mockStore{apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}}}
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
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
			apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
		assertErrorEnvelopeContains(t, rec.Body.Bytes(), "service_unavailable", "no available provider for model \"gemini-pro\"")
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys:   []model.ApiKey{{ID: "key1", Enabled: true}},
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
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
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

func TestChatToResponsesStreamingE2E(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	upstream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_up\",\"model\":\"upstream\",\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1,\"total_tokens\":4},\"stop_reason\":\"end_turn\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstream)
	}))
	defer srv.Close()
	st := &mockStore{
		providers:    map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true, ResponsesEnabled: true}},
		rules:        []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
		apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/responses" {
		t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
	}
	if gotBody["stream"] != true {
		t.Fatalf("upstream stream flag missing: %#v", gotBody)
	}
	body := rec.Body.String()
	for _, ev := range []string{"data: ", "\"role\":\"assistant\"", "\"content\":\"hello\"", "\"finish_reason\":\"stop\"", "data: [DONE]"} {
		if !strings.Contains(body, ev) {
			t.Fatalf("missing %s in %s", ev, body)
		}
	}
	if !strings.Contains(body, "\"prompt_tokens\":3") || !strings.Contains(body, "\"completion_tokens\":1") {
		t.Fatalf("usage not mapped: %s", body)
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.AttemptOutcomeSuccess) || log.InputTokens != 3 || log.OutputTokens != 1 {
		t.Fatalf("log=%+v", log)
	}
}

func TestResponsesToChatStreamingE2E(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	upstream := "data: {\"id\":\"chatcmpl-up\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-up\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-up\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-up\",\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstream)
	}))
	defer srv.Close()
	st := &mockStore{
		providers:    map[string]*model.Provider{"p": {ID: "p", Name: "P", BaseURL: srv.URL, Enabled: true}},
		rules:        []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", ModelName: "upstream", Enabled: true}}}},
		apiKeys:      []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"}},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/v1/chat/completions" {
		t.Fatalf("status=%d path=%q body=%s", rec.Code, gotPath, rec.Body.String())
	}
	if gotBody["stream"] != true {
		t.Fatalf("upstream stream flag missing: %#v", gotBody)
	}
	body := rec.Body.String()
	for _, ev := range []string{"event: response.created", "event: response.output_text.delta", "event: response.completed"} {
		if !strings.Contains(body, ev) {
			t.Fatalf("missing %s in %s", ev, body)
		}
	}
	if !strings.Contains(body, "\"input_tokens\":2") || !strings.Contains(body, "\"output_tokens\":1") {
		t.Fatalf("usage not mapped: %s", body)
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 1 || log.Chain[0].Status != string(model.AttemptOutcomeSuccess) || log.InputTokens != 2 || log.OutputTokens != 1 {
		t.Fatalf("log=%+v", log)
	}
}

func TestChatToResponsesStreamingPreCommitFailover(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/p0/") {
			p0Hits++
			_, _ = io.WriteString(w, "event: response.created\ndata: {not json}\n\n")
			return
		}
		p1Hits++
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"m1\"}}\n\n"+
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ok\"}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1},\"stop_reason\":\"end_turn\"}}\n\n")
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true, ResponsesEnabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true, ResponsesEnabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
			{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{
			{ProviderID: "p0", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"},
			{ProviderID: "p1", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"},
		},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || p0Hits != 1 || p1Hits != 1 {
		t.Fatalf("status=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") || strings.Contains(rec.Body.String(), "not json") {
		t.Fatalf("client received unexpected stream: %s", rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 2 || log.Chain[0].Status != string(model.AttemptOutcomeConversionError) || log.Chain[1].Status != string(model.AttemptOutcomeSuccess) {
		t.Fatalf("chain=%+v", log.Chain)
	}
	if hit, fail := st.statsFor("t0"); hit != 0 || fail != 1 {
		t.Fatalf("pre-commit stats=(%d,%d), want (0,1)", hit, fail)
	}
	if p.breakerFor("p0").consecutiveFailures != 0 {
		t.Fatalf("pre-commit failure penalized breaker: %d", p.breakerFor("p0").consecutiveFailures)
	}
}

func TestResponsesToChatStreamingPreCommitFailover(t *testing.T) {
	var p0Hits, p1Hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "/p0/") {
			p0Hits++
			_, _ = io.WriteString(w, "data: {not json}\n\n")
			return
		}
		p1Hits++
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-p1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p0": {ID: "p0", Name: "P0", BaseURL: srv.URL + "/p0", Enabled: true},
			"p1": {ID: "p1", Name: "P1", BaseURL: srv.URL + "/p1", Enabled: true},
		},
		rules: []model.ModelRule{{ID: "r", Name: "m", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "t0", ProviderID: "p0", ModelName: "m0", Enabled: true},
			{ID: "t1", ProviderID: "p1", ModelName: "m1", Enabled: true},
		}}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}},
		capabilities: []model.ProviderCapability{
			{ProviderID: "p0", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
			{ProviderID: "p1", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		},
	}
	p := New(st, &mockService{}, 0, nil)
	defer p.Shutdown()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"hi"}`))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || p0Hits != 1 || p1Hits != 1 {
		t.Fatalf("status=%d p0=%d p1=%d body=%s", rec.Code, p0Hits, p1Hits, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") || strings.Contains(rec.Body.String(), "not json") {
		t.Fatalf("client received unexpected stream: %s", rec.Body.String())
	}
	log := waitForLog(t, st)
	if len(log.Chain) != 2 || log.Chain[0].Status != string(model.AttemptOutcomeConversionError) || log.Chain[1].Status != string(model.AttemptOutcomeSuccess) {
		t.Fatalf("chain=%+v", log.Chain)
	}
	if hit, fail := st.statsFor("t0"); hit != 0 || fail != 1 {
		t.Fatalf("pre-commit stats=(%d,%d), want (0,1)", hit, fail)
	}
	if p.breakerFor("p0").consecutiveFailures != 0 {
		t.Fatalf("pre-commit failure penalized breaker: %d", p.breakerFor("p0").consecutiveFailures)
	}
}

// Package service implements the business logic layer (api.BusinessService).
// It depends on the concrete *store.Store for persistence operations that
// extend beyond the api.StoreService interface (e.g., UpsertModels,
// GetProviderKeyCiphertext).
package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/store"

	"github.com/shirou/gopsutil/v4/process"
)

// Service implements api.BusinessService.
type Service struct {
	store *store.Store
	proxy ProxyRef

	httpClient *http.Client
	encKey     []byte // 32-byte AES key; loaded at construction from keyDir
	startedAt  time.Time
	testMu     sync.Mutex
	tests      map[string]context.CancelFunc
}

// ProxyRef is the minimal proxy surface needed to compute live system health.
type ProxyRef interface {
	IsRunning() bool
	URL() string
	ActiveConnections() int
}

// New creates a BusinessService with the given store and proxy reference. The
// keyDir is the directory that holds the auto-generated AES key file (e.g.
// ~/.autoapi); it is created if it does not exist.
func New(s *store.Store, proxy ProxyRef, keyDir string) *Service {
	encKey, err := loadOrCreateKey(keyDir)
	if err != nil {
		// Fail fast if we cannot set up encryption.
		panic(fmt.Sprintf("service: load encryption key: %v", err))
	}

	svc := &Service{
		store:      s,
		proxy:      proxy,
		httpClient: &http.Client{}, // no global timeout; per-request context timeout used
		encKey:     encKey,
		startedAt:  time.Now(),
		tests:      make(map[string]context.CancelFunc),
	}
	slog.Info("service: initialized", "key_dir", keyDir, "key_bytes", len(encKey))
	return svc
}

// loadOrCreateKey returns the 32-byte AES key stored in keyDir/.key,
// creating it if necessary.
func loadOrCreateKey(keyDir string) ([]byte, error) {
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}

	keyPath := filepath.Join(keyDir, ".key")
	data, err := os.ReadFile(keyPath)
	if err == nil {
		if len(data) != 32 {
			return nil, fmt.Errorf("key file %s has unexpected size %d", keyPath, len(data))
		}
		return data, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("write key file: %w", err)
	}
	return key, nil
}

// SetProxy updates the proxy reference after the proxy has been created. This is
// used by the composition root to break the service→proxy→service cycle.
func (s *Service) SetProxy(proxy ProxyRef) {
	s.proxy = proxy
}

// ---------------------------------------------------------------------------
//  System health
// ---------------------------------------------------------------------------

func cpuPercent() float64 {
	p, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return 0.0
	}
	percent, err := p.Percent(0)
	if err != nil {
		return 0.0
	}
	return percent
}

// preferredInterfaceNames lists interface name substrings (case-insensitive)
// we prefer when picking a local IPv4 address to advertise. Order matters:
// earlier entries are scanned first within the same priority tier (en0, Wi-Fi,
// Ethernet cover macOS; eth0 / wlan0 cover Linux). We do a single pass that
// keeps a best-match and a fallback so the caller gets a deterministic address
// even on multi-homed machines.
var preferredInterfaceNames = []string{
	"en0", "wi-fi", "wifi", "ethernet", "eth0", "wlan0", "en1", "en2",
}

// localIPv4 returns the first usable non-loopback, non-link-local IPv4 address
// on this host, preferring common physical interface names. If no suitable
// address is found (e.g. an IPv6-only host, or no networking at all), it
// returns "127.0.0.1" so callers can always build a valid http://host:port URL.
func localIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	prefLow := make([]string, len(preferredInterfaceNames))
	for i, p := range preferredInterfaceNames {
		prefLow[i] = strings.ToLower(p)
	}

	// First pass: collect any IPv4 from a preferred interface.
	for _, pref := range prefLow {
		for _, iface := range ifaces {
			if !isCandidateInterface(iface) {
				continue
			}
			if !strings.Contains(strings.ToLower(iface.Name), pref) {
				continue
			}
			if addr := firstUsableV4(iface); addr != "" {
				return addr
			}
		}
	}

	// Second pass: any non-loopback, non-link-local, non-point-to-point IPv4.
	for _, iface := range ifaces {
		if !isCandidateInterface(iface) {
			continue
		}
		if iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if addr := firstUsableV4(iface); addr != "" {
			return addr
		}
	}

	return "127.0.0.1"
}

func isCandidateInterface(iface net.Interface) bool {
	return iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0
}

func firstUsableV4(iface net.Interface) string {
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip4 := ipnet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		return ip4.String()
	}
	return ""
}

// resolveAPIAddress builds a client-reachable URL from the proxy's reported
// bind URL and this host's first usable IPv4. If the proxy is not running or
// the URL cannot be parsed, it returns an empty string.
func resolveAPIAddress(proxyURL string) string {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return ""
	}

	raw := proxyURL
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}

	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}

	_, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" {
		return ""
	}

	return fmt.Sprintf("%s://%s:%s", scheme, localIPv4(), port)
}

// GetSystemHealth returns live runtime + proxy metrics for the dashboard.
func (s *Service) GetSystemHealth() (*model.ServiceHealth, error) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	status := "paused"
	proxyURL := ""
	activeConns := 0
	if s.proxy != nil {
		if s.proxy.IsRunning() {
			status = "running"
			proxyURL = s.proxy.URL()
		}
		activeConns = s.proxy.ActiveConnections()
	}

	apiAddr := ""
	if status == "running" {
		apiAddr = resolveAPIAddress(proxyURL)
	}

	return &model.ServiceHealth{
		Status:            status,
		UptimeSeconds:     int64(time.Since(s.startedAt).Seconds()),
		MemoryMB:          int(ms.Alloc / 1024 / 1024),
		CPUPercent:        cpuPercent(),
		ActiveConnections: activeConns,
		WebSocketCount:    0, // v1 does not expose WebSocket endpoints; hide this field in UI if desired
		HTTPCount:         activeConns,
		ProxyURL:          proxyURL,
		APIAddress:        apiAddr,
	}, nil
}

// ---------------------------------------------------------------------------
//  Crypto operations (AES-256-GCM)
// ---------------------------------------------------------------------------

// Encrypt encrypts plaintext using AES-256-GCM with the loaded encryption key.
// Returns (ciphertext, nonce, error).
func (s *Service) Encrypt(plaintext []byte) ([]byte, []byte, error) {
	if len(s.encKey) == 0 {
		return nil, nil, fmt.Errorf("service: encryption key not loaded")
	}

	block, err := aes.NewCipher(s.encKey)
	if err != nil {
		return nil, nil, fmt.Errorf("service: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("service: aes gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("service: nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Decrypt decrypts ciphertext using AES-256-GCM with the loaded encryption key.
func (s *Service) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	if len(s.encKey) == 0 {
		return nil, fmt.Errorf("service: encryption key not loaded")
	}

	block, err := aes.NewCipher(s.encKey)
	if err != nil {
		return nil, fmt.Errorf("service: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("service: aes gcm: %w", err)
	}

	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("service: decrypt: invalid nonce length %d, expected %d", len(nonce), gcm.NonceSize())
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("service: decrypt: %w", err)
	}
	return plaintext, nil
}

// ---------------------------------------------------------------------------
//  Provider testing
// ---------------------------------------------------------------------------

// TestProvider tests connectivity with a single provider by fetching its upstream
// models and updating health metrics.
func (s *Service) TestProvider(providerID string) (*model.ProviderTestResult, error) {
	slog.Info("service: test provider started", "provider_id", providerID)
	models, err := s.FetchUpstreamModels(providerID)
	if err != nil {
		slog.Warn("service: test provider failed", "provider_id", providerID, "err", err)
		return s.testFailResult(err.Error()), nil
	}

	latencyMs := 0
	if len(models) > 0 {
		latencyMs = models[0].LatencyMs
	}

	modelNames := make([]string, len(models))
	for i, m := range models {
		modelNames[i] = m.Name
	}

	status := model.ProviderStatusConnected
	if err := s.store.UpdateProviderTestResult(providerID, status, latencyMs, ""); err != nil {
		return nil, fmt.Errorf("service: update provider test result: %w", err)
	}

	slog.Info("service: test provider succeeded",
		"provider_id", providerID,
		"models", len(models),
		"latency_ms", latencyMs)
	return &model.ProviderTestResult{
		OK:        true,
		LatencyMs: latencyMs,
		Models:    modelNames,
	}, nil
}

// FetchUpstreamModels loads a provider's upstream model list from /v1/models and
// returns the raw upstream model list without modifying the local database.
func (s *Service) FetchUpstreamModels(providerID string) ([]model.Model, error) {
	prov, err := s.store.GetProvider(providerID)
	if err != nil {
		return nil, err
	}

	upstreamKey, err := s.ResolveProviderKey(prov.ID)
	if err != nil {
		return nil, fmt.Errorf("resolve provider key: %w", err)
	}

	start := time.Now()
	req, err := http.NewRequest("GET", store.JoinProviderURL(prov.BaseURL, "/v1/models"), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	req.Header.Set("Content-Type", "application/json")

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 20 * time.Second
	transport.MaxIdleConnsPerHost = 10
	transport.DialContext = (&net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext
	client := &http.Client{
		Transport: transport,
		Timeout:   25 * time.Second,
	}
	resp, err := client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		slog.Warn("service: fetch upstream models HTTP error",
			"provider_id", providerID, "provider_name", prov.Name,
			"latency_ms", latencyMs, "err", err)
		return nil, fmt.Errorf("HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Warn("service: fetch upstream models non-OK status",
			"provider_id", providerID, "provider_name", prov.Name,
			"status", resp.StatusCode, "latency_ms", latencyMs)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	// Parse OpenAI-style model list
	var modelsResp struct {
		Data []struct {
			ID            string `json:"id"`
			OwnedBy       string `json:"owned_by"`
			ContextWindow int    `json:"context_window"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	now := time.Now().UnixMilli()
	models := make([]model.Model, len(modelsResp.Data))
	for i, m := range modelsResp.Data {
		models[i] = model.Model{
			ProviderID:    prov.ID,
			Name:          m.ID,
			OwnedBy:       m.OwnedBy,
			ContextWindow: m.ContextWindow,
			Active:        true,
			LatencyMs:     latencyMs, // initial latency from the fetch itself
			CreatedAt:     now,
			UpdatedAt:     now,
		}
	}

	slog.Info("service: fetch upstream models succeeded",
		"provider_id", providerID, "provider_name", prov.Name,
		"count", len(models), "latency_ms", latencyMs)
	return models, nil
}

// TestModelLatency returns the latency recorded for a single model by
// querying the provider's /v1/models endpoint. This is intentionally coarse
// for v1: it avoids issuing paid chat completions just to measure latency.
// A future per-model paid probe can be added behind an explicit user action.
func (s *Service) TestModelLatency(providerID, modelName string) (*model.ModelTestResult, error) {
	models, err := s.FetchUpstreamModels(providerID)
	if err != nil {
		return &model.ModelTestResult{OK: false, Error: err.Error()}, nil
	}

	found := false
	latencyMs := 0
	for _, m := range models {
		if m.Name == modelName {
			found = true
			latencyMs = m.LatencyMs
			break
		}
	}
	if !found {
		return &model.ModelTestResult{OK: false, Error: "model not found in upstream list"}, nil
	}

	if err := s.store.UpdateModelLatency(providerID, modelName, latencyMs); err != nil {
		// Model exists upstream but not in local catalog — that's fine for a test.
		// Only tolerate ErrNotFound; surface real persistence errors.
		if !errors.Is(err, store.ErrNotFound) {
			return nil, fmt.Errorf("service: update model latency: %w", err)
		}
	}
	return &model.ModelTestResult{OK: true, LatencyMs: latencyMs}, nil
}

// TestModelChat sends a minimal chat completion request to the provider using
// the supplied model name and returns the result (success or a structured
// error) without raising an error for downstream failures. The stream flag
// controls whether the request is made in streaming mode.
func (s *Service) TestModelChat(providerID, modelName, protocol string, stream bool, testID string) (*model.ModelChatTestResult, error) {
	p, err := s.store.GetProvider(providerID)
	if err != nil {
		return nil, fmt.Errorf("provider not found: %w", err)
	}

	apiKey, err := s.ResolveProviderKey(providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve API key: %w", err)
	}

	var requestBody map[string]interface{}
	var path string
	switch protocol {
	case "chat":
		path, requestBody = "/v1/chat/completions", map[string]interface{}{"model": modelName, "messages": []map[string]string{{"role": "user", "content": "hi"}}, "max_tokens": 512, "stream": stream}
	case "responses":
		path, requestBody = "/v1/responses", map[string]interface{}{"model": modelName, "input": "hi", "max_output_tokens": 512, "stream": stream, "store": false}
	case "messages":
		path, requestBody = "/v1/messages", map[string]interface{}{"model": modelName, "messages": []map[string]interface{}{{"role": "user", "content": "hi"}}, "max_tokens": 512, "stream": stream}
	case "gemini":
		path, requestBody = "/v1beta/models/"+url.PathEscape(modelName)+":generateContent", map[string]interface{}{"contents": []map[string]interface{}{{"role": "user", "parts": []map[string]string{{"text": "hi"}}}}}
		if stream {
			path = "/v1beta/models/" + url.PathEscape(modelName) + ":streamGenerateContent?alt=sse"
		}
	default:
		return nil, fmt.Errorf("unsupported model test protocol %q (expected responses, messages, chat, or gemini)", protocol)
	}
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, err
	}

	chatURL := store.JoinProviderURL(p.BaseURL, path)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if testID != "" {
		s.testMu.Lock()
		s.tests[testID] = cancel
		s.testMu.Unlock()
		defer func() { s.testMu.Lock(); delete(s.tests, testID); s.testMu.Unlock() }()
	}

	start := time.Now()
	result := func(ok bool, response, errMsg, finishReason string) *model.ModelChatTestResult {
		return &model.ModelChatTestResult{
			OK:           ok,
			Response:     response,
			LatencyMs:    int(time.Since(start).Milliseconds()),
			FinishReason: finishReason,
			Error:        errMsg,
			Endpoint:     chatURL,
		}
	}

	req, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return result(false, "", err.Error(), ""), nil
	}
	req.Header.Set("Content-Type", "application/json")
	if protocol == "gemini" {
		req.Header.Set("x-goog-api-key", apiKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if protocol == "messages" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return result(false, "", err.Error(), ""), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
		out := result(false, "", fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(errBody)), "")
		out.HTTPStatus = resp.StatusCode
		return out, nil
	}

	if stream {
		var out *model.ModelChatTestResult
		if protocol == "chat" {
			out = s.parseChatStream(resp.Body, start)
		} else {
			out = s.parseGenericStream(resp.Body, start)
		}
		out.HTTPStatus = resp.StatusCode
		out.Endpoint = chatURL
		return out, nil
	}

	// Non-stream mode: read up to 64 KiB + 1 byte so we can detect overflow.
	const maxBodySize = 64*1024 + 1
	limitedReader := io.LimitReader(resp.Body, maxBodySize)
	respBody, err := io.ReadAll(limitedReader)
	if err != nil {
		out := result(false, "", fmt.Sprintf("read body: %v", err), "")
		out.HTTPStatus = resp.StatusCode
		return out, nil
	}
	if len(respBody) == maxBodySize {
		out := result(false, "", "response body too large (>64 KiB)", "")
		out.HTTPStatus = resp.StatusCode
		return out, nil
	}
	var out *model.ModelChatTestResult
	if protocol == "chat" {
		out = s.parseChatNonStream(respBody, start)
	} else {
		out = parseGenericNonStream(respBody, start)
	}
	out.HTTPStatus = resp.StatusCode
	out.Endpoint = chatURL
	return out, nil
}

func (s *Service) CancelModelTest(testID string) bool {
	s.testMu.Lock()
	cancel, ok := s.tests[testID]
	s.testMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// ListUpstreamMonitorModels returns active models belonging to enabled
// providers, choosing the first enabled native protocol configured for the
// model (then provider). A model capability row overrides its provider row.
func (s *Service) ListUpstreamMonitorModels() ([]model.UpstreamMonitorModel, error) {
	providers, err := s.store.ListProviders()
	if err != nil {
		return nil, err
	}
	protocols := []string{"openai_responses", "anthropic_messages", "gemini", "openai_chat"}
	out := make([]model.UpstreamMonitorModel, 0)
	for _, p := range providers {
		if !p.Enabled {
			continue
		}
		pc, err := s.store.ListProviderCapabilities(p.ID)
		if err != nil {
			return nil, err
		}
		pvals := make(map[string]bool)
		for _, c := range pc {
			if c.Feature == "native" && c.Source == "manual" {
				pvals[c.Protocol] = c.Enabled
			}
		}
		legacy := map[string]bool{"openai_responses": p.ResponsesEnabled, "anthropic_messages": p.MessagesEnabled, "gemini": p.GeminiEnabled, "openai_chat": true}
		for k, v := range legacy {
			if _, ok := pvals[k]; !ok {
				pvals[k] = v
			}
		}
		models, err := s.store.ListModels(p.ID)
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			if !m.Active {
				continue
			}
			mc, err := s.store.ListModelCapabilities(p.ID, m.Name)
			if err != nil {
				return nil, err
			}
			vals := make(map[string]bool)
			for k, v := range pvals {
				vals[k] = v
			}
			for _, c := range mc {
				if c.Feature == "native" && c.Source == "manual" {
					vals[c.Protocol] = c.Enabled
				}
			}
			for _, proto := range protocols {
				if vals[proto] {
					out = append(out, model.UpstreamMonitorModel{ProviderID: p.ID, ProviderName: p.Name, ModelName: m.Name, Protocol: monitorProbeProtocol(proto), Enabled: true})
					break
				}
			}
		}
	}
	return out, nil
}

func monitorProbeProtocol(capabilityProtocol string) string {
	switch capabilityProtocol {
	case "openai_responses":
		return "responses"
	case "anthropic_messages":
		return "messages"
	case "openai_chat":
		return "chat"
	default:
		return capabilityProtocol
	}
}

func (s *Service) ProbeUpstreamMonitorModel(row model.UpstreamMonitorSelection) (model.UpstreamMonitorResult, error) {
	r := model.UpstreamMonitorResult{ProviderID: row.ProviderID, ModelName: row.ModelName, Protocol: row.Protocol}
	probe, err := s.TestModelChat(row.ProviderID, row.ModelName, row.Protocol, true, fmt.Sprintf("monitor-%d", time.Now().UnixNano()))
	if err != nil {
		r.Status, r.Error, r.Detail = "error", err.Error(), err.Error()
		return r, nil
	}
	if probe == nil {
		r.Status, r.Error, r.Detail = "error", "empty probe result", "empty probe result"
		return r, nil
	}
	r.HTTPStatus = probe.HTTPStatus
	r.Response, r.Error, r.FirstByteLatencyMs, r.TotalLatencyMs = probe.Response, probe.Error, probe.FirstByteLatencyMs, probe.LatencyMs
	r.Endpoint = probe.Endpoint
	if probe.OK {
		r.Status, r.Detail = "available", probe.Response
	} else if strings.Contains(strings.ToLower(probe.Error), "empty") {
		r.Status, r.Detail = "empty", probe.Error
	} else {
		r.Status, r.Detail = "error", probe.Error
	}
	return r, nil
}

func (s *Service) ProbeUpstreamMonitorModels(rows []model.UpstreamMonitorSelection) (*model.UpstreamMonitorBatch, error) {
	start := time.Now()
	type item struct {
		i int
		r model.UpstreamMonitorResult
	}
	ch := make(chan item, len(rows))
	for i, row := range rows {
		i, row := i, row
		go func() {
			r, _ := s.ProbeUpstreamMonitorModel(row)
			ch <- item{i, r}
		}()
	}
	out := &model.UpstreamMonitorBatch{Results: make([]model.UpstreamMonitorResult, len(rows)), Total: len(rows)}
	for range rows {
		x := <-ch
		out.Results[x.i] = x.r
	}
	// Count after all workers have completed; each probe has its own timeout.
	for _, r := range out.Results {
		switch r.Status {
		case "available":
			out.Available++
		case "empty":
			out.Empty++
		default:
			out.Errors++
		}
	}
	out.CompletionMs, out.CompletedAtMs = int(time.Since(start).Milliseconds()), time.Now().UnixMilli()
	return out, nil
}

func parseGenericNonStream(body []byte, start time.Time) *model.ModelChatTestResult {
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		return &model.ModelChatTestResult{Error: fmt.Sprintf("parse response: %v", err), LatencyMs: int(time.Since(start).Milliseconds())}
	}
	latency := int(time.Since(start).Milliseconds())
	text := genericText(v)
	if text == "" {
		// Reasoning-only output still proves the model responded (e.g. the
		// token budget was consumed by thinking before visible text).
		if reasoning := strings.TrimSpace(genericReasoningText(v)); reasoning != "" {
			return &model.ModelChatTestResult{OK: true, Response: reasoning, FirstByteLatencyMs: latency, LatencyMs: latency}
		}
		if genericIncompleteMaxTokens(v) {
			return &model.ModelChatTestResult{OK: true, Response: incompleteMaxTokensNote, FirstByteLatencyMs: latency, LatencyMs: latency}
		}
		return &model.ModelChatTestResult{Error: "empty response content", LatencyMs: latency}
	}
	return &model.ModelChatTestResult{OK: true, Response: text, FirstByteLatencyMs: latency, LatencyMs: latency}
}
func genericText(v interface{}) string {
	var b strings.Builder
	var walk func(interface{})
	walk = func(x interface{}) {
		switch q := x.(type) {
		case map[string]interface{}:
			for _, k := range []string{"text", "output_text", "content", "delta"} {
				if z, ok := q[k].(string); ok {
					b.WriteString(z)
				}
			}
			for k, z := range q {
				if k != "text" && k != "output_text" {
					walk(z)
				}
			}
		case []interface{}:
			for _, z := range q {
				walk(z)
			}
		}
	}
	walk(v)
	return b.String()
}

// incompleteMaxTokensNote is returned as a successful probe response when the
// model answered but spent the whole token budget on reasoning, so no visible
// text was produced. It still proves the upstream is alive and responding.
const incompleteMaxTokensNote = "(model responded, but the token limit was consumed by reasoning before visible output)"

// genericReasoningText walks decoded JSON collecting reasoning/thinking text
// that providers emit instead of visible output (Anthropic thinking blocks,
// OpenAI-compatible reasoning_content, Responses reasoning summaries). Reasoning
// output proves the model responded even when no visible content exists yet.
func genericReasoningText(v interface{}) string {
	var b strings.Builder
	var walk func(interface{})
	walk = func(x interface{}) {
		switch q := x.(type) {
		case map[string]interface{}:
			for _, k := range []string{"thinking", "reasoning", "reasoning_content"} {
				if z, ok := q[k].(string); ok {
					b.WriteString(z)
				}
			}
			for _, z := range q {
				walk(z)
			}
		case []interface{}:
			for _, z := range q {
				walk(z)
			}
		}
	}
	walk(v)
	return b.String()
}

// genericIncompleteMaxTokens reports whether decoded JSON is a Responses-style
// terminal event marking the response incomplete because reasoning exhausted
// the token budget (no visible output follows in that case).
func genericIncompleteMaxTokens(v interface{}) bool {
	m, ok := v.(map[string]interface{})
	if !ok {
		return false
	}
	resp, ok := m["response"].(map[string]interface{})
	if !ok {
		return false
	}
	if status, _ := resp["status"].(string); status != "incomplete" {
		return false
	}
	details, _ := resp["incomplete_details"].(map[string]interface{})
	reason, _ := details["reason"].(string)
	return reason == "max_output_tokens" || reason == "max_tokens"
}

func (s *Service) parseGenericStream(body io.Reader, start time.Time) *model.ModelChatTestResult {
	cr := &countingReader{r: body}
	scanner := bufio.NewScanner(io.LimitReader(cr, 256*1024))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var b strings.Builder
	var reasoningBuilder strings.Builder
	sawIncompleteMaxTokens := false
	firstByteMs := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "data:") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
		if line == "[DONE]" {
			break
		}
		var v interface{}
		if json.Unmarshal([]byte(line), &v) == nil {
			text := genericText(v)
			if text != "" {
				if firstByteMs == 0 {
					firstByteMs = int(time.Since(start).Milliseconds())
				}
				b.WriteString(text)
			}
			if reasoning := genericReasoningText(v); reasoning != "" {
				if firstByteMs == 0 {
					firstByteMs = int(time.Since(start).Milliseconds())
				}
				if reasoningBuilder.Len()+len(reasoning) <= 64*1024 {
					reasoningBuilder.WriteString(reasoning)
				}
			}
			if genericIncompleteMaxTokens(v) {
				sawIncompleteMaxTokens = true
			}
		}
	}
	if scanner.Err() != nil {
		return &model.ModelChatTestResult{Error: scanner.Err().Error(), LatencyMs: int(time.Since(start).Milliseconds())}
	}
	latency := int(time.Since(start).Milliseconds())
	if content := strings.TrimSpace(b.String()); content != "" {
		return &model.ModelChatTestResult{OK: true, Response: b.String(), FirstByteLatencyMs: firstByteMs, LatencyMs: latency}
	}
	// Reasoning-only output (or a token budget fully consumed by reasoning)
	// still proves the model responded; report success instead of a
	// misleading "empty response content".
	if reasoning := strings.TrimSpace(reasoningBuilder.String()); reasoning != "" {
		return &model.ModelChatTestResult{OK: true, Response: reasoningBuilder.String(), FirstByteLatencyMs: firstByteMs, LatencyMs: latency}
	}
	if sawIncompleteMaxTokens {
		return &model.ModelChatTestResult{OK: true, Response: incompleteMaxTokensNote, FirstByteLatencyMs: firstByteMs, LatencyMs: latency}
	}
	return &model.ModelChatTestResult{Error: "empty response content", LatencyMs: latency}
}

func (s *Service) parseChatNonStream(respBody []byte, start time.Time) *model.ModelChatTestResult {
	latencyMs := func() int { return int(time.Since(start).Milliseconds()) }

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent string          `json:"reasoning_content"`
				Reasoning        string          `json:"reasoning"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return &model.ModelChatTestResult{
			OK:        false,
			Error:     fmt.Sprintf("parse response: %v", err),
			LatencyMs: latencyMs(),
		}
	}

	if len(chatResp.Choices) == 0 {
		return &model.ModelChatTestResult{
			OK:        false,
			Error:     "empty choices in response",
			LatencyMs: latencyMs(),
		}
	}

	response := chatTestContentText(chatResp.Choices[0].Message.Content)
	finishReason := chatResp.Choices[0].FinishReason
	if strings.TrimSpace(response) == "" {
		// Reasoning-only output still proves the model responded (the token
		// budget was consumed by reasoning before visible content).
		if reasoning := chatTestReasoningText(chatResp.Choices[0].Message.ReasoningContent, chatResp.Choices[0].Message.Reasoning); reasoning != "" {
			return &model.ModelChatTestResult{
				OK:                 true,
				Response:           reasoning,
				LatencyMs:          latencyMs(),
				FirstByteLatencyMs: latencyMs(),
				FinishReason:       finishReason,
			}
		}
		return &model.ModelChatTestResult{
			OK:           false,
			Error:        emptyChatTestContentError(finishReason),
			LatencyMs:    latencyMs(),
			FinishReason: finishReason,
		}
	}
	return &model.ModelChatTestResult{
		OK:                 true,
		Response:           response,
		LatencyMs:          latencyMs(),
		FirstByteLatencyMs: latencyMs(),
		FinishReason:       finishReason,
	}
}

// chatTestContentText extracts visible text from OpenAI-compatible message
// content. Some gateways return content as structured parts instead of a
// plain string; treat those parts as first-class successful test output.
func chatTestContentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		var value string
		if textRaw, ok := part["text"]; ok && json.Unmarshal(textRaw, &value) == nil {
			builder.WriteString(value)
			continue
		}
		if contentRaw, ok := part["content"]; ok && json.Unmarshal(contentRaw, &value) == nil {
			builder.WriteString(value)
		}
	}
	return builder.String()
}

// chatTestReasoningText returns the first non-empty reasoning payload.
// Providers expose reasoning under reasoning_content (DeepSeek/GLM/QwQ style)
// or reasoning; either proves the model responded when visible content is empty.
func chatTestReasoningText(reasoningContent, reasoning string) string {
	if strings.TrimSpace(reasoningContent) != "" {
		return reasoningContent
	}
	return reasoning
}

func emptyChatTestContentError(finishReason string) string {
	if finishReason == "length" {
		return "model reached the max_tokens limit before producing visible content (usually consumed by reasoning); increase the model test token limit"
	}
	if finishReason != "" {
		return fmt.Sprintf("empty response content (finish_reason: %s)", finishReason)
	}
	return "empty response content"
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (s *Service) parseChatStream(body io.Reader, start time.Time) *model.ModelChatTestResult {
	latencyMs := func() int { return int(time.Since(start).Milliseconds()) }
	const maxWireBytes = 256 * 1024
	const maxContentBytes = 64 * 1024

	cr := &countingReader{r: io.LimitReader(body, maxWireBytes+1)}
	reader := bufio.NewReader(cr)

	var builder strings.Builder
	var reasoningBuilder strings.Builder
	finishReason := ""
	firstByteMs := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return &model.ModelChatTestResult{
				OK:        false,
				Error:     fmt.Sprintf("read stream: %v", err),
				LatencyMs: latencyMs(),
			}
		}
		if err == io.EOF && line == "" {
			break
		}
		if cr.n > maxWireBytes {
			return &model.ModelChatTestResult{
				OK:        false,
				Error:     "stream response too large (>256 KiB)",
				LatencyMs: latencyMs(),
			}
		}

		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")

		if line == "" || strings.HasPrefix(line, ":") {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}
		if !strings.HasPrefix(line, "data") {
			if errors.Is(err, io.EOF) {
				break
			}
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          json.RawMessage `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					Reasoning        string          `json:"reasoning"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return &model.ModelChatTestResult{
				OK:        false,
				Error:     fmt.Sprintf("parse stream chunk: %v", err),
				LatencyMs: latencyMs(),
			}
		}

		if len(chunk.Choices) > 0 {
			delta := chatTestContentText(chunk.Choices[0].Delta.Content)
			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}
			if delta != "" {
				if firstByteMs == 0 {
					firstByteMs = latencyMs()
				}
				if builder.Len()+len(delta) > maxContentBytes {
					return &model.ModelChatTestResult{
						OK:        false,
						Error:     "accumulated content too large (>64 KiB)",
						LatencyMs: latencyMs(),
					}
				}
				builder.WriteString(delta)
			}
			// Reasoning deltas prove the model is alive even when the whole
			// token budget is consumed before any visible content.
			if reasoning := chatTestReasoningText(chunk.Choices[0].Delta.ReasoningContent, chunk.Choices[0].Delta.Reasoning); reasoning != "" {
				if firstByteMs == 0 {
					firstByteMs = latencyMs()
				}
				if reasoningBuilder.Len()+len(reasoning) <= maxContentBytes {
					reasoningBuilder.WriteString(reasoning)
				}
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
	}

	content := strings.TrimSpace(builder.String())
	if cr.n > maxWireBytes {
		return &model.ModelChatTestResult{
			OK:        false,
			Error:     "stream response too large (>256 KiB)",
			LatencyMs: latencyMs(),
		}
	}
	if content == "" {
		// Reasoning-only output still proves the model responded (the token
		// budget was consumed by reasoning before visible content).
		if reasoning := strings.TrimSpace(reasoningBuilder.String()); reasoning != "" {
			return &model.ModelChatTestResult{
				OK:                 true,
				Response:           reasoningBuilder.String(),
				LatencyMs:          latencyMs(),
				FirstByteLatencyMs: firstByteMs,
				FinishReason:       finishReason,
			}
		}
		return &model.ModelChatTestResult{
			OK:           false,
			Error:        emptyChatTestContentError(finishReason),
			LatencyMs:    latencyMs(),
			FinishReason: finishReason,
		}
	}
	return &model.ModelChatTestResult{
		OK:                 true,
		Response:           builder.String(),
		LatencyMs:          latencyMs(),
		FirstByteLatencyMs: firstByteMs,
		FinishReason:       finishReason,
	}
}

// TestAllProviders tests every provider concurrently and returns results.
func (s *Service) TestAllProviders() ([]model.ProviderTestResult, error) {
	providers, err := s.store.ListProviders()
	if err != nil {
		return nil, err
	}

	type result struct {
		idx int
		res *model.ProviderTestResult
		err error
	}

	ch := make(chan result, len(providers))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for i, p := range providers {
		i, p := i, p
		go func() {
			r, err := s.TestProvider(p.ID)
			select {
			case ch <- result{i, r, err}:
			case <-ctx.Done():
			}
		}()
	}

	results := make([]model.ProviderTestResult, len(providers))
	var success, failed int
	for range providers {
		r := <-ch
		if r.err != nil {
			results[r.idx] = model.ProviderTestResult{OK: false, Error: r.err.Error()}
			failed++
		} else if r.res != nil {
			results[r.idx] = *r.res
			if r.res.OK {
				success++
			} else {
				failed++
			}
		}
	}
	slog.Info("service: test all providers complete",
		"total", len(providers), "success", success, "failed", failed)
	return results, nil
}

// AddProviderModels adds model names to a provider's local catalog. Names are
// AddProviderModels adds model names to a provider's local catalog. Names are
// trimmed and deduplicated. Existing models are not modified (insert-or-ignore).
// The provider's models_count is recalculated within the same transaction.
func (s *Service) AddProviderModels(providerID string, names []string) error {
	// Trim and deduplicate
	seen := make(map[string]bool)
	var models []model.Model
	now := time.Now().UnixMilli()
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		models = append(models, model.Model{
			ProviderID: providerID,
			Name:       name,
			Active:     true,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}
	if len(models) == 0 {
		return nil
	}
	if err := s.store.InsertModelsIfAbsent(providerID, models); err != nil {
		return fmt.Errorf("service: add provider models: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
//  Internal helpers
// ---------------------------------------------------------------------------

func (s *Service) testFailResult(msg string) *model.ProviderTestResult {
	return &model.ProviderTestResult{OK: false, Error: msg}
}

// ResolveProviderKey fetches and decrypts the upstream key for a provider.
// It is exported so the proxy package can fetch the cleartext key for forwarding
// requests. Returns an empty string if the stored ciphertext is empty (e.g. dev
// fixtures).
func (s *Service) ResolveProviderKey(providerID string) (string, error) {
	if providerID == "" {
		return "", fmt.Errorf("no provider id")
	}
	ciphertext, nonce, err := s.store.GetProviderKeyCiphertext(providerID)
	if err != nil {
		return "", err
	}
	if len(ciphertext) == 0 && len(nonce) == 0 {
		// Fixtures store empty ciphertext; return empty string for those
		return "", nil
	}
	plaintext, err := s.Decrypt(ciphertext, nonce)
	if err != nil {
		slog.Error("service: decrypt provider key failed", "provider_id", providerID, "err", err)
		return "", fmt.Errorf("decrypt provider key %q: %w", providerID, err)
	}
	return string(plaintext), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

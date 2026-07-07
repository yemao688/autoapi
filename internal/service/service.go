// Package service implements the business logic layer (api.BusinessService).
// It depends on the concrete *store.Store for persistence operations that
// extend beyond the api.StoreService interface (e.g., UpsertModels,
// GetProviderKeyCiphertext).
package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/store"

	"github.com/shirou/gopsutil/v4/process"
)

// Service implements api.BusinessService.
type Service struct {
	store *store.Store
	proxy ProxyRef

	encKey    []byte // 32-byte AES key; loaded at construction from keyDir
	startedAt time.Time
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

	return &Service{
		store:     s,
		proxy:     proxy,
		encKey:    encKey,
		startedAt: time.Now(),
	}
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

	return &model.ServiceHealth{
		Status:            status,
		UptimeSeconds:     int64(time.Since(s.startedAt).Seconds()),
		MemoryMB:          int(ms.Alloc / 1024 / 1024),
		CPUPercent:        cpuPercent(),
		ActiveConnections: activeConns,
		WebSocketCount:    0, // v1 does not expose WebSocket endpoints; hide this field in UI if desired
		HTTPCount:         activeConns,
		ProxyURL:          proxyURL,
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
	models, err := s.FetchUpstreamModels(providerID)
	if err != nil {
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
	if err := s.store.UpdateProviderTestResult(providerID, status, len(models), latencyMs, ""); err != nil {
		return nil, fmt.Errorf("service: update provider test result: %w", err)
	}

	return &model.ProviderTestResult{
		OK:        true,
		LatencyMs: latencyMs,
		Models:    modelNames,
	}, nil
}

// FetchUpstreamModels loads a provider's upstream model list from /v1/models,
// upserts them into the local database (preserving active/latency state), and
// returns the persisted list.
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
	req, err := http.NewRequest("GET", prov.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("HTTP error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	// Parse OpenAI-style model list
	var modelsResp struct {
		Data []struct {
			ID           string `json:"id"`
			OwnedBy      string `json:"owned_by"`
			ContextWindow int   `json:"context_window"`
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

	if err := s.store.UpsertModels(providerID, models); err != nil {
		return nil, fmt.Errorf("service: upsert models: %w", err)
	}

	return s.store.ListModels(providerID)
}

// TestModelLatency measures the API latency for a single model by sending a
// tiny chat completion request. This gives a per-model latency rather than the
// coarse latency of the provider's /v1/models list. For embedding-only or
// non-chat models the provider may return an error; the caller should treat
// that as a probe failure.
func (s *Service) TestModelLatency(providerID, modelName string) (*model.ModelTestResult, error) {
	prov, err := s.store.GetProvider(providerID)
	if err != nil {
		return &model.ModelTestResult{OK: false, Error: err.Error()}, nil
	}

	upstreamKey, err := s.ResolveProviderKey(providerID)
	if err != nil {
		return &model.ModelTestResult{OK: false, Error: err.Error()}, nil
	}

	payload := map[string]interface{}{
		"model": modelName,
		"messages": []map[string]string{
			{"role": "user", "content": "ping"},
		},
		"max_tokens": 1,
	}
	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("service: marshal latency probe body: %w", err)
	}

	start := time.Now()
	req, err := http.NewRequest("POST", prov.BaseURL+"/v1/chat/completions", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("service: create latency probe request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return &model.ModelTestResult{OK: false, Error: err.Error()}, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return &model.ModelTestResult{OK: false, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}, nil
	}

	if err := s.store.UpdateModelLatency(providerID, modelName, latencyMs); err != nil {
		return nil, fmt.Errorf("service: update model latency: %w", err)
	}

	return &model.ModelTestResult{OK: true, LatencyMs: latencyMs}, nil
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
	for range providers {
		r := <-ch
		if r.err != nil {
			results[r.idx] = model.ProviderTestResult{OK: false, Error: r.err.Error()}
		} else if r.res != nil {
			results[r.idx] = *r.res
		}
	}
	return results, nil
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
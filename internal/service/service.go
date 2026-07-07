// Package service implements the business logic layer (api.BusinessService).
// It depends on the concrete *store.Store for persistence operations that
// extend beyond the api.StoreService interface (e.g., UpsertModels,
// GetProviderKeyCiphertext).
package service

import (
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
		CPUPercent:        0.0, // TODO: CPU on macOS requires gopsutil; keep 0 for v1
		ActiveConnections: activeConns,
		WebSocketCount:    0, // not implemented in v1
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

// TestProvider tests connectivity with a single provider by hitting its
// /v1/models endpoint, parsing the response, and updating the model list.
func (s *Service) TestProvider(providerID string) (*model.ProviderTestResult, error) {
	prov, err := s.store.GetProvider(providerID)
	if err != nil {
		return nil, err
	}

	// Get decrypted upstream provider key.
	upstreamKey, err := s.ResolveProviderKey(prov.ID)
	if err != nil {
		return &model.ProviderTestResult{
			OK:    false,
			Error: fmt.Sprintf("resolve provider key: %v", err),
		}, nil
	}

	start := time.Now()
	req, err := http.NewRequest("GET", prov.BaseURL+"/v1/models", nil)
	if err != nil {
		return s.testFailResult("create request: " + err.Error()), nil
	}
	req.Header.Set("Authorization", "Bearer "+upstreamKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	latencyMs := int(time.Since(start).Milliseconds())
	if err != nil {
		return s.testFailResult(fmt.Sprintf("HTTP error: %v", err)), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return s.testFailResult(fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))), nil
	}

	// Parse OpenAI-style model list
	var modelsResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return s.testFailResult("parse response: " + err.Error()), nil
	}

	modelNames := make([]string, len(modelsResp.Data))
	for i, m := range modelsResp.Data {
		modelNames[i] = m.ID
	}

	// Upsert models in store
	if err := s.store.UpsertModels(providerID, modelNames); err != nil {
		return nil, fmt.Errorf("service: upsert models: %w", err)
	}

	// Update provider test result
	status := model.ProviderStatusConnected
	if err := s.store.UpdateProviderTestResult(providerID, status, len(modelNames), latencyMs, ""); err != nil {
		return nil, fmt.Errorf("service: update provider test result: %w", err)
	}

	return &model.ProviderTestResult{
		OK:        true,
		LatencyMs: latencyMs,
		Models:    modelNames,
	}, nil
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
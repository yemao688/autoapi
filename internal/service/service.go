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
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sync"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/store"

	"golang.org/x/crypto/argon2"
)

// Service implements api.BusinessService.
type Service struct {
	store *store.Store
	proxy ProxyRef

	mu        sync.RWMutex
	encKey    []byte // 32-byte AES key; nil until Unlock succeeds
	startedAt time.Time
}

// ProxyRef is the minimal proxy surface needed to compute live system health.
type ProxyRef interface {
	IsRunning() bool
	URL() string
	ActiveConnections() int
}

// New creates a BusinessService with the given store and proxy reference.
func New(s *store.Store, proxy ProxyRef) *Service {
	return &Service{
		store:     s,
		proxy:     proxy,
		startedAt: time.Now(),
	}
}

// SetProxy updates the proxy reference after the proxy has been created. This is
// used by the composition root to break the service→proxy→service cycle.
func (s *Service) SetProxy(proxy ProxyRef) {
	s.proxy = proxy
}

// ---------------------------------------------------------------------------
//  Master password
// ---------------------------------------------------------------------------

// argon2Params defines the hashing parameters.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MB
	argonThreads = 4
	argonKeyLen  = 64 // 32 bytes verification + 32 bytes encryption key
	saltLen      = 16
)

// SetMasterPassword creates the initial master password. The password is
// hashed with argon2id; the first 32 bytes of the derived key are stored as
// the verification hash, the last 32 bytes become the in-memory encryption
// key. Fails if a master password already exists.
func (s *Service) SetMasterPassword(password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	has, err := s.hasMasterPassword()
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("service: master password already set")
	}

	salt, key := deriveKey(password)
	hash := key[:32]
	encKey := key[32:]

	if err := s.storeSetMasterPassword(salt, hash); err != nil {
		return err
	}
	s.encKey = make([]byte, 32)
	copy(s.encKey, encKey)
	return nil
}

// ChangeMasterPassword verifies the old password and replaces it with a new
// one. All existing provider upstream keys are re-encrypted with the new key.
func (s *Service) ChangeMasterPassword(old, new string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify old password
	salt, storedHash, err := s.storeGetMasterPassword()
	if err != nil {
		return err
	}
	oldKey := argon2.IDKey([]byte(old), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if subtle.ConstantTimeCompare(oldKey[:32], storedHash) != 1 {
		return fmt.Errorf("service: wrong master password")
	}

	// Re-encrypt all provider upstream keys with the new key.
	newSalt, newKey := deriveKey(new)
	newHash := newKey[:32]
	newEncKey := newKey[32:]

	providers, err := s.store.ListProviders()
	if err != nil {
		return err
	}
	for _, prov := range providers {
		ciphertext, nonce, err := s.store.GetProviderKeyCiphertext(prov.ID)
		if err != nil {
			continue // skip providers with no stored key
		}
		if len(ciphertext) == 0 && len(nonce) == 0 {
			continue // fixtures / providers without keys
		}
		// Decrypt with old key
		block, err := aes.NewCipher(oldKey[32:])
		if err != nil {
			return err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			// If we can't decrypt a key with the old password, skip it
			// (it might be a fixture with dummy ciphertext)
			continue
		}
		// Re-encrypt with new key
		newNonce := make([]byte, 12)
		if _, err := io.ReadFull(rand.Reader, newNonce); err != nil {
			return err
		}
		newBlock, _ := aes.NewCipher(newEncKey)
		newGCM, _ := cipher.NewGCM(newBlock)
		newCT := newGCM.Seal(nil, newNonce, plaintext, nil)

		if err := s.store.UpdateProviderKeyCiphertext(prov.ID, newCT, newNonce, prov.KeyMasked); err != nil {
			return fmt.Errorf("service: re-encrypt provider key %q: %w", prov.ID, err)
		}
	}

	// Store new master password
	if err := s.storeSetMasterPassword(newSalt, newHash); err != nil {
		return err
	}
	s.encKey = make([]byte, 32)
	copy(s.encKey, newEncKey)
	return nil
}

// HasMasterPassword returns whether a master password has been configured.
func (s *Service) HasMasterPassword() bool {
	ok, _ := s.hasMasterPassword()
	return ok
}

// Unlock verifies the password and derives the encryption key in memory.
func (s *Service) Unlock(password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	salt, storedHash, err := s.storeGetMasterPassword()
	if err != nil {
		return err
	}

	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if subtle.ConstantTimeCompare(key[:32], storedHash) != 1 {
		return fmt.Errorf("service: wrong master password")
	}

	s.encKey = make([]byte, 32)
	copy(s.encKey, key[32:])
	return nil
}

// IsUnlocked returns true if the encryption key is present in memory.
func (s *Service) IsUnlocked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.encKey != nil
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

// Encrypt encrypts plaintext using AES-256-GCM with the current in-memory key.
// Returns (ciphertext, nonce, error).
func (s *Service) Encrypt(plaintext []byte) ([]byte, []byte, error) {
	s.mu.RLock()
	key := s.encKey
	s.mu.RUnlock()
	if key == nil {
		return nil, nil, fmt.Errorf("service: not unlocked")
	}

	block, err := aes.NewCipher(key)
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

// Decrypt decrypts ciphertext using AES-256-GCM with the current in-memory key.
func (s *Service) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	s.mu.RLock()
	key := s.encKey
	s.mu.RUnlock()
	if key == nil {
		return nil, fmt.Errorf("service: not unlocked")
	}

	block, err := aes.NewCipher(key)
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

func deriveKey(password string) (salt []byte, key []byte) {
	salt = make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		panic(fmt.Sprintf("service: salt generation failed: %v", err))
	}
	key = argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return
}

func (s *Service) hasMasterPassword() (bool, error) {
	var count int
	err := s.store.RawDB().QueryRow(`SELECT COUNT(*) FROM master_password`).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *Service) storeSetMasterPassword(salt, hash []byte) error {
	now := time.Now().UnixMilli()
	return s.store.ExecRaw(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO master_password (id, salt, hash, updated_at) VALUES (1, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET salt=excluded.salt, hash=excluded.hash, updated_at=excluded.updated_at`,
			salt, hash, now)
		return err
	})
}

func (s *Service) storeGetMasterPassword() (salt, hash []byte, err error) {
	row := s.store.RawDB().QueryRow(`SELECT salt, hash FROM master_password WHERE id = 1`)
	if err := row.Scan(&salt, &hash); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, fmt.Errorf("service: master password not set")
		}
		return nil, nil, err
	}
	return
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

package store

import (
	"database/sql"
	"fmt"

	"autoapi/internal/model"
)

// ListAPIKeys returns every API key. Ciphertext is never exposed.
func (s *Store) ListAPIKeys() ([]model.ApiKey, error) {
	rows, err := s.db.Query(`
		SELECT id, provider_id, name, key_prefix, key_suffix,
		       permission, environment, monthly_tokens,
		       last_used_at, expires_at, created_at, updated_at
		FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	defer rows.Close()

	var out []model.ApiKey
	for rows.Next() {
		var k model.ApiKey
		var providerID sql.NullString
		if err := rows.Scan(
			&k.ID, &providerID, &k.Name, &k.KeyPrefix, &k.KeySuffix,
			&k.Permission, &k.Environment, &k.MonthlyTokens,
			&k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan api key: %w", err)
		}
		k.ProviderID = providerID.String
		k.KeyMasked = k.KeyPrefix + "****" + k.KeySuffix
		out = append(out, k)
	}
	if out == nil {
		out = []model.ApiKey{}
	}
	return out, rows.Err()
}

// maskKey produces the prefix/suffix/masked fields shown in the UI.
// Pure function — exported so the App layer can apply it before the
// ciphertext reaches the store.
func maskKey(plaintext string) (prefix, suffix, masked string) {
	if len(plaintext) > 12 {
		prefix = plaintext[:12]
	} else {
		prefix = plaintext
	}
	if len(plaintext) > 4 {
		suffix = plaintext[len(plaintext)-4:]
	} else {
		suffix = plaintext
	}
	masked = prefix + "****" + suffix
	return
}

// CreateAPIKeyCiphertext is the real insertion path: the caller (App layer)
// has already encrypted the cleartext key and computed the display mask; the
// store persists only opaque bytes plus the pre-computed mask.
func (s *Store) CreateAPIKeyCiphertext(in model.ApiKeyInput, ciphertext, nonce []byte) (*model.ApiKey, error) {
	now := nowMs()
	id := makeID()

	prefix, suffix, masked := maskKey(in.Key)

	k := &model.ApiKey{
		ID:          id,
		ProviderID:  in.ProviderID,
		Name:        in.Name,
		KeyPrefix:   prefix,
		KeySuffix:   suffix,
		KeyMasked:   masked,
		Permission:  in.Permission,
		Environment: in.Environment,
		ExpiresAt:   in.ExpiresAt,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO api_keys (id, provider_id, name, key_prefix, key_suffix,
			                      key_ciphertext, key_nonce,
			                      permission, environment, monthly_tokens,
			                      last_used_at, expires_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?,
			        ?, ?,
			        ?, ?, 0,
			        0, ?, ?, ?)`,
			k.ID, nullString(k.ProviderID), k.Name, k.KeyPrefix, k.KeySuffix,
			ciphertext, nonce,
			k.Permission, k.Environment,
			k.ExpiresAt, k.CreatedAt, k.UpdatedAt)
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: create api key: %w", err)
	}
	return k, nil
}

// UpdateAPIKeyCiphertext updates an existing API key with new ciphertext.
func (s *Store) UpdateAPIKeyCiphertext(id string, in model.ApiKeyInput, ciphertext, nonce []byte) (*model.ApiKey, error) {
	now := nowMs()

	prefix, suffix, _ := maskKey(in.Key)

	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE api_keys SET provider_id=?, name=?, key_prefix=?, key_suffix=?,
			                    key_ciphertext=?, key_nonce=?,
			                    permission=?, environment=?, expires_at=?, updated_at=?
			WHERE id=?`,
			nullString(in.ProviderID), in.Name, prefix, suffix,
			ciphertext, nonce,
			in.Permission, in.Environment, in.ExpiresAt, now, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update api key %q: %w", id, ErrNotFound)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.getAPIKeyByID(id)
}

// DeleteAPIKey removes an API key by ID.
func (s *Store) DeleteAPIKey(id string) error {
	return s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete api key: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: delete api key %q: %w", id, ErrNotFound)
		}
		return nil
	})
}

// GetAPIKeyCiphertext returns the ciphertext+nonce for decryption by the
// service layer. Not part of the StoreService interface.
func (s *Store) GetAPIKeyCiphertext(id string) (ciphertext, nonce []byte, providerID string, err error) {
	row := s.db.QueryRow(`
		SELECT key_ciphertext, key_nonce, COALESCE(provider_id, '')
		FROM api_keys WHERE id = ?`, id)
	if err := row.Scan(&ciphertext, &nonce, &providerID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, "", fmt.Errorf("store: get api key %q: %w", id, ErrNotFound)
		}
		return nil, nil, "", fmt.Errorf("store: get api key %q: %w", id, err)
	}
	return
}

// getAPIKeyByID returns a single key row (without ciphertext) for internal use.
func (s *Store) getAPIKeyByID(id string) (*model.ApiKey, error) {
	row := s.db.QueryRow(`
		SELECT id, COALESCE(provider_id,''), name, key_prefix, key_suffix,
		       permission, environment, monthly_tokens,
		       last_used_at, expires_at, created_at, updated_at
		FROM api_keys WHERE id = ?`, id)

	var k model.ApiKey
	if err := row.Scan(
		&k.ID, &k.ProviderID, &k.Name, &k.KeyPrefix, &k.KeySuffix,
		&k.Permission, &k.Environment, &k.MonthlyTokens,
		&k.LastUsedAt, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get api key %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get api key %q: %w", id, err)
	}
	k.KeyMasked = k.KeyPrefix + "****" + k.KeySuffix
	return &k, nil
}

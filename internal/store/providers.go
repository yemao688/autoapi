package store

import (
	"database/sql"
	"fmt"

	"autoapi/internal/model"
)

// ListProviders returns every provider ordered by name.
func (s *Store) ListProviders() ([]model.Provider, error) {
	rows, err := s.db.Query(`
		SELECT id, name, base_url, status, api_key_id, api_key_ref,
		       models_count, monthly_tokens, avg_latency_ms,
		       last_tested_at, error_message, is_custom,
		       created_at, updated_at
		FROM providers ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list providers: %w", err)
	}
	defer rows.Close()

	var out []model.Provider
	for rows.Next() {
		var p model.Provider
		var apiKeyID, apiKeyRef sql.NullString
		if err := rows.Scan(
			&p.ID, &p.Name, &p.BaseURL, &p.Status, &apiKeyID, &apiKeyRef,
			&p.ModelsCount, &p.MonthlyTokens, &p.AvgLatencyMs,
			&p.LastTestedAt, &p.ErrorMessage, &p.IsCustom,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan provider: %w", err)
		}
		p.APIKeyID = apiKeyID.String
		p.APIKeyRef = apiKeyRef.String
		out = append(out, p)
	}
	if out == nil {
		out = []model.Provider{}
	}
	return out, rows.Err()
}

// GetProvider returns a single provider by ID.
func (s *Store) GetProvider(id string) (*model.Provider, error) {
	row := s.db.QueryRow(`
		SELECT id, name, base_url, status, api_key_id, api_key_ref,
		       models_count, monthly_tokens, avg_latency_ms,
		       last_tested_at, error_message, is_custom,
		       created_at, updated_at
		FROM providers WHERE id = ?`, id)

	var p model.Provider
	var apiKeyID, apiKeyRef sql.NullString
	if err := row.Scan(
		&p.ID, &p.Name, &p.BaseURL, &p.Status, &apiKeyID, &apiKeyRef,
		&p.ModelsCount, &p.MonthlyTokens, &p.AvgLatencyMs,
		&p.LastTestedAt, &p.ErrorMessage, &p.IsCustom,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get provider %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get provider %q: %w", id, err)
	}
	p.APIKeyID = apiKeyID.String
	p.APIKeyRef = apiKeyRef.String
	return &p, nil
}

// CreateProvider inserts a new provider and returns the full entity.
func (s *Store) CreateProvider(in model.ProviderInput) (*model.Provider, error) {
	now := nowMs()
	id := makeID()

	// Derive key ref from the associated API key if set.
	keyRef := ""
	if in.APIKeyID != "" {
		keyRef = s.resolveAPIKeyRef(in.APIKeyID)
	}

	p := &model.Provider{
		ID:        id,
		Name:      in.Name,
		BaseURL:   in.BaseURL,
		Status:    model.ProviderStatusUnknown,
		APIKeyID:  in.APIKeyID,
		APIKeyRef: keyRef,
		IsCustom:  in.IsCustom,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO providers (id, name, base_url, status, api_key_id, api_key_ref,
			                       models_count, monthly_tokens, avg_latency_ms,
			                       last_tested_at, error_message, is_custom,
			                       created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?,
			        0, 0, 0,
			        0, '', ?, ?, ?)`,
			p.ID, p.Name, p.BaseURL, p.Status, nullString(p.APIKeyID), nullString(p.APIKeyRef),
			boolInt(p.IsCustom), p.CreatedAt, p.UpdatedAt)
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: create provider: %w", err)
	}
	return p, nil
}

// UpdateProvider modifies an existing provider.
func (s *Store) UpdateProvider(id string, in model.ProviderInput) (*model.Provider, error) {
	now := nowMs()

	keyRef := ""
	if in.APIKeyID != "" {
		keyRef = s.resolveAPIKeyRef(in.APIKeyID)
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE providers SET name=?, base_url=?, api_key_id=?, api_key_ref=?,
			                     is_custom=?, updated_at=?
			WHERE id=?`,
			in.Name, in.BaseURL, nullString(in.APIKeyID), nullString(keyRef),
			boolInt(in.IsCustom), now, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("store: update provider %q: %w", id, ErrNotFound)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.GetProvider(id)
}

// DeleteProvider removes a provider by ID (models cascade).
func (s *Store) DeleteProvider(id string) error {
	return s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM providers WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete provider: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: delete provider %q: %w", id, ErrNotFound)
		}
		return nil
	})
}

// resolveAPIKeyRef returns the masked display string for an API key.
func (s *Store) resolveAPIKeyRef(keyID string) string {
	var prefix, suffix string
	err := s.db.QueryRow(`SELECT key_prefix, key_suffix FROM api_keys WHERE id = ?`, keyID).Scan(&prefix, &suffix)
	if err != nil {
		return ""
	}
	return prefix + "****" + suffix
}

// UpdateProviderHealth updates only the health-related fields of a provider.
func (s *Store) UpdateProviderHealth(id string, status model.ProviderStatus, errorMessage string) error {
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE providers SET status=?, error_message=?, updated_at=?
			WHERE id=?`,
			status, errorMessage, now, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update provider health %q: %w", id, ErrNotFound)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
//  Internal helpers used by the service layer
// ---------------------------------------------------------------------------

// UpsertModels replaces all models for a provider. Called after a successful
// provider test.
func (s *Store) UpsertModels(providerID string, names []string) error {
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		// Delete existing models for this provider.
		if _, err := tx.Exec(`DELETE FROM models WHERE provider_id = ?`, providerID); err != nil {
			return err
		}
		for _, name := range names {
			id := makeID()
			if _, err := tx.Exec(`
				INSERT INTO models (id, provider_id, name, context_window, created_at)
				VALUES (?, ?, ?, 0, ?)`,
				id, providerID, name, now); err != nil {
				return err
			}
		}
		// Update provider model count.
		_, err := tx.Exec(`UPDATE providers SET models_count = ?, updated_at = ? WHERE id = ?`,
			len(names), now, providerID)
		return err
	})
}

// UpdateProviderTestResult updates provider test outcome fields.
func (s *Store) UpdateProviderTestResult(id string, status model.ProviderStatus, modelsCount int, avgLatency int, errMsg string) error {
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			UPDATE providers SET status=?, models_count=?, avg_latency_ms=?,
			                     last_tested_at=?, error_message=?, updated_at=?
			WHERE id=?`,
			status, modelsCount, avgLatency, now, errMsg, now, id)
		return err
	})
}

// ---------------------------------------------------------------------------
//  SQL helpers
// ---------------------------------------------------------------------------

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

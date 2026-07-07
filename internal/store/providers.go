package store

import (
	"database/sql"
	"fmt"

	"autoapi/internal/model"
)

// ListProviders returns every provider ordered by name.
func (s *Store) ListProviders() ([]model.Provider, error) {
	rows, err := s.db.Query(`
		SELECT id, name, base_url, status,
		       key_ciphertext, key_nonce, key_masked,
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
		if err := rows.Scan(
			&p.ID, &p.Name, &p.BaseURL, &p.Status,
			&p.KeyCiphertext, &p.KeyNonce, &p.KeyMasked,
			&p.ModelsCount, &p.MonthlyTokens, &p.AvgLatencyMs,
			&p.LastTestedAt, &p.ErrorMessage, &p.IsCustom,
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scan provider: %w", err)
		}
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
		SELECT id, name, base_url, status,
		       key_ciphertext, key_nonce, key_masked,
		       models_count, monthly_tokens, avg_latency_ms,
		       last_tested_at, error_message, is_custom,
		       created_at, updated_at
		FROM providers WHERE id = ?`, id)

	var p model.Provider
	if err := row.Scan(
		&p.ID, &p.Name, &p.BaseURL, &p.Status,
		&p.KeyCiphertext, &p.KeyNonce, &p.KeyMasked,
		&p.ModelsCount, &p.MonthlyTokens, &p.AvgLatencyMs,
		&p.LastTestedAt, &p.ErrorMessage, &p.IsCustom,
		&p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get provider %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get provider %q: %w", id, err)
	}
	return &p, nil
}

// CreateProvider inserts a new provider and returns the full entity.
// Upstream keys are managed separately by the App layer via
// UpdateProviderKeyCiphertext; this method does not touch key columns.
func (s *Store) CreateProvider(in model.ProviderInput) (*model.Provider, error) {
	now := nowMs()
	id := makeID()

	p := &model.Provider{
		ID:        id,
		Name:      in.Name,
		BaseURL:   in.BaseURL,
		Status:    model.ProviderStatusUnknown,
		IsCustom:  in.IsCustom,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO providers (id, name, base_url, status,
			                       key_ciphertext, key_nonce, key_masked,
			                       models_count, monthly_tokens, avg_latency_ms,
			                       last_tested_at, error_message, is_custom,
			                       created_at, updated_at)
			VALUES (?, ?, ?, ?,
			        NULL, NULL, '',
			        0, 0, 0,
			        0, '', ?, ?, ?)`,
			p.ID, p.Name, p.BaseURL, p.Status,
			boolInt(p.IsCustom), p.CreatedAt, p.UpdatedAt)
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: create provider: %w", err)
	}
	return p, nil
}

// UpdateProvider modifies an existing provider without touching key columns.
// Upstream keys are managed separately by the App layer via
// UpdateProviderKeyCiphertext.
func (s *Store) UpdateProvider(id string, in model.ProviderInput) (*model.Provider, error) {
	now := nowMs()

	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE providers SET name=?, base_url=?, is_custom=?, updated_at=?
			WHERE id=?`,
			in.Name, in.BaseURL, boolInt(in.IsCustom), now, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
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

// UpsertModels inserts or updates models for a provider, preserving existing
// active, latency_ms, and created_at values on conflict.
func (s *Store) UpsertModels(providerID string, models []model.Model) error {
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		for _, m := range models {
			id := m.ID
			if id == "" {
				id = makeID()
			}
			createdAt := m.CreatedAt
			if createdAt == 0 {
				createdAt = now
			}
			_, err := tx.Exec(`
				INSERT INTO models (id, provider_id, name, context_window, owned_by, active, latency_ms, updated_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(provider_id, name) DO UPDATE SET
					context_window = excluded.context_window,
					owned_by = excluded.owned_by,
					updated_at = excluded.updated_at
					-- active, latency_ms, created_at preserved from existing row
`,
				id, providerID, m.Name, m.ContextWindow, m.OwnedBy, boolInt(m.Active), m.LatencyMs, now, createdAt)
			if err != nil {
				return err
			}
		}
		// Update provider model count and timestamp.
		_, err := tx.Exec(`
			UPDATE providers SET models_count = (SELECT COUNT(*) FROM models WHERE provider_id = ?), updated_at = ?
			WHERE id = ?`,
			providerID, now, providerID)
		return err
	})
}

// UpdateModelLatency sets the measured latency for a single model.
func (s *Store) UpdateModelLatency(providerID, modelName string, latencyMs int) error {
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE models SET latency_ms = ?, updated_at = ?
			WHERE provider_id = ? AND name = ?`,
			latencyMs, now, providerID, modelName)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update model latency %q for provider %q: %w", modelName, providerID, ErrNotFound)
		}
		return nil
	})
}

// SetModelsActive enables or disables a set of model names for a provider.
func (s *Store) SetModelsActive(providerID string, modelNames []string, active bool) error {
	now := nowMs()
	activeInt := 0
	if active {
		activeInt = 1
	}
	return s.execTx(func(tx *sql.Tx) error {
		for _, name := range modelNames {
			res, err := tx.Exec(`
				UPDATE models SET active = ?, updated_at = ?
				WHERE provider_id = ? AND name = ?`,
				activeInt, now, providerID, name)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				return fmt.Errorf("store: set model active %q for provider %q: %w", name, providerID, ErrNotFound)
			}
		}
		return nil
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

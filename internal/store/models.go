package store

import (
	"database/sql"
	"fmt"

	"autoapi/internal/model"
)

// ListModels returns all models for a provider. If providerID is empty,
// returns all models across all providers.
func (s *Store) ListModels(providerID string) ([]model.Model, error) {
	var rows *sql.Rows
	var err error
	if providerID == "" {
		rows, err = s.db.Query(`
			SELECT id, provider_id, name, context_window, owned_by, active, latency_ms, request_price, updated_at, created_at
			FROM models ORDER BY active DESC, name ASC`)
	} else {
		rows, err = s.db.Query(`
			SELECT id, provider_id, name, context_window, owned_by, active, latency_ms, request_price, updated_at, created_at
			FROM models WHERE provider_id = ? ORDER BY active DESC, name ASC`, providerID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list models: %w", err)
	}
	defer rows.Close()

	var out []model.Model
	for rows.Next() {
		var m model.Model
		var activeInt int
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.Name, &m.ContextWindow, &m.OwnedBy, &activeInt, &m.LatencyMs, &m.RequestPrice, &m.UpdatedAt, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan model: %w", err)
		}
		m.Active = activeInt != 0
		out = append(out, m)
	}
	if out == nil {
		out = []model.Model{}
	}
	return out, rows.Err()
}

// GetModel returns a single model by provider ID and model name.
func (s *Store) GetModel(providerID, name string) (*model.Model, error) {
	row := s.db.QueryRow(`
		SELECT id, provider_id, name, context_window, owned_by, active, latency_ms, request_price, updated_at, created_at
		FROM models WHERE provider_id = ? AND name = ?`, providerID, name)
	var m model.Model
	var activeInt int
	if err := row.Scan(&m.ID, &m.ProviderID, &m.Name, &m.ContextWindow, &m.OwnedBy, &activeInt, &m.LatencyMs, &m.RequestPrice, &m.UpdatedAt, &m.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get model %q for provider %q: %w", name, providerID, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get model %q for provider %q: %w", name, providerID, err)
	}
	m.Active = activeInt != 0
	return &m, nil
}

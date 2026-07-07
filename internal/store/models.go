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
			SELECT id, provider_id, name, context_window, created_at
			FROM models ORDER BY name ASC`)
	} else {
		rows, err = s.db.Query(`
			SELECT id, provider_id, name, context_window, created_at
			FROM models WHERE provider_id = ? ORDER BY name ASC`, providerID)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list models: %w", err)
	}
	defer rows.Close()

	var out []model.Model
	for rows.Next() {
		var m model.Model
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.Name, &m.ContextWindow, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("store: scan model: %w", err)
		}
		out = append(out, m)
	}
	if out == nil {
		out = []model.Model{}
	}
	return out, rows.Err()
}

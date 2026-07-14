package store

import (
	"database/sql"
	"fmt"

	"autoapi/internal/model"
)

func (s *Store) ListProviderCapabilities(providerID string) ([]model.ProviderCapability, error) {
	query := `SELECT provider_id, protocol, feature, enabled, source, updated_at FROM provider_capabilities`
	args := []any{}
	if providerID != "" {
		query += ` WHERE provider_id = ?`
		args = append(args, providerID)
	}
	query += ` ORDER BY provider_id ASC, protocol ASC, feature ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list provider capabilities: %w", err)
	}
	defer rows.Close()

	out := []model.ProviderCapability{}
	for rows.Next() {
		var c model.ProviderCapability
		if err := rows.Scan(&c.ProviderID, &c.Protocol, &c.Feature, &c.Enabled, &c.Source, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan provider capability: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SetProviderCapability(providerID, protocol, feature string, enabled bool) error {
	if feature == "" {
		feature = "native"
	}
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO provider_capabilities (provider_id, protocol, feature, enabled, source, updated_at)
			VALUES (?, ?, ?, ?, 'manual', ?)
			ON CONFLICT(provider_id, protocol, feature) DO UPDATE SET enabled=excluded.enabled, source='manual', updated_at=excluded.updated_at`,
			providerID, protocol, feature, boolInt(enabled), now)
		if err != nil {
			return fmt.Errorf("store: set provider capability: %w", err)
		}
		return nil
	})
}

func (s *Store) GetProviderCapabilities(providerID string) ([]model.ProviderCapability, error) {
	return s.ListProviderCapabilities(providerID)
}

func (s *Store) ProviderSupportsProtocol(providerID string, protocol string) (bool, error) {
	var enabled bool
	err := s.db.QueryRow(`SELECT enabled FROM provider_capabilities WHERE provider_id = ? AND protocol = ? AND feature = 'native'`, providerID, protocol).Scan(&enabled)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: provider supports protocol: %w", err)
	}
	return enabled, nil
}

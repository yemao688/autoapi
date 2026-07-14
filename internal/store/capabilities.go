package store

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	"autoapi/internal/model"
)

// Allowed provider capability protocols. Must stay in sync with proxy protocol constants.
var (
	providerCapabilityProtocols = map[string]bool{
		"openai_responses":   true,
		"anthropic_messages": true,
		"gemini":             true,
		"openai_chat":        true,
		"openai":             true,
	}

	// providerCapabilityFeatures allows the legacy 'native' capability plus every
	// canonical feature. Unknown feature strings are rejected to keep the capability
	// snapshot deterministic.
	providerCapabilityFeatures = func() map[string]bool {
		m := map[string]bool{
			"native": true,
			"model":  true,
		}
		for _, f := range []model.Feature{
			model.FeatureTools,
			model.FeatureVision,
			model.FeatureReasoning,
			model.FeatureStructuredOutput,
			model.FeatureStateful,
			model.FeatureCacheControl,
			model.FeatureAudio,
			model.FeatureDocument,
			model.FeatureStreaming,
		} {
			m[string(f)] = true
		}
		return m
	}()

	providerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func (s *Store) ListProviderCapabilities(providerID string) ([]model.ProviderCapability, error) {
	query := `SELECT provider_id, protocol, feature, enabled, source, updated_at FROM provider_capabilities WHERE 1=1`
	args := []any{}
	if providerID != "" {
		query += ` AND provider_id = ?`
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

// GetProviderCapabilitiesForProviders returns all capability rows for the
// supplied providers in one query. An empty list returns an empty snapshot.
func (s *Store) GetProviderCapabilitiesForProviders(providerIDs []string) ([]model.ProviderCapability, error) {
	if len(providerIDs) == 0 {
		return []model.ProviderCapability{}, nil
	}
	seen := map[string]bool{}
	var uniq []string
	for _, id := range providerIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	var out []model.ProviderCapability
	for _, chunk := range chunkStrings(uniq, providerChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			placeholders[i], args[i] = "?", id
		}
		rows, err := s.db.Query(`SELECT provider_id, protocol, feature, enabled, source, updated_at FROM provider_capabilities WHERE provider_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY provider_id, protocol, feature`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: get provider capabilities: %w", err)
		}
		scanErr := func() error {
			for rows.Next() {
				var c model.ProviderCapability
				if err := rows.Scan(&c.ProviderID, &c.Protocol, &c.Feature, &c.Enabled, &c.Source, &c.UpdatedAt); err != nil {
					return err
				}
				out = append(out, c)
			}
			return rows.Err()
		}()
		rows.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	return out, nil
}

func (s *Store) listCapabilities(suffix string, args ...any) ([]model.ProviderCapability, error) {
	rows, err := s.db.Query(`SELECT provider_id, protocol, feature, enabled, source, updated_at FROM provider_capabilities`+suffix+` ORDER BY provider_id, protocol, feature`, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list provider capabilities: %w", err)
	}
	defer rows.Close()
	var out []model.ProviderCapability
	for rows.Next() {
		var c model.ProviderCapability
		if err := rows.Scan(&c.ProviderID, &c.Protocol, &c.Feature, &c.Enabled, &c.Source, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SetProviderCapability(providerID, protocol, feature string, enabled bool) error {
	if providerID == "" {
		return fmt.Errorf("store: provider_id is required")
	}
	if !providerIDPattern.MatchString(providerID) {
		return fmt.Errorf("store: provider_id contains invalid characters")
	}
	if !providerCapabilityProtocols[protocol] {
		return fmt.Errorf("store: unsupported protocol %q", protocol)
	}
	if feature == "" {
		feature = "native"
	}
	if !providerCapabilityFeatures[feature] {
		return fmt.Errorf("store: unsupported feature %q", feature)
	}
	now := nowMs()
	return s.execTx(func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM providers WHERE id=?)`, providerID).Scan(&exists); err != nil {
			return fmt.Errorf("store: check provider exists: %w", err)
		}
		if !exists {
			return fmt.Errorf("store: provider %q does not exist", providerID)
		}
		_, err := tx.Exec(`
			INSERT INTO provider_capabilities (provider_id, protocol, feature, enabled, source, updated_at)
			VALUES (?, ?, ?, ?, 'manual', ?)
			ON CONFLICT(provider_id, protocol, feature) DO UPDATE SET enabled=excluded.enabled, source='manual', updated_at=excluded.updated_at`,
			providerID, protocol, feature, boolInt(enabled), now)
		if err != nil {
			return fmt.Errorf("store: set provider capability: %w", err)
		}
		if feature == "native" {
			column := legacyCapabilityColumn(protocol)
			if column != "" {
				if _, err := tx.Exec(`UPDATE providers SET `+column+`=? WHERE id=?`, boolInt(enabled), providerID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) DeleteProviderFeatureCapability(providerID, protocol, feature string) error {
	if providerID == "" {
		return fmt.Errorf("store: provider_id is required")
	}
	if !providerIDPattern.MatchString(providerID) {
		return fmt.Errorf("store: provider_id contains invalid characters")
	}
	if !providerCapabilityProtocols[protocol] {
		return fmt.Errorf("store: unsupported protocol %q", protocol)
	}
	if feature == "" || feature == "native" {
		return fmt.Errorf("store: deleting the native capability is not supported through this method")
	}
	if !providerCapabilityFeatures[feature] {
		return fmt.Errorf("store: unsupported feature %q", feature)
	}
	return s.execTx(func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM providers WHERE id=?)`, providerID).Scan(&exists); err != nil {
			return fmt.Errorf("store: check provider exists: %w", err)
		}
		if !exists {
			return fmt.Errorf("store: provider %q does not exist", providerID)
		}
		_, err := tx.Exec(`DELETE FROM provider_capabilities WHERE provider_id=? AND protocol=? AND feature=?`, providerID, protocol, feature)
		if err != nil {
			return fmt.Errorf("store: delete provider capability: %w", err)
		}
		return nil
	})
}

func legacyCapabilityColumn(protocol string) string {
	switch protocol {
	case "openai_responses":
		return "responses_enabled"
	case "anthropic_messages":
		return "messages_enabled"
	case "gemini":
		return "gemini_enabled"
	}
	return ""
}

func (s *Store) GetProviderCapabilities(providerID string) ([]model.ProviderCapability, error) {
	return s.ListProviderCapabilities(providerID)
}

func (s *Store) ProviderSupportsProtocol(providerID string, protocol string) (bool, error) {
	var enabled bool
	err := s.db.QueryRow(`SELECT enabled FROM provider_capabilities WHERE provider_id = ? AND protocol = ? AND feature = 'native' AND source = 'manual'`, providerID, protocol).Scan(&enabled)
	if err == sql.ErrNoRows {
		column := legacyCapabilityColumn(protocol)
		if column == "" {
			return false, nil
		}
		err = s.db.QueryRow(`SELECT `+column+` FROM providers WHERE id = ?`, providerID).Scan(&enabled)
		if err == sql.ErrNoRows {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("store: provider legacy capability: %w", err)
		}
		return enabled, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: provider supports protocol: %w", err)
	}
	return enabled, nil
}

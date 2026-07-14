package store

import (
	"autoapi/internal/model"
	"database/sql"
	"fmt"
	"strings"
)

// ListModelCapabilities returns stored override rows. Missing rows mean inherit.
func (s *Store) ListModelCapabilities(providerID, modelName string) ([]model.ModelCapability, error) {
	q := `SELECT provider_id, model_name, protocol, feature, enabled, source, updated_at FROM model_capabilities WHERE 1=1`
	args := []any{}
	if providerID != "" {
		q += ` AND provider_id=?`
		args = append(args, providerID)
	}
	if modelName != "" {
		q += ` AND model_name=?`
		args = append(args, modelName)
	}
	q += ` ORDER BY provider_id, model_name, protocol, feature`
	return s.scanModelCapabilities(q, args...)
}

func (s *Store) scanModelCapabilities(q string, args ...any) ([]model.ModelCapability, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list model capabilities: %w", err)
	}
	defer rows.Close()
	out := []model.ModelCapability{}
	for rows.Next() {
		var c model.ModelCapability
		if err := rows.Scan(&c.ProviderID, &c.ModelName, &c.Protocol, &c.Feature, &c.Enabled, &c.Source, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SetModelCapability(providerID, modelName, protocol, feature string, enabled bool) error {
	providerID, modelName, protocol, feature = strings.TrimSpace(providerID), strings.TrimSpace(modelName), strings.TrimSpace(protocol), strings.TrimSpace(feature)
	if providerID == "" || modelName == "" || protocol == "" || feature == "" {
		return fmt.Errorf("store: capability fields are required")
	}
	if !providerCapabilityProtocols[protocol] {
		return fmt.Errorf("store: unsupported protocol %q", protocol)
	}
	if feature != "native" && !providerCapabilityFeatures[feature] {
		return fmt.Errorf("store: unsupported feature %q", feature)
	}
	return s.execTx(func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow(`SELECT 1 FROM models WHERE provider_id=? AND name=?`, providerID, modelName).Scan(&exists); err == sql.ErrNoRows {
			return fmt.Errorf("store: model not found: %w", ErrNotFound)
		} else if err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO model_capabilities(provider_id,model_name,protocol,feature,enabled,source,updated_at) VALUES(?,?,?,?,?,'manual',?) ON CONFLICT(provider_id,model_name,protocol,feature) DO UPDATE SET enabled=excluded.enabled,source='manual',updated_at=excluded.updated_at`, providerID, modelName, protocol, feature, boolInt(enabled), nowMs())
		return err
	})
}

// DeleteModelCapability removes an override so the provider capability inherits.
func (s *Store) DeleteModelCapability(providerID, modelName, protocol, feature string) error {
	providerID, modelName, protocol, feature = strings.TrimSpace(providerID), strings.TrimSpace(modelName), strings.TrimSpace(protocol), strings.TrimSpace(feature)
	if providerID == "" || modelName == "" || protocol == "" || feature == "" {
		return fmt.Errorf("store: capability fields are required")
	}
	if !providerCapabilityProtocols[protocol] {
		return fmt.Errorf("store: unsupported protocol %q", protocol)
	}
	if feature != "native" && !providerCapabilityFeatures[feature] {
		return fmt.Errorf("store: unsupported feature %q", feature)
	}
	return s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM model_capabilities WHERE provider_id=? AND model_name=? AND protocol=? AND feature=?`, providerID, modelName, protocol, feature)
		return err
	})
}

func (s *Store) GetModelCapabilitiesForModels(refs []model.ProviderModelRef) ([]model.ModelCapability, error) {
	if len(refs) == 0 {
		return []model.ModelCapability{}, nil
	}
	seen := map[string]bool{}
	var uniq []model.ProviderModelRef
	for _, r := range refs {
		key := r.ProviderID + "\x00" + r.ModelName
		if seen[key] {
			continue
		}
		seen[key] = true
		uniq = append(uniq, r)
	}
	var out []model.ModelCapability
	for _, chunk := range chunkModelRefs(uniq, modelRefChunkSize) {
		clauses := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*2)
		for _, r := range chunk {
			clauses = append(clauses, "(provider_id=? AND model_name=?)")
			args = append(args, r.ProviderID, r.ModelName)
		}
		rows, err := s.db.Query(`SELECT provider_id, model_name, protocol, feature, enabled, source, updated_at FROM model_capabilities WHERE `+strings.Join(clauses, " OR ")+` ORDER BY provider_id,model_name,protocol,feature`, args...)
		if err != nil {
			return nil, fmt.Errorf("store: get model capabilities for models: %w", err)
		}
		scanErr := func() error {
			for rows.Next() {
				var c model.ModelCapability
				if err := rows.Scan(&c.ProviderID, &c.ModelName, &c.Protocol, &c.Feature, &c.Enabled, &c.Source, &c.UpdatedAt); err != nil {
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

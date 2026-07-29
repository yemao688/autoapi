package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"autoapi/internal/toolconfig"
)

const toolPresetColumns = `
	id, tool, kind, name, provider_id, vendor, base_url, api_key_enc,
	api_key_id, models_json, extra_json, created_at, updated_at`

const toolStateUpsertSQL = `
		INSERT INTO tool_state (tool, active_preset_id, config_path, applied_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(tool) DO UPDATE SET
			active_preset_id = excluded.active_preset_id,
			config_path = excluded.config_path,
			applied_at = excluded.applied_at`

const toolFileStateUpsertSQL = `
		INSERT INTO tool_file_state (tool, resource, path, applied_file_hash, applied_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(tool, resource) DO UPDATE SET
			path = excluded.path,
			applied_file_hash = excluded.applied_file_hash,
			applied_at = excluded.applied_at`

// CreateToolPreset inserts a preset and fills its generated ID and timestamps.
func (s *Store) CreateToolPreset(p *toolconfig.Preset) error {
	if p == nil {
		return fmt.Errorf("store: create tool preset: nil preset")
	}

	modelsJSON, extraJSON, err := marshalToolPresetJSON(p)
	if err != nil {
		return fmt.Errorf("store: create tool preset: %w", err)
	}

	now := nowMs()
	var id int64
	if err := s.execTx(func(tx *sql.Tx) error {
		result, err := tx.Exec(`
			INSERT INTO tool_presets (
				tool, kind, name, provider_id, vendor, base_url, api_key_enc,
				api_key_id, models_json, extra_json, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(p.Tool), string(p.Kind), p.Name, p.ProviderID, p.Vendor, p.BaseURL, p.APIKeyEnc,
			p.APIKeyID, modelsJSON, extraJSON, now, now)
		if err != nil {
			return classifyToolPresetError(err)
		}
		id, err = result.LastInsertId()
		return err
	}); err != nil {
		return fmt.Errorf("store: create tool preset: %w", err)
	}

	p.ID = id
	p.CreatedAt = now
	p.UpdatedAt = now
	return nil
}

// UpdateToolPreset updates an existing preset and bumps its update timestamp.
func (s *Store) UpdateToolPreset(p *toolconfig.Preset) error {
	if p == nil {
		return fmt.Errorf("store: update tool preset: nil preset")
	}

	modelsJSON, extraJSON, err := marshalToolPresetJSON(p)
	if err != nil {
		return fmt.Errorf("store: update tool preset: %w", err)
	}

	now := nowMs()
	if now <= p.UpdatedAt {
		now = p.UpdatedAt + 1
	}
	if err := s.execTx(func(tx *sql.Tx) error {
		result, err := tx.Exec(`
			UPDATE tool_presets SET
				tool=?, kind=?, name=?, provider_id=?, vendor=?, base_url=?, api_key_enc=?,
				api_key_id=?, models_json=?, extra_json=?, updated_at=?
			WHERE id=?`,
			string(p.Tool), string(p.Kind), p.Name, p.ProviderID, p.Vendor, p.BaseURL, p.APIKeyEnc,
			p.APIKeyID, modelsJSON, extraJSON, now, p.ID)
		if err != nil {
			return classifyToolPresetError(err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("store: update tool preset %d: %w", p.ID, ErrNotFound)
		}
		return nil
	}); err != nil {
		return err
	}

	p.UpdatedAt = now
	return nil
}

// DeleteToolPreset removes a preset by ID.
func (s *Store) DeleteToolPreset(id int64) error {
	err := s.execTx(func(tx *sql.Tx) error {
		result, err := tx.Exec(`DELETE FROM tool_presets WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete tool preset: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("store: delete tool preset %d: %w", id, ErrNotFound)
		}
		if _, err := tx.Exec(`UPDATE tool_state SET active_preset_id = 0 WHERE active_preset_id = ?`, id); err != nil {
			return fmt.Errorf("store: clear active tool preset %d: %w", id, err)
		}
		return nil
	})
	return err
}

// SyncToolPresets applies staged provider-row removals and upserts in one
// writer transaction. It is used after a successful opencode file commit so a
// failed file commit never changes the parked-provider mirror.
func (s *Store) SyncToolPresets(upserts []toolconfig.Preset, removeIDs []int64) error {
	type preparedPreset struct {
		preset    toolconfig.Preset
		models    string
		extra     string
		updatedAt int64
	}
	prepared := make([]preparedPreset, 0, len(upserts))
	for _, preset := range upserts {
		models, extra, err := marshalToolPresetJSON(&preset)
		if err != nil {
			return fmt.Errorf("store: sync tool presets: %w", err)
		}
		now := nowMs()
		if now <= preset.UpdatedAt {
			now = preset.UpdatedAt + 1
		}
		prepared = append(prepared, preparedPreset{preset: preset, models: models, extra: extra, updatedAt: now})
	}

	err := s.execTx(func(tx *sql.Tx) error {
		for _, id := range removeIDs {
			if _, err := tx.Exec(`DELETE FROM tool_presets WHERE id = ?`, id); err != nil {
				return fmt.Errorf("store: sync delete tool preset %d: %w", id, err)
			}
			if _, err := tx.Exec(`UPDATE tool_state SET active_preset_id = 0 WHERE active_preset_id = ?`, id); err != nil {
				return fmt.Errorf("store: sync clear active tool preset %d: %w", id, err)
			}
		}
		for _, item := range prepared {
			p := item.preset
			if p.ID == 0 {
				if _, err := tx.Exec(`
					INSERT INTO tool_presets (
						tool, kind, name, provider_id, vendor, base_url, api_key_enc,
						api_key_id, models_json, extra_json, created_at, updated_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					string(p.Tool), string(p.Kind), p.Name, p.ProviderID, p.Vendor, p.BaseURL, p.APIKeyEnc,
					p.APIKeyID, item.models, item.extra, item.updatedAt, item.updatedAt); err != nil {
					return classifyToolPresetError(err)
				}
				continue
			}
			result, err := tx.Exec(`
				UPDATE tool_presets SET
					tool=?, kind=?, name=?, provider_id=?, vendor=?, base_url=?, api_key_enc=?,
					api_key_id=?, models_json=?, extra_json=?, updated_at=?
				WHERE id=?`,
				string(p.Tool), string(p.Kind), p.Name, p.ProviderID, p.Vendor, p.BaseURL, p.APIKeyEnc,
				p.APIKeyID, item.models, item.extra, item.updatedAt, p.ID)
			if err != nil {
				return classifyToolPresetError(err)
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("store: sync update tool preset %d: %w", p.ID, ErrNotFound)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: sync tool presets: %w", err)
	}
	return nil
}

// GetToolPreset returns a preset by ID. A missing preset is not an error.
func (s *Store) GetToolPreset(id int64) (*toolconfig.Preset, error) {
	row := s.db.QueryRow(`SELECT `+toolPresetColumns+` FROM tool_presets WHERE id = ?`, id)
	p, err := scanToolPreset(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get tool preset %d: %w", id, err)
	}
	return p, nil
}

// ListToolPresets returns presets for one tool, or all presets when tool is empty.
func (s *Store) ListToolPresets(tool string) ([]toolconfig.Preset, error) {
	query := `SELECT ` + toolPresetColumns + ` FROM tool_presets`
	args := []interface{}{}
	if tool != "" {
		query += ` WHERE tool = ?`
		args = append(args, tool)
	}
	query += ` ORDER BY tool, name`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list tool presets: %w", err)
	}
	defer rows.Close()

	out := make([]toolconfig.Preset, 0)
	for rows.Next() {
		p, err := scanToolPreset(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan tool preset: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tool presets: %w", err)
	}
	return out, nil
}

// GetToolState returns the persisted state for a tool. Missing state is a zero value.
func (s *Store) GetToolState(tool string) (*toolconfig.ToolState, error) {
	row := s.db.QueryRow(`
		SELECT tool, active_preset_id, config_path, applied_at
		FROM tool_state WHERE tool = ?`, tool)

	var st toolconfig.ToolState
	if err := row.Scan(&st.Tool, &st.ActivePresetID, &st.ConfigPath, &st.AppliedAt); err != nil {
		if err == sql.ErrNoRows {
			return &toolconfig.ToolState{}, nil
		}
		return nil, fmt.Errorf("store: get tool state %q: %w", tool, err)
	}
	return &st, nil
}

// SaveToolState inserts or updates the state for a tool.
func (s *Store) SaveToolState(st *toolconfig.ToolState) error {
	if st == nil {
		return fmt.Errorf("store: save tool state: nil state")
	}

	return s.execTx(func(tx *sql.Tx) error {
		return upsertToolState(tx, st)
	})
}

// GetToolFileStates returns all persisted resource states for a tool.
func (s *Store) GetToolFileStates(tool string) ([]toolconfig.ToolFileState, error) {
	rows, err := s.db.Query(`
		SELECT tool, resource, path, applied_file_hash, applied_at
		FROM tool_file_state WHERE tool = ? ORDER BY resource`, tool)
	if err != nil {
		return nil, fmt.Errorf("store: get tool file states %q: %w", tool, err)
	}
	defer rows.Close()

	states := make([]toolconfig.ToolFileState, 0)
	for rows.Next() {
		var st toolconfig.ToolFileState
		if err := rows.Scan(&st.Tool, &st.Resource, &st.Path, &st.AppliedFileHash, &st.AppliedAt); err != nil {
			return nil, fmt.Errorf("store: scan tool file state: %w", err)
		}
		states = append(states, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: get tool file states %q: %w", tool, err)
	}
	return states, nil
}

// SaveToolFileState inserts or updates one resource's state for a tool.
func (s *Store) SaveToolFileState(st *toolconfig.ToolFileState) error {
	if st == nil {
		return fmt.Errorf("store: save tool file state: nil state")
	}

	return s.execTx(func(tx *sql.Tx) error {
		return upsertToolFileState(tx, st)
	})
}

// SaveToolApplyState persists the tool-level state and all per-resource file
// states in a single writer transaction.
func (s *Store) SaveToolApplyState(st *toolconfig.ToolState, files []toolconfig.ToolFileState) error {
	if st == nil {
		return fmt.Errorf("store: save tool apply state: nil state")
	}

	return s.execTx(func(tx *sql.Tx) error {
		if err := upsertToolState(tx, st); err != nil {
			return err
		}
		for i := range files {
			if err := upsertToolFileState(tx, &files[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertToolState(tx *sql.Tx, st *toolconfig.ToolState) error {
	if _, err := tx.Exec(toolStateUpsertSQL, string(st.Tool), st.ActivePresetID, st.ConfigPath, st.AppliedAt); err != nil {
		return fmt.Errorf("store: save tool state: %w", err)
	}
	return nil
}

func upsertToolFileState(tx *sql.Tx, st *toolconfig.ToolFileState) error {
	if _, err := tx.Exec(toolFileStateUpsertSQL, string(st.Tool), string(st.Resource), st.Path, st.AppliedFileHash, st.AppliedAt); err != nil {
		return fmt.Errorf("store: save tool file state: %w", err)
	}
	return nil
}

func marshalToolPresetJSON(p *toolconfig.Preset) (string, string, error) {
	models, err := json.Marshal(p.Models)
	if err != nil {
		return "", "", fmt.Errorf("marshal models: %w", err)
	}
	extra, err := json.Marshal(p.Extra)
	if err != nil {
		return "", "", fmt.Errorf("marshal extra: %w", err)
	}
	return string(models), string(extra), nil
}

func scanToolPreset(scanner rowScanner) (*toolconfig.Preset, error) {
	var (
		p         toolconfig.Preset
		tool      string
		kind      string
		modelsRaw string
		extraRaw  string
	)
	if err := scanner.Scan(
		&p.ID, &tool, &kind, &p.Name, &p.ProviderID, &p.Vendor, &p.BaseURL, &p.APIKeyEnc,
		&p.APIKeyID, &modelsRaw, &extraRaw, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("scan tool preset: %w", err)
	}
	p.Tool = toolconfig.Tool(tool)
	p.Kind = toolconfig.PresetKind(kind)
	if err := json.Unmarshal([]byte(modelsRaw), &p.Models); err != nil {
		return nil, fmt.Errorf("unmarshal tool preset models: %w", err)
	}
	if err := json.Unmarshal([]byte(extraRaw), &p.Extra); err != nil {
		return nil, fmt.Errorf("unmarshal tool preset extra: %w", err)
	}
	return &p, nil
}

func classifyToolPresetError(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint failed") && strings.Contains(message, "tool_presets") {
		return fmt.Errorf("%w: tool preset name already exists: %v", toolconfig.ErrConflict, err)
	}
	return err
}

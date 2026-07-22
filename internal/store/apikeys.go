package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"autoapi/internal/model"
)

// ListAPIKeys returns every access token. No secret material is exposed.
func (s *Store) ListAPIKeys() ([]model.ApiKey, error) {
	now := time.Now()
	startToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	rolling := now.AddDate(0, 0, -30).UnixMilli()
	rows, err := s.db.Query(`
		SELECT k.id, k.name, k.expires_at, k.created_at, k.updated_at, k.enabled, k.last_used_at,
		 COALESCE((SELECT SUM(input_tokens + output_tokens) FROM request_logs l WHERE l.api_key_id=k.id AND l.status_code != 0 AND l.timestamp_ms >= ?), 0),
		 COALESCE((SELECT SUM(input_tokens + output_tokens) FROM request_logs l WHERE l.api_key_id=k.id AND l.status_code != 0 AND l.timestamp_ms >= ?), 0)
		FROM api_keys k ORDER BY k.created_at DESC`, startToday, rolling)
	if err != nil {
		return nil, fmt.Errorf("store: list api keys: %w", err)
	}
	defer rows.Close()

	var out []model.ApiKey
	for rows.Next() {
		var k model.ApiKey
		if err := rows.Scan(
			&k.ID, &k.Name, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt, &k.Enabled, &k.LastUsedAt, &k.TodayTokens, &k.ThirtyDayTokens,
		); err != nil {
			return nil, fmt.Errorf("store: scan api key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		out[i].AllowedRuleIDs, err = apiKeyAllowedRuleIDs(s.db, out[i].ID)
		if err != nil {
			return nil, fmt.Errorf("store: load api key allowlist: %w", err)
		}
	}
	if out == nil {
		out = []model.ApiKey{}
	}
	return out, nil
}

// CreateAPIKey creates a simple access token. The generated row ID is the token
// value presented to callers of the proxy.
func (s *Store) CreateAPIKey(in model.ApiKeyInput) (*model.ApiKey, error) {
	now := nowMs()
	id := makeID()

	k := &model.ApiKey{
		ID:             id,
		Name:           in.Name,
		ExpiresAt:      in.ExpiresAt,
		CreatedAt:      now,
		UpdatedAt:      now,
		AllowedRuleIDs: append([]string{}, in.AllowedRuleIDs...),
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO api_keys (id, name, expires_at, created_at, updated_at, enabled)
			VALUES (?, ?, ?, ?, ?, 1)`,
			k.ID, k.Name, k.ExpiresAt, k.CreatedAt, k.UpdatedAt)
		if err != nil {
			return err
		}
		return replaceAPIKeyAllowlist(tx, k.ID, in.AllowedRuleIDs)
	}); err != nil {
		return nil, fmt.Errorf("store: create api key: %w", err)
	}
	slog.Info("store: api key created", "id", k.ID, "name", k.Name)
	return k, nil
}

func (s *Store) SetAPIKeyEnabled(id string, enabled bool) error {
	return s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE api_keys SET enabled=?, updated_at=? WHERE id=?`, boolInt(enabled), nowMs(), id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: set api key enabled %q: %w", id, ErrNotFound)
		}
		return nil
	})
}

// GetAPIKey performs a direct lookup used by proxy authentication.
func (s *Store) GetAPIKey(id string) (*model.ApiKey, error) { return s.getAPIKeyByID(id) }

// UpdateAPIKey updates the name and expiry of an access token.
func (s *Store) UpdateAPIKey(id string, in model.ApiKeyInput) (*model.ApiKey, error) {
	now := nowMs()

	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE api_keys SET name=?, expires_at=?, updated_at=?
			WHERE id=?`,
			in.Name, in.ExpiresAt, now, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update api key %q: %w", id, ErrNotFound)
		}
		return replaceAPIKeyAllowlist(tx, id, in.AllowedRuleIDs)
	}); err != nil {
		return nil, err
	}
	slog.Info("store: api key updated", "id", id, "name", in.Name)
	return s.getAPIKeyByID(id)
}

// DeleteAPIKey removes an access token by ID.
func (s *Store) DeleteAPIKey(id string) error {
	err := s.execTx(func(tx *sql.Tx) error {
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
	if err == nil {
		slog.Info("store: api key deleted", "id", id)
	}
	return err
}

// getAPIKeyByID returns a single access token for internal use.
func (s *Store) getAPIKeyByID(id string) (*model.ApiKey, error) {
	row := s.db.QueryRow(`
		SELECT id, name, expires_at, created_at, updated_at, enabled, last_used_at
		FROM api_keys WHERE id = ?`, id)

	var k model.ApiKey
	if err := row.Scan(
		&k.ID, &k.Name, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt, &k.Enabled, &k.LastUsedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get api key %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get api key %q: %w", id, err)
	}
	allowed, err := apiKeyAllowedRuleIDs(s.db, k.ID)
	if err != nil {
		return nil, fmt.Errorf("store: load api key allowlist: %w", err)
	}
	k.AllowedRuleIDs = allowed
	return &k, nil
}

func replaceAPIKeyAllowlist(tx *sql.Tx, keyID string, ruleIDs []string) error {
	if _, err := tx.Exec(`DELETE FROM api_key_model_rules WHERE api_key_id = ?`, keyID); err != nil {
		return err
	}
	for _, ruleID := range ruleIDs {
		if _, err := tx.Exec(`INSERT INTO api_key_model_rules (api_key_id, model_rule_id) VALUES (?, ?)`, keyID, ruleID); err != nil {
			return err
		}
	}
	return nil
}

func apiKeyAllowedRuleIDs(db *sql.DB, keyID string) ([]string, error) {
	rows, err := db.Query(`SELECT model_rule_id FROM api_key_model_rules WHERE api_key_id = ? ORDER BY model_rule_id`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

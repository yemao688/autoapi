package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"autoapi/internal/model"
)

// chainJSONFor serialises a Chain slice to the persisted JSON blob. Returns
// an empty string for nil/empty slices so the column stores a stable
// sentinel ("") rather than "null" — the scan side uses the same convention
// to round-trip cleanly.
func chainJSONFor(chain []model.RequestLogChainEntry) string {
	if len(chain) == 0 {
		return ""
	}
	buf, err := json.Marshal(chain)
	if err != nil {
		// Encoding an in-memory struct should never fail; fall back to the
		// empty sentinel so we don't lose the log row.
		return ""
	}
	return string(buf)
}

// chainFromJSON deserialises a stored chain_json blob. An empty string or
// any parse error yields a nil slice — the UI treats nil/empty Chain as
// "no per-attempt history" (older rows from before migration 012).
func chainFromJSON(raw string) []model.RequestLogChainEntry {
	if raw == "" {
		return nil
	}
	var out []model.RequestLogChainEntry
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// QueryLogs filters request_logs by the given parameters with pagination.
// Returns the rows, total count, and any error.
//
// All filters compose with AND. The same WHERE clause is reused for the COUNT(*)
// query so the reported total always matches the filtered set. Search uses a
// single LIKE %term% clause matched against model, route_label, and error to
// keep the index plan simple; the parameterised placeholder protects against
// SQL injection regardless of the user-supplied content.
func (s *Store) QueryLogs(q model.LogQuery) ([]model.RequestLog, int64, error) {
	// Build dynamic WHERE clause. args is shared between the COUNT and DATA
	// queries so both apply identical filters.
	where := "WHERE 1=1"
	args := []interface{}{}

	if q.StartDate > 0 {
		where += " AND timestamp_ms >= ?"
		args = append(args, q.StartDate)
	}
	if q.EndDate > 0 {
		where += " AND timestamp_ms <= ?"
		args = append(args, q.EndDate)
	}
	if q.Provider != "" {
		where += " AND provider_id = ?"
		args = append(args, q.Provider)
	}
	if q.RouteID != "" {
		where += " AND route_id = ?"
		args = append(args, q.RouteID)
	}
	if q.Model != "" {
		where += " AND model = ?"
		args = append(args, q.Model)
	}
	if q.Status != "" {
		switch q.Status {
		case "success":
			where += " AND status_code >= 200 AND status_code < 300"
		case "failed":
			where += " AND (status_code >= 400 OR error != '')"
		case "rate_limited":
			where += " AND status_code = 429"
		}
	}
	if q.Search != "" {
		// LIKE is case-insensitive for ASCII by default in SQLite. The
		// pattern is fully parameterised so user input cannot escape the
		// literal context.
		where += " AND (model LIKE ? OR route_label LIKE ? OR error LIKE ?)"
		pattern := "%" + q.Search + "%"
		args = append(args, pattern, pattern, pattern)
	}

	// Total count
	var total int64
	countQuery := "SELECT COUNT(*) FROM request_logs " + where
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: query logs count: %w", err)
	}

	// Paginated data
	page := q.Page
	if page < 1 {
		page = 1
	}
	pageSize := q.PageSize
	if pageSize < 1 || pageSize > 1000 {
		pageSize = 50
	}
	offset := (page - 1) * pageSize

	dataQuery := fmt.Sprintf(`
		SELECT id, timestamp_ms, status_code, provider_id, provider_name, model,
		       input_tokens, output_tokens, cost, latency_ms, first_token_ms, is_stream,
		       route_id, route_label,
		       api_key_id, COALESCE(error, ''),
		       cache_creation, cache_hit,
		       COALESCE(chain_json, ''), user_agent, client_ip, request_id
		FROM request_logs %s
		ORDER BY timestamp_ms DESC
		LIMIT ? OFFSET ?`, where)

	dataArgs := append(args, pageSize, offset)
	rows, err := s.db.Query(dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: query logs data: %w", err)
	}
	defer rows.Close()

	var logs []model.RequestLog
	for rows.Next() {
		var (
			l       model.RequestLog
			chainJS string
		)
		if err := rows.Scan(
			&l.ID, &l.Timestamp, &l.StatusCode,
			&l.ProviderID, &l.ProviderName, &l.Model,
			&l.InputTokens, &l.OutputTokens, &l.Cost, &l.LatencyMs, &l.FirstTokenMs, &l.IsStream,
			&l.RouteID, &l.RouteLabel, &l.APIKeyID, &l.Error,
			&l.CacheCreation, &l.CacheHit,
			&chainJS, &l.UserAgent, &l.ClientIP, &l.RequestID,
		); err != nil {
			return nil, 0, fmt.Errorf("store: scan log: %w", err)
		}
		l.Chain = chainFromJSON(chainJS)
		logs = append(logs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if logs == nil {
		logs = []model.RequestLog{}
	}

	return logs, total, nil
}

// InsertRequestLog appends a single log entry. Used by the proxy (Phase 4)
// through the Writer.
func (s *Store) InsertRequestLog(l model.RequestLog) error {
	if l.Cost == 0 && (l.InputTokens > 0 || l.OutputTokens > 0) {
		l.Cost = estimateCost(l.Model, int64(l.InputTokens), int64(l.OutputTokens))
	}
	chainJSON := chainJSONFor(l.Chain)
	return s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO request_logs (id, timestamp_ms, status_code, provider_id, provider_name, model,
			                          input_tokens, output_tokens, cost, latency_ms, first_token_ms, is_stream,
			                          route_id, route_label,
			                          api_key_id, error,
			                          cache_creation, cache_hit,
			                          chain_json, user_agent, client_ip, request_id)
			VALUES (?, ?, ?, ?, ?, ?,
			        ?, ?, ?, ?, ?, ?,
			        ?, ?,
			        ?, ?,
			        ?, ?,
			        ?, ?, ?, ?)`,
			l.ID, l.Timestamp, l.StatusCode, l.ProviderID, l.ProviderName, l.Model,
			l.InputTokens, l.OutputTokens, l.Cost, l.LatencyMs, l.FirstTokenMs, boolInt(l.IsStream),
			l.RouteID, l.RouteLabel,
			l.APIKeyID, l.Error,
			l.CacheCreation, l.CacheHit,
			chainJSON, l.UserAgent, l.ClientIP, l.RequestID)
		return err
	})
}

// InsertRequestLogsBatch inserts multiple log entries in a single transaction.
func (s *Store) InsertRequestLogsBatch(logs []model.RequestLog) error {
	return s.execTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`
			INSERT INTO request_logs (id, timestamp_ms, status_code, provider_id, provider_name, model,
			                          input_tokens, output_tokens, cost, latency_ms, first_token_ms, is_stream,
			                          route_id, route_label,
			                          api_key_id, error,
			                          cache_creation, cache_hit,
			                          chain_json, user_agent, client_ip, request_id)
			VALUES (?, ?, ?, ?, ?, ?,
			        ?, ?, ?, ?, ?, ?,
			        ?, ?,
			        ?, ?,
			        ?, ?,
			        ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, l := range logs {
			if l.Cost == 0 && (l.InputTokens > 0 || l.OutputTokens > 0) {
				l.Cost = estimateCost(l.Model, int64(l.InputTokens), int64(l.OutputTokens))
			}
			chainJSON := chainJSONFor(l.Chain)
			if _, err := stmt.Exec(
				l.ID, l.Timestamp, l.StatusCode, l.ProviderID, l.ProviderName, l.Model,
				l.InputTokens, l.OutputTokens, l.Cost, l.LatencyMs, l.FirstTokenMs, boolInt(l.IsStream),
				l.RouteID, l.RouteLabel,
				l.APIKeyID, l.Error,
				l.CacheCreation, l.CacheHit,
				chainJSON, l.UserAgent, l.ClientIP, l.RequestID,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// PurgeLogs deletes request_logs older than the given number of days.
func (s *Store) PurgeLogs(olderThanDays int) (int, error) {
	cutoff := time.Now().AddDate(0, 0, -olderThanDays).UnixMilli()
	var count int
	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM request_logs WHERE timestamp_ms < ?`, cutoff)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		count = int(n)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("store: purge logs: %w", err)
	}
	slog.Info("store: purged logs", "count", count, "days", olderThanDays)
	return count, nil
}

// ClearLogs deletes ALL request logs. Returns the number of rows deleted.
func (s *Store) ClearLogs() (int, error) {
	var count int
	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM request_logs`)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		count = int(n)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("store: clear logs: %w", err)
	}
	slog.Info("store: cleared logs", "count", count)
	return count, nil
}

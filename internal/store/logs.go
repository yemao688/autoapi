package store

import (
	"database/sql"
	"fmt"
	"time"

	"autoapi/internal/model"
)

// QueryLogs filters request_logs by the given parameters with pagination.
// Returns the rows, total count, and any error.
func (s *Store) QueryLogs(q model.LogQuery) ([]model.RequestLog, int64, error) {
	// Build dynamic WHERE clause.
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
		       input_tokens, output_tokens, cost, latency_ms, route_id, route_label,
		       api_key_id, COALESCE(error, '')
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
		var l model.RequestLog
		if err := rows.Scan(
			&l.ID, &l.Timestamp, &l.StatusCode,
			&l.ProviderID, &l.ProviderName, &l.Model,
			&l.InputTokens, &l.OutputTokens, &l.Cost, &l.LatencyMs,
			&l.RouteID, &l.RouteLabel, &l.APIKeyID, &l.Error,
		); err != nil {
			return nil, 0, fmt.Errorf("store: scan log: %w", err)
		}
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
	return s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO request_logs (id, timestamp_ms, status_code, provider_id, provider_name, model,
			                          input_tokens, output_tokens, cost, latency_ms, route_id, route_label,
			                          api_key_id, error)
			VALUES (?, ?, ?, ?, ?, ?,
			        ?, ?, ?, ?, ?, ?,
			        ?, ?)`,
			l.ID, l.Timestamp, l.StatusCode, l.ProviderID, l.ProviderName, l.Model,
			l.InputTokens, l.OutputTokens, l.Cost, l.LatencyMs, l.RouteID, l.RouteLabel,
			l.APIKeyID, l.Error)
		return err
	})
}

// InsertRequestLogsBatch inserts multiple log entries in a single transaction.
func (s *Store) InsertRequestLogsBatch(logs []model.RequestLog) error {
	return s.execTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`
			INSERT INTO request_logs (id, timestamp_ms, status_code, provider_id, provider_name, model,
			                          input_tokens, output_tokens, cost, latency_ms, route_id, route_label,
			                          api_key_id, error)
			VALUES (?, ?, ?, ?, ?, ?,
			        ?, ?, ?, ?, ?, ?,
			        ?, ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, l := range logs {
			if l.Cost == 0 && (l.InputTokens > 0 || l.OutputTokens > 0) {
				l.Cost = estimateCost(l.Model, int64(l.InputTokens), int64(l.OutputTokens))
			}
			if _, err := stmt.Exec(
				l.ID, l.Timestamp, l.StatusCode, l.ProviderID, l.ProviderName, l.Model,
				l.InputTokens, l.OutputTokens, l.Cost, l.LatencyMs, l.RouteID, l.RouteLabel,
				l.APIKeyID, l.Error,
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
	return count, nil
}

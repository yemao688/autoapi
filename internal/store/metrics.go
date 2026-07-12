package store

import (
	"autoapi/internal/model"
	"database/sql"
	"fmt"
	"time"
)

func metricMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
func metricTime(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// UpsertTargetRuntimeSummaries writes a complete checkpoint in one transaction.
func (s *Store) UpsertTargetRuntimeSummaries(items []model.TargetRuntimeSummary) error {
	if len(items) == 0 {
		return nil
	}
	return s.execTx(func(tx *sql.Tx) error {
		for _, v := range items {
			if err := v.Validate(); err != nil {
				return fmt.Errorf("invalid runtime summary: %w", err)
			}
			k := v.Key.Normalized()
			_, err := tx.Exec(`INSERT INTO target_runtime_summary (target_id,provider_id,model_name,endpoint,requests,attempts,successes,failures,status_429,status_5xx,transport,client_aborts,truncated,downstream,last_used,last_success,last_failure,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(target_id,provider_id,model_name,endpoint) DO UPDATE SET requests=excluded.requests,attempts=excluded.attempts,successes=excluded.successes,failures=excluded.failures,status_429=excluded.status_429,status_5xx=excluded.status_5xx,transport=excluded.transport,client_aborts=excluded.client_aborts,truncated=excluded.truncated,downstream=excluded.downstream,last_used=excluded.last_used,last_success=excluded.last_success,last_failure=excluded.last_failure,updated_at=excluded.updated_at`, k.TargetID, k.ProviderID, k.ModelName, k.Endpoint, v.Requests, v.Attempts, v.Successes, v.Failures, v.Status429, v.Status5xx, v.Transport, v.ClientAborts, v.Truncated, v.Downstream, metricMS(v.LastUsed), metricMS(v.LastSuccess), metricMS(v.LastFailure), metricMS(v.UpdatedAt))
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) LoadActiveTargetRuntimeSummaries(now time.Time, ttl time.Duration) ([]model.TargetRuntimeSummary, error) {
	rows, err := s.db.Query(`SELECT target_id,provider_id,model_name,endpoint,requests,attempts,successes,failures,status_429,status_5xx,transport,client_aborts,truncated,downstream,last_used,last_success,last_failure,updated_at FROM target_runtime_summary WHERE last_used >= ?`, now.Add(-ttl).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TargetRuntimeSummary
	for rows.Next() {
		var v model.TargetRuntimeSummary
		var lu, ls, lf, up int64
		if err := rows.Scan(&v.Key.TargetID, &v.Key.ProviderID, &v.Key.ModelName, &v.Key.Endpoint, &v.Requests, &v.Attempts, &v.Successes, &v.Failures, &v.Status429, &v.Status5xx, &v.Transport, &v.ClientAborts, &v.Truncated, &v.Downstream, &lu, &ls, &lf, &up); err != nil {
			return nil, err
		}
		v.LastUsed, v.LastSuccess, v.LastFailure, v.UpdatedAt = metricTime(lu), metricTime(ls), metricTime(lf), metricTime(up)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) CleanupTargetRuntimeSummaries(now time.Time, ttl time.Duration) error {
	return s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM target_runtime_summary WHERE last_used < ?`, now.Add(-ttl).UnixMilli())
		return err
	})
}

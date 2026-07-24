package store

import "autoapi/internal/model"

// buildLogFilter constructs a shared WHERE clause + parameter slice from
// a LogQuery. It is used by QueryLogs, UsageStats aggregations, and
// chart queries so all three apply identical filter semantics.
//
// When includePending is false, a `status_code != 0` predicate is added
// so pending (two-phase) log rows do not inflate aggregation counts.
// QueryLogs passes true because the log table should show pending rows;
// all aggregation queries pass false.
//
// Pagination fields (Page, PageSize) are intentionally ignored — they
// only apply to QueryLogs, not to aggregations or charts.
func buildLogFilter(q model.LogQuery, includePending bool) (where string, args []interface{}) {
	where = "WHERE 1=1"
	args = []interface{}{}

	if !includePending {
		where += " AND status_code != 0"
	}
	if q.StartDate > 0 {
		where += " AND timestamp_ms >= ?"
		args = append(args, q.StartDate)
	}
	if q.EndDate > 0 {
		where += " AND timestamp_ms <= ?"
		args = append(args, q.EndDate)
	}
	if q.APIKeyID != "" {
		where += " AND api_key_id = ?"
		args = append(args, q.APIKeyID)
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
	return where, args
}

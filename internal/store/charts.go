package store

import (
	"fmt"
	"math"
	"time"

	"autoapi/internal/model"
)

// GetChartAggregates returns pre-aggregated time-series and breakdown data for
// the dashboard charts. It uses the same filter semantics as QueryLogs so that
// chart numbers always match the filtered request log table.
func (s *Store) GetChartAggregates(q model.ChartQuery) (*model.ChartAggregates, error) {
	bucketExpr, bucketSize := chartBucketExpr(q.StartDate, q.EndDate)
	where, args := buildChartFilter(q)

	buckets, err := s.chartBuckets(bucketExpr, where, args)
	if err != nil {
		return nil, fmt.Errorf("store: chart buckets: %w", err)
	}

	breakdown, err := s.chartStatusBreakdown(where, args)
	if err != nil {
		return nil, fmt.Errorf("store: chart status breakdown: %w", err)
	}

	shares, err := s.chartProviderShares(where, args)
	if err != nil {
		return nil, fmt.Errorf("store: chart provider shares: %w", err)
	}

	return &model.ChartAggregates{
		Range:           chartRangeLabel(q.StartDate, q.EndDate),
		BucketSize:      bucketSize,
		Buckets:         buckets,
		StatusBreakdown: breakdown,
		ProviderShares:  shares,
	}, nil
}

func chartBucketExpr(startDate, endDate int64) (string, string) {
	// Default to daily when no range is supplied, or when the range spans more
	// than one day. Use hourly buckets for intra-day ranges.
	if endDate > startDate && endDate-startDate <= 24*60*60*1000 {
		return "strftime('%Y-%m-%d %H:00', timestamp_ms/1000, 'unixepoch', 'localtime')", "hour"
	}
	return "strftime('%Y-%m-%d', timestamp_ms/1000, 'unixepoch', 'localtime')", "day"
}

func chartRangeLabel(startDate, endDate int64) string {
	format := func(ms int64) string {
		if ms <= 0 {
			return ""
		}
		return time.UnixMilli(ms).Format("2006-01-02")
	}
	return fmt.Sprintf("%s..%s", format(startDate), format(endDate))
}

func buildChartFilter(q model.ChartQuery) (where string, args []interface{}) {
	where = "WHERE 1=1"
	args = []interface{}{}
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
	if q.Search != "" {
		where += " AND (model LIKE ? OR route_label LIKE ? OR error LIKE ?)"
		pattern := "%" + q.Search + "%"
		args = append(args, pattern, pattern, pattern)
	}
	return where, args
}

func (s *Store) chartBuckets(bucketExpr, where string, args []interface{}) ([]model.TimeBucket, error) {
	query := fmt.Sprintf(`
		SELECT
			%s AS bucket,
			SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) AS success,
			SUM(CASE WHEN status_code = 429 THEN 1 ELSE 0 END) AS rate_limited,
			SUM(CASE WHEN (status_code >= 400 AND status_code != 429) OR error != '' THEN 1 ELSE 0 END) AS errors,
			SUM(input_tokens) AS input_tokens,
			SUM(output_tokens) AS output_tokens,
			SUM(cost) AS cost,
			AVG(latency_ms) AS avg_latency_ms,
			AVG(CASE WHEN is_stream = 1 AND first_token_ms > 0 THEN first_token_ms END) AS avg_ttft_ms
		FROM request_logs %s
		GROUP BY bucket
		ORDER BY bucket ASC`, bucketExpr, where)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var buckets []model.TimeBucket
	for rows.Next() {
		var b model.TimeBucket
		var avgLatency sqlNullFloat64
		var avgTTFT sqlNullFloat64
		if err := rows.Scan(
			&b.Bucket,
			&b.Success,
			&b.RateLimited,
			&b.Error,
			&b.InputTokens,
			&b.OutputTokens,
			&b.Cost,
			&avgLatency,
			&avgTTFT,
		); err != nil {
			return nil, err
		}
		b.AvgLatencyMs = int64(math.Round(avgLatency.Float64))
		b.AvgTTFTMs = int64(math.Round(avgTTFT.Float64))
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if buckets == nil {
		buckets = []model.TimeBucket{}
	}
	return buckets, nil
}

type sqlNullFloat64 struct {
	Float64 float64
	Valid   bool
}

func (n *sqlNullFloat64) Scan(value interface{}) error {
	if value == nil {
		n.Float64, n.Valid = 0, false
		return nil
	}
	n.Valid = true
	switch v := value.(type) {
	case float64:
		n.Float64 = v
	case int64:
		n.Float64 = float64(v)
	case int:
		n.Float64 = float64(v)
	default:
		return fmt.Errorf("unsupported sqlNullFloat64 type: %T", value)
	}
	return nil
}

func (s *Store) chartStatusBreakdown(where string, args []interface{}) ([]model.StatusBreakdown, error) {
	query := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success,
			COALESCE(SUM(CASE WHEN status_code = 429 THEN 1 ELSE 0 END), 0) AS rate_limited,
			COALESCE(SUM(CASE WHEN (status_code >= 400 AND status_code != 429) OR error != '' THEN 1 ELSE 0 END), 0) AS errors,
			COALESCE(SUM(CASE WHEN status_code NOT BETWEEN 200 AND 299 AND status_code != 429 AND (status_code < 400 OR status_code = 0) AND error = '' THEN 1 ELSE 0 END), 0) AS other,
			COUNT(*) AS total
		FROM request_logs %s`, where)

	var success, rateLimited, errors, other, total int64
	row := s.db.QueryRow(query, args...)
	if err := row.Scan(&success, &rateLimited, &errors, &other, &total); err != nil {
		return nil, err
	}
	if total == 0 {
		return []model.StatusBreakdown{}, nil
	}

	parts := []struct {
		label string
		count int64
	}{
		{"2xx", success},
		{"429", rateLimited},
		{"错误", errors},
		{"其他", other},
	}
	var breakdown []model.StatusBreakdown
	for _, p := range parts {
		if p.count == 0 {
			continue
		}
		breakdown = append(breakdown, model.StatusBreakdown{
			Label:   p.label,
			Count:   p.count,
			Percent: round2(float64(p.count) / float64(total) * 100),
		})
	}
	return breakdown, nil
}

func (s *Store) chartProviderShares(where string, args []interface{}) ([]model.ProviderShare, error) {
	query := fmt.Sprintf(`
		SELECT provider_name, provider_id,
			   SUM(input_tokens) + SUM(output_tokens) AS tokens,
			   SUM(cost) AS cost,
			   COUNT(*) AS requests
		FROM request_logs %s
		GROUP BY provider_id, provider_name
		ORDER BY tokens DESC`, where)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var shares []model.ProviderShare
	var totalTokens int64
	for rows.Next() {
		var p model.ProviderShare
		var requests int64
		if err := rows.Scan(&p.ProviderName, &p.ProviderID, &p.Tokens, &p.Cost, &requests); err != nil {
			return nil, err
		}
		totalTokens += p.Tokens
		shares = append(shares, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if totalTokens > 0 {
		for i := range shares {
			shares[i].Percent = round2(float64(shares[i].Tokens) / float64(totalTokens) * 100)
		}
	}
	if shares == nil {
		shares = []model.ProviderShare{}
	}
	return shares, nil
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

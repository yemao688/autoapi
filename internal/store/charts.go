package store

import (
	"fmt"
	"time"

	"autoapi/internal/model"
)

// GetUsageTrends returns pre-aggregated usage-trend buckets for the
// usage-stats chart. It uses the same filter semantics as QueryLogs so chart
// numbers always match the filtered request log table.
func (s *Store) GetUsageTrends(q model.UsageTrendsQuery) (*model.UsageTrends, error) {
	bucketExpr, bucketSize := chartBucketExpr(q.StartDate, q.EndDate)
	where, args := buildChartFilter(q)

	buckets, err := s.chartBuckets(bucketExpr, where, args)
	if err != nil {
		return nil, fmt.Errorf("store: chart buckets: %w", err)
	}

	return &model.UsageTrends{
		Range:      chartRangeLabel(q.StartDate, q.EndDate),
		BucketSize: bucketSize,
		Buckets:    buckets,
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

func buildChartFilter(q model.UsageTrendsQuery) (where string, args []interface{}) {
	// Convert UsageTrendsQuery to LogQuery and reuse the shared filter
	// builder. UsageTrendsQuery has no Status field, so it maps to an
	// empty status — which buildLogFilter treats as "no status filter".
	return buildLogFilter(model.LogQuery{
		StartDate: q.StartDate,
		EndDate:   q.EndDate,
		APIKeyID:  q.APIKeyID,
		Provider:  q.Provider,
		RouteID:   q.RouteID,
		Model:     q.Model,
		Search:    q.Search,
	}, false)
}

func (s *Store) chartBuckets(bucketExpr, where string, args []interface{}) ([]model.UsageTrendBucket, error) {
	query := fmt.Sprintf(`
		SELECT
			%s AS bucket,
			SUM(cost) AS cost,
			SUM(cache_creation) AS cache_creation,
			SUM(cache_hit) AS cache_hit,
			SUM(input_tokens) AS input_tokens,
			SUM(output_tokens) AS output_tokens
		FROM request_logs %s
		GROUP BY bucket
		ORDER BY bucket ASC`, bucketExpr, where)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var buckets []model.UsageTrendBucket
	for rows.Next() {
		var b model.UsageTrendBucket
		if err := rows.Scan(
			&b.Bucket,
			&b.Cost,
			&b.CacheCreation,
			&b.CacheHit,
			&b.Input,
			&b.Output,
		); err != nil {
			return nil, err
		}
		buckets = append(buckets, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if buckets == nil {
		buckets = []model.UsageTrendBucket{}
	}
	return buckets, nil
}

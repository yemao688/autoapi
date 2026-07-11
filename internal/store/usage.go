package store

import (
	"fmt"
	"log/slog"
	"math"

	"autoapi/internal/model"
)

// UsageStats aggregates data for the usage-stats page under the given
// filter. All sub-queries (tokenStats, logStats, providerShares,
// modelRanking) receive the same filter snapshot so cards, charts, and
// lists always agree on the same filtered row set.
//
// Pagination fields (Page, PageSize) in q are ignored — the aggregations
// span the entire filtered range.
func (s *Store) UsageStats(q model.LogQuery) (*model.UsageStats, error) {
	u := &model.UsageStats{}

	var err error
	u.TokenStats, err = s.tokenStatsFiltered(q)
	if err != nil {
		return nil, err
	}

	u.Providers, err = s.providerSharesFiltered(q)
	if err != nil {
		return nil, err
	}

	u.ModelRanking, err = s.modelRankingFiltered(q, 8)
	if err != nil {
		return nil, err
	}

	u.LogStats, err = s.logStatsFiltered(q)
	if err != nil {
		return nil, err
	}

	return u, nil
}

// tokenStatsFiltered computes KPI cards for the filtered range. Unlike
// the old tokenStats() which compared calendar months, filtered stats
// show totals for the selected period with no delta (shown as "—" in
// the UI) because comparison-period semantics under arbitrary date
// ranges are ambiguous and were not requested.
func (s *Store) tokenStatsFiltered(q model.LogQuery) ([]model.Stat, error) {
	where, args := buildLogFilter(q, false)

	var totalTokens int64
	var totalCost float64
	var totalReqs int64

	// Single query for tokens + cost + count to avoid three scans.
	row := s.db.QueryRow(fmt.Sprintf(`
		SELECT
			COALESCE(SUM(input_tokens + output_tokens), 0),
			COALESCE(SUM(cost), 0),
			COUNT(*)
		FROM request_logs %s`, where), args...)
	if err := row.Scan(&totalTokens, &totalCost, &totalReqs); err != nil {
		slog.Error("store: tokenStatsFiltered scan", "err", err)
	}

	return []model.Stat{
		makeStat("usage.stats.totalRequests", fmt.Sprintf("%d", totalReqs), "—", ""),
		makeStat("usage.stats.totalTokens", fmt.Sprintf("%d", totalTokens), "—", ""),
		makeStat("usage.stats.estimatedCost", fmt.Sprintf("$%.2f", totalCost), "—", ""),
	}, nil
}

// providerSharesFiltered computes the percentage breakdown by upstream
// provider under the given filter.
func (s *Store) providerSharesFiltered(q model.LogQuery) ([]model.ProviderShare, error) {
	where, args := buildLogFilter(q, false)
	query := fmt.Sprintf(`
		SELECT COALESCE(provider_id, ''), COALESCE(provider_name, ''),
		       COALESCE(SUM(input_tokens + output_tokens), 0),
		       COALESCE(SUM(cost), 0)
		FROM request_logs %s
		GROUP BY provider_id
		ORDER BY 3 DESC`, where)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: provider shares filtered: %w", err)
	}
	defer rows.Close()

	var shares []model.ProviderShare
	var grandTotal int64
	for rows.Next() {
		var ps model.ProviderShare
		if err := rows.Scan(&ps.ProviderID, &ps.ProviderName, &ps.Tokens, &ps.Cost); err != nil {
			return nil, fmt.Errorf("store: scan provider share: %w", err)
		}
		grandTotal += ps.Tokens
		shares = append(shares, ps)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if grandTotal > 0 {
		for i := range shares {
			shares[i].Percent = math.Round(float64(shares[i].Tokens)/float64(grandTotal)*10000) / 100
		}
	}
	if shares == nil {
		shares = []model.ProviderShare{}
	}
	return shares, nil
}

// modelRankingFiltered returns the top N models by request count under
// the given filter. Ordered by request count descending.
func (s *Store) modelRankingFiltered(q model.LogQuery, limit int) ([]model.ModelRanking, error) {
	where, args := buildLogFilter(q, false)
	query := fmt.Sprintf(`
		SELECT model, COALESCE(provider_name, ''),
		       COUNT(*) AS reqs,
		       COALESCE(SUM(input_tokens + output_tokens), 0),
		       COALESCE(SUM(cost), 0)
		FROM request_logs %s
		GROUP BY model
		ORDER BY reqs DESC
		LIMIT ?`, where)
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: model ranking filtered: %w", err)
	}
	defer rows.Close()

	var rankings []model.ModelRanking
	for rows.Next() {
		var mr model.ModelRanking
		if err := rows.Scan(&mr.Model, &mr.ProviderName, &mr.Requests, &mr.Tokens, &mr.Cost); err != nil {
			return nil, fmt.Errorf("store: scan model ranking: %w", err)
		}
		rankings = append(rankings, mr)
	}
	if rankings == nil {
		rankings = []model.ModelRanking{}
	}
	return rankings, rows.Err()
}

// logStatsFiltered computes aggregate quality-of-service stats under
// the given filter: total requests, success rate, and error count.
// Deltas are not shown ("—") because comparison-period semantics are
// ambiguous under arbitrary filters.
func (s *Store) logStatsFiltered(q model.LogQuery) ([]model.Stat, error) {
	where, args := buildLogFilter(q, false)

	var total, success, errCount int64

	row := s.db.QueryRow(fmt.Sprintf(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 AND error = '' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN error != '' OR status_code >= 400 THEN 1 ELSE 0 END), 0)
		FROM request_logs %s`, where), args...)
	if err := row.Scan(&total, &success, &errCount); err != nil {
		return nil, fmt.Errorf("store: log stats filtered: %w", err)
	}

	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}

	return []model.Stat{
		makeStat("usage.stats.totalRequests", fmt.Sprintf("%d", total), "—", ""),
		makeStat("usage.stats.successRate", fmt.Sprintf("%.1f%%", successRate), "—", ""),
		makeStat("usage.stats.errors", fmt.Sprintf("%d", errCount), "—", ""),
	}, nil
}

// ----- Dashboard helpers (used by dashboard.go, not filtered) -----

func (s *Store) sumCostSince(startMs int64) float64 {
	row := s.db.QueryRow(`
		SELECT COALESCE(SUM(cost), 0)
		FROM request_logs WHERE timestamp_ms >= ? AND status_code != 0`, startMs)
	var total float64
	if err := row.Scan(&total); err != nil {
		slog.Error("store: sumCostSince", "err", err)
	}
	return total
}

func (s *Store) countRequestsSince(startMs int64) int64 {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE timestamp_ms >= ? AND status_code != 0`, startMs)
	var n int64
	if err := row.Scan(&n); err != nil {
		slog.Error("store: countRequestsSince scan failed", "err", err)
	}
	return n
}

func deltaCostStr(current, previous float64) string {
	if previous == 0 {
		return "—"
	}
	pct := (current - previous) / previous * 100
	if pct >= 0 {
		return fmt.Sprintf("+%.1f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}

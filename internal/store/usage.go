package store

import (
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"autoapi/internal/model"
)

// UsageStats aggregates data for the usage-stats page.
func (s *Store) UsageStats() (*model.UsageStats, error) {
	u := &model.UsageStats{}

	// Token stats
	var err error
	u.TokenStats, err = s.tokenStats()
	if err != nil {
		return nil, err
	}

	// Token trend (30 days)
	u.TokenTrend30, err = s.tokenTrend(30)
	if err != nil {
		return nil, err
	}

	// Provider shares
	u.Providers, err = s.providerShares()
	if err != nil {
		return nil, err
	}

	// Model ranking (top 5)
	u.ModelRanking, err = s.modelRanking(5)
	if err != nil {
		return nil, err
	}

	// Log stats
	u.LogStats, err = s.logStats()
	if err != nil {
		return nil, err
	}

	// Recent logs (last 50)
	logs, total, err := s.QueryLogs(model.LogQuery{
		Page:     1,
		PageSize: 50,
	})
	if err != nil {
		return nil, err
	}
	u.Logs = logs
	u.LogTotal = total

	return u, nil
}

// tokenStats computes 30-day aggregate KPI cards.
func (s *Store) tokenStats() ([]model.Stat, error) {
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).UnixMilli()
	startOfPrevMonth := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, now.Location()).UnixMilli()

	thisMonth := s.sumTokensSince(startOfMonth)
	prevMonth := s.sumTokensSince(startOfPrevMonth) - thisMonth
	if prevMonth < 0 {
		prevMonth = 0
	}

	thisCost := s.sumCostSince(startOfMonth)
	prevCost := s.sumCostSince(startOfPrevMonth) - thisCost
	if prevCost < 0 {
		prevCost = 0
	}

	thisRequests := s.countRequestsSince(startOfMonth)
	prevRequests := s.countRequestsSince(startOfPrevMonth) - thisRequests
	if prevRequests < 0 {
		prevRequests = 0
	}

	return []model.Stat{
		makeStat("Total Requests", fmt.Sprintf("%d", thisRequests), deltaStr(thisRequests, prevRequests), ""),
		makeStat("Total Tokens", fmt.Sprintf("%d", thisMonth), deltaStr(thisMonth, prevMonth), ""),
		makeStat("Estimated Cost", fmt.Sprintf("$%.2f", thisCost), deltaCostStr(thisCost, prevCost), ""),
	}, nil
}

func (s *Store) sumCostSince(startMs int64) float64 {
	row := s.db.QueryRow(`
		SELECT COALESCE(SUM(cost), 0)
		FROM request_logs WHERE timestamp_ms >= ?`, startMs)
	var total float64
	if err := row.Scan(&total); err != nil {
		slog.Error("store: sumCostSince", "err", err)
	}
	return total
}

func (s *Store) countRequestsSince(startMs int64) int64 {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM request_logs WHERE timestamp_ms >= ?`, startMs)
	var n int64
	row.Scan(&n)
	return n
}

// providerShares computes the percentage breakdown by provider for the last 30 days.
func (s *Store) providerShares() ([]model.ProviderShare, error) {
	cutoff := time.Now().AddDate(0, 0, -30).UnixMilli()
	rows, err := s.db.Query(`
		SELECT COALESCE(provider_id, ''), COALESCE(provider_name, ''), COALESCE(SUM(input_tokens + output_tokens), 0), COALESCE(SUM(cost), 0)
		FROM request_logs WHERE timestamp_ms >= ?
		GROUP BY provider_id ORDER BY 3 DESC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("store: provider shares: %w", err)
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

// modelRanking returns the top N models by token count in the last 30 days.
func (s *Store) modelRanking(limit int) ([]model.ModelRanking, error) {
	cutoff := time.Now().AddDate(0, 0, -30).UnixMilli()
	rows, err := s.db.Query(`
		SELECT model, COALESCE(provider_name, ''),
		       COUNT(*) AS reqs,
		       COALESCE(SUM(input_tokens + output_tokens), 0),
		       COALESCE(SUM(cost), 0)
		FROM request_logs WHERE timestamp_ms >= ?
		GROUP BY model
		ORDER BY reqs DESC
		LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("store: model ranking: %w", err)
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

// logStats computes aggregate statistics on request_logs.
func (s *Store) logStats() ([]model.Stat, error) {
	cutoff := time.Now().AddDate(0, 0, -30).UnixMilli()

	// Total logs, success count, error count, p95 latency
	type logAgg struct {
		total       int64
		success     int64
		errCount    int64
		latencies   []int
	}

	rows, err := s.db.Query(`
		SELECT status_code, latency_ms,
		       CASE WHEN error != '' THEN 1 ELSE 0 END AS has_err
		FROM request_logs WHERE timestamp_ms >= ?`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("store: log stats: %w", err)
	}
	defer rows.Close()

	var total, success, errCount int64
	var latencies []int
	for rows.Next() {
		var status int
		var lat int
		var hasErr int
		if err := rows.Scan(&status, &lat, &hasErr); err != nil {
			continue
		}
		total++
		if status >= 200 && status < 300 && hasErr == 0 {
			success++
		}
		if hasErr == 1 || status >= 400 {
			errCount++
		}
		latencies = append(latencies, lat)
	}

	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total) * 100
	}
	p95 := computeP95(latencies)

	return []model.Stat{
		makeStat("Total Requests (30d)", fmt.Sprintf("%d", total), "", ""),
		makeStat("Success Rate", fmt.Sprintf("%.1f%%", successRate), "", ""),
		makeStat("P95 Latency", fmt.Sprintf("%dms", p95), "", ""),
		makeStat("Errors (30d)", fmt.Sprintf("%d", errCount), "", ""),
	}, nil
}

func computeP95(values []int) int {
	if len(values) == 0 {
		return 0
	}
	// Sort-based 95th percentile. sort.Ints is O(n log n) — required because
	// request-log slices can grow into the 100K+ range over months of use.
	sorted := make([]int, len(values))
	copy(sorted, values)
	sort.Ints(sorted)
	idx := int(math.Ceil(float64(len(sorted))*0.95) - 1)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
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

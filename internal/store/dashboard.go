package store

import (
	"fmt"
	"time"

	"autoapi/internal/model"
)

// Dashboard aggregates everything the dashboard page needs.
func (s *Store) Dashboard() (*model.DashboardData, error) {
	d := &model.DashboardData{}

	// Stats: today, this week, this month tokens
	now := time.Now()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	startOfWeek := now.AddDate(0, 0, -int(now.Weekday())).UnixMilli()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).UnixMilli()

	var err error
	d.Stats, err = s.computeStats(startOfDay, startOfWeek, startOfMonth)
	if err != nil {
		return nil, err
	}

	// Token trend (last 7 days)
	d.TokenTrend, err = s.tokenTrend(7)
	if err != nil {
		return nil, err
	}

	// Providers
	d.Providers, err = s.ListProviders()
	if err != nil {
		return nil, err
	}

	// Recent activity (last 10)
	d.RecentActivity, err = s.recentLogs(10)
	if err != nil {
		return nil, err
	}

	// ServiceHealth is intentionally left empty here; App.GetDashboard() fills it
	// from Service.GetSystemHealth() to avoid two sources of truth.

	return d, nil
}

func (s *Store) computeStats(startOfDay, startOfWeek, startOfMonth int64) ([]model.Stat, error) {
	providerCount := s.countProviders()

	todayTokens := s.sumTokensSince(startOfDay)
	weekTokens := s.sumTokensSince(startOfWeek)
	monthTokens := s.sumTokensSince(startOfMonth)

	todayCost := s.sumCostSince(startOfDay)
	weekCost := s.sumCostSince(startOfWeek)
	monthCost := s.sumCostSince(startOfMonth)

	// Previous period for delta calculation
	prevWeekStart := startOfWeek - (7 * 24 * 60 * 60 * 1000)
	prevMonthStart := startOfMonth - (28 * 24 * 60 * 60 * 1000)
	prevWeekTokens := s.sumTokensSince(prevWeekStart)
	prevMonthTokens := s.sumTokensSince(prevMonthStart)
	prevWeekCost := s.sumCostSince(prevWeekStart)
	prevMonthCost := s.sumCostSince(prevMonthStart)

	stats := []model.Stat{
		makeStat("Today's Tokens", fmt.Sprintf("%d", todayTokens), deltaStr(todayTokens, prevWeekTokens/7), ""),
		makeStat("This Week", fmt.Sprintf("%d", weekTokens), deltaStr(weekTokens, prevWeekTokens), ""),
		makeStat("This Month", fmt.Sprintf("%d", monthTokens), deltaStr(monthTokens, prevMonthTokens), ""),
		makeStat("Today's Cost", fmt.Sprintf("$%.2f", todayCost), deltaCostStr(todayCost, prevWeekCost/7), ""),
		makeStat("This Week Cost", fmt.Sprintf("$%.2f", weekCost), deltaCostStr(weekCost, prevWeekCost), ""),
		makeStat("This Month Cost", fmt.Sprintf("$%.2f", monthCost), deltaCostStr(monthCost, prevMonthCost), ""),
		makeStat("Active Providers", fmt.Sprintf("%d", providerCount), "", ""),
	}
	return stats, nil
}

func (s *Store) sumTokensSince(startMs int64) int64 {
	row := s.db.QueryRow(`
		SELECT COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM request_logs WHERE timestamp_ms >= ?`, startMs)
	var total int64
	row.Scan(&total)
	return total
}

func (s *Store) countProviders() int {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM providers`)
	var n int
	row.Scan(&n)
	return n
}

// tokenTrend returns daily token sums for the last N days.
func (s *Store) tokenTrend(days int) ([]model.TokenTrendPoint, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UnixMilli()
	rows, err := s.db.Query(`
		SELECT date(timestamp_ms / 1000, 'unixepoch') AS day,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cost), 0)
		FROM request_logs
		WHERE timestamp_ms >= ?
		GROUP BY day
		ORDER BY day ASC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("store: token trend: %w", err)
	}
	defer rows.Close()

	// Build a map for day → point, then fill gaps.
	pointMap := map[string]*model.TokenTrendPoint{}
	for rows.Next() {
		var date string
		var in, out int64
		var cost float64
		if err := rows.Scan(&date, &in, &out, &cost); err != nil {
			return nil, fmt.Errorf("store: scan token trend: %w", err)
		}
		pointMap[date] = &model.TokenTrendPoint{
			Date:         date,
			InputTokens:  in,
			OutputTokens: out,
			Cost:         cost,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fill zero-value points for missing days.
	var trend []model.TokenTrendPoint
	for i := days - 1; i >= 0; i-- {
		date := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		if pt, ok := pointMap[date]; ok {
			trend = append(trend, *pt)
		} else {
			trend = append(trend, model.TokenTrendPoint{
				Date: date,
			})
		}
	}
	return trend, nil
}

func (s *Store) recentLogs(limit int) ([]model.RequestLog, error) {
	rows, err := s.db.Query(`
		SELECT id, timestamp_ms, status_code, provider_id, provider_name, model,
		       input_tokens, output_tokens, cost, latency_ms, route_id, route_label,
		       api_key_id, COALESCE(error, '')
		FROM request_logs
		ORDER BY timestamp_ms DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent logs: %w", err)
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
			return nil, fmt.Errorf("store: scan recent log: %w", err)
		}
		logs = append(logs, l)
	}
	if logs == nil {
		logs = []model.RequestLog{}
	}
	return logs, rows.Err()
}

func (s *Store) getProxyPort() int {
	settings, err := s.GetSettings()
	if err != nil {
		return 8344
	}
	return settings.Server.Port
}

// ---------------------------------------------------------------------------
//  Stat helpers
// ---------------------------------------------------------------------------

func makeStat(label, value, delta, trend string) model.Stat {
	if trend == "" {
		trend = "flat"
	}
	return model.Stat{
		Label: label,
		Value: value,
		Delta: delta,
		Trend: trend,
	}
}

func deltaStr(current, previous int64) string {
	if previous == 0 {
		return "—"
	}
	pct := float64(current-previous) / float64(previous) * 100
	if pct >= 0 {
		return fmt.Sprintf("+%.1f%%", pct)
	}
	return fmt.Sprintf("%.1f%%", pct)
}

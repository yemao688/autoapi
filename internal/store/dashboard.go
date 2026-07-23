package store

import (
	"fmt"
	"time"

	"autoapi/internal/model"
)

// Dashboard aggregates everything the dashboard page needs.
func (s *Store) Dashboard() (*model.DashboardData, error) {
	d := &model.DashboardData{}

	var err error
	d.Stats, err = s.computeDashboardStats()
	if err != nil {
		return nil, err
	}

	// Token trend (last 7 days)
	d.TokenTrend, err = s.tokenTrend()
	if err != nil {
		return nil, err
	}

	// Model rule summaries (max 6, display order, no targets)
	d.ModelRules, err = s.listModelRuleSummaries()
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

func (s *Store) computeDashboardStats() ([]model.Stat, error) {
	providerCount := s.countProviders()

	now := time.Now()
	loc := now.Location()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UnixMilli()
	startOfWeek := now.AddDate(0, 0, -int(now.Weekday()))
	startOfWeekMs := time.Date(startOfWeek.Year(), startOfWeek.Month(), startOfWeek.Day(), 0, 0, 0, 0, loc).UnixMilli()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).UnixMilli()

	todayTokens := s.sumTokensSince(startOfDay)
	weekTokens := s.sumTokensSince(startOfWeekMs)
	monthTokens := s.sumTokensSince(startOfMonth)

	todayCost := s.sumCostSince(startOfDay)
	weekCost := s.sumCostSince(startOfWeekMs)
	monthCost := s.sumCostSince(startOfMonth)

	// Previous period for delta calculation
	prevWeekStart := startOfWeekMs - (7 * 24 * 60 * 60 * 1000)
	prevMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, -1, 0).UnixMilli()
	prevWeekTokens := s.sumTokensSince(prevWeekStart)
	prevMonthTokens := s.sumTokensSince(prevMonthStart)
	prevWeekCost := s.sumCostSince(prevWeekStart)
	prevMonthCost := s.sumCostSince(prevMonthStart)

	stats := []model.Stat{
		makeStat("dashboard.stats.todayTokens", fmt.Sprintf("%d", todayTokens), deltaStr(todayTokens, prevWeekTokens/7), ""),
		makeStat("dashboard.stats.thisWeek", fmt.Sprintf("%d", weekTokens), deltaStr(weekTokens, prevWeekTokens), ""),
		makeStat("dashboard.stats.thisMonth", fmt.Sprintf("%d", monthTokens), deltaStr(monthTokens, prevMonthTokens), ""),
		makeStat("dashboard.stats.todayCost", fmt.Sprintf("$%.2f", todayCost), deltaCostStr(todayCost, prevWeekCost/7), ""),
		makeStat("dashboard.stats.thisWeekCost", fmt.Sprintf("$%.2f", weekCost), deltaCostStr(weekCost, prevWeekCost), ""),
		makeStat("dashboard.stats.thisMonthCost", fmt.Sprintf("$%.2f", monthCost), deltaCostStr(monthCost, prevMonthCost), ""),
		makeStat("dashboard.stats.providerCount", fmt.Sprintf("%d", providerCount), "", ""),
	}
	return stats, nil
}

func (s *Store) sumTokensSince(startMs int64) int64 {
	row := s.db.QueryRow(`
		SELECT COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM request_logs WHERE timestamp_ms >= ? AND status_code != 0`, startMs)
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

// tokenTrend returns exactly 7 daily token sums for the last 7 local calendar
// days (today-6 through today), using SQLite localtime grouping and an
// exclusive upper bound at tomorrow midnight.
func (s *Store) tokenTrend() ([]model.TokenTrendPoint, error) {
	now := time.Now()
	loc := now.Location()

	// 7 local calendar-day buckets: today-6 (inclusive) through today,
	// ending at tomorrow midnight (exclusive) so a request at 23:59:59
	// today still falls in today's bucket.
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -6)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)

	startMs := start.UnixMilli()
	endMs := end.UnixMilli()

	rows, err := s.db.Query(`
		SELECT date(timestamp_ms / 1000, 'unixepoch', 'localtime') AS day,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cost), 0)
		FROM request_logs
		WHERE timestamp_ms >= ? AND timestamp_ms < ? AND status_code != 0
		GROUP BY day
		ORDER BY day ASC`, startMs, endMs)
	if err != nil {
		return nil, fmt.Errorf("store: token trend: %w", err)
	}
	defer rows.Close()

	// Build a map for day → point, then fill missing calendar days.
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

	var trend []model.TokenTrendPoint
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		if pt, ok := pointMap[date]; ok {
			trend = append(trend, *pt)
		} else {
			trend = append(trend, model.TokenTrendPoint{Date: date})
		}
	}
	return trend, nil
}

// listModelRuleSummaries returns up to 6 model rules for the dashboard in
// display order. Targets are not loaded; disabled rules are included. An
// empty rule set returns an empty slice (not nil).
func (s *Store) listModelRuleSummaries() ([]model.ModelRuleSummary, error) {
	now := time.Now()
	loc := now.Location()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	startMs := start.UnixMilli()
	end := start.AddDate(0, 0, 1)
	endMs := end.UnixMilli()

	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.enabled,
		       COALESCE(SUM(CASE WHEN l.status_code >= 200 AND l.status_code < 300 AND COALESCE(l.error, '') = '' THEN 1 ELSE 0 END), 0) AS success,
		       COUNT(l.id) AS total
		FROM model_rules r
		LEFT JOIN request_logs l ON l.route_id = r.id
		    AND l.timestamp_ms >= ?
		    AND l.timestamp_ms < ?
		    AND l.status_code != 0
		GROUP BY r.id, r.name, r.enabled
		ORDER BY r.display_order ASC, r.created_at DESC, r.id DESC
		LIMIT ?`, startMs, endMs, 6)
	if err != nil {
		return nil, fmt.Errorf("store: list model rule summaries: %w", err)
	}
	defer rows.Close()

	var summaries []model.ModelRuleSummary
	for rows.Next() {
		var sum model.ModelRuleSummary
		var total, success int64
		if err := rows.Scan(&sum.ID, &sum.Name, &sum.Enabled, &success, &total); err != nil {
			return nil, fmt.Errorf("store: scan model rule summary: %w", err)
		}
		sum.TodayRequestCount = total
		if total > 0 {
			rate := float64(success) / float64(total) * 100
			sum.TodaySuccessRate = &rate
		}
		summaries = append(summaries, sum)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if summaries == nil {
		summaries = []model.ModelRuleSummary{}
	}
	return summaries, nil
}

func (s *Store) recentLogs(limit int) ([]model.RequestLog, error) {
	rows, err := s.db.Query(`
		SELECT id, timestamp_ms, status_code, provider_id, provider_name, model,
		       reasoning_effort, input_tokens, output_tokens, cost, latency_ms, first_token_ms, is_stream,
		       route_id, route_label,
		       api_key_id, COALESCE(error, ''),
		       cache_creation, cache_hit,
		       COALESCE(chain_json, ''), user_agent, client_ip, request_id, request_uri
		FROM request_logs
		ORDER BY timestamp_ms DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent logs: %w", err)
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
			&l.ReasoningEffort,
			&l.InputTokens, &l.OutputTokens, &l.Cost, &l.LatencyMs, &l.FirstTokenMs, &l.IsStream,
			&l.RouteID, &l.RouteLabel, &l.APIKeyID, &l.Error,
			&l.CacheCreation, &l.CacheHit,
			&chainJS, &l.UserAgent, &l.ClientIP, &l.RequestID, &l.RequestURI,
		); err != nil {
			return nil, fmt.Errorf("store: scan recent log: %w", err)
		}
		l.Chain = chainFromJSON(chainJS)
		logs = append(logs, l)
	}
	if logs == nil {
		logs = []model.RequestLog{}
	}
	return logs, rows.Err()
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

package store

import (
	"fmt"
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestDashboard_ProviderCountIncludesAllProviders(t *testing.T) {
	s := newTestStore(t)

	// Create three providers: connected, disabled, and error status.
	if _, err := s.CreateProvider(model.ProviderInput{Name: "Connected", BaseURL: "https://a.example.com"}); err != nil {
		t.Fatalf("create connected provider: %v", err)
	}
	p2, err := s.CreateProvider(model.ProviderInput{Name: "Disabled", BaseURL: "https://b.example.com"})
	if err != nil {
		t.Fatalf("create disabled provider: %v", err)
	}
	if err := s.SetProviderEnabled(p2.ID, false); err != nil {
		t.Fatalf("disable provider: %v", err)
	}
	p3, err := s.CreateProvider(model.ProviderInput{Name: "Error", BaseURL: "https://c.example.com"})
	if err != nil {
		t.Fatalf("create error provider: %v", err)
	}
	if err := s.UpdateProviderTestResult(p3.ID, model.ProviderStatusError, 0, "down"); err != nil {
		t.Fatalf("set provider error: %v", err)
	}

	d, err := s.Dashboard()
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}

	var providerCount string
	for _, st := range d.Stats {
		if st.Label == "dashboard.stats.providerCount" {
			providerCount = st.Value
		}
	}
	if providerCount != "3" {
		t.Fatalf("providerCount = %q, want 3", providerCount)
	}

	// The old activeProviders key should no longer be emitted.
	for _, st := range d.Stats {
		if st.Label == "dashboard.stats.activeProviders" {
			t.Fatalf("old activeProviders key still emitted in stats")
		}
	}
}

func TestRecentLogs_ReasoningEffort(t *testing.T) {
	s := newTestStore(t)

	if err := s.InsertRequestLog(model.RequestLog{
		ID:              "recent-reasoning",
		Timestamp:       time.Now().UnixMilli(),
		StatusCode:      200,
		ProviderID:      "provider",
		ProviderName:    "Provider",
		Model:           "model",
		ReasoningEffort: "high",
	}); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
	}

	logs, err := s.recentLogs(10)
	if err != nil {
		t.Fatalf("recentLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("recent logs = %d, want 1", len(logs))
	}
	if logs[0].ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want %q", logs[0].ReasoningEffort, "high")
	}
}

func TestDashboard_ModelRuleSummaries(t *testing.T) {
	s := newTestStore(t)

	// Create 7 rules; the first is disabled (and will fall beyond the 6-item
	// limit), the 5th is disabled but inside the top 6, and the 6th will have
	// today's stats. The dashboard summary should include disabled rules that
	// are within the top 6, obey display order, and cap at 6.
	rules := make([]*model.ModelRule, 7)
	for i := 0; i < 7; i++ {
		r, err := s.CreateModelRule(model.ModelRuleInput{
			Name:    fmt.Sprintf("rule-%d", i),
			Enabled: i != 0 && i != 5, // rule-0 and rule-5 disabled
			Targets: []model.ModelRuleTargetInput{{ProviderID: "p", ModelName: "m"}},
		})
		if err != nil {
			t.Fatalf("create rule %d: %v", i, err)
		}
		rules[i] = r
	}

	// Insert some request logs for the 6th rule (index 5).
	// Two successes and one failure today; one status_code=0 ignored.
	r6 := rules[5]
	now := time.Now().UnixMilli()
	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", RouteID: r6.ID, InputTokens: 1, OutputTokens: 1},
		{ID: "l2", Timestamp: now, StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", RouteID: r6.ID, InputTokens: 1, OutputTokens: 1},
		{ID: "l3", Timestamp: now, StatusCode: 500, ProviderID: "p", ProviderName: "P", Model: "m", RouteID: r6.ID, InputTokens: 1, OutputTokens: 1},
		{ID: "l4", Timestamp: now, StatusCode: 0, ProviderID: "p", ProviderName: "P", Model: "m", RouteID: r6.ID, InputTokens: 1, OutputTokens: 1},
		// Yesterday for r6 should be excluded from today's summary.
		{ID: "l5", Timestamp: now - 24*time.Hour.Milliseconds(), StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", RouteID: r6.ID, InputTokens: 1, OutputTokens: 1},
	}
	if err := s.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("insert logs: %v", err)
	}

	d, err := s.Dashboard()
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}

	if len(d.ModelRules) != 6 {
		t.Fatalf("model rule summaries = %d, want 6", len(d.ModelRules))
	}
	if d.ModelRules == nil {
		t.Fatal("ModelRules should be empty slice, not nil")
	}

	// Display order puts newest rule at the top (display_order 0), so the
	// order is rule-6, rule-5, rule-4, rule-3, rule-2, rule-1.
	wanted := []string{"rule-6", "rule-5", "rule-4", "rule-3", "rule-2", "rule-1"}
	for i, want := range wanted {
		if d.ModelRules[i].Name != want {
			t.Fatalf("summary[%d].Name = %q, want %q", i, d.ModelRules[i].Name, want)
		}
	}

	// Disabled rule-0 must be excluded because it is 7th in display order
	// (oldest) and the limit is 6.
	for _, sum := range d.ModelRules {
		if sum.Name == "rule-0" {
			t.Fatalf("disabled rule-0 should be beyond the 6-item limit")
		}
	}

	// The 5th displayed rule (rule-1) should have no completed requests.
	if d.ModelRules[5].TodayRequestCount != 0 || d.ModelRules[5].TodaySuccessRate != nil {
		t.Fatalf("rule-1 summary: expected 0 requests and nil rate, got %d %v", d.ModelRules[5].TodayRequestCount, d.ModelRules[5].TodaySuccessRate)
	}

	// rule-5 is disabled but must still appear in the top-6 summary.
	if d.ModelRules[1].Name != "rule-5" || d.ModelRules[1].Enabled {
		t.Fatalf("rule-5 should be disabled and in summary[1]: got name=%s enabled=%v", d.ModelRules[1].Name, d.ModelRules[1].Enabled)
	}

	// The 2nd displayed rule (rule-5) should have 3 completed requests and
	// 2/3 success rate.
	if d.ModelRules[1].Name != "rule-5" {
		t.Fatalf("expected summary[1] to be rule-5, got %s", d.ModelRules[1].Name)
	}
	if d.ModelRules[1].TodayRequestCount != 3 {
		t.Fatalf("rule-5 request count = %d, want 3", d.ModelRules[1].TodayRequestCount)
	}
	if d.ModelRules[1].TodaySuccessRate == nil {
		t.Fatal("rule-5 success rate should not be nil")
	}
	if *d.ModelRules[1].TodaySuccessRate < 66.6 || *d.ModelRules[1].TodaySuccessRate > 66.7 {
		t.Fatalf("rule-5 success rate = %f, want ~66.67", *d.ModelRules[1].TodaySuccessRate)
	}

	// Targets must not be loaded (ModelRuleSummary has no targets field).
}

func TestDashboard_ModelRuleSummariesEmpty(t *testing.T) {
	s := newTestStore(t)

	d, err := s.Dashboard()
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	if d.ModelRules == nil {
		t.Fatal("ModelRules should be empty slice, not nil")
	}
	if len(d.ModelRules) != 0 {
		t.Fatalf("ModelRules length = %d, want 0", len(d.ModelRules))
	}
}

func TestDashboard_TokenTrendExactlySevenBuckets(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	loc := now.Location()
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	yesterdayMidnight := todayMidnight.AddDate(0, 0, -1)
	sixDaysAgoMidnight := todayMidnight.AddDate(0, 0, -6)

	// Insert one log yesterday, one log six days ago, and one log just before
	// the six-day window. The boundary log should be excluded.
	logs := []model.RequestLog{
		{ID: "l1", Timestamp: yesterdayMidnight.Add(time.Hour).UnixMilli(), StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", InputTokens: 10, OutputTokens: 5, Cost: 0.001},
		{ID: "l2", Timestamp: sixDaysAgoMidnight.Add(time.Hour).UnixMilli(), StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", InputTokens: 20, OutputTokens: 10, Cost: 0.002},
		{ID: "l3", Timestamp: sixDaysAgoMidnight.Add(-time.Second).UnixMilli(), StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", InputTokens: 99, OutputTokens: 99, Cost: 0.099},
	}
	if err := s.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("insert logs: %v", err)
	}

	d, err := s.Dashboard()
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}

	if len(d.TokenTrend) != 7 {
		t.Fatalf("token trend buckets = %d, want 7", len(d.TokenTrend))
	}

	// First bucket is six days ago; last bucket is today.
	wantFirst := sixDaysAgoMidnight.Format("2006-01-02")
	wantLast := todayMidnight.Format("2006-01-02")
	if d.TokenTrend[0].Date != wantFirst {
		t.Fatalf("first bucket date = %q, want %q", d.TokenTrend[0].Date, wantFirst)
	}
	if d.TokenTrend[6].Date != wantLast {
		t.Fatalf("last bucket date = %q, want %q", d.TokenTrend[6].Date, wantLast)
	}

	// The six-days-ago bucket should have the data from l2.
	if d.TokenTrend[0].InputTokens != 20 || d.TokenTrend[0].OutputTokens != 10 {
		t.Fatalf("six-days-ago bucket tokens = %d/%d, want 20/10", d.TokenTrend[0].InputTokens, d.TokenTrend[0].OutputTokens)
	}

	// The yesterday bucket should have l1 data.
	if d.TokenTrend[5].InputTokens != 10 || d.TokenTrend[5].OutputTokens != 5 {
		t.Fatalf("yesterday bucket tokens = %d/%d, want 10/5", d.TokenTrend[5].InputTokens, d.TokenTrend[5].OutputTokens)
	}

	// Today should be empty (zero).
	if d.TokenTrend[6].InputTokens != 0 || d.TokenTrend[6].OutputTokens != 0 || d.TokenTrend[6].Cost != 0 {
		t.Fatalf("today bucket should be empty, got %+v", d.TokenTrend[6])
	}

	// The boundary log (just before six days ago) should be excluded.
	for _, pt := range d.TokenTrend {
		if pt.InputTokens == 99 {
			t.Fatalf("boundary log leaked into bucket %s", pt.Date)
		}
	}
}

func TestDashboard_TokenTrendMidnightBoundary(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	loc := now.Location()
	todayMidnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	yesterdayMidnight := todayMidnight.AddDate(0, 0, -1)

	// One log one millisecond before today midnight should be in yesterday.
	// One log at exactly today midnight should be in today.
	logs := []model.RequestLog{
		{ID: "l1", Timestamp: todayMidnight.Add(-time.Millisecond).UnixMilli(), StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", InputTokens: 1, OutputTokens: 1},
		{ID: "l2", Timestamp: todayMidnight.Add(time.Millisecond).UnixMilli(), StatusCode: 200, ProviderID: "p", ProviderName: "P", Model: "m", InputTokens: 2, OutputTokens: 2},
	}
	if err := s.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("insert logs: %v", err)
	}

	d, err := s.Dashboard()
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}

	var yesterdayBucket, todayBucket *model.TokenTrendPoint
	for i := range d.TokenTrend {
		if d.TokenTrend[i].Date == yesterdayMidnight.Format("2006-01-02") {
			yesterdayBucket = &d.TokenTrend[i]
		}
		if d.TokenTrend[i].Date == todayMidnight.Format("2006-01-02") {
			todayBucket = &d.TokenTrend[i]
		}
	}
	if yesterdayBucket == nil || todayBucket == nil {
		t.Fatalf("missing yesterday or today bucket in trend: %+v", d.TokenTrend)
	}

	if yesterdayBucket.InputTokens != 1 {
		t.Fatalf("yesterday bucket input = %d, want 1", yesterdayBucket.InputTokens)
	}
	if todayBucket.InputTokens != 2 {
		t.Fatalf("today bucket input = %d, want 2", todayBucket.InputTokens)
	}
}

func TestDashboard_TokenTrendEmpty(t *testing.T) {
	s := newTestStore(t)

	d, err := s.Dashboard()
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}

	if len(d.TokenTrend) != 7 {
		t.Fatalf("empty token trend buckets = %d, want 7", len(d.TokenTrend))
	}
	for _, pt := range d.TokenTrend {
		if pt.InputTokens != 0 || pt.OutputTokens != 0 || pt.Cost != 0 {
			t.Fatalf("empty trend bucket should be zero, got %+v", pt)
		}
	}
}

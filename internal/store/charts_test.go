package store

import (
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestChartAggregates_Empty(t *testing.T) {
	st := newTestStore(t)

	agg, err := st.GetChartAggregates(model.ChartQuery{})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if len(agg.Buckets) != 0 {
		t.Errorf("expected 0 buckets, got %d", len(agg.Buckets))
	}
	if len(agg.StatusBreakdown) != 0 {
		t.Errorf("expected 0 status breakdown, got %d", len(agg.StatusBreakdown))
	}
	if len(agg.ProviderShares) != 0 {
		t.Errorf("expected 0 provider shares, got %d", len(agg.ProviderShares))
	}
}

func TestChartAggregates_BucketsAndBreakdown(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, FirstTokenMs: 120, IsStream: true, RouteID: "r1", RouteLabel: "default"},
		{ID: "l2", Timestamp: now - time.Hour.Milliseconds(), StatusCode: 429, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 10, OutputTokens: 0, Cost: 0, LatencyMs: 200, RouteID: "r1", RouteLabel: "default"},
		{ID: "l3", Timestamp: now - 2*time.Hour.Milliseconds(), StatusCode: 500, ProviderID: "p2", ProviderName: "Anthropic", Model: "claude-3", InputTokens: 200, OutputTokens: 100, Cost: 0.01, LatencyMs: 800, RouteID: "r2", RouteLabel: "backup"},
		{ID: "l4", Timestamp: now - 25*time.Hour.Milliseconds(), StatusCode: 200, ProviderID: "p2", ProviderName: "Anthropic", Model: "claude-3", InputTokens: 50, OutputTokens: 20, Cost: 0.002, LatencyMs: 300, RouteID: "r2", RouteLabel: "backup"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	// Intra-day range should use hourly buckets.
	start := now - 3*time.Hour.Milliseconds()
	end := now + time.Hour.Milliseconds()
	agg, err := st.GetChartAggregates(model.ChartQuery{StartDate: start, EndDate: end})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if agg.BucketSize != "hour" {
		t.Errorf("expected hourly buckets, got %s", agg.BucketSize)
	}
	if len(agg.Buckets) != 3 {
		t.Errorf("expected 3 hourly buckets, got %d", len(agg.Buckets))
	}

	// Daily range should use daily buckets.
	start = now - 48 * time.Hour.Milliseconds()
	agg, err = st.GetChartAggregates(model.ChartQuery{StartDate: start, EndDate: end})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if agg.BucketSize != "day" {
		t.Errorf("expected daily buckets, got %s", agg.BucketSize)
	}
	if len(agg.Buckets) != 2 {
		t.Errorf("expected 2 daily buckets, got %d", len(agg.Buckets))
	}

	// Status breakdown should sum to ~100 across the filtered range.
	var totalPct float64
	var totalCount int64
	for _, b := range agg.StatusBreakdown {
		totalPct += b.Percent
		totalCount += b.Count
	}
	if totalCount != 4 {
		t.Errorf("expected 4 total requests, got %d", totalCount)
	}
	if totalPct < 99.9 || totalPct > 100.1 {
		t.Errorf("expected status breakdown ~100%%, got %.2f", totalPct)
	}

	// Provider shares should sum to 100.
	var tokenPct float64
	for _, s := range agg.ProviderShares {
		tokenPct += s.Percent
	}
	if tokenPct < 99.9 || tokenPct > 100.1 {
		t.Errorf("expected provider shares ~100%%, got %.2f", tokenPct)
	}
}

func TestChartAggregates_Filters(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, RouteID: "r1", RouteLabel: "default"},
		{ID: "l2", Timestamp: now, StatusCode: 200, ProviderID: "p2", ProviderName: "Anthropic", Model: "claude-3", InputTokens: 200, OutputTokens: 100, Cost: 0.01, LatencyMs: 800, RouteID: "r2", RouteLabel: "backup"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	start := now - time.Hour.Milliseconds()
	end := now + time.Hour.Milliseconds()

	cases := []struct {
		name  string
		q     model.ChartQuery
		want  int64
	}{
		{"provider", model.ChartQuery{StartDate: start, EndDate: end, Provider: "p1"}, 1},
		{"route", model.ChartQuery{StartDate: start, EndDate: end, RouteID: "r2"}, 1},
		{"model", model.ChartQuery{StartDate: start, EndDate: end, Model: "gpt-4o"}, 1},
		{"search model", model.ChartQuery{StartDate: start, EndDate: end, Search: "claude"}, 1},
		{"search route label", model.ChartQuery{StartDate: start, EndDate: end, Search: "backup"}, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg, err := st.GetChartAggregates(tc.q)
			if err != nil {
				t.Fatalf("GetChartAggregates failed: %v", err)
			}
			var total int64
			for _, b := range agg.Buckets {
				total += b.Success + b.RateLimited + b.Error
			}
			if total != tc.want {
				t.Errorf("expected %d requests, got %d", tc.want, total)
			}
		})
	}
}

func TestChartAggregates_StatusZeroWithErrorIsError(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 0, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, RouteID: "r1", RouteLabel: "default", Error: "client disconnect"},
		{ID: "l2", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 10, OutputTokens: 0, Cost: 0, LatencyMs: 200, RouteID: "r1", RouteLabel: "default"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	agg, err := st.GetChartAggregates(model.ChartQuery{})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if len(agg.StatusBreakdown) != 2 {
		t.Fatalf("expected 2 status classes, got %d", len(agg.StatusBreakdown))
	}

	var errorCount, totalCount int64
	for _, b := range agg.StatusBreakdown {
		totalCount += b.Count
		if b.Label == "错误" {
			errorCount = b.Count
		}
	}
	if totalCount != 2 {
		t.Errorf("expected total 2, got %d", totalCount)
	}
	if errorCount != 1 {
		t.Errorf("expected 1 error (status 0 with error message), got %d", errorCount)
	}
}

func TestChartAggregates_StatusZeroWithoutErrorIsOther(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 0, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, RouteID: "r1", RouteLabel: "default"},
		{ID: "l2", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 10, OutputTokens: 0, Cost: 0, LatencyMs: 200, RouteID: "r1", RouteLabel: "default"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	agg, err := st.GetChartAggregates(model.ChartQuery{})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if len(agg.StatusBreakdown) != 2 {
		t.Fatalf("expected 2 status classes (2xx + other), got %d", len(agg.StatusBreakdown))
	}

	var found2xx, foundOther bool
	for _, b := range agg.StatusBreakdown {
		if b.Label == "2xx" && b.Count == 1 {
			found2xx = true
		}
		if b.Label == "其他" && b.Count == 1 {
			foundOther = true
		}
	}
	if !found2xx {
		t.Errorf("expected 1 request in 2xx class")
	}
	if !foundOther {
		t.Errorf("expected 1 request in 其他 class")
	}
}

func TestChartAggregates_TTFTAveraging(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, FirstTokenMs: 100, IsStream: true, RouteID: "r1", RouteLabel: "default"},
		{ID: "l2", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, FirstTokenMs: 300, IsStream: true, RouteID: "r1", RouteLabel: "default"},
		{ID: "l3", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, FirstTokenMs: 0, IsStream: false, RouteID: "r1", RouteLabel: "default"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	agg, err := st.GetChartAggregates(model.ChartQuery{})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if len(agg.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(agg.Buckets))
	}
	if agg.Buckets[0].AvgTTFTMs != 200 {
		t.Errorf("expected avg TTFT 200ms, got %d", agg.Buckets[0].AvgTTFTMs)
	}
}

func TestChartAggregates_SearchNoSQLInjection(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, RouteID: "r1", RouteLabel: "default"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	malicious := "' OR 1=1 --"
	agg, err := st.GetChartAggregates(model.ChartQuery{Search: malicious})
	if err != nil {
		t.Fatalf("search with malicious input should not error: %v", err)
	}
	if len(agg.Buckets) != 0 {
		t.Errorf("malicious search should match nothing, got %d buckets", len(agg.Buckets))
	}
}

func TestChartAggregates_ProviderShareOrdering(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 1000, OutputTokens: 500, Cost: 0.05, LatencyMs: 500, RouteID: "r1", RouteLabel: "default"},
		{ID: "l2", Timestamp: now, StatusCode: 200, ProviderID: "p2", ProviderName: "Anthropic", Model: "claude-3", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, RouteID: "r2", RouteLabel: "backup"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	agg, err := st.GetChartAggregates(model.ChartQuery{})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if len(agg.ProviderShares) != 2 {
		t.Fatalf("expected 2 provider shares, got %d", len(agg.ProviderShares))
	}
	if agg.ProviderShares[0].ProviderName != "OpenAI" {
		t.Errorf("expected first provider OpenAI, got %s", agg.ProviderShares[0].ProviderName)
	}
	if agg.ProviderShares[0].Percent < 90 {
		t.Errorf("expected OpenAI share > 90%%, got %.2f", agg.ProviderShares[0].Percent)
	}
}

func TestChartAggregates_EmptyBucketAvgs(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, FirstTokenMs: 120, IsStream: true, RouteID: "r1", RouteLabel: "default"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	agg, err := st.GetChartAggregates(model.ChartQuery{})
	if err != nil {
		t.Fatalf("GetChartAggregates failed: %v", err)
	}
	if len(agg.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(agg.Buckets))
	}
	if agg.Buckets[0].AvgLatencyMs != 500 {
		t.Errorf("expected avg latency 500, got %d", agg.Buckets[0].AvgLatencyMs)
	}
	if agg.Buckets[0].AvgTTFTMs != 120 {
		t.Errorf("expected avg TTFT 120, got %d", agg.Buckets[0].AvgTTFTMs)
	}
}

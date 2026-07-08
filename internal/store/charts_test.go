package store

import (
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestUsageTrends_Empty(t *testing.T) {
	st := newTestStore(t)

	agg, err := st.GetUsageTrends(model.UsageTrendsQuery{})
	if err != nil {
		t.Fatalf("GetUsageTrends failed: %v", err)
	}
	if len(agg.Buckets) != 0 {
		t.Errorf("expected 0 buckets, got %d", len(agg.Buckets))
	}
}

func TestUsageTrends_BucketShape(t *testing.T) {
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
	agg, err := st.GetUsageTrends(model.UsageTrendsQuery{StartDate: start, EndDate: end})
	if err != nil {
		t.Fatalf("GetUsageTrends failed: %v", err)
	}
	if agg.BucketSize != "hour" {
		t.Errorf("expected hourly buckets, got %s", agg.BucketSize)
	}
	if len(agg.Buckets) != 3 {
		t.Errorf("expected 3 hourly buckets, got %d", len(agg.Buckets))
	}

	// Daily range should use daily buckets.
	start = now - 48*time.Hour.Milliseconds()
	agg, err = st.GetUsageTrends(model.UsageTrendsQuery{StartDate: start, EndDate: end})
	if err != nil {
		t.Fatalf("GetUsageTrends failed: %v", err)
	}
	if agg.BucketSize != "day" {
		t.Errorf("expected daily buckets, got %s", agg.BucketSize)
	}
	if len(agg.Buckets) != 2 {
		t.Errorf("expected 2 daily buckets, got %d", len(agg.Buckets))
	}
}

func TestUsageTrends_AggregationFields(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, CacheCreation: 5, CacheHit: 7, RouteID: "r1", RouteLabel: "default"},
		{ID: "l2", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 200, OutputTokens: 80, Cost: 0.011, CacheCreation: 2, CacheHit: 3, RouteID: "r1", RouteLabel: "default"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	agg, err := st.GetUsageTrends(model.UsageTrendsQuery{StartDate: now - time.Hour.Milliseconds(), EndDate: now + time.Hour.Milliseconds()})
	if err != nil {
		t.Fatalf("GetUsageTrends failed: %v", err)
	}
	if len(agg.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(agg.Buckets))
	}
	b := agg.Buckets[0]
	if b.Input != 300 {
		t.Errorf("expected input 300, got %d", b.Input)
	}
	if b.Output != 130 {
		t.Errorf("expected output 130, got %d", b.Output)
	}
	if b.Cost < 0.015 || b.Cost > 0.017 {
		t.Errorf("expected cost ~0.016, got %.4f", b.Cost)
	}
	if b.CacheCreation != 7 {
		t.Errorf("expected cache_creation 7, got %d", b.CacheCreation)
	}
	if b.CacheHit != 10 {
		t.Errorf("expected cache_hit 10, got %d", b.CacheHit)
	}
}

func TestUsageTrends_Filters(t *testing.T) {
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
		name    string
		q       model.UsageTrendsQuery
		wantTot int64
	}{
		{"provider", model.UsageTrendsQuery{StartDate: start, EndDate: end, Provider: "p1"}, 150},
		{"route", model.UsageTrendsQuery{StartDate: start, EndDate: end, RouteID: "r2"}, 300},
		{"model", model.UsageTrendsQuery{StartDate: start, EndDate: end, Model: "gpt-4o"}, 150},
		{"search model", model.UsageTrendsQuery{StartDate: start, EndDate: end, Search: "claude"}, 300},
		{"search route label", model.UsageTrendsQuery{StartDate: start, EndDate: end, Search: "backup"}, 300},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg, err := st.GetUsageTrends(tc.q)
			if err != nil {
				t.Fatalf("GetUsageTrends failed: %v", err)
			}
			var total int64
			for _, b := range agg.Buckets {
				total += b.Input + b.Output
			}
			if total != tc.wantTot {
				t.Errorf("expected %d tokens, got %d", tc.wantTot, total)
			}
		})
	}
}

func TestUsageTrends_SearchNoSQLInjection(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()

	logs := []model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 100, OutputTokens: 50, Cost: 0.005, LatencyMs: 500, RouteID: "r1", RouteLabel: "default"},
	}
	if err := st.InsertRequestLogsBatch(logs); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}

	malicious := "' OR 1=1 --"
	agg, err := st.GetUsageTrends(model.UsageTrendsQuery{Search: malicious})
	if err != nil {
		t.Fatalf("search with malicious input should not error: %v", err)
	}
	if len(agg.Buckets) != 0 {
		t.Errorf("malicious search should match nothing, got %d buckets", len(agg.Buckets))
	}
}

func TestUsageTrends_NoStatusOrProviderBreakdown(t *testing.T) {
	// The single-trend chart surface no longer needs status or provider
	// breakdowns; the response should carry only Range, BucketSize, Buckets.
	st := newTestStore(t)
	now := time.Now().Truncate(time.Hour).UnixMilli()
	if err := st.InsertRequestLogsBatch([]model.RequestLog{
		{ID: "l1", Timestamp: now, StatusCode: 200, ProviderID: "p1", ProviderName: "OpenAI", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1, Cost: 0, RouteID: "r1", RouteLabel: "default"},
	}); err != nil {
		t.Fatalf("seed logs failed: %v", err)
	}
	agg, err := st.GetUsageTrends(model.UsageTrendsQuery{})
	if err != nil {
		t.Fatalf("GetUsageTrends failed: %v", err)
	}
	if agg.Range == "" {
		t.Errorf("expected range label to be populated")
	}
	if agg.BucketSize != "day" {
		t.Errorf("expected default daily buckets, got %s", agg.BucketSize)
	}
	if len(agg.Buckets) != 1 {
		t.Fatalf("expected 1 bucket, got %d", len(agg.Buckets))
	}
}

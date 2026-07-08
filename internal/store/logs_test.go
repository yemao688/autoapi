package store

import (
	"context"
	"os"
	"testing"

	"autoapi/internal/model"
)

// initDev override is set in store_test.go; declare here for the per-package
// test binary so the logs test file compiles standalone if needed.
var _ = func() {
	if initDev == nil {
		initDev = func(*Store) {}
	}
}

func newLogsTestStore(t *testing.T) *Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "autoapi-logs-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s, err := New(context.Background(), StoreDeps{DSN: dir + "/test.db"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedLogs inserts the given logs and returns them in insertion order.
// Convenience helper so the test bodies stay focused on the filter assertions.
func seedLogs(t *testing.T, s *Store, logs []model.RequestLog) {
	t.Helper()
	for _, l := range logs {
		if err := s.InsertRequestLog(l); err != nil {
			t.Fatalf("InsertRequestLog(%s): %v", l.ID, err)
		}
	}
}

// fixedLogs returns a deterministic test set covering provider/route/model/
// status/error combinations. Timestamps are strictly increasing so the
// ORDER BY timestamp_ms DESC ordering is stable across all subtests.
func fixedLogs() []model.RequestLog {
	base := int64(1700000000000) // 2023-11-14
	hour := int64(3600 * 1000)
	return []model.RequestLog{
		{
			ID:           "log-1",
			Timestamp:    base,
			StatusCode:   200,
			ProviderID:   "p-openai",
			ProviderName: "OpenAI",
			Model:        "gpt-4o",
			RouteID:      "r-fast",
			RouteLabel:   "Fast Lane",
			InputTokens:  100, OutputTokens: 50, LatencyMs: 200,
		},
		{
			ID:           "log-2",
			Timestamp:    base + 1*hour,
			StatusCode:   429,
			ProviderID:   "p-openai",
			ProviderName: "OpenAI",
			Model:        "gpt-4o",
			RouteID:      "r-fast",
			RouteLabel:   "Fast Lane",
			InputTokens:  50, OutputTokens: 0, LatencyMs: 100,
		},
		{
			ID:           "log-3",
			Timestamp:    base + 2*hour,
			StatusCode:   500,
			ProviderID:   "p-anthropic",
			ProviderName: "Anthropic",
			Model:        "claude-3-5-sonnet",
			RouteID:      "r-deep",
			RouteLabel:   "Deep Dive",
			Error:        "upstream timeout",
			InputTokens:  200, OutputTokens: 0, LatencyMs: 30000,
		},
		{
			ID:           "log-4",
			Timestamp:    base + 3*hour,
			StatusCode:   200,
			ProviderID:   "p-anthropic",
			ProviderName: "Anthropic",
			Model:        "claude-3-5-sonnet",
			RouteID:      "r-deep",
			RouteLabel:   "Deep Dive",
			InputTokens:  300, OutputTokens: 120, LatencyMs: 400,
		},
		{
			ID:           "log-5",
			Timestamp:    base + 4*hour,
			StatusCode:   200,
			ProviderID:   "p-deepseek",
			ProviderName: "DeepSeek",
			Model:        "deepseek-chat",
			RouteID:      "r-default",
			RouteLabel:   "",
			InputTokens:  80, OutputTokens: 80, LatencyMs: 600,
		},
	}
}

// idsOf returns just the IDs of a log slice, for compact assertions.
func idsOf(logs []model.RequestLog) []string {
	out := make([]string, 0, len(logs))
	for _, l := range logs {
		out = append(out, l.ID)
	}
	return out
}

// ----- Empty store -----

func TestQueryLogsEmpty(t *testing.T) {
	s := newLogsTestStore(t)

	logs, total, err := s.QueryLogs(model.LogQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected total 0, got %d", total)
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 logs, got %d", len(logs))
	}
}

// ----- Provider filter (ORDER BY timestamp_ms DESC: log-5, log-4, log-3, log-2, log-1) -----

func TestQueryLogsByProvider(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	cases := []struct {
		provider string
		wantIDs  []string
	}{
		{"p-openai", []string{"log-2", "log-1"}},
		{"p-anthropic", []string{"log-4", "log-3"}},
		{"p-deepseek", []string{"log-5"}},
		{"", []string{"log-5", "log-4", "log-3", "log-2", "log-1"}}, // all
		{"nonexistent", nil},
	}
	for _, tc := range cases {
		t.Run("provider="+tc.provider, func(t *testing.T) {
			logs, total, err := s.QueryLogs(model.LogQuery{
				Provider: tc.provider, Page: 1, PageSize: 50,
			})
			if err != nil {
				t.Fatalf("QueryLogs: %v", err)
			}
			if total != int64(len(tc.wantIDs)) {
				t.Fatalf("total: want %d, got %d (ids=%v)", len(tc.wantIDs), total, idsOf(logs))
			}
			if got := idsOf(logs); !equalStringSlices(got, tc.wantIDs) {
				t.Fatalf("ids: want %v, got %v", tc.wantIDs, got)
			}
		})
	}
}

// ----- Route filter -----

func TestQueryLogsByRoute(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	logs, total, err := s.QueryLogs(model.LogQuery{
		RouteID: "r-fast", Page: 1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 2 {
		t.Fatalf("total: want 2, got %d", total)
	}
	if got := idsOf(logs); !equalStringSlices(got, []string{"log-2", "log-1"}) {
		t.Fatalf("ids: want [log-2 log-1], got %v", got)
	}

	// Empty route_id should match everything.
	logs, total, err = s.QueryLogs(model.LogQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("QueryLogs (empty route): %v", err)
	}
	if total != 5 {
		t.Fatalf("total without route filter: want 5, got %d", total)
	}
	if len(logs) != 5 {
		t.Fatalf("expected 5 logs, got %d", len(logs))
	}
}

// ----- Model filter -----

func TestQueryLogsByModel(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	logs, total, err := s.QueryLogs(model.LogQuery{
		Model: "claude-3-5-sonnet", Page: 1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 2 {
		t.Fatalf("total: want 2, got %d", total)
	}
	if got := idsOf(logs); !equalStringSlices(got, []string{"log-4", "log-3"}) {
		t.Fatalf("ids: want [log-4 log-3], got %v", got)
	}

	// Unknown model.
	logs, total, err = s.QueryLogs(model.LogQuery{
		Model: "gpt-5-turbo", Page: 1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs (unknown model): %v", err)
	}
	if total != 0 || len(logs) != 0 {
		t.Fatalf("expected empty result, got total=%d len=%d", total, len(logs))
	}
}

// ----- Status filter -----

func TestQueryLogsByStatus(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	cases := []struct {
		status  string
		wantIDs []string
	}{
		// success = 2xx; excludes the 429 and 500 entries.
		{"success", []string{"log-5", "log-4", "log-1"}},
		// failed = status >= 400 OR error != ''. log-3 is 500 + error;
		// log-2 is 429 (rate limited) without an error string → failed.
		{"failed", []string{"log-3", "log-2"}},
		// rate_limited = status == 429.
		{"rate_limited", []string{"log-2"}},
		{"", []string{"log-5", "log-4", "log-3", "log-2", "log-1"}},
	}
	for _, tc := range cases {
		t.Run("status="+tc.status, func(t *testing.T) {
			logs, total, err := s.QueryLogs(model.LogQuery{
				Status: tc.status, Page: 1, PageSize: 50,
			})
			if err != nil {
				t.Fatalf("QueryLogs: %v", err)
			}
			if total != int64(len(tc.wantIDs)) {
				t.Fatalf("total: want %d, got %d (ids=%v)", len(tc.wantIDs), total, idsOf(logs))
			}
			if got := idsOf(logs); !equalStringSlices(got, tc.wantIDs) {
				t.Fatalf("ids: want %v, got %v", tc.wantIDs, got)
			}
		})
	}
}

// ----- Date range filter -----

func TestQueryLogsByDateRange(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	base := int64(1700000000000)
	hour := int64(3600 * 1000)

	// [base, base+2*hour] → log-1 (base) + log-2 (base+1h) + log-3 (base+2h)
	logs, total, err := s.QueryLogs(model.LogQuery{
		StartDate: base, EndDate: base + 2*hour, Page: 1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs (start+end): %v", err)
	}
	if total != 3 {
		t.Fatalf("total: want 3, got %d (ids=%v)", total, idsOf(logs))
	}
	if got := idsOf(logs); !equalStringSlices(got, []string{"log-3", "log-2", "log-1"}) {
		t.Fatalf("ids: want [log-3 log-2 log-1], got %v", got)
	}

	// Only start → everything from base+1h onwards (4 logs).
	logs, total, err = s.QueryLogs(model.LogQuery{
		StartDate: base + hour, Page: 1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs (start only): %v", err)
	}
	if total != 4 {
		t.Fatalf("total: want 4, got %d (ids=%v)", total, idsOf(logs))
	}
	if got := idsOf(logs); !equalStringSlices(got, []string{"log-5", "log-4", "log-3", "log-2"}) {
		t.Fatalf("ids: want [log-5 log-4 log-3 log-2], got %v", got)
	}

	// Only end → everything up to base+2h (3 logs).
	logs, total, err = s.QueryLogs(model.LogQuery{
		EndDate: base + 2*hour, Page: 1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs (end only): %v", err)
	}
	if total != 3 {
		t.Fatalf("total: want 3, got %d (ids=%v)", total, idsOf(logs))
	}
}

// ----- Search filter -----

func TestQueryLogsBySearch(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	cases := []struct {
		search string
		want   []string
	}{
		// model match
		{"claude", []string{"log-4", "log-3"}},
		{"gpt-4o", []string{"log-2", "log-1"}},
		// route_label match (case-insensitive ASCII LIKE in SQLite)
		{"Deep Dive", []string{"log-4", "log-3"}},
		{"Fast Lane", []string{"log-2", "log-1"}},
		// error match
		{"timeout", []string{"log-3"}},
		// substring overlap across fields; only the matching row is returned
		// because the OR is wrapped inside a single predicate.
		{"sonnet", []string{"log-4", "log-3"}},
		// 'deep' is a prefix of both route_label "Deep Dive" and model
		// "deepseek-chat" — verifies the LIKE pattern is applied to all three
		// columns and the OR composes correctly.
		{"deep", []string{"log-5", "log-4", "log-3"}},
		// no match
		{"nope", nil},
	}
	for _, tc := range cases {
		t.Run("search="+tc.search, func(t *testing.T) {
			logs, total, err := s.QueryLogs(model.LogQuery{
				Search: tc.search, Page: 1, PageSize: 50,
			})
			if err != nil {
				t.Fatalf("QueryLogs: %v", err)
			}
			if total != int64(len(tc.want)) {
				t.Fatalf("total: want %d, got %d (ids=%v)", len(tc.want), total, idsOf(logs))
			}
			if got := idsOf(logs); !equalStringSlices(got, tc.want) {
				t.Fatalf("ids: want %v, got %v", tc.want, got)
			}
		})
	}
}

// ----- Combined filters -----

func TestQueryLogsCombined(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	// anthropic + claude + success → log-4 only.
	logs, total, err := s.QueryLogs(model.LogQuery{
		Provider: "p-anthropic",
		Model:    "claude-3-5-sonnet",
		Status:   "success",
		Page:     1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].ID != "log-4" {
		t.Fatalf("expected [log-4], got total=%d ids=%v", total, idsOf(logs))
	}

	// r-fast + status=success → log-1 only.
	logs, total, err = s.QueryLogs(model.LogQuery{
		RouteID: "r-fast",
		Status:  "success",
		Page:    1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs (r-fast+success): %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].ID != "log-1" {
		t.Fatalf("expected [log-1], got total=%d ids=%v", total, idsOf(logs))
	}

	// search=sonnet + status=failed → log-3 only (log-4 is success).
	logs, total, err = s.QueryLogs(model.LogQuery{
		Search: "sonnet",
		Status: "failed",
		Page:   1, PageSize: 50,
	})
	if err != nil {
		t.Fatalf("QueryLogs (sonnet+failed): %v", err)
	}
	if total != 1 || len(logs) != 1 || logs[0].ID != "log-3" {
		t.Fatalf("expected [log-3], got total=%d ids=%v", total, idsOf(logs))
	}
}

// ----- Pagination -----

func TestQueryLogsPagination(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	// Page 1, size 2 → newest two first: log-5, log-4 (ordered by timestamp DESC).
	logs, total, err := s.QueryLogs(model.LogQuery{Page: 1, PageSize: 2})
	if err != nil {
		t.Fatalf("QueryLogs page 1: %v", err)
	}
	if total != 5 {
		t.Fatalf("total: want 5, got %d", total)
	}
	if got := idsOf(logs); !equalStringSlices(got, []string{"log-5", "log-4"}) {
		t.Fatalf("page 1 ids: want [log-5 log-4], got %v", got)
	}

	// Page 2, size 2 → log-3, log-2.
	logs, total, err = s.QueryLogs(model.LogQuery{Page: 2, PageSize: 2})
	if err != nil {
		t.Fatalf("QueryLogs page 2: %v", err)
	}
	if total != 5 {
		t.Fatalf("page 2 total: want 5, got %d", total)
	}
	if got := idsOf(logs); !equalStringSlices(got, []string{"log-3", "log-2"}) {
		t.Fatalf("page 2 ids: want [log-3 log-2], got %v", got)
	}

	// Page 3, size 2 → log-1 (partial last page).
	logs, total, err = s.QueryLogs(model.LogQuery{Page: 3, PageSize: 2})
	if err != nil {
		t.Fatalf("QueryLogs page 3: %v", err)
	}
	if total != 5 {
		t.Fatalf("page 3 total: want 5, got %d", total)
	}
	if got := idsOf(logs); !equalStringSlices(got, []string{"log-1"}) {
		t.Fatalf("page 3 ids: want [log-1], got %v", got)
	}

	// Page past the end → empty page but total still 5.
	logs, total, err = s.QueryLogs(model.LogQuery{Page: 99, PageSize: 2})
	if err != nil {
		t.Fatalf("QueryLogs page 99: %v", err)
	}
	if total != 5 {
		t.Fatalf("page 99 total: want 5, got %d", total)
	}
	if len(logs) != 0 {
		t.Fatalf("page 99 should be empty, got %v", idsOf(logs))
	}
}

// ----- Total reflects filters (count and data share the WHERE clause) -----

func TestQueryLogsTotalMatchesFilter(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	// Filter that yields 2 rows. Page beyond that → total must still be 2.
	_, total, err := s.QueryLogs(model.LogQuery{
		Provider: "p-openai", Page: 5, PageSize: 2,
	})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 2 {
		t.Fatalf("total: want 2 (filter total), got %d", total)
	}
}

// ----- SQL injection / hostile input -----

func TestQueryLogsSQLInjection(t *testing.T) {
	s := newLogsTestStore(t)
	seedLogs(t, s, fixedLogs())

	// Each of these inputs would break the query or drop the table if
	// interpolated raw. They must be passed as bound parameters and treated
	// as literal text inside the LIKE pattern. We avoid inputs whose LIKE
	// wildcard behaviour would incidentally match real rows (e.g. bare "%")
	// because those don't tell us anything about injection safety.
	hostile := []string{
		`'; DROP TABLE request_logs; --`,
		`' OR '1'='1`,
		`%' OR 1=1 --`,
		`"; DELETE FROM request_logs; --`,
		`' UNION SELECT id, 0, 0, '', '', '', 0, 0, 0, 0, 0, 0, '', '', '', '' FROM request_logs -- `,
	}

	for _, h := range hostile {
		t.Run("search="+h, func(t *testing.T) {
			// Should not error, should return zero rows, should not delete the table.
			_, total, err := s.QueryLogs(model.LogQuery{
				Search: h, Page: 1, PageSize: 10,
			})
			if err != nil {
				t.Fatalf("QueryLogs returned error for hostile input %q: %v", h, err)
			}
			if total != 0 {
				t.Fatalf("expected total 0 for hostile input %q, got %d", h, total)
			}
			// Confirm the table is still intact afterwards.
			all, totalAll, err := s.QueryLogs(model.LogQuery{Page: 1, PageSize: 50})
			if err != nil {
				t.Fatalf("post-injection QueryLogs: %v", err)
			}
			if totalAll != 5 || len(all) != 5 {
				t.Fatalf("table corrupted after hostile input: total=%d len=%d", totalAll, len(all))
			}
		})
	}

	// Hostile input in other string fields (provider/route/model). Same expectations.
	for field, host := range map[string]string{
		"provider": "'; DROP TABLE request_logs; --",
		"route":    `' OR '1'='1`,
		"model":    `' UNION SELECT 1 --`,
	} {
		t.Run("field="+field, func(t *testing.T) {
			q := model.LogQuery{Page: 1, PageSize: 10}
			switch field {
			case "provider":
				q.Provider = host
			case "route":
				q.RouteID = host
			case "model":
				q.Model = host
			}
			_, total, err := s.QueryLogs(q)
			if err != nil {
				t.Fatalf("QueryLogs(%s) hostile: %v", field, err)
			}
			if total != 0 {
				t.Fatalf("expected total 0 for hostile %s input, got %d", field, total)
			}
		})
	}
}

// ----- Chain + diagnostics round-trip -----
//
// TestRequestLogChainRoundTrip covers the new migration 012 columns:
// chain_json, user_agent, client_ip, request_id. The test inserts a
// log with a populated chain and request context, queries it back, and
// verifies the Go-side decoding produces a non-empty Chain slice with
// the right number of attempts and the per-attempt fields intact.
// This guards the JSON-marshalling + Scan pair in QueryLogs and
// InsertRequestLog against regressions in either direction.

func TestRequestLogChainRoundTrip(t *testing.T) {
	s := newLogsTestStore(t)

	chain := []model.RequestLogChainEntry{
		{
			AttemptOrder: 1,
			ProviderID:   "p-openai",
			ProviderName: "OpenAI",
			ModelName:    "gpt-4o",
			TargetID:     "t-1",
			Status:       "retryable",
			StatusCode:   429,
			Error:        "rate limited",
			LatencyMs:    120,
		},
		{
			AttemptOrder: 2,
			ProviderID:   "p-deepseek",
			ProviderName: "DeepSeek",
			ModelName:    "deepseek-chat",
			TargetID:     "t-2",
			Status:       "success",
			StatusCode:   200,
			Error:        "",
			LatencyMs:    350,
		},
	}

	in := model.RequestLog{
		ID:         "log-chain",
		Timestamp:  1700000000000,
		StatusCode: 200,
		ProviderID: "p-deepseek",
		// The final provider/model/route fields should match the last chain
		// entry. The proxy sets them from the successful candidate, so a
		// test that stores them in line with the last chain entry models the
		// real data shape and proves the columns are not lost in transit.
		ProviderName: "DeepSeek",
		Model:        "deepseek-chat",
		RouteID:      "r-fallback",
		RouteLabel:   "Fallback",
		APIKeyID:     "key-1",
		InputTokens:  10,
		OutputTokens: 20,
		UserAgent:    "curl/8.0",
		ClientIP:     "192.168.1.5",
		RequestID:    "req-abc",
		Chain:        chain,
	}
	if err := s.InsertRequestLog(in); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
	}

	logs, total, err := s.QueryLogs(model.LogQuery{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("expected 1 log, got total=%d len=%d", total, len(logs))
	}
	got := logs[0]

	if got.UserAgent != "curl/8.0" {
		t.Fatalf("UserAgent: want %q, got %q", "curl/8.0", got.UserAgent)
	}
	if got.ClientIP != "192.168.1.5" {
		t.Fatalf("ClientIP: want %q, got %q", "192.168.1.5", got.ClientIP)
	}
	if got.RequestID != "req-abc" {
		t.Fatalf("RequestID: want %q, got %q", "req-abc", got.RequestID)
	}
	if len(got.Chain) != 2 {
		t.Fatalf("Chain: want 2 entries, got %d", len(got.Chain))
	}
	if got.Chain[0].Status != "retryable" || got.Chain[0].StatusCode != 429 {
		t.Fatalf("Chain[0]: want retryable/429, got %s/%d", got.Chain[0].Status, got.Chain[0].StatusCode)
	}
	if got.Chain[1].Status != "success" || got.Chain[1].ProviderID != "p-deepseek" {
		t.Fatalf("Chain[1]: want success/p-deepseek, got %s/%s", got.Chain[1].Status, got.Chain[1].ProviderID)
	}
	if got.Chain[0].LatencyMs != 120 || got.Chain[1].LatencyMs != 350 {
		t.Fatalf("Chain latencies: want [120, 350], got [%d, %d]", got.Chain[0].LatencyMs, got.Chain[1].LatencyMs)
	}
}

// TestRequestLogEmptyChainPersistsAsEmpty makes sure that a log row with
// no chain (the pre-migration-012 shape, or streaming single-attempt
// requests) round-trips as a nil/empty Chain slice and never as a
// "null" JSON blob. The store treats an empty chain as a sentinel
// empty string on disk so SQLite never has to distinguish "" from "null".

func TestRequestLogEmptyChainPersistsAsEmpty(t *testing.T) {
	s := newLogsTestStore(t)
	in := model.RequestLog{
		ID:        "log-no-chain",
		Timestamp: 1700000001000,
		Chain:     nil,
	}
	if err := s.InsertRequestLog(in); err != nil {
		t.Fatalf("InsertRequestLog: %v", err)
	}
	logs, _, err := s.QueryLogs(model.LogQuery{Page: 1, PageSize: 5})
	if err != nil {
		t.Fatalf("QueryLogs: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if len(logs[0].Chain) != 0 {
		t.Fatalf("expected empty chain, got %d entries", len(logs[0].Chain))
	}
}

// ----- helpers -----

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

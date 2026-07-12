package store

import (
	"autoapi/internal/model"
	"testing"
	"time"
)

func summary(now time.Time, n int64) model.TargetRuntimeSummary {
	return model.TargetRuntimeSummary{Key: model.TargetMetricKey{TargetID: "t", ProviderID: "p", ModelName: "m", Endpoint: "e"}, Requests: n, LastUsed: now, UpdatedAt: now}
}
func TestRuntimeSummaryBatchValidationAndOverwrite(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	if err := s.UpsertTargetRuntimeSummaries([]model.TargetRuntimeSummary{summary(now, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTargetRuntimeSummaries([]model.TargetRuntimeSummary{summary(now, 9)}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LoadActiveTargetRuntimeSummaries(now, time.Hour)
	if len(got) != 1 || got[0].Requests != 9 {
		t.Fatalf("%+v", got)
	}
	bad := summary(now, 2)
	bad.Key.ProviderID = model.MetricProviderPreflight
	if err := s.UpsertTargetRuntimeSummaries([]model.TargetRuntimeSummary{summary(now, 3), bad}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	got, _ = s.LoadActiveTargetRuntimeSummaries(now, time.Hour)
	if got[0].Requests != 9 {
		t.Fatal("batch was partially committed")
	}
}
func TestRuntimeSummaryTTLCleanup(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().UTC()
	old := summary(now.Add(-2*time.Hour), 1)
	if err := s.UpsertTargetRuntimeSummaries([]model.TargetRuntimeSummary{old}); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupTargetRuntimeSummaries(now, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LoadActiveTargetRuntimeSummaries(now, time.Hour)
	if len(got) != 0 {
		t.Fatal(got)
	}
}

package proxy

import (
	"sync"
	"time"

	"autoapi/internal/model"
)

const (
	targetFailureWindow    = 5 * time.Minute
	targetFailureThreshold = 5
)

type targetBreaker struct {
	mu                       sync.Mutex
	failures                 []time.Time
	lastSuccess, lastFailure time.Time
	lastReason               string
}

type TargetBreakerStatus struct {
	Key           model.RouteModeKey `json:"key"`
	TargetID      string             `json:"target_id"`
	Order         int                `json:"order"`
	Endpoint      string             `json:"endpoint"`
	State         string             `json:"state"`
	FailureCount  int                `json:"failure_count"`
	WindowSeconds int                `json:"window_seconds"`
	LastSuccessMs int64              `json:"last_success_ms"`
	LastFailureMs int64              `json:"last_failure_ms"`
	RecoveryAtMs  int64              `json:"recovery_at_ms"`
	FailureReason string             `json:"failure_reason,omitempty"`
}

func (b *targetBreaker) prune(now time.Time) {
	cutoff := now.Add(-targetFailureWindow)
	i := 0
	for i < len(b.failures) && !b.failures[i].After(cutoff) {
		i++
	}
	if i > 0 {
		b.failures = append([]time.Time(nil), b.failures[i:]...)
	}
}
func (b *targetBreaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	return len(b.failures) < targetFailureThreshold
}
func (b *targetBreaker) recordFailure(now time.Time, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	b.failures = append(b.failures, now)
	b.lastFailure, b.lastReason = now, reason
}
func (b *targetBreaker) recordSuccess(now time.Time) { b.mu.Lock(); b.lastSuccess = now; b.mu.Unlock() }
func (b *targetBreaker) status(key model.RouteModeKey, endpoint string, order int, now time.Time) TargetBreakerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prune(now)
	s := TargetBreakerStatus{Key: key, TargetID: key.TargetID, Order: order, Endpoint: endpoint, State: "closed", FailureCount: len(b.failures), WindowSeconds: int(targetFailureWindow.Seconds()), FailureReason: b.lastReason}
	if len(b.failures) >= targetFailureThreshold {
		s.State = "open"
		s.RecoveryAtMs = b.failures[0].Add(targetFailureWindow).UnixMilli()
	}
	if !b.lastSuccess.IsZero() {
		s.LastSuccessMs = b.lastSuccess.UnixMilli()
	}
	if !b.lastFailure.IsZero() {
		s.LastFailureMs = b.lastFailure.UnixMilli()
	}
	return s
}
func targetFailure(err error, status int) bool {
	if err != nil {
		return isCircuitBreakerFailure(err, status)
	}
	return status == 401 || status == 403 || status == 429 || status >= 500
}

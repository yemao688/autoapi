package proxy

import (
	"testing"
	"time"
)

func TestCircuitBreaker_OpensAfterFailures(t *testing.T) {
	cb := NewCircuitBreaker()
	if cb.CurrentState() != StateClosed {
		t.Fatalf("expected closed, got %v", cb.CurrentState())
	}
	for i := 0; i < failureThreshold-1; i++ {
		cb.Record(false)
		if !cb.Allow() {
			t.Fatalf("expected still allowed after %d failures", i+1)
		}
	}
	cb.Record(false)
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected open after %d failures, got %v", failureThreshold, cb.CurrentState())
	}
	if cb.Allow() {
		t.Fatal("expected open breaker to reject traffic")
	}
}

func TestCircuitBreaker_Recovery(t *testing.T) {
	now := time.Now()
	cb := NewCircuitBreaker()
	cb.nowFn = func() time.Time { return now }
	cb.recoveryTimeout = time.Minute

	// Open the breaker.
	for i := 0; i < failureThreshold; i++ {
		cb.Record(false)
	}
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected open, got %v", cb.CurrentState())
	}

	// Before timeout: still closed.
	if cb.Allow() {
		t.Fatal("expected open before recovery timeout")
	}

	// Advance past timeout: single probe allowed.
	now = now.Add(2 * time.Minute)
	if !cb.Allow() {
		t.Fatal("expected half-open probe to be allowed")
	}
	if cb.CurrentState() != StateHalfOpen {
		t.Fatalf("expected half-open, got %v", cb.CurrentState())
	}
	// Only one probe at a time.
	if cb.Allow() {
		t.Fatal("expected only one half-open probe at a time")
	}
}

func TestCircuitBreaker_HalfOpenSuccessCloses(t *testing.T) {
	now := time.Now()
	cb := NewCircuitBreaker()
	cb.nowFn = func() time.Time { return now }
	cb.recoveryTimeout = time.Minute

	for i := 0; i < failureThreshold; i++ {
		cb.Record(false)
	}
	now = now.Add(2 * time.Minute)
	cb.Allow() // move to half-open and claim probe
	cb.Record(true)
	if cb.CurrentState() != StateClosed {
		t.Fatalf("expected closed after successful probe, got %v", cb.CurrentState())
	}
	if !cb.Allow() {
		t.Fatal("expected traffic to be allowed after close")
	}
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	now := time.Now()
	cb := NewCircuitBreaker()
	cb.nowFn = func() time.Time { return now }
	cb.recoveryTimeout = time.Minute

	for i := 0; i < failureThreshold; i++ {
		cb.Record(false)
	}
	now = now.Add(2 * time.Minute)
	cb.Allow() // move to half-open and claim probe
	cb.Record(false)
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected open after failed probe, got %v", cb.CurrentState())
	}
	if cb.Allow() {
		t.Fatal("expected traffic to be rejected after re-open")
	}
}

func TestCircuitBreaker_SuccessResetsFailures(t *testing.T) {
	cb := NewCircuitBreaker()
	for i := 0; i < failureThreshold-2; i++ {
		cb.Record(false)
	}
	cb.Record(true)
	for i := 0; i < failureThreshold-1; i++ {
		cb.Record(false)
	}
	if cb.CurrentState() != StateClosed {
		t.Fatalf("expected closed, got %v", cb.CurrentState())
	}
	cb.Record(false)
	if cb.CurrentState() != StateOpen {
		t.Fatalf("expected open, got %v", cb.CurrentState())
	}
}

func TestCircuitBreaker_CurrentStateDoesNotClaimHalfOpenProbe(t *testing.T) {
	now := time.Now()
	cb := NewCircuitBreaker()
	cb.nowFn = func() time.Time { return now }
	cb.recoveryTimeout = time.Minute
	for i := 0; i < failureThreshold; i++ {
		cb.Record(false)
	}
	now = now.Add(2 * time.Minute)
	if cb.CurrentState() != StateOpen {
		t.Fatalf("state read changed open breaker: %v", cb.CurrentState())
	}
	if !cb.Allow() {
		t.Fatal("expected the actual Allow call to claim the probe")
	}
}

package proxy

import (
	"sync"
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

func TestRouteModeCircuitBreakerThresholdRecoveryAndCancel(t *testing.T) {
	now := time.Unix(100, 0)
	cb := NewRouteModeCircuitBreaker()
	cb.nowFn = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		cb.Record(false)
	}
	if cb.CurrentState() != StateClosed {
		t.Fatalf("route breaker opened early: %v", cb.CurrentState())
	}
	cb.Record(false)
	if cb.CurrentState() != StateOpen {
		t.Fatalf("route breaker did not open at threshold: %v", cb.CurrentState())
	}
	now = now.Add(29 * time.Second)
	if cb.Allow() {
		t.Fatal("route breaker recovered before 30 seconds")
	}
	now = now.Add(time.Second)
	if !cb.Allow() || cb.CurrentState() != StateHalfOpen {
		t.Fatalf("route breaker did not allow half-open probe: %v", cb.CurrentState())
	}
	if cb.Allow() {
		t.Fatal("route breaker allowed a second probe")
	}
	cb.CancelProbe()
	if cb.CurrentState() != StateOpen {
		t.Fatalf("cancel did not return route breaker to open: %v", cb.CurrentState())
	}
	now = now.Add(time.Second)
	if !cb.Allow() {
		t.Fatal("cancel extended the cooldown instead of preserving openedAt")
	}
}

func TestRouteModeCircuitBreakerConcurrentSingleProbe(t *testing.T) {
	now := time.Unix(100, 0)
	cb := NewRouteModeCircuitBreaker()
	cb.nowFn = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		cb.Record(false)
	}
	now = now.Add(30 * time.Second)
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cb.Allow() {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("concurrent route probes=%d, want 1", allowed)
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

func TestBreakerLeaseRejectsStaleClosedLeases(t *testing.T) {
	for _, newCB := range []struct {
		name string
		new  func() *CircuitBreaker
	}{
		{name: "provider", new: NewCircuitBreaker},
		{name: "route", new: NewRouteModeCircuitBreaker},
	} {
		for _, outcome := range []string{"success", "failure", "cancel"} {
			t.Run(newCB.name+"/"+outcome, func(t *testing.T) {
				cb := newCB.new()
				now := time.Unix(100, 0)
				cb.nowFn = func() time.Time { return now }
				allowed, stale := cb.Acquire()
				if !allowed || stale.halfOpen {
					t.Fatal("expected closed lease")
				}
				for i := 0; i < cb.failureThreshold; i++ {
					cb.Record(false)
				}
				now = now.Add(cb.recoveryTimeout)
				allowed, fresh := cb.Acquire()
				if !allowed || !fresh.halfOpen || !cb.pendingProbe {
					t.Fatal("expected fresh half-open lease")
				}
				openedAt, generation := cb.openedAt, cb.generation
				switch outcome {
				case "success":
					if cb.settle(stale, true) {
						t.Fatal("stale success was accepted")
					}
				case "failure":
					if cb.settle(stale, false) {
						t.Fatal("stale failure was accepted")
					}
				case "cancel":
					if cb.settleCancel(stale) {
						t.Fatal("stale cancel was accepted")
					}
				}
				if !cb.pendingProbe || cb.CurrentState() != StateHalfOpen || !cb.openedAt.Equal(openedAt) || cb.generation != generation {
					t.Fatalf("stale lease changed fresh lease: state=%v pending=%v openedAt=%v generation=%d", cb.CurrentState(), cb.pendingProbe, cb.openedAt, cb.generation)
				}
				if !cb.settle(fresh, true) || cb.CurrentState() != StateClosed {
					t.Fatal("fresh lease did not settle successfully")
				}
			})
		}
	}
}

func TestBreakerLeaseRejectsStaleHalfOpenAfterReacquire(t *testing.T) {
	for _, newCB := range []struct {
		name string
		new  func() *CircuitBreaker
	}{
		{name: "provider", new: NewCircuitBreaker},
		{name: "route", new: NewRouteModeCircuitBreaker},
	} {
		t.Run(newCB.name, func(t *testing.T) {
			cb := newCB.new()
			now := time.Unix(100, 0)
			cb.nowFn = func() time.Time { return now }
			for i := 0; i < cb.failureThreshold; i++ {
				cb.Record(false)
			}
			now = now.Add(cb.recoveryTimeout)
			_, stale := cb.Acquire()
			if !stale.halfOpen {
				t.Fatal("expected half-open stale lease")
			}
			if !cb.settleCancel(stale) {
				t.Fatal("initial half-open cancel failed")
			}
			now = now.Add(cb.recoveryTimeout)
			_, fresh := cb.Acquire()
			openedAt, generation := cb.openedAt, cb.generation
			if cb.settle(stale, true) || cb.settle(stale, false) || cb.settleCancel(stale) {
				t.Fatal("stale half-open lease was accepted after reacquire")
			}
			if !cb.pendingProbe || cb.CurrentState() != StateHalfOpen || !cb.openedAt.Equal(openedAt) || cb.generation != generation {
				t.Fatal("stale lease changed reacquired probe")
			}
			if !cb.settleCancel(fresh) || cb.CurrentState() != StateOpen {
				t.Fatal("fresh reacquired lease did not cancel")
			}
		})
	}
}

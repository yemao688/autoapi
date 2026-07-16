// circuitbreaker.go implements a per-provider circuit breaker for the proxy.
// It is heavily inspired by the CC-Switch pattern: after enough consecutive
// failures the breaker opens, rejecting traffic for a cooldown period. A single
// probe is allowed through in half-open state; success closes the breaker and
// failure re-opens it.
package proxy

import (
	"log/slog"
	"sync"
	"time"
)

// State is the circuit breaker state.
type State int

const (
	// StateClosed means requests flow normally.
	StateClosed State = iota
	// StateOpen means requests are rejected; after a timeout one probe is allowed.
	StateOpen
	// StateHalfOpen means a single probe request is in progress.
	StateHalfOpen
)

const (
	// failureThreshold is the number of consecutive failures that open the breaker.
	failureThreshold = 4
	// recoveryTimeout is how long the breaker stays open before allowing a probe.
	recoveryTimeout = 60 * time.Second
)

// CircuitBreaker guards a single provider.
type CircuitBreaker struct {
	state               State
	generation          uint64
	consecutiveFailures int
	failureThreshold    int
	openedAt            time.Time
	pendingProbe        bool
	recoveryTimeout     time.Duration
	nowFn               func() time.Time
	mutex               sync.Mutex
}

// BreakerLease is an opaque permission issued by Acquire. Its fields are
// intentionally private so callers cannot manufacture a valid lease.
type BreakerLease struct {
	generation uint64
	halfOpen   bool
}

// StringStatus is a detached, read-only breaker view for diagnostics.
type BreakerStatus struct {
	State State `json:"state"`
}

// NewCircuitBreaker creates a closed circuit breaker with default settings.
func NewCircuitBreaker() *CircuitBreaker {
	return newCircuitBreaker(failureThreshold, recoveryTimeout)
}

func newCircuitBreaker(threshold int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            StateClosed,
		generation:       1,
		failureThreshold: threshold,
		recoveryTimeout:  timeout,
		nowFn:            time.Now,
	}
}

func NewRouteModeCircuitBreaker() *CircuitBreaker {
	return newCircuitBreaker(3, 30*time.Second)
}

// CurrentState returns the breaker state for tests/debugging. It does not
// trigger the open→half-open timeout transition.
func (cb *CircuitBreaker) CurrentState() State {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	return cb.state
}

func (cb *CircuitBreaker) ConsecutiveFailures() int {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	return cb.consecutiveFailures
}

func (cb *CircuitBreaker) Generation() uint64 {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	return cb.generation
}

// Acquire claims permission for one candidate execution. ownsHalfOpen is true
// when this call owns the single half-open probe lease.
func (cb *CircuitBreaker) Acquire() (bool, BreakerLease) {
	return cb.acquireLease()
}

func (cb *CircuitBreaker) acquireLease() (bool, BreakerLease) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case StateClosed:
		return true, BreakerLease{generation: cb.generation}
	case StateOpen:
		if cb.nowFn().Sub(cb.openedAt) >= cb.recoveryTimeout {
			cb.state = StateHalfOpen
			cb.pendingProbe = true
			cb.generation++
			return true, BreakerLease{generation: cb.generation, halfOpen: true}
		}
		return false, BreakerLease{}
	case StateHalfOpen:
		if cb.pendingProbe {
			return false, BreakerLease{}
		}
		cb.pendingProbe = true
		cb.generation++
		return true, BreakerLease{generation: cb.generation, halfOpen: true}
	default:
		return false, BreakerLease{}
	}
}

// Allow is the compatibility wrapper for callers that do not need lease
// ownership information.
func (cb *CircuitBreaker) Allow() bool {
	allowed, _ := cb.acquireLease()
	return allowed
}

func (cb *CircuitBreaker) canContinue(lease BreakerLease) bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	if lease.generation == 0 || lease.generation != cb.generation {
		return false
	}
	if lease.halfOpen {
		return cb.state == StateHalfOpen && cb.pendingProbe
	}
	return cb.state == StateClosed
}

func (cb *CircuitBreaker) settle(lease BreakerLease, success bool) bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	if lease.generation == 0 || lease.generation != cb.generation {
		return false
	}
	if lease.halfOpen {
		if cb.state != StateHalfOpen || !cb.pendingProbe {
			return false
		}
		oldState := cb.state
		cb.pendingProbe = false
		if success {
			cb.state = StateClosed
			cb.consecutiveFailures = 0
		} else {
			cb.state = StateOpen
			cb.openedAt = cb.nowFn()
			cb.consecutiveFailures++
		}
		cb.generation++
		cb.logTransition(oldState, success)
		return true
	}
	if cb.state != StateClosed {
		return false
	}
	if success {
		cb.consecutiveFailures = 0
		return true
	}
	cb.consecutiveFailures++
	threshold := cb.failureThreshold
	if threshold <= 0 {
		threshold = failureThreshold
	}
	if cb.consecutiveFailures >= threshold {
		cb.state = StateOpen
		cb.openedAt = cb.nowFn()
		cb.generation++
		cb.logTransition(StateClosed, false)
	}
	return true
}

func (cb *CircuitBreaker) settleCancel(lease BreakerLease) bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	if lease.generation == 0 || lease.generation != cb.generation || !lease.halfOpen || cb.state != StateHalfOpen || !cb.pendingProbe {
		return false
	}
	cb.pendingProbe = false
	cb.state = StateOpen
	cb.generation++
	return true
}

func (cb *CircuitBreaker) logTransition(oldState State, success bool) {
	if oldState != cb.state {
		slog.Info("proxy: circuit breaker state changed", "from", oldState, "to", cb.state, "success", success, "consecutiveFailures", cb.consecutiveFailures)
	}
}

// WouldAllow reports whether the breaker would allow a request without
// consuming a probe or transitioning from open to half-open. It is used by
// the matcher to decide which candidates are available without claiming the
// single half-open probe.
func (cb *CircuitBreaker) WouldAllow() bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		return cb.nowFn().Sub(cb.openedAt) >= cb.recoveryTimeout
	case StateHalfOpen:
		return !cb.pendingProbe
	default:
		return false
	}
}

// Record updates the breaker based on the outcome of the last request. When
// the breaker is half-open, a probe outcome transitions to closed or re-opens
// the circuit.
func (cb *CircuitBreaker) Record(success bool) {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	oldState := cb.state

	switch cb.state {
	case StateClosed:
		if success {
			cb.consecutiveFailures = 0
		} else {
			cb.consecutiveFailures++
			threshold := cb.failureThreshold
			if threshold <= 0 {
				threshold = failureThreshold
			}
			if cb.consecutiveFailures >= threshold {
				cb.state = StateOpen
				cb.openedAt = cb.nowFn()
				cb.generation++
			}
		}
	case StateHalfOpen:
		cb.pendingProbe = false
		if success {
			cb.state = StateClosed
			cb.consecutiveFailures = 0
			cb.generation++
		} else {
			cb.state = StateOpen
			cb.openedAt = cb.nowFn()
			cb.consecutiveFailures++
			cb.generation++
		}
	case StateOpen:
		if success {
			cb.state = StateClosed
			cb.consecutiveFailures = 0
			cb.generation++
		}
	}

	if oldState != cb.state {
		slog.Info("proxy: circuit breaker state changed", "from", oldState, "to", cb.state, "success", success, "consecutiveFailures", cb.consecutiveFailures)
	}
}

// CancelProbe releases a claimed half-open probe without changing the
// cooldown start time. It is neutral: closed breakers and non-pending probes
// are left untouched.
func (cb *CircuitBreaker) CancelProbe() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	if cb.state == StateHalfOpen && cb.pendingProbe {
		cb.pendingProbe = false
		cb.state = StateOpen
		cb.generation++
	}
}

// String returns a human-readable state name.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

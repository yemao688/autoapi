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
	consecutiveFailures int
	openedAt            time.Time
	pendingProbe        bool
	recoveryTimeout     time.Duration
	nowFn               func() time.Time
	mutex               sync.Mutex
}

// StringStatus is a detached, read-only breaker view for diagnostics.
type BreakerStatus struct {
	State State `json:"state"`
}

// NewCircuitBreaker creates a closed circuit breaker with default settings.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		state:           StateClosed,
		recoveryTimeout: recoveryTimeout,
		nowFn:           time.Now,
	}
}

// CurrentState returns the breaker state for tests/debugging. It does not
// trigger the open→half-open timeout transition.
func (cb *CircuitBreaker) CurrentState() State {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	return cb.state
}

// Allow reports whether a request may be sent to the provider. It also performs
// the open→half-open transition when the recovery timeout has elapsed.
func (cb *CircuitBreaker) Allow() bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		if cb.nowFn().Sub(cb.openedAt) >= cb.recoveryTimeout {
			cb.state = StateHalfOpen
			cb.pendingProbe = true
			return true
		}
		return false
	case StateHalfOpen:
		if cb.pendingProbe {
			return false
		}
		cb.pendingProbe = true
		return true
	default:
		return false
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
			if cb.consecutiveFailures >= failureThreshold {
				cb.state = StateOpen
				cb.openedAt = cb.nowFn()
			}
		}
	case StateHalfOpen:
		cb.pendingProbe = false
		if success {
			cb.state = StateClosed
			cb.consecutiveFailures = 0
		} else {
			cb.state = StateOpen
			cb.openedAt = cb.nowFn()
			cb.consecutiveFailures++
		}
	case StateOpen:
		if success {
			cb.state = StateClosed
			cb.consecutiveFailures = 0
		}
	}

	if oldState != cb.state {
		slog.Info("proxy: circuit breaker state changed", "from", oldState, "to", cb.state, "success", success, "consecutiveFailures", cb.consecutiveFailures)
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

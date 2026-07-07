// errorcat.go categorizes upstream failures so the failover loop can decide
// whether to retry the next candidate, open the circuit breaker, or return
// immediately to the client.
package proxy

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// ErrorCategory classifies the outcome of an upstream attempt.
type ErrorCategory int

const (
	// CategoryRetryable means the next candidate may be tried.
	CategoryRetryable ErrorCategory = iota
	// CategoryNonRetryable means the request should fail immediately.
	CategoryNonRetryable
	// CategoryClientAbort means the client canceled the request.
	CategoryClientAbort
)

// CategorizeError classifies an upstream response. If err is non-nil, the error
// type takes precedence over the status code. Client-side 4xx errors are
// generally non-retryable, except 401/403/404/408/409/429 which may succeed on
// a different provider; 5xx and network errors are retryable.
func CategorizeError(err error, statusCode int) ErrorCategory {
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return CategoryClientAbort
		}
		if isNetError(err) {
			return CategoryRetryable
		}
		// Unknown error: treat as retryable to be safe.
		return CategoryRetryable
	}

	switch statusCode {
	case 400, 405, 406, 413, 414, 415, 422, 501:
		return CategoryNonRetryable
	case 401, 403, 404, 408, 409, 429:
		return CategoryRetryable
	}
	if statusCode >= 500 {
		return CategoryRetryable
	}
	return CategoryNonRetryable
}

// isCircuitBreakerFailure reports whether a failure should count toward the
// provider circuit breaker. Only true network/transport failures and 5xx
// upstream responses open the breaker; 4xx client errors do not.
func isCircuitBreakerFailure(err error, statusCode int) bool {
	if err != nil {
		return isNetError(err)
	}
	return statusCode >= 500
}

// isClientDisconnect reports whether err represents the client disconnecting
// (broken pipe on write, or request context canceled) — NOT an upstream failure.
// Such errors must not penalize the provider's circuit breaker or health.
func isClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "write" {
		// broken pipe / connection reset writing TO the client
		return true
	}
	return false
}

// isNetError reports whether the error is a network, timeout, connection, or
// DNS failure. These are the failure modes circuit breakers are meant to
// detect.
func isNetError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

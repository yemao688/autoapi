// errorcat.go categorizes upstream failures so the failover loop can decide
// whether to retry the next candidate, open the circuit breaker, or return
// immediately to the client.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
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
// a different provider; 5xx and network errors are retryable. Unknown 4xx
// codes (e.g. 451, 460) are ALSO retryable: a misbehaving provider returning
// an odd status should not kill failover for the rest of the chain — let the
// next candidate try. The explicit NonRetryable list (400/405/406/413/414/
// 415/422/501) is reserved for HTTP codes whose semantics are clearly
// "client error, do not retry" per RFC 9110 and OpenAI API behavior.
//
// Note: this is a categorizer only — it does NOT decide whether to penalize
// the provider's circuit breaker. See isCircuitBreakerFailure for that: only
// 5xx and net errors count toward the breaker, so an unknown 4xx that is now
// retryable will failover WITHOUT tripping the breaker.
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

	// Explicit non-retryable 4xx: the request itself is malformed in a
	// way that no other provider can fix. Any other status is either
	// retryable (auth/quota/5xx/unknown 4xx) or out of the failure
	// path (< 400).
	switch statusCode {
	case 400, 405, 406, 413, 414, 415, 422, 501:
		return CategoryNonRetryable
	}
	if statusCode >= 400 {
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

// isConnReset reports whether the error indicates the upstream connection was
// reset or closed prematurely — a truncated response. This catches raw
// *net.OpError connection-reset errors that don't unwrap to io.ErrUnexpectedEOF.
func isConnReset(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		s := opErr.Err.Error()
		if strings.Contains(s, "connection reset") || strings.Contains(s, "broken pipe") {
			return true
		}
	}
	return false
}

// extractUpstreamError attempts to parse a human-readable error message from
// an upstream HTTP response body. Most OpenAI-compatible APIs return errors in
// the form {"error":{"message":"...","type":"...","code":"..."}}. If the body
// is not valid JSON or does not contain an error field, the raw body (truncated
// to 500 bytes) is returned as a fallback.
//
// The result is always <= 500 bytes so it fits in a log column without blowing
// up the chain JSON.
func extractUpstreamError(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	// Try OpenAI-style nested error envelope.
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		msg := envelope.Error.Message
		if envelope.Error.Type != "" {
			msg = envelope.Error.Type + ": " + msg
		}
		return truncateErr(msg)
	}
	// Some providers use a flat {"message":"..."} or {"detail":"..."} shape.
	var flat struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &flat); err == nil {
		if flat.Message != "" {
			return truncateErr(flat.Message)
		}
		if flat.Detail != "" {
			return truncateErr(flat.Detail)
		}
		if flat.Error != "" {
			return truncateErr(flat.Error)
		}
	}
	// Fallback: raw body truncated.
	return truncateErr(string(body))
}

func truncateErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		return s[:497] + "..."
	}
	return s
}

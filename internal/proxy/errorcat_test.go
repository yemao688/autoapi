package proxy

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
)

func TestCategorizeError_StatusCodes(t *testing.T) {
	cases := []struct {
		status int
		want   ErrorCategory
	}{
		// Non-retryable: explicit list. Per RFC 9110 / OpenAI
		// semantics, these are "request itself is malformed" — no
		// other provider will succeed.
		{400, CategoryNonRetryable},
		{405, CategoryNonRetryable},
		{406, CategoryNonRetryable},
		{413, CategoryNonRetryable},
		{414, CategoryNonRetryable},
		{415, CategoryNonRetryable},
		{422, CategoryNonRetryable},
		{501, CategoryNonRetryable},
		// Retryable: documented retryable 4xx (auth/quota/conflict).
		{401, CategoryRetryable},
		{403, CategoryRetryable},
		{404, CategoryRetryable},
		{408, CategoryRetryable},
		{409, CategoryRetryable},
		{429, CategoryRetryable},
		// Retryable: 5xx upstream failures.
		{500, CategoryRetryable},
		{502, CategoryRetryable},
		{503, CategoryRetryable},
		{504, CategoryRetryable},
		// Retryable: unknown / vendor-specific 4xx. A misbehaving
		// provider returning an odd status must not block failover
		// to the rest of the chain.
		{451, CategoryRetryable}, // legal-blocked
		{460, CategoryRetryable}, // vendor-specific
		{463, CategoryRetryable}, // vendor-specific
		{499, CategoryRetryable}, // nginx-style custom
		// 2xx is not categorized as a failure path
	}
	for _, tc := range cases {
		got := CategorizeError(nil, tc.status)
		if got != tc.want {
			t.Errorf("status %d: got %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestCategorizeError_ClientAbort(t *testing.T) {
	got := CategorizeError(context.Canceled, 0)
	if got != CategoryClientAbort {
		t.Fatalf("context.Canceled: got %v, want %v", got, CategoryClientAbort)
	}
}

func TestCategorizeError_NetworkErrors(t *testing.T) {
	cases := []error{
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		errors.New("some network error"),
	}
	for _, err := range cases {
		got := CategorizeError(err, 0)
		if got != CategoryRetryable {
			t.Errorf("err %v: got %v, want retryable", err, got)
		}
	}
}

func TestIsCircuitBreakerFailure(t *testing.T) {
	if !isCircuitBreakerFailure(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, 0) {
		t.Fatal("expected connection refused to be a breaker failure")
	}
	if !isCircuitBreakerFailure(nil, 503) {
		t.Fatal("expected 503 to be a breaker failure")
	}
	if isCircuitBreakerFailure(nil, 429) {
		t.Fatal("expected 429 not to be a breaker failure")
	}
	if isCircuitBreakerFailure(nil, 400) {
		t.Fatal("expected 400 not to be a breaker failure")
	}
}

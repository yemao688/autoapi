package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptrace"
	"sync"
	"time"
)

// errFirstByteTimeout is the cancellation cause used when the per-attempt
// first-byte timer fires before response headers arrive. It lets error
// classification distinguish an upstream first-byte timeout from a genuine
// client disconnect — both surface as context.Canceled from http.Client,
// but only the timer carries this cause.
var errFirstByteTimeout = errors.New("first-byte timeout")

// withFirstByteTimer bounds the time an attempt waits for upstream response
// headers without bounding body reads afterwards. This replaces the old
// per-attempt Transport.Clone() + ResponseHeaderTimeout override, which
// discarded the connection pool on every attempt (no keep-alive reuse, a
// full TCP+TLS handshake per attempt, and "unexpected EOF" races from
// constantly-churned connections).
//
// Semantics match ResponseHeaderTimeout: the window starts when the request
// is fully written (httptrace WroteRequest) and disarms on the first
// response header byte (GotFirstResponseByte). Unlike context.WithDeadline —
// which http.Client enforces for the ENTIRE request lifecycle and would kill
// long LLM streams mid-body — post-header body reads stay governed only by
// the parent (client) context. Transport-internal retries (e.g. GetBody
// replay on a dead reused connection) re-arm the window via WroteRequest.
//
// d <= 0 disables the timer. The returned CancelFunc must always be called
// once the attempt has finished (success or failure) to release the timer.
func withFirstByteTimer(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, causeCancel := context.WithCancelCause(parent)
	cancel := context.CancelFunc(func() { causeCancel(nil) })
	if d <= 0 {
		return ctx, cancel
	}
	var mu sync.Mutex
	var timer *time.Timer
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			mu.Lock()
			defer mu.Unlock()
			if timer == nil {
				timer = time.AfterFunc(d, func() { causeCancel(errFirstByteTimeout) })
			} else {
				// Transport retried the write (e.g. replayed onto a
				// fresh connection after a dead idle one) — restart the
				// first-byte window for the new attempt on the wire.
				timer.Reset(d)
			}
		},
		GotFirstResponseByte: func() {
			mu.Lock()
			defer mu.Unlock()
			if timer != nil {
				timer.Stop()
			}
		},
	}
	return httptrace.WithClientTrace(ctx, trace), cancel
}

// mapFirstByteTimeout rewrites a cancellation produced by withFirstByteTimer
// into a timeout-class error so downstream classification (CategorizeError,
// isTimeoutError) treats it as a retryable upstream timeout rather than a
// client abort. Errors not caused by the timer pass through unchanged.
func mapFirstByteTimeout(ctx context.Context, err error, d time.Duration) error {
	if err != nil && errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), errFirstByteTimeout) {
		return fmt.Errorf("upstream did not send response headers within %s: %w", d, context.DeadlineExceeded)
	}
	return err
}

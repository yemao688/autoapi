package proxy

import (
	"context"
	"fmt"
	"io"
	"time"
)

// readFirstBodyByte performs the only bounded body read in a streaming
// attempt. Closing the body on timeout unblocks readers that do not expose a
// net.Conn deadline, and waiting for the goroutine prevents leaks.
func readFirstBodyByte(ctx context.Context, body io.ReadCloser, deadline time.Time) ([]byte, error) {
	return readBodyChunk(ctx, body, 32*1024, deadline)
}

// readBodyChunk reads one body chunk while a pre-commit deadline is active.
// The body is closed on cancellation so a provider that stalls in Read cannot
// leave a goroutine behind. The result channel is buffered and the reader is
// always joined after Close.
func readBodyChunk(ctx context.Context, body io.ReadCloser, size int, deadline time.Time) ([]byte, error) {
	return readBodyChunkWithError(ctx, body, size, deadline, func() error {
		return &firstBodyByteTimeoutError{deadline: deadline}
	})
}

func readBodyChunkWithError(ctx context.Context, body io.ReadCloser, size int, deadline time.Time, makeTimeoutErr func() error) ([]byte, error) {
	result := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		buf := make([]byte, size)
		n, err := body.Read(buf)
		result <- struct {
			data []byte
			err  error
		}{buf[:n], err}
	}()
	var timer <-chan time.Time
	var t *time.Timer
	if !deadline.IsZero() {
		t = time.NewTimer(time.Until(deadline))
		defer t.Stop()
		timer = t.C
	}
	select {
	case r := <-result:
		return r.data, r.err
	case <-ctx.Done():
		_ = body.Close()
		r := <-result
		if len(r.data) > 0 {
			return r.data, r.err
		}
		return nil, ctx.Err()
	case <-timer:
		_ = body.Close()
		r := <-result
		if ctx.Err() != nil {
			return r.data, ctx.Err()
		}
		return r.data, makeTimeoutErr()
	}
}

type firstBodyByteTimeoutError struct{ deadline time.Time }

func (e *firstBodyByteTimeoutError) Error() string   { return "first response-body byte timeout" }
func (e *firstBodyByteTimeoutError) Timeout() bool   { return true }
func (e *firstBodyByteTimeoutError) Temporary() bool { return true }

type stallTimeoutError struct{ timeout time.Duration }

func (e *stallTimeoutError) Error() string {
	return fmt.Sprintf("upstream stream stalled (no data for %s)", e.timeout)
}
func (e *stallTimeoutError) Timeout() bool   { return true }
func (e *stallTimeoutError) Temporary() bool { return true }

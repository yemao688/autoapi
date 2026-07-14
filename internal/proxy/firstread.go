package proxy

import (
	"context"
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
		if r.err == nil {
			r.err = &firstBodyByteTimeoutError{deadline: deadline}
		}
		return r.data, r.err
	}
}

type firstBodyByteTimeoutError struct{ deadline time.Time }

func (e *firstBodyByteTimeoutError) Error() string   { return "first response-body byte timeout" }
func (e *firstBodyByteTimeoutError) Timeout() bool   { return true }
func (e *firstBodyByteTimeoutError) Temporary() bool { return true }

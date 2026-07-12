package proxy

import (
	"context"
	"io"
	"testing"
	"time"
)

type blockingBody struct{ closed chan struct{} }

func (b *blockingBody) Read([]byte) (int, error) { <-b.closed; return 0, io.ErrUnexpectedEOF }
func (b *blockingBody) Close() error {
	select {
	case <-b.closed:
	default:
		close(b.closed)
	}
	return nil
}

func TestReadFirstBodyByteClientCancellation(t *testing.T) {
	body := &blockingBody{closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	_, err := readFirstBodyByte(ctx, body, time.Now().Add(time.Second))
	if err != context.Canceled {
		t.Fatalf("expected client cancellation, got %v", err)
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("body was not closed")
	}
}

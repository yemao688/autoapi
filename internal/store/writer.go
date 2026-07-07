package store

import (
	"database/sql"
	"fmt"
)

// writeOp is a unit of work submitted to the serial write goroutine.
type writeOp struct {
	fn   func(tx *sql.Tx) error
	done chan error
}

// Writer serialises all write operations through a single goroutine so that
// SQLite's single-writer limitation is handled cleanly. Submit blocks until
// the operation completes, so callers have a synchronous guarantee.
//
// Reads are NOT routed through the Writer — they go directly against *sql.DB,
// which is safe under WAL mode (concurrent readers do not block).
type Writer struct {
	db   *sql.DB
	ch   chan *writeOp
	done chan struct{}
}

// NewWriter creates a writer with the given buffer capacity.
func NewWriter(db *sql.DB, cap int) *Writer {
	return &Writer{
		db:   db,
		ch:   make(chan *writeOp, cap),
		done: make(chan struct{}),
	}
}

// Submit enqueues a write operation and blocks until it completes. fn runs
// inside a transaction. Returns the error from fn, or an error from the
// transaction lifecycle.
//
// Caller contract for non-critical writes (e.g. request_log persistence on the
// proxy hot path): a Submit returning ErrQueueFull means the system is under
// sustained write pressure. For non-critical writes, callers MUST treat this
// as a drop, NOT as a request-failing error — losing one log entry is much
// better than 503-ing a forwarded LLM call. The buffered channel (cap 1024)
// absorbs typical bursts; ErrQueueFull should only surface under extreme
// pressure or after process restart.
func (w *Writer) Submit(fn func(tx *sql.Tx) error) error {
	op := &writeOp{
		fn:   fn,
		done: make(chan error, 1),
	}
	select {
	case w.ch <- op:
		return <-op.done
	default:
		return fmt.Errorf("%w: queue full", ErrQueueFull)
	}
}

// Run starts the serial write loop. Call in a goroutine:
//
//	go writer.Run()
func (w *Writer) Run() {
	for op := range w.ch {
		err := w.exec(op.fn)
		op.done <- err
	}
	close(w.done)
}

// Close stops the writer after queued operations drain. Blocks until the
// goroutine exits.
func (w *Writer) Close() {
	close(w.ch)
	<-w.done
}

func (w *Writer) exec(fn func(tx *sql.Tx) error) error {
	tx, err := w.db.Begin()
	if err != nil {
		return fmt.Errorf("store: writer begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

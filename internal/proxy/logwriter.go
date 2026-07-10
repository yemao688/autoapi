// logwriter.go implements an asynchronous, batched request-log writer for the
// proxy. It reduces per-request latency by moving SQLite inserts out of the
// request path and into a background goroutine that flushes either when a
// batch size threshold is reached or a timer expires.
//
// The writer supports two operations:
//   - Enqueue: inserts a new log row (used for pending rows at request start).
//   - EnqueueUpdate: updates an existing log row by ID (used when a request
//     completes and the final status/tokens/cost/chain are known).
//
// Both operations are batched and flushed independently on the same goroutine.
package proxy

import (
	"log/slog"
	"sync"
	"time"

	"autoapi/internal/model"
)

const (
	logWriterBatchSize     = 100
	logWriterFlushInterval = 1 * time.Second
	logWriterQueueSize     = 1000
)

// logWriter accepts request log entries and flushes them in batches to the
// store. Enqueue/EnqueueUpdate are non-blocking; a full queue causes the log
// to be dropped.
type logWriter struct {
	store         storeProxy
	ch            chan model.RequestLog // insert channel
	updateCh      chan model.RequestLog // update channel
	stopCh        chan struct{}
	doneCh        chan struct{}
	mu            sync.Mutex
	stopped       bool
	batchSize     int
	flushInterval time.Duration
	wg            sync.WaitGroup

	// onFlush is an optional callback fired after each successful batch flush
	// (inserts or updates). It runs on the writer goroutine; the API layer
	// uses it to emit real-time UI events when new logs are persisted.
	// Guard with muFlush when reading/writing.
	muFlush sync.Mutex
	onFlush func()
}

// newLogWriter starts a background log writer. The caller must call Stop to
// release the goroutine and flush any pending logs.
func newLogWriter(s storeProxy) *logWriter {
	w := &logWriter{
		store:         s,
		ch:            make(chan model.RequestLog, logWriterQueueSize),
		updateCh:      make(chan model.RequestLog, logWriterQueueSize),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		batchSize:     logWriterBatchSize,
		flushInterval: logWriterFlushInterval,
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

// Enqueue adds a log entry to the background insert queue. It returns false
// if the queue is full or the writer has been stopped.
func (w *logWriter) Enqueue(l model.RequestLog) bool {
	w.mu.Lock()
	stopped := w.stopped
	w.mu.Unlock()
	if stopped {
		return false
	}
	select {
	case w.ch <- l:
		return true
	default:
		return false
	}
}

// EnqueueUpdate adds a log entry to the background update queue. The entry's
// ID must match a previously-inserted row. Returns false if the queue is full
// or the writer has been stopped.
func (w *logWriter) EnqueueUpdate(l model.RequestLog) bool {
	w.mu.Lock()
	stopped := w.stopped
	w.mu.Unlock()
	if stopped {
		return false
	}
	select {
	case w.updateCh <- l:
		return true
	default:
		return false
	}
}

// Stop gracefully shuts down the writer, flushing any buffered logs before
// returning.
func (w *logWriter) Stop() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.stopped = true
	close(w.stopCh)
	w.mu.Unlock()
	w.wg.Wait()
}

func (w *logWriter) loop() {
	defer w.wg.Done()
	defer close(w.doneCh)

	insertBatch := make([]model.RequestLog, 0, w.batchSize)
	updateBatch := make([]model.RequestLog, 0, w.batchSize)
	timer := time.NewTimer(w.flushInterval)
	defer timer.Stop()

	fireCallback := func() {
		w.muFlush.Lock()
		cb := w.onFlush
		w.muFlush.Unlock()
		if cb != nil {
			cb()
		}
	}

	flushInserts := func() {
		if len(insertBatch) == 0 {
			return
		}
		if err := w.store.InsertRequestLogsBatch(insertBatch); err != nil {
			slog.Error("proxy: log batch insert failed", "err", err)
		} else {
			slog.Debug("proxy: log batch inserted", "count", len(insertBatch))
			fireCallback()
		}
		insertBatch = insertBatch[:0]
	}

	flushUpdates := func() {
		if len(updateBatch) == 0 {
			return
		}
		if err := w.store.UpdateRequestLogsBatch(updateBatch); err != nil {
			slog.Error("proxy: log batch update failed", "err", err)
		} else {
			slog.Debug("proxy: log batch updated", "count", len(updateBatch))
			fireCallback()
		}
		updateBatch = updateBatch[:0]
	}

	flushAll := func() {
		flushInserts()
		flushUpdates()
	}

	flushAllAndReset := func() {
		flushAll()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(w.flushInterval)
	}

	for {
		select {
		case l := <-w.ch:
			insertBatch = append(insertBatch, l)
			if len(insertBatch) >= w.batchSize {
				flushAllAndReset()
			}
		case l := <-w.updateCh:
			updateBatch = append(updateBatch, l)
			if len(updateBatch) >= w.batchSize {
				flushAllAndReset()
			}
		case <-timer.C:
			flushAll()
			timer.Reset(w.flushInterval)
		case <-w.stopCh:
			// Drain any logs that were already enqueued before the stop
			// signal and flush them before exiting.
			for {
				select {
				case l := <-w.ch:
					insertBatch = append(insertBatch, l)
				case l := <-w.updateCh:
					updateBatch = append(updateBatch, l)
				default:
					flushAll()
					return
				}
			}
		}
	}
}

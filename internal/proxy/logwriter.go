// logwriter.go implements an asynchronous, batched request-log writer for the
// proxy. It reduces per-request latency by moving SQLite inserts out of the
// request path and into a background goroutine that flushes either when a
// batch size threshold is reached or a timer expires.
package proxy

import (
	"sync"
	"time"

	"autoapi/internal/model"
	"log/slog"
)

const (
	logWriterBatchSize = 100
	logWriterFlushInterval = 1 * time.Second
	logWriterQueueSize = 1000
)

// logWriter accepts request log entries and flushes them in batches to the
// store. Enqueue is non-blocking; a full queue causes the log to be dropped.
type logWriter struct {
	store         storeProxy
	ch            chan model.RequestLog
	stopCh        chan struct{}
	doneCh        chan struct{}
	mu            sync.Mutex
	stopped       bool
	batchSize     int
	flushInterval time.Duration
	wg            sync.WaitGroup

	// onFlush is an optional callback fired after each successful batch flush.
	// It runs on the writer goroutine; the API layer uses it to emit real-time
	// UI events when new logs are persisted. Guard with mu when reading/writing.
	muFlush  sync.Mutex
	onFlush  func()
}

// newLogWriter starts a background log writer. The caller must call Stop to
// release the goroutine and flush any pending logs.
func newLogWriter(s storeProxy) *logWriter {
	w := &logWriter{
		store:         s,
		ch:            make(chan model.RequestLog, logWriterQueueSize),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
		batchSize:     logWriterBatchSize,
		flushInterval: logWriterFlushInterval,
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

// Enqueue adds a log entry to the background queue. It returns false if the
// queue is full or the writer has been stopped.
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

	batch := make([]model.RequestLog, 0, w.batchSize)
	timer := time.NewTimer(w.flushInterval)
	defer timer.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := w.store.InsertRequestLogsBatch(batch); err != nil {
			slog.Error("proxy: failed to flush request logs",
				"err", err,
				"count", len(batch))
		} else {
			// Fire the real-time UI event after a successful batch write.
			// Read under muFlush so a concurrent OnLogFlush swap doesn't race
			// with the in-progress callback dispatch.
			w.muFlush.Lock()
			cb := w.onFlush
			w.muFlush.Unlock()
			if cb != nil {
				cb()
			}
		}
		batch = batch[:0]
	}

	flushAndReset := func() {
		flush()
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
			batch = append(batch, l)
			if len(batch) >= w.batchSize {
				flushAndReset()
			}
		case <-timer.C:
			flush()
			timer.Reset(w.flushInterval)
		case <-w.stopCh:
			// Drain any logs that were already enqueued before the stop signal
			// and flush them before exiting.
			for {
				select {
				case l := <-w.ch:
					batch = append(batch, l)
				default:
					flush()
					return
				}
			}
		}
	}
}

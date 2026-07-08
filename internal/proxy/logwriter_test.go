package proxy

import (
	"sync"
	"testing"
	"time"

	"autoapi/internal/model"
)

type fakeBatchStore struct {
	mu   sync.Mutex
	logs []model.RequestLog
}

func (f *fakeBatchStore) ListProviders() ([]model.Provider, error)            { return nil, nil }
func (f *fakeBatchStore) ListModelRules() ([]model.ModelRule, error)            { return nil, nil }
func (f *fakeBatchStore) GetProvider(id string) (*model.Provider, error)       { return nil, nil }
func (f *fakeBatchStore) ListAPIKeys() ([]model.ApiKey, error)                 { return nil, nil }
func (f *fakeBatchStore) GetProviderKeyCiphertext(providerID string) (ciphertext, nonce []byte, err error) {
	return nil, nil, nil
}
func (f *fakeBatchStore) InsertRequestLog(l model.RequestLog) error { return nil }
func (f *fakeBatchStore) InsertRequestLogsBatch(logs []model.RequestLog) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, logs...)
	return nil
}
func (f *fakeBatchStore) ListModels(providerID string) ([]model.Model, error) { return nil, nil }
func (f *fakeBatchStore) GetSettings() (*model.Settings, error)               { return &model.Settings{}, nil }
func (f *fakeBatchStore) Dashboard() (*model.DashboardData, error)             { return &model.DashboardData{}, nil }
func (f *fakeBatchStore) UpdateProviderHealth(id string, status model.ProviderStatus, errorMessage string) error {
	return nil
}
func (f *fakeBatchStore) IncrementTargetStats(targetID string, hitDelta, failDelta int64) error {
	return nil
}

func (f *fakeBatchStore) logCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.logs)
}

func TestLogWriter_BatchFlush(t *testing.T) {
	st := &fakeBatchStore{}
	w := newLogWriter(st)
	defer w.Stop()

	for i := 0; i < 5; i++ {
		if !w.Enqueue(model.RequestLog{ID: "id", APIKeyID: "k1"}) {
			t.Fatal("failed to enqueue log")
		}
	}

	// Wait for the flush interval to elapse.
	time.Sleep(2 * logWriterFlushInterval)

	if st.logCount() != 5 {
		t.Fatalf("expected 5 logs flushed, got %d", st.logCount())
	}
}

func TestLogWriter_StopFlushesPending(t *testing.T) {
	st := &fakeBatchStore{}
	w := newLogWriter(st)

	for i := 0; i < 3; i++ {
		if !w.Enqueue(model.RequestLog{ID: "id", APIKeyID: "k1"}) {
			t.Fatal("failed to enqueue log")
		}
	}
	w.Stop()

	if st.logCount() != 3 {
		t.Fatalf("expected 3 logs flushed on stop, got %d", st.logCount())
	}
}

// TestLogWriter_OnFlush verifies that the OnFlush callback is invoked after
// each successful batch insert. The callback is the hook the api layer uses
// to emit a debounced "log:new" Wails event to the frontend.
func TestLogWriter_OnFlush(t *testing.T) {
	st := &fakeBatchStore{}
	w := newLogWriter(st)
	defer w.Stop()

	var (
		mu    sync.Mutex
		hits  int
	)
	// Simulate the api.App.wireLogEventEmitter shape: a goroutine-safe counter
	// that bumps every time the writer flushes a batch.
	OnLogFlushLike(w, func() {
		mu.Lock()
		hits++
		mu.Unlock()
	})

	for i := 0; i < 5; i++ {
		if !w.Enqueue(model.RequestLog{ID: "id", APIKeyID: "k1"}) {
			t.Fatal("failed to enqueue log")
		}
	}

	// Wait for the periodic flush to elapse.
	time.Sleep(2 * logWriterFlushInterval)

	mu.Lock()
	got := hits
	mu.Unlock()
	if got < 1 {
		t.Fatalf("expected onFlush to fire at least once, got %d", got)
	}
	if st.logCount() != 5 {
		t.Fatalf("expected 5 logs flushed, got %d", st.logCount())
	}
}

// OnLogFlushLike is a tiny test helper that registers fn as the logWriter's
// post-flush callback. It mirrors the api-layer call site (proxy.Proxy.OnLogFlush
// is a method; the underlying field is private to the package, so the test
// pokes it directly inside the proxy package).
func OnLogFlushLike(w *logWriter, fn func()) {
	w.muFlush.Lock()
	w.onFlush = fn
	w.muFlush.Unlock()
}

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
func (f *fakeBatchStore) ListRoutes() ([]model.Route, error)                   { return nil, nil }
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

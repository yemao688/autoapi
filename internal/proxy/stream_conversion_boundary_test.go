package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"autoapi/internal/model"
)

var streamPoisonErr = errors.New("stream poison conversion error")

type poisonStreamConverter struct {
	failOn int
	calls  int
}

func (c *poisonStreamConverter) Write([]byte) ([]byte, error) {
	c.calls++
	if c.failOn > 0 && c.calls == c.failOn {
		return []byte("poison-bytes"), streamPoisonErr
	}
	return []byte("valid-bytes"), nil
}

func (c *poisonStreamConverter) Close() ([]byte, error) { return nil, nil }

func splitChunkUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "a")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(2 * time.Millisecond)
		_, _ = io.WriteString(w, "b")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
}

func TestStreamConversionFirstWriteErrorDiscardsReturnedBytes(t *testing.T) {
	srv := splitChunkUpstream(t)
	defer srv.Close()
	spy := &metricSpy{}
	p := New(&mockStore{}, &mockService{}, 0, nil)
	p.metricSink = spy
	converter := &poisonStreamConverter{failOn: 1}
	c := candidate{targetID: "t", provider: &model.Provider{ID: "p", Name: "P"}, modelName: "m", protocol: ProtocolOpenAIChat, firstByteBudget: time.Second}
	prep := AttemptPreparation{Body: []byte(`{"model":"m"}`), Path: "/v1/chat/completions", NewStreamConverter: func() StreamConverter { return converter }}
	upstreamURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	rec := httptest.NewRecorder()
	logEntry := &model.RequestLog{}
	result, _ := p.streamAttempt(context.Background(), rec, req, c, "key", prep.Body, upstreamURL, prep, 0, 0, logEntry)
	if result.Status != model.AttemptOutcomeConversionError || result.Committed || result.StreamErr != streamPoisonErr {
		t.Fatalf("first conversion error result=%+v", result)
	}
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Fatalf("poison bytes/header committed: status=%d headers=%v body=%q", rec.Code, rec.Header(), rec.Body.String())
	}
	if events := spy.Events(); len(events) != 1 || events[0].AttemptOutcome != model.AttemptOutcomeConversionError {
		t.Fatalf("metric events=%+v", events)
	}
}

func TestStreamConversionLaterWriteErrorPreservesOnlyPriorBytes(t *testing.T) {
	srv := splitChunkUpstream(t)
	defer srv.Close()
	p := New(&mockStore{}, &mockService{}, 0, nil)
	converter := &poisonStreamConverter{failOn: 2}
	c := candidate{targetID: "t", provider: &model.Provider{ID: "p", Name: "P"}, modelName: "m", protocol: ProtocolOpenAIChat, firstByteBudget: time.Second}
	prep := AttemptPreparation{Body: []byte(`{"model":"m"}`), Path: "/v1/chat/completions", NewStreamConverter: func() StreamConverter { return converter }}
	upstreamURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	rec := httptest.NewRecorder()
	result, _ := p.streamAttempt(context.Background(), rec, req, c, "key", prep.Body, upstreamURL, prep, 0, 0, &model.RequestLog{})
	if result.Status != model.AttemptOutcomeConversionError || !result.Committed || result.StreamErr != streamPoisonErr {
		t.Fatalf("later conversion error result=%+v", result)
	}
	if rec.Body.String() != "valid-bytes" || strings.Contains(rec.Body.String(), "poison-bytes") {
		t.Fatalf("committed body=%q", rec.Body.String())
	}
}

func TestConvertStreamChunkDiscardsOutputWithError(t *testing.T) {
	out, err := convertStreamChunk(&poisonStreamConverter{failOn: 1}, []byte("input"))
	if !errors.Is(err, streamPoisonErr) || out != nil {
		t.Fatalf("output=%q err=%v, want nil output and sentinel error", out, err)
	}
}

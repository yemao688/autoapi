package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/store"
)

func TestParseChatNonStreamContentForms(t *testing.T) {
	s := &Service{}
	start := time.Now()
	t.Run("string content", func(t *testing.T) {
		result := s.parseChatNonStream([]byte(`{"choices":[{"message":{"content":" ok "},"finish_reason":"stop"}]}`), start)
		if !result.OK || result.Response != " ok " || result.FinishReason != "stop" {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("array content", func(t *testing.T) {
		result := s.parseChatNonStream([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"hello"},{"type":"text","text":" world"}]},"finish_reason":"stop"}]}`), start)
		if !result.OK || result.Response != "hello world" {
			t.Fatalf("result=%+v", result)
		}
	})
	t.Run("length without visible output is diagnostic", func(t *testing.T) {
		result := s.parseChatNonStream([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`), start)
		if result.OK || result.FinishReason != "length" || !strings.Contains(result.Error, "max_tokens") || !strings.Contains(result.Error, "reasoning") {
			t.Fatalf("result=%+v", result)
		}
	})
}

func TestParseChatStreamReasoningAndLength(t *testing.T) {
	s := &Service{}
	start := time.Now()
	stream := "data: {\"choices\":[{\"delta\":{\"reasoning\":\"hidden\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n" +
		"data: [DONE]\n\n"
	result := s.parseChatStream(strings.NewReader(stream), start)
	if result.OK || result.FinishReason != "length" || !strings.Contains(result.Error, "max_tokens") || !strings.Contains(result.Error, "reasoning") {
		t.Fatalf("result=%+v", result)
	}
}

func TestParseResponsesStreamDelta(t *testing.T) {
	s := &Service{}
	result := s.parseGenericStream(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"+"data: {\"delta\":\" world\"}\n\n"+"data: [DONE]\n\n"), time.Now())
	if !result.OK || result.Response != "hello world" {
		t.Fatalf("result=%+v", result)
	}
}

type delayedChunks struct {
	chunks [][]byte
	delay  time.Duration
	i      int
}

func (r *delayedChunks) Read(p []byte) (int, error) {
	if r.i >= len(r.chunks) {
		return 0, io.EOF
	}
	if r.i > 0 {
		time.Sleep(r.delay)
	}
	n := copy(p, r.chunks[r.i])
	r.i++
	return n, nil
}

func TestStreamFirstByteLatencyWaitsForVisibleContent(t *testing.T) {
	delay := 25 * time.Millisecond
	start := time.Now()
	chat := &delayedChunks{delay: delay, chunks: [][]byte{
		[]byte("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"),
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}}
	chatResult := (&Service{}).parseChatStream(chat, start)
	if !chatResult.OK || chatResult.Response != "hello" || chatResult.FirstByteLatencyMs < int(delay/time.Millisecond) {
		t.Fatalf("chat result=%+v", chatResult)
	}

	start = time.Now()
	generic := &delayedChunks{delay: delay, chunks: [][]byte{
		[]byte("data: {\"type\":\"response.created\"}\n\n"),
		[]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"),
		[]byte("data: [DONE]\n\n"),
	}}
	genericResult := (&Service{}).parseGenericStream(generic, start)
	if !genericResult.OK || genericResult.Response != "hello" || genericResult.FirstByteLatencyMs < int(delay/time.Millisecond) {
		t.Fatalf("generic result=%+v", genericResult)
	}
}

func TestMessagesModelTestRequestShapeAndHeaders(t *testing.T) {
	var got map[string]interface{}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got)
		if r.Header.Get("x-api-key") != "secret" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing Anthropic headers: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		if got["model"] == "rate-limited" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"hello"}]}`))
	}))
	defer srv.Close()
	st, err := store.New(t.Context(), store.StoreDeps{DSN: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, nil, t.TempDir())
	p, err := st.CreateProvider(model.ProviderInput{Name: "Anthropic", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	ct, nonce, err := svc.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateProviderKeyCiphertext(p.ID, ct, nonce, "****"); err != nil {
		t.Fatal(err)
	}
	result, err := svc.TestModelChat(p.ID, "claude", "messages", false, "")
	if err != nil || !result.OK || result.HTTPStatus != http.StatusOK || path != "/v1/messages" || got["model"] != "claude" {
		t.Fatalf("result=%+v err=%v path=%s body=%v", result, err, path, got)
	}
	rateResult, err := svc.TestModelChat(p.ID, "rate-limited", "messages", false, "")
	if err != nil || rateResult.OK || rateResult.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("rate result=%+v err=%v", rateResult, err)
	}
}

func TestUpstreamMonitorListAndBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/v1/messages" {
			_, _ = w.Write([]byte("data: {\"content\":[{\"type\":\"text\",\"text\":\"message ok\"}]}\n\ndata: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"chat ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	st, err := store.New(t.Context(), store.StoreDeps{DSN: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, nil, t.TempDir())
	p, err := st.CreateProvider(model.ProviderInput{Name: "Monitor", BaseURL: srv.URL, MessagesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	key, nonce, err := svc.Encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateProviderKeyCiphertext(p.ID, key, nonce, "****"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AddProviderModels(p.ID, []string{"chat-model", "message-model"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProviderCapability(p.ID, "anthropic_messages", "native", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProviderCapability(p.ID, "openai_chat", "native", false); err != nil {
		t.Fatal(err)
	}
	if err := st.SetModelCapability(p.ID, "chat-model", "anthropic_messages", "native", false); err != nil {
		t.Fatal(err)
	}

	listed, err := svc.ListUpstreamMonitorModels()
	if err != nil || len(listed) != 1 || listed[0].ModelName != "message-model" || listed[0].Protocol != "messages" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	batch, err := svc.ProbeUpstreamMonitorModels([]model.UpstreamMonitorSelection{{ProviderID: p.ID, ModelName: "message-model", Protocol: "messages"}})
	if err != nil || batch.Total != 1 || batch.Available != 1 || batch.Results[0].Status != "available" || batch.Results[0].HTTPStatus != http.StatusOK || batch.Results[0].Response != "message ok" {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	single, err := svc.ProbeUpstreamMonitorModel(model.UpstreamMonitorSelection{ProviderID: p.ID, ModelName: "message-model", Protocol: "messages"})
	if err != nil || single.Status != "available" || single.HTTPStatus != http.StatusOK || single.Response != "message ok" {
		t.Fatalf("single=%+v err=%v", single, err)
	}
}

package service

import (
	"encoding/json"
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
	if err != nil || !result.OK || path != "/v1/messages" || got["model"] != "claude" {
		t.Fatalf("result=%+v err=%v path=%s body=%v", result, err, path, got)
	}
}

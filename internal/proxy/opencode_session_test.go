package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"autoapi/internal/model"
)

func TestConversationAnchorIsStableAcrossTurns(t *testing.T) {
	turn1 := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"}]}`)
	turn2 := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	if a, b := conversationAnchor(ProtocolOpenAIChat, turn1), conversationAnchor(ProtocolOpenAIChat, turn2); a == "" || a != b {
		t.Fatalf("chat anchor changed across turns: %q vs %q", a, b)
	}
	other := []byte(`{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"different"}]}`)
	if a, b := conversationAnchor(ProtocolOpenAIChat, turn1), conversationAnchor(ProtocolOpenAIChat, other); a == b {
		t.Fatalf("chat anchor did not distinguish conversations: %q", a)
	}
}

func TestConversationAnchorProtocols(t *testing.T) {
	cases := []struct {
		name     string
		protocol Protocol
		first    string
		second   string
	}{
		{
			name:     "responses",
			protocol: ProtocolOpenAIResponses,
			first:    `{"model":"m","instructions":"sys","input":[{"type":"message","role":"user","content":"hello"}]}`,
			second:   `{"model":"m","instructions":"sys","input":[{"type":"message","role":"user","content":"hello"},{"type":"message","role":"assistant","content":"hi"},{"type":"message","role":"user","content":"again"}]}`,
		},
		{
			name:     "messages",
			protocol: ProtocolAnthropicMessages,
			first:    `{"model":"m","system":"sys","messages":[{"role":"user","content":"hello"}]}`,
			second:   `{"model":"m","system":"sys","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`,
		},
		{
			name:     "gemini",
			protocol: ProtocolGemini,
			first:    `{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
			second:   `{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hi"}]},{"role":"user","parts":[{"text":"again"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := conversationAnchor(tc.protocol, []byte(tc.first))
			b := conversationAnchor(tc.protocol, []byte(tc.second))
			if a == "" || a != b {
				t.Fatalf("anchor changed across turns: %q vs %q", a, b)
			}
		})
	}
}

func TestConversationAnchorRequiresUserContent(t *testing.T) {
	cases := []struct {
		protocol Protocol
		body     string
	}{
		{ProtocolOpenAIChat, `{"model":"m","messages":[{"role":"system","content":"sys"}]}`},
		{ProtocolOpenAIResponses, `{"model":"m","instructions":"sys","input":[]}`},
		{ProtocolAnthropicMessages, `{"model":"m","messages":[{"role":"assistant","content":"hi"}]}`},
		{ProtocolGemini, `{"contents":[{"role":"model","parts":[{"text":"hi"}]}]}`},
	}
	for _, tc := range cases {
		if got := conversationAnchor(tc.protocol, []byte(tc.body)); got != "" {
			t.Fatalf("%s anchor = %q, want empty", tc.protocol, got)
		}
	}
}

func TestOpenCodeSessionIDPartitionsKeysAndConversations(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	other := []byte(`{"model":"m","messages":[{"role":"user","content":"other"}]}`)
	base := openCodeSessionID("key1", ProtocolOpenAIChat, body)
	if base == "" || !strings.HasPrefix(base, "autoapi-") {
		t.Fatalf("session id = %q", base)
	}
	if again := openCodeSessionID("key1", ProtocolOpenAIChat, body); again != base {
		t.Fatalf("session id not stable: %q vs %q", base, again)
	}
	if otherKey := openCodeSessionID("key2", ProtocolOpenAIChat, body); otherKey == base {
		t.Fatalf("session id did not partition api keys: %q", base)
	}
	if otherConv := openCodeSessionID("key1", ProtocolOpenAIChat, other); otherConv == base {
		t.Fatalf("session id did not partition conversations: %q", base)
	}
}

func TestMaybeInjectOpenCodeSession(t *testing.T) {
	openCode := &model.Provider{BaseURL: "https://opencode.ai/inference/go/openai/v1"}
	other := &model.Provider{BaseURL: "https://api.example.com/v1"}
	cases := []struct {
		name      string
		provider  *model.Provider
		sessionID string
		headers   map[string]string
		want      string
	}{
		{name: "injects when missing", provider: openCode, sessionID: "autoapi-abc", want: "autoapi-abc"},
		{name: "keeps client x-opencode-session", provider: openCode, sessionID: "autoapi-abc", headers: map[string]string{"x-opencode-session": "client-session"}, want: "client-session"},
		{name: "keeps native session header", provider: openCode, sessionID: "autoapi-abc", headers: map[string]string{"x-session-affinity": "native-session"}, want: ""},
		{name: "skips other providers", provider: other, sessionID: "autoapi-abc", want: ""},
		{name: "skips empty session id", provider: openCode, sessionID: "", want: ""},
		{name: "skips nil provider", provider: nil, sessionID: "autoapi-abc", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tc.headers {
				h.Set(k, v)
			}
			maybeInjectOpenCodeSession(h, tc.provider, tc.sessionID)
			if got := h.Get("x-opencode-session"); got != tc.want {
				t.Fatalf("x-opencode-session = %q, want %q", got, tc.want)
			}
		})
	}
}

// sessionCaptureTransport records every upstream request and answers with a
// minimal chat completion (or SSE when the request asks for a stream).
type sessionCaptureTransport struct {
	mu      sync.Mutex
	headers []http.Header
}

func (t *sessionCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.headers = append(t.headers, req.Header.Clone())
	t.mu.Unlock()

	body, _ := io.ReadAll(req.Body)
	stream := bytes.Contains(body, []byte(`"stream":true`))
	contentType := "application/json"
	responseBody := `{"id":"chatcmpl","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	if stream {
		contentType = "text/event-stream"
		responseBody = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(responseBody)),
		Request:    req,
	}, nil
}

func (s *sessionCaptureTransport) lastHeader(t *testing.T) http.Header {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.headers) == 0 {
		t.Fatal("no upstream request captured")
	}
	return s.headers[len(s.headers)-1]
}

func TestProxyInjectsOpenCodeSessionOnChatAndStream(t *testing.T) {
	st := &mockStore{
		providers: map[string]*model.Provider{
			"p": {ID: "p", Name: "OpenCode Go", BaseURL: "https://opencode.ai/inference/go/openai/v1", Enabled: true},
		},
		rules: []model.ModelRule{{
			ID: "r", Name: "x", Enabled: true,
			Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}},
		}},
		apiKeys: []model.ApiKey{{ID: "key1", Enabled: true}, {ID: "key2", Enabled: true}},
	}
	p := New(st, &mockService{}, 0, func() (*model.Settings, error) { return &model.Settings{}, nil })
	defer func() { _ = p.Shutdown() }()
	tr := &sessionCaptureTransport{}
	p.transport = tr

	request := func(t *testing.T, apiKey, body string, headers map[string]string) *http.Response {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		p.router.ServeHTTP(rec, req)
		return rec.Result()
	}

	t.Run("non-stream first turn", func(t *testing.T) {
		resp := request(t, "key1", `{"model":"x","messages":[{"role":"user","content":"hello"}]}`, nil)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		first := tr.lastHeader(t).Get("x-opencode-session")
		if !strings.HasPrefix(first, "autoapi-") {
			t.Fatalf("x-opencode-session = %q", first)
		}

		resp = request(t, "key1", `{"model":"x","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`, nil)
		defer func() { _ = resp.Body.Close() }()
		if got := tr.lastHeader(t).Get("x-opencode-session"); got != first {
			t.Fatalf("session changed across turns: %q vs %q", first, got)
		}

		resp = request(t, "key2", `{"model":"x","messages":[{"role":"user","content":"hello"}]}`, nil)
		defer func() { _ = resp.Body.Close() }()
		if got := tr.lastHeader(t).Get("x-opencode-session"); got == first {
			t.Fatalf("session did not partition api keys: %q", got)
		}
	})

	t.Run("client header wins", func(t *testing.T) {
		resp := request(t, "key1", `{"model":"x","messages":[{"role":"user","content":"hello"}]}`, map[string]string{"x-opencode-session": "client-session"})
		defer func() { _ = resp.Body.Close() }()
		if got := tr.lastHeader(t).Get("x-opencode-session"); got != "client-session" {
			t.Fatalf("x-opencode-session = %q, want client-session", got)
		}
	})

	t.Run("stream path", func(t *testing.T) {
		resp := request(t, "key1", `{"model":"x","stream":true,"messages":[{"role":"user","content":"hello"}]}`, nil)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		if got := tr.lastHeader(t).Get("x-opencode-session"); !strings.HasPrefix(got, "autoapi-") {
			t.Fatalf("x-opencode-session = %q", got)
		}
	})
}

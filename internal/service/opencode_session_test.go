package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"autoapi/internal/model"
	"autoapi/internal/store"
)

// captureTransport records the outgoing request and returns a successful
// minimal chat completion, so request-shaping tests never touch the network.
type captureTransport struct {
	req *http.Request
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.req = req
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)),
		Request:    req,
	}, nil
}

func newModelTestService(t *testing.T, baseURL string) (*Service, string) {
	t.Helper()

	st, err := store.New(t.Context(), store.StoreDeps{DSN: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := New(st, nil, t.TempDir())
	p, err := st.CreateProvider(model.ProviderInput{Name: "test", BaseURL: baseURL})
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
	return svc, p.ID
}

func TestModelChatSendsOpenCodeSessionHeader(t *testing.T) {
	t.Run("opencode go uses the caller session id", func(t *testing.T) {
		svc, providerID := newModelTestService(t, "https://opencode.ai/inference/go/openai/v1")
		tr := &captureTransport{}
		svc.httpClient = &http.Client{Transport: tr}

		result, err := svc.TestModelChat(providerID, "deepseek-v4.1-flash", "chat", false, "test-session-42")
		if err != nil {
			t.Fatal(err)
		}
		if !result.OK {
			t.Fatalf("result=%+v", result)
		}
		if got := tr.req.Header.Get("x-opencode-session"); got != "test-session-42" {
			t.Fatalf("x-opencode-session = %q, want %q", got, "test-session-42")
		}
	})

	t.Run("empty test id still sends a non-empty session id", func(t *testing.T) {
		svc, providerID := newModelTestService(t, "https://opencode.ai/inference/go/openai/v1")
		tr := &captureTransport{}
		svc.httpClient = &http.Client{Transport: tr}

		result, err := svc.TestModelChat(providerID, "deepseek-v4.1-flash", "chat", false, "")
		if err != nil {
			t.Fatal(err)
		}
		if !result.OK {
			t.Fatalf("result=%+v", result)
		}
		if got := tr.req.Header.Get("x-opencode-session"); got == "" {
			t.Fatal("x-opencode-session is empty")
		}
	})

	t.Run("other providers do not receive the header", func(t *testing.T) {
		svc, providerID := newModelTestService(t, "https://api.example.com/v1")
		tr := &captureTransport{}
		svc.httpClient = &http.Client{Transport: tr}

		result, err := svc.TestModelChat(providerID, "model", "chat", false, "test-session-42")
		if err != nil {
			t.Fatal(err)
		}
		if !result.OK {
			t.Fatalf("result=%+v", result)
		}
		if got := tr.req.Header.Get("x-opencode-session"); got != "" {
			t.Fatalf("unexpected x-opencode-session = %q", got)
		}
	})
}

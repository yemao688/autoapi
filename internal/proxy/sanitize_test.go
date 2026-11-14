package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func sanitizeWrap(msgs ...string) string {
	return `{"model":"m","stream":true,"messages":[` + strings.Join(msgs, ",") + `],"temperature":0.5}`
}

func sanitizeRoles(t *testing.T, body []byte) []string {
	t.Helper()
	var doc struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal sanitized body: %v", err)
	}
	roles := make([]string, 0, len(doc.Messages))
	for _, m := range doc.Messages {
		roles = append(roles, m.Role)
	}
	return roles
}

func TestSanitizeChatMessages(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantRemoved int
		wantRoles   []string // nil means "expect unchanged body"
	}{
		{
			name:        "null content removed",
			body:        sanitizeWrap(`{"role":"user","content":"hi"}`, `{"role":"assistant","content":null}`, `{"role":"user","content":"go on"}`),
			wantRemoved: 1,
			wantRoles:   []string{"user", "user"},
		},
		{
			name:        "empty string removed",
			body:        sanitizeWrap(`{"role":"user","content":"hi"}`, `{"role":"assistant","content":""}`),
			wantRemoved: 1,
			wantRoles:   []string{"user"},
		},
		{
			name:        "whitespace string removed",
			body:        sanitizeWrap(`{"role":"assistant","content":"   "}`, `{"role":"user","content":"hi"}`),
			wantRemoved: 1,
			wantRoles:   []string{"user"},
		},
		{
			name:        "missing content removed",
			body:        sanitizeWrap(`{"role":"assistant"}`, `{"role":"user","content":"hi"}`),
			wantRemoved: 1,
			wantRoles:   []string{"user"},
		},
		{
			name:        "empty array content removed",
			body:        sanitizeWrap(`{"role":"assistant","content":[]}`, `{"role":"user","content":"hi"}`),
			wantRemoved: 1,
			wantRoles:   []string{"user"},
		},
		{
			name:        "all-blank text parts removed",
			body:        sanitizeWrap(`{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":"  "}]}`, `{"role":"user","content":"hi"}`),
			wantRemoved: 1,
			wantRoles:   []string{"user"},
		},
		{
			name:        "empty tool_calls array still removed",
			body:        sanitizeWrap(`{"role":"assistant","content":null,"tool_calls":[]}`, `{"role":"user","content":"hi"}`),
			wantRemoved: 1,
			wantRoles:   []string{"user"},
		},
		{
			name:        "assistant with tool_calls kept despite null content",
			body:        sanitizeWrap(`{"role":"user","content":"hi"}`, `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]}`, `{"role":"tool","tool_call_id":"call_1","content":"ok"}`),
			wantRemoved: 0,
			wantRoles:   []string{"user", "assistant", "tool"},
		},
		{
			name:        "assistant with tool_calls kept despite empty string content",
			body:        sanitizeWrap(`{"role":"assistant","content":"","tool_calls":[{"id":"call_1"}]}`),
			wantRemoved: 0,
			wantRoles:   []string{"assistant"},
		},
		{
			name:        "non-blank text part kept",
			body:        sanitizeWrap(`{"role":"assistant","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]}`),
			wantRemoved: 0,
			wantRoles:   []string{"assistant"},
		},
		{
			name:        "non-text part kept",
			body:        sanitizeWrap(`{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"u"}}]}`),
			wantRemoved: 0,
			wantRoles:   []string{"assistant"},
		},
		{
			name:        "unknown content shape kept",
			body:        sanitizeWrap(`{"role":"assistant","content":42}`),
			wantRemoved: 0,
			wantRoles:   []string{"assistant"},
		},
		{
			name:        "empty user message untouched",
			body:        sanitizeWrap(`{"role":"user","content":""}`, `{"role":"assistant","content":"hi"}`),
			wantRemoved: 0,
			wantRoles:   []string{"user", "assistant"},
		},
		{
			name:        "multiple removals preserve order",
			body:        sanitizeWrap(`{"role":"system","content":"s"}`, `{"role":"assistant","content":""}`, `{"role":"user","content":"a"}`, `{"role":"assistant","content":null}`, `{"role":"user","content":"b"}`),
			wantRemoved: 2,
			wantRoles:   []string{"system", "user", "user"},
		},
		{
			name:        "no empty messages leaves body unchanged",
			body:        sanitizeWrap(`{"role":"user","content":"hi"}`, `{"role":"assistant","content":"hello"}`),
			wantRemoved: 0,
			wantRoles:   nil,
		},
		{
			name:        "no messages field passes through",
			body:        `{"model":"m","input":[]}`,
			wantRemoved: 0,
			wantRoles:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, removed, err := sanitizeChatMessages([]byte(tc.body))
			if err != nil {
				t.Fatalf("sanitizeChatMessages: %v", err)
			}
			if removed != tc.wantRemoved {
				t.Fatalf("removed=%d, want %d", removed, tc.wantRemoved)
			}
			if tc.wantRoles == nil {
				if string(out) != tc.body {
					t.Fatalf("body changed unexpectedly:\n got: %s\nwant: %s", out, tc.body)
				}
				return
			}
			roles := sanitizeRoles(t, out)
			if len(roles) != len(tc.wantRoles) {
				t.Fatalf("roles=%v, want %v (body %s)", roles, tc.wantRoles, out)
			}
			for i := range roles {
				if roles[i] != tc.wantRoles[i] {
					t.Fatalf("roles=%v, want %v", roles, tc.wantRoles)
				}
			}
			// Other top-level fields must survive.
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatal(err)
			}
			if _, ok := doc["temperature"]; !ok {
				t.Fatalf("top-level fields lost in %s", out)
			}
		})
	}
}

func TestSanitizeChatMessagesInvalidJSON(t *testing.T) {
	if _, _, err := sanitizeChatMessages([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if _, _, err := sanitizeChatMessages([]byte(`{"messages":{"not":"array"}}`)); err == nil {
		t.Fatal("expected error for non-array messages")
	}
}

func TestSanitizeUpstreamBodyProtocolGate(t *testing.T) {
	body := []byte(sanitizeWrap(`{"role":"assistant","content":""}`))
	for _, proto := range []Protocol{ProtocolAnthropicMessages, ProtocolGemini, ProtocolOpenAIResponses} {
		out, removed, err := sanitizeUpstreamBody(proto, body)
		if err != nil || removed != 0 || string(out) != string(body) {
			t.Fatalf("protocol %s must pass through unchanged (out=%s removed=%d err=%v)", proto, out, removed, err)
		}
	}
	out, removed, err := sanitizeUpstreamBody(ProtocolOpenAIChat, body)
	if err != nil || removed != 1 || strings.Contains(string(out), "assistant") {
		t.Fatalf("chat protocol must sanitize (out=%s removed=%d err=%v)", out, removed, err)
	}
}

package proxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"autoapi/internal/model"
	"autoapi/internal/store"
)

// clientSessionHeaders are the conversation-scoped headers a client may send
// itself. When one is present we never synthesize x-opencode-session: the
// upstream either understands that native header or the client has already
// made its own choice.
var clientSessionHeaders = []string{
	"x-opencode-session",
	"x-deepseek-harness-session-id",
	"x-session-affinity",
	"session_id",
}

// openCodeSessionForRequest derives the session id for one inbound request.
// It only does the body work when at least one candidate actually points at
// an OpenCode gateway; every other route returns "" without parsing.
func openCodeSessionForRequest(apiKeyID string, candidates []candidate, body []byte) string {
	if len(candidates) == 0 || len(body) == 0 {
		return ""
	}
	needed := false
	for _, c := range candidates {
		if c.provider != nil && store.RequiresOpenCodeSession(c.provider.BaseURL) {
			needed = true
			break
		}
	}
	if !needed {
		return ""
	}
	return openCodeSessionID(apiKeyID, candidates[0].protocol, body)
}

// openCodeSessionID turns a conversation anchor into a stable, opaque id.
// The API key partitions sessions between callers; the anchor partitions
// conversations for one caller.
func openCodeSessionID(apiKeyID string, protocol Protocol, body []byte) string {
	anchor := conversationAnchor(protocol, body)
	if anchor == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte("autoapi-opencode-session\x00"))
	h.Write([]byte(apiKeyID))
	h.Write([]byte{0})
	h.Write([]byte(anchor))
	sum := h.Sum(nil)
	return "autoapi-" + hex.EncodeToString(sum[:16])
}

// conversationAnchor extracts the parts of a request that identify a
// conversation and stay identical from turn to turn: the system/instructions
// text plus the first user message. Later turns append messages, so hashing
// the whole body would break affinity on every request.
func conversationAnchor(protocol Protocol, body []byte) string {
	switch protocol {
	case ProtocolOpenAIChat:
		return chatConversationAnchor(body)
	case ProtocolOpenAIResponses:
		return responsesConversationAnchor(body)
	case ProtocolAnthropicMessages:
		return messagesConversationAnchor(body)
	case ProtocolGemini:
		return geminiConversationAnchor(body)
	default:
		return ""
	}
}

func chatConversationAnchor(body []byte) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return ""
	}
	var parts []string
	if raw, ok := fields["system"]; ok {
		if s := anchorRaw(raw); s != "" {
			parts = append(parts, s)
		}
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if raw, ok := fields["messages"]; ok {
		if json.Unmarshal(raw, &messages) != nil {
			return ""
		}
	}
	for _, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			if s := anchorRaw(msg.Content); s != "" {
				parts = append(parts, s)
			}
		case "user":
			if s := anchorRaw(msg.Content); s != "" {
				parts = append(parts, s)
				return joinAnchor(parts)
			}
		}
	}
	return ""
}

func responsesConversationAnchor(body []byte) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return ""
	}
	var parts []string
	if raw, ok := fields["instructions"]; ok {
		if s := anchorRaw(raw); s != "" {
			parts = append(parts, s)
		}
	}
	raw, ok := fields["input"]
	if !ok {
		return ""
	}
	var input string
	if json.Unmarshal(raw, &input) == nil {
		if input == "" {
			return ""
		}
		parts = append(parts, input)
		return joinAnchor(parts)
	}
	var items []struct {
		Role    string          `json:"role"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	for _, item := range items {
		if item.Role != "user" {
			continue
		}
		if item.Type != "" && item.Type != "message" {
			continue
		}
		if s := anchorRaw(item.Content); s != "" {
			parts = append(parts, s)
			return joinAnchor(parts)
		}
	}
	return ""
}

func messagesConversationAnchor(body []byte) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return ""
	}
	var parts []string
	if raw, ok := fields["system"]; ok {
		if s := anchorRaw(raw); s != "" {
			parts = append(parts, s)
		}
	}
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if raw, ok := fields["messages"]; ok {
		if json.Unmarshal(raw, &messages) != nil {
			return ""
		}
	}
	for _, msg := range messages {
		if msg.Role != "user" {
			continue
		}
		if s := anchorRaw(msg.Content); s != "" {
			parts = append(parts, s)
			return joinAnchor(parts)
		}
	}
	return ""
}

func geminiConversationAnchor(body []byte) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return ""
	}
	var parts []string
	if raw, ok := fields["systemInstruction"]; ok {
		if s := anchorRaw(raw); s != "" {
			parts = append(parts, s)
		}
	}
	var contents []struct {
		Role  string          `json:"role"`
		Parts json.RawMessage `json:"parts"`
	}
	if raw, ok := fields["contents"]; ok {
		if json.Unmarshal(raw, &contents) != nil {
			return ""
		}
	}
	for _, content := range contents {
		if content.Role != "" && content.Role != "user" {
			continue
		}
		if s := anchorRaw(content.Parts); s != "" {
			parts = append(parts, s)
			return joinAnchor(parts)
		}
	}
	return ""
}

// anchorRaw compacts one JSON value so insignificant whitespace cannot move
// the session id. The value is capped later, after the parts are joined.
func anchorRaw(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return strings.TrimSpace(string(raw))
	}
	return buf.String()
}

func joinAnchor(parts []string) string {
	const maxAnchorBytes = 16 << 10
	anchor := strings.Join(parts, "\x00")
	if len(anchor) > maxAnchorBytes {
		anchor = anchor[:maxAnchorBytes]
	}
	return anchor
}

// maybeInjectOpenCodeSession adds the header OpenCode Go requires, but only
// for an OpenCode gateway and only when the client has not supplied a
// session identity of its own.
func maybeInjectOpenCodeSession(h http.Header, provider *model.Provider, sessionID string) {
	if provider == nil || sessionID == "" || !store.RequiresOpenCodeSession(provider.BaseURL) {
		return
	}
	if hasClientSessionHeader(h) {
		return
	}
	h.Set("x-opencode-session", sessionID)
}

func hasClientSessionHeader(h http.Header) bool {
	for _, name := range clientSessionHeaders {
		if strings.TrimSpace(h.Get(name)) != "" {
			return true
		}
	}
	return false
}

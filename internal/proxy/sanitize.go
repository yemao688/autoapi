// sanitize.go implements the provider opt-in request sanitizer for strict
// upstreams (e.g. Kimi/Moonshot) that reject assistant messages which have
// BOTH empty content AND no tool_calls with
// "the message at position X with role 'assistant' must not be empty".
// Clients (e.g. opencode) persist such empty assistant messages after
// zero-output aborted generations, poisoning the conversation for every
// subsequent request.
//
// Removal is safe by invariant: a removed message has no tool_calls, so no
// tool message can reference it via tool_call_id (tool messages only ever
// reference tool_calls-bearing assistant messages, which are always kept).
package proxy

import (
	"encoding/json"
	"strings"
)

// sanitizeUpstreamBody dispatches on the attempt's UPSTREAM protocol. Only
// the chat protocol is sanitized in this phase; every other protocol passes
// through unchanged (extension point for future per-protocol rules).
func sanitizeUpstreamBody(protocol Protocol, body []byte) ([]byte, int, error) {
	if protocol != ProtocolOpenAIChat {
		return body, 0, nil
	}
	return sanitizeChatMessages(body)
}

// sanitizeChatMessages removes assistant messages that have BOTH empty
// content AND no tool_calls from an OpenAI chat-completions request body.
// It returns the (possibly unchanged) body and the number of removed
// messages. Bodies without a top-level messages array pass through
// unchanged; invalid JSON returns an error so the caller can fail open.
func sanitizeChatMessages(body []byte) ([]byte, int, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, err
	}
	raw, ok := doc["messages"]
	if !ok {
		return body, 0, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		// messages present but not an array: not something we understand —
		// treat as an error so the caller logs and passes the original body.
		return nil, 0, err
	}
	kept := messages[:0]
	removed := 0
	for _, m := range messages {
		if isRemovableEmptyAssistant(m) {
			removed++
			continue
		}
		kept = append(kept, m)
	}
	if removed == 0 {
		return body, 0, nil
	}
	// Re-marshal via the doc so every other top-level field is preserved.
	packed, err := json.Marshal(kept)
	if err != nil {
		return nil, 0, err
	}
	doc["messages"] = packed
	newBody, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, err
	}
	return newBody, removed, nil
}

// isRemovableEmptyAssistant reports whether a raw message is an assistant
// message with BOTH empty content AND no tool_calls.
func isRemovableEmptyAssistant(raw json.RawMessage) bool {
	var msg struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return false
	}
	if msg.Role != "assistant" {
		return false
	}
	if hasToolCalls(msg.ToolCalls) {
		return false
	}
	return chatContentIsEmpty(msg.Content)
}

// hasToolCalls reports whether tool_calls is a non-empty array.
func hasToolCalls(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var calls []json.RawMessage
	if err := json.Unmarshal(raw, &calls); err != nil {
		return false
	}
	return len(calls) > 0
}

// chatContentIsEmpty reports whether a chat message content field is empty:
// missing/null, a blank string, an empty array, or an array where every
// element is a text part whose text is blank. An array containing any
// non-text part, or any non-blank text part, is NOT empty.
func chatContentIsEmpty(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s) == ""
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		// Unknown content shape (e.g. a number): treat as non-empty so we
		// never delete something we do not understand.
		return false
	}
	if len(parts) == 0 {
		return true
	}
	for _, p := range parts {
		if p.Type != "text" {
			return false
		}
		if strings.TrimSpace(p.Text) != "" {
			return false
		}
	}
	return true
}

package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// StreamConverter converts upstream SSE bytes to client SSE bytes.
// It owns framing internally (handles line splits, partial events).
type StreamConverter interface {
	Write(p []byte) ([]byte, error)
	Close() ([]byte, error)
}

type messagesToResponsesStreamConverter struct {
	lines        []byte
	event        string
	data         []byte
	created      bool
	responseID   string
	itemIndex    int
	partIndex    int
	itemType     string
	toolID       string
	toolName     string
	toolArgs     strings.Builder
	text         strings.Builder
	inputTokens  int
	outputTokens int
	stopReason   string
	terminal     bool
	closed       bool
}

func newMessagesToResponsesStreamConverter() StreamConverter {
	return &messagesToResponsesStreamConverter{responseID: "resp_" + strconv.FormatInt(time.Now().UnixNano(), 10), itemIndex: -1}
}

func (c *messagesToResponsesStreamConverter) Write(p []byte) ([]byte, error) {
	if c.closed || len(p) == 0 {
		return nil, nil
	}
	c.lines = append(c.lines, p...)
	return c.drain(false)
}

func (c *messagesToResponsesStreamConverter) Close() ([]byte, error) {
	if c.closed {
		return nil, nil
	}
	c.closed = true
	return c.drain(true)
}

func (c *messagesToResponsesStreamConverter) drain(eof bool) ([]byte, error) {
	var out []byte
	for {
		n := bytes.IndexByte(c.lines, '\n')
		if n < 0 {
			break
		}
		line := bytes.TrimSuffix(c.lines[:n], []byte{'\r'})
		c.lines = append([]byte(nil), c.lines[n+1:]...)
		b, err := c.processLine(line)
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	if eof && len(c.lines) > 0 {
		line := bytes.TrimSuffix(c.lines, []byte{'\r'})
		c.lines = nil
		b, err := c.processLine(line)
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	if eof && (len(c.data) > 0 || c.event != "") {
		b, err := c.processEvent()
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	return out, nil
}

func (c *messagesToResponsesStreamConverter) processLine(line []byte) ([]byte, error) {
	if len(line) == 0 {
		return c.processEvent()
	}
	switch {
	case bytes.HasPrefix(line, []byte("event:")):
		value := line[len("event:"):]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		c.event = string(value)
	case bytes.HasPrefix(line, []byte("data:")):
		value := line[len("data:"):]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		if len(c.data) > 0 {
			c.data = append(c.data, '\n')
		}
		c.data = append(c.data, value...)
	}
	return nil, nil
}

func (c *messagesToResponsesStreamConverter) processEvent() ([]byte, error) {
	if c.terminal {
		return nil, nil
	}
	event, data := c.event, append([]byte(nil), c.data...)
	c.event, c.data = "", nil
	if event == "ping" || len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, nil
	}
	var v map[string]interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	typ, _ := v["type"].(string)
	if typ == "" {
		typ = event
	}
	obj := func(typ string, payload map[string]interface{}) []byte {
		payload["type"] = typ
		b, _ := json.Marshal(payload)
		return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", typ, b))
	}
	get := func(m map[string]interface{}, key string) map[string]interface{} {
		x, _ := m[key].(map[string]interface{})
		return x
	}
	message := get(v, "message")
	if typ == "message_start" {
		u := get(message, "usage")
		c.inputTokens = intNumber(u, "input_tokens")
		if !c.created {
			c.created = true
			return append(obj("response.created", map[string]interface{}{"response": map[string]interface{}{"id": c.responseID, "object": "response", "status": "in_progress", "usage": map[string]interface{}{"input_tokens": c.inputTokens, "output_tokens": 0}}}), obj("response.in_progress", map[string]interface{}{"response": map[string]interface{}{"id": c.responseID, "status": "in_progress"}})...), nil
		}
	}
	switch typ {
	case "content_block_start":
		block := get(v, "content_block")
		if block == nil {
			block = get(v, "delta")
		}
		kind, _ := block["type"].(string)
		c.itemIndex++
		c.partIndex = 0
		c.itemType = kind
		if kind == "text" {
			c.text.Reset()
			out := obj("response.output_item.added", map[string]interface{}{"output_index": c.itemIndex, "item": map[string]interface{}{"type": "message", "id": "msg_" + strconv.Itoa(c.itemIndex), "role": "assistant", "content": []interface{}{}}})
			out = append(out, obj("response.content_part.added", map[string]interface{}{"output_index": c.itemIndex, "content_index": c.partIndex, "part": map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}}})...)
			if d := get(block, "delta"); d != nil {
				if text, _ := d["text"].(string); text != "" {
					c.text.WriteString(text)
					out = append(out, obj("response.output_text.delta", map[string]interface{}{"output_index": c.itemIndex, "content_index": c.partIndex, "delta": text})...)
				}
			}
			return out, nil
		}
		if kind == "tool_use" {
			c.toolID, _ = block["id"].(string)
			c.toolName, _ = block["name"].(string)
			c.toolArgs.Reset()
			return obj("response.output_item.added", map[string]interface{}{"output_index": c.itemIndex, "item": map[string]interface{}{"type": "function_call", "id": c.toolID, "call_id": c.toolID, "name": c.toolName, "arguments": ""}}), nil
		}
	case "content_block_delta":
		d := get(v, "delta")
		kind, _ := d["type"].(string)
		if kind == "text_delta" {
			text, _ := d["text"].(string)
			c.text.WriteString(text)
			return obj("response.output_text.delta", map[string]interface{}{"output_index": c.itemIndex, "content_index": c.partIndex, "delta": text}), nil
		}
		if kind == "input_json_delta" {
			part, _ := d["partial_json"].(string)
			c.toolArgs.WriteString(part)
			return obj("response.function_call_arguments.delta", map[string]interface{}{"output_index": c.itemIndex, "delta": part}), nil
		}
	case "content_block_stop":
		if c.itemType == "text" {
			out := obj("response.output_text.done", map[string]interface{}{"output_index": c.itemIndex, "content_index": c.partIndex, "text": c.text.String()})
			out = append(out, obj("response.content_part.done", map[string]interface{}{"output_index": c.itemIndex, "content_index": c.partIndex})...)
			return append(out, obj("response.output_item.done", map[string]interface{}{"output_index": c.itemIndex})...), nil
		}
		if c.itemType == "tool_use" {
			out := obj("response.function_call_arguments.done", map[string]interface{}{"output_index": c.itemIndex, "arguments": c.toolArgs.String()})
			return append(out, obj("response.output_item.done", map[string]interface{}{"output_index": c.itemIndex})...), nil
		}
	case "message_delta":
		d := get(v, "delta")
		c.stopReason, _ = d["stop_reason"].(string)
		u := get(v, "usage")
		if n := intNumber(u, "output_tokens"); n > 0 {
			c.outputTokens = n
		}
	case "message_stop":
		c.terminal = true
		return obj("response.completed", map[string]interface{}{"response": map[string]interface{}{"id": c.responseID, "object": "response", "status": "completed", "usage": map[string]interface{}{"input_tokens": c.inputTokens, "output_tokens": c.outputTokens}, "stop_reason": c.stopReason}}), nil
	}
	return nil, nil
}

func intNumber(m map[string]interface{}, key string) int { n, _ := m[key].(float64); return int(n) }

// responsesToMessagesStreamConverter translates the Responses wire format
// into Anthropic's Messages SSE format. It deliberately does not emit a
// terminal event from Close: EOF without a Responses terminal is truncated.
type responsesToMessagesStreamConverter struct {
	lines        []byte
	event        string
	data         []byte
	started      bool
	blockType    string
	blockID      string
	blockName    string
	blockIndex   int
	blockOpen    bool
	blockStopped bool
	toolArgs     strings.Builder
	inputTokens  int
	outputTokens int
	stopReason   string
	terminal     bool
	closed       bool
}

func newResponsesToMessagesStreamConverter() StreamConverter {
	return &responsesToMessagesStreamConverter{blockIndex: -1}
}

func (c *responsesToMessagesStreamConverter) Write(p []byte) ([]byte, error) {
	if c.closed || len(p) == 0 {
		return nil, nil
	}
	c.lines = append(c.lines, p...)
	return c.drain(false)
}

func (c *responsesToMessagesStreamConverter) Close() ([]byte, error) {
	if c.closed {
		return nil, nil
	}
	c.closed = true
	return c.drain(true)
}

func (c *responsesToMessagesStreamConverter) drain(eof bool) ([]byte, error) {
	var out []byte
	for {
		n := bytes.IndexByte(c.lines, '\n')
		if n < 0 {
			break
		}
		line := bytes.TrimSuffix(c.lines[:n], []byte{'\r'})
		c.lines = append([]byte(nil), c.lines[n+1:]...)
		b, err := c.processLine(line)
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	if eof && len(c.lines) > 0 {
		line := bytes.TrimSuffix(c.lines, []byte{'\r'})
		c.lines = nil
		b, err := c.processLine(line)
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	if eof && (len(c.data) > 0 || c.event != "") {
		b, err := c.processEvent()
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
	return out, nil
}

func (c *responsesToMessagesStreamConverter) processLine(line []byte) ([]byte, error) {
	if len(line) == 0 {
		return c.processEvent()
	}
	switch {
	case bytes.HasPrefix(line, []byte("event:")):
		v := line[6:]
		if len(v) > 0 && v[0] == ' ' {
			v = v[1:]
		}
		c.event = string(v)
	case bytes.HasPrefix(line, []byte("data:")):
		v := line[5:]
		if len(v) > 0 && v[0] == ' ' {
			v = v[1:]
		}
		if len(c.data) > 0 {
			c.data = append(c.data, '\n')
		}
		c.data = append(c.data, v...)
	}
	return nil, nil
}

func anthropicEvent(name string, payload map[string]interface{}) []byte {
	payload["type"] = name
	b, _ := json.Marshal(payload)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", name, b))
}

func (c *responsesToMessagesStreamConverter) processEvent() ([]byte, error) {
	event, data := c.event, append([]byte(nil), c.data...)
	c.event, c.data = "", nil
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, nil
	}
	var v map[string]interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	typ, _ := v["type"].(string)
	if typ == "" {
		typ = event
	}
	get := func(m map[string]interface{}, key string) map[string]interface{} {
		x, _ := m[key].(map[string]interface{})
		return x
	}
	if c.terminal {
		return nil, nil
	}
	switch typ {
	case "response.created":
		if c.started {
			return nil, nil
		}
		c.started = true
		response := get(v, "response")
		id, _ := response["id"].(string)
		model, _ := response["model"].(string)
		u := get(response, "usage")
		c.inputTokens = intNumber(u, "input_tokens")
		return anthropicEvent("message_start", map[string]interface{}{"message": map[string]interface{}{"id": id, "type": "message", "role": "assistant", "model": model, "content": []interface{}{}, "usage": map[string]interface{}{"input_tokens": c.inputTokens, "output_tokens": 0}}}), nil
	case "response.output_item.added":
		item := get(v, "item")
		kind, _ := item["type"].(string)
		if kind != "message" && kind != "function_call" {
			slog.Debug("proxy: dropping unsupported Responses output item", "type", kind)
			return nil, nil
		}
		if c.blockOpen {
			return nil, nil
		}
		c.blockOpen, c.blockStopped = true, false
		c.blockIndex++
		c.blockType = kind
		c.blockID, _ = item["call_id"].(string)
		if c.blockID == "" {
			c.blockID, _ = item["id"].(string)
		}
		c.blockName, _ = item["name"].(string)
		c.toolArgs.Reset()
		block := map[string]interface{}{"type": "text"}
		if kind == "function_call" {
			block = map[string]interface{}{"type": "tool_use", "id": c.blockID, "name": c.blockName, "input": map[string]interface{}{}}
		}
		return anthropicEvent("content_block_start", map[string]interface{}{"index": c.blockIndex, "content_block": block}), nil
	case "response.output_text.delta":
		if c.blockType != "message" || !c.blockOpen {
			return nil, nil
		}
		text, _ := v["delta"].(string)
		return anthropicEvent("content_block_delta", map[string]interface{}{"index": c.blockIndex, "delta": map[string]interface{}{"type": "text_delta", "text": text}}), nil
	case "response.function_call_arguments.delta":
		if c.blockType != "function_call" || !c.blockOpen {
			return nil, nil
		}
		part, _ := v["delta"].(string)
		c.toolArgs.WriteString(part)
		return anthropicEvent("content_block_delta", map[string]interface{}{"index": c.blockIndex, "delta": map[string]interface{}{"type": "input_json_delta", "partial_json": part}}), nil
	case "response.output_text.done", "response.content_part.done", "response.output_item.done":
		if !c.blockOpen || c.blockStopped {
			return nil, nil
		}
		c.blockStopped = true
		c.blockOpen = false
		return anthropicEvent("content_block_stop", map[string]interface{}{"index": c.blockIndex}), nil
	case "response.function_call_arguments.done":
		return nil, nil
	case "response.completed":
		return c.finish(get(v, "response"), "completed"), nil
	case "response.incomplete":
		response := get(v, "response")
		details, _ := response["incomplete_details"].(map[string]interface{})
		reason, _ := details["reason"].(string)
		// An incomplete Responses stream must never be translated into a
		// normal Anthropic message_stop. That terminal is interpreted as a
		// successful stream by the usage accumulator, which would hide the
		// truncation and prevent pre-commit failover. Return an explicit
		// conversion error for every incomplete reason, including the common
		// token-limit reasons. If output was already committed, streamAttempt
		// records this as truncated and accounts provider usage; otherwise it
		// remains retryable without penalizing provider health/breaker.
		c.terminal = true
		if reason == "" {
			reason = "unknown"
		}
		return nil, fmt.Errorf("responses stream incomplete: %s", reason)
	case "response.failed":
		// Do not emit an error event or message_stop. Returning an error lets
		// streamAttempt distinguish an upstream conversion failure from a
		// successfully delivered stream.
		c.terminal = true
		message := "Responses response failed"
		if details := get(v, "response"); details != nil {
			if status, _ := details["status"].(string); status != "" {
				message += ": " + status
			}
		}
		return nil, fmt.Errorf("responses stream failed: %s", message)
	default:
		if strings.Contains(typ, "reasoning") || strings.Contains(typ, "refusal") {
			slog.Debug("proxy: dropping unsupported Responses event", "type", typ)
		}
	}
	return nil, nil
}

func (c *responsesToMessagesStreamConverter) finish(response map[string]interface{}, reason string) []byte {
	if c.terminal {
		return nil
	}
	c.terminal = true
	u := getMap(response, "usage")
	if n := intNumber(u, "output_tokens"); n > 0 {
		c.outputTokens = n
	}
	if reason == "completed" {
		if s, ok := response["stop_reason"].(string); ok && s != "" {
			c.stopReason = s
		} else {
			c.stopReason = "end_turn"
		}
	} else {
		c.stopReason = reason
	}
	var out []byte
	if c.blockOpen && !c.blockStopped {
		c.blockStopped = true
		c.blockOpen = false
		out = append(out, anthropicEvent("content_block_stop", map[string]interface{}{"index": c.blockIndex})...)
	}
	out = append(out, anthropicEvent("message_delta", map[string]interface{}{"delta": map[string]interface{}{"stop_reason": c.stopReason}, "usage": map[string]interface{}{"output_tokens": c.outputTokens}})...)
	out = append(out, anthropicEvent("message_stop", map[string]interface{}{})...)
	return out
}

func getMap(m map[string]interface{}, key string) map[string]interface{} {
	x, _ := m[key].(map[string]interface{})
	return x
}

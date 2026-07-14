package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
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
		return obj("response.completed", map[string]interface{}{"response": map[string]interface{}{"id": c.responseID, "object": "response", "status": "completed", "usage": map[string]interface{}{"input_tokens": c.inputTokens, "output_tokens": c.outputTokens}, "stop_reason": c.stopReason}}), nil
	}
	return nil, nil
}

func intNumber(m map[string]interface{}, key string) int { n, _ := m[key].(float64); return int(n) }

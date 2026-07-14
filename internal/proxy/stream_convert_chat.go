package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func sseEvent(name string, payload map[string]any) []byte {
	payload["type"] = name
	b, _ := json.Marshal(payload)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", name, b))
}

// ---------------------------------------------------------------------------
// Chat -> Responses
// ---------------------------------------------------------------------------

type chatToResponsesStreamConverter struct {
	lines            []byte
	event            string
	data             []byte
	err              error
	responseID       string
	inputTokens      int
	outputTokens     int
	totalTokens      int
	inDetails        map[string]any
	outDetails       map[string]any
	started          bool
	messageStarted   bool
	messageItemIndex int
	nextItemIndex    int
	finishReason     string
	terminal         bool
	closed           bool
	calls            map[int]*chatToolCallState
}

type chatToolCallState struct {
	id        string
	name      string
	args      strings.Builder
	itemIndex int
	emitted   bool
}

func newChatToResponsesStreamConverter() StreamConverter {
	return &chatToResponsesStreamConverter{
		responseID:       "resp_" + strconv.FormatInt(time.Now().UnixNano(), 10),
		nextItemIndex:    0,
		messageItemIndex: -1,
		calls:            map[int]*chatToolCallState{},
	}
}

func (c *chatToResponsesStreamConverter) Write(p []byte) ([]byte, error) {
	if c.closed || len(p) == 0 {
		return nil, nil
	}
	c.lines = append(c.lines, p...)
	return c.drain(false)
}

func (c *chatToResponsesStreamConverter) Close() ([]byte, error) {
	if c.closed {
		return nil, nil
	}
	c.closed = true
	out, err := c.drain(true)
	if err != nil {
		return out, err
	}
	if c.err != nil {
		return out, nil
	}
	if c.terminal {
		return out, nil
	}
	c.terminal = true
	return append(out, c.incompleteEvent("max_output_tokens")...), nil
}

func (c *chatToResponsesStreamConverter) drain(eof bool) ([]byte, error) {
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

func (c *chatToResponsesStreamConverter) processLine(line []byte) ([]byte, error) {
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

type chatStreamEvent struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Choices []chatStreamChoice `json:"choices"`
	Usage   json.RawMessage    `json:"usage"`
}

type chatStreamChoice struct {
	Index        int             `json:"index"`
	Delta        chatStreamDelta `json:"delta"`
	FinishReason string          `json:"finish_reason"`
}

type chatStreamDelta struct {
	Role         string                    `json:"role"`
	Content      string                    `json:"content"`
	ToolCalls    []chatStreamToolCallDelta `json:"tool_calls"`
	Refusal      json.RawMessage           `json:"refusal"`
	Audio        json.RawMessage           `json:"audio"`
	FunctionCall json.RawMessage           `json:"function_call"`
}

type chatStreamToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (c *chatToResponsesStreamConverter) processEvent() ([]byte, error) {
	if c.terminal || c.err != nil {
		return nil, nil
	}
	_, data := c.event, append([]byte(nil), c.data...)
	c.event, c.data = "", nil
	if bytes.Equal(data, []byte("[DONE]")) {
		return c.handleDone()
	}
	if len(data) == 0 {
		return nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		c.err = err
		return nil, c.err
	}
	var e chatStreamEvent
	if err := json.Unmarshal(data, &e); err != nil {
		c.err = err
		return nil, c.err
	}
	var out []byte

	if e.Usage != nil && len(e.Choices) == 0 {
		u, err := chatUsageToResponses(e.Usage)
		if err != nil {
			c.err = err
			return nil, c.err
		}
		if n, ok := u["input_tokens"].(int); ok {
			c.inputTokens = n
		}
		if n, ok := u["output_tokens"].(int); ok {
			c.outputTokens = n
		}
		if n, ok := u["total_tokens"].(int); ok {
			c.totalTokens = n
		}
		if d, ok := u["input_tokens_details"].(map[string]any); ok {
			c.inDetails = d
		}
		if d, ok := u["output_tokens_details"].(map[string]any); ok {
			c.outDetails = d
		}
	}

	if len(e.Choices) > 0 {
		if len(e.Choices) > 1 {
			c.err = fmt.Errorf("multiple Chat stream choices not supported")
			return nil, c.err
		}
		choice := e.Choices[0]
		if choice.Index != 0 {
			c.err = fmt.Errorf("Chat stream choice index must be 0")
			return nil, c.err
		}
		var rawChoices []json.RawMessage
		if err := json.Unmarshal(raw["choices"], &rawChoices); err != nil {
			c.err = err
			return nil, c.err
		}
		var rawChoice map[string]json.RawMessage
		if err := json.Unmarshal(rawChoices[0], &rawChoice); err != nil {
			c.err = err
			return nil, c.err
		}
		if err := c.validateDelta(rawChoice); err != nil {
			c.err = err
			return nil, c.err
		}

		for _, tc := range choice.Delta.ToolCalls {
			b, err := c.processToolCallDelta(tc)
			if err != nil {
				c.err = err
				return nil, c.err
			}
			out = append(out, b...)
		}

		if choice.Delta.Content != "" {
			if !c.messageStarted {
				c.messageStarted = true
				c.messageItemIndex = c.nextItemIndex
				c.nextItemIndex++
				out = append(out, c.startEvents()...)
				out = append(out, c.messageSetup()...)
			}
			out = append(out, sseEvent("response.output_text.delta", map[string]any{
				"output_index":  c.messageItemIndex,
				"content_index": 0,
				"delta":         choice.Delta.Content,
			})...)
		}

		if choice.FinishReason != "" {
			c.finishReason = choice.FinishReason
		}
	}

	if c.finishReason != "" && e.Usage != nil && len(e.Choices) == 0 {
		term, err := c.terminalEvents()
		if err != nil {
			c.err = err
			return nil, c.err
		}
		out = append(out, term...)
	}

	return out, nil
}

func (c *chatToResponsesStreamConverter) validateDelta(raw map[string]json.RawMessage) error {
	deltaRaw, ok := raw["delta"]
	if !ok {
		return nil
	}
	var delta map[string]json.RawMessage
	if err := json.Unmarshal(deltaRaw, &delta); err != nil {
		return err
	}
	allowed := map[string]bool{"role": true, "content": true, "tool_calls": true}
	for k := range delta {
		if !allowed[k] {
			return fmt.Errorf("unsupported Chat stream delta field %q", k)
		}
	}
	tcRaw, ok := delta["tool_calls"]
	if !ok {
		return nil
	}
	var tcs []map[string]json.RawMessage
	if err := json.Unmarshal(tcRaw, &tcs); err != nil {
		return err
	}
	for _, tc := range tcs {
		for k := range tc {
			if k != "index" && k != "id" && k != "type" && k != "function" {
				return fmt.Errorf("unsupported Chat stream tool_call delta field %q", k)
			}
		}
		fnRaw, ok := tc["function"]
		if !ok {
			continue
		}
		var fn map[string]json.RawMessage
		if err := json.Unmarshal(fnRaw, &fn); err != nil {
			return err
		}
		for k := range fn {
			if k != "name" && k != "arguments" {
				return fmt.Errorf("unsupported Chat stream function delta field %q", k)
			}
		}
	}
	return nil
}

func (c *chatToResponsesStreamConverter) processToolCallDelta(tc chatStreamToolCallDelta) ([]byte, error) {
	var out []byte
	state := c.calls[tc.Index]
	if !c.started {
		c.started = true
		out = append(out, c.startEvents()...)
	}
	if state == nil {
		state = &chatToolCallState{itemIndex: c.nextItemIndex}
		c.nextItemIndex++
		c.calls[tc.Index] = state
	}
	if tc.Type != "" && tc.Type != "function" {
		return nil, fmt.Errorf("unsupported Chat stream tool_call type %q", tc.Type)
	}
	if !state.emitted && (tc.ID != "" || tc.Function.Name != "") {
		state.emitted = true
		state.id = tc.ID
		state.name = tc.Function.Name
		callID := state.id
		if callID == "" {
			callID = fmt.Sprintf("call_%d", tc.Index)
		}
		itemID := fmt.Sprintf("fc_%d", tc.Index)
		out = append(out, sseEvent("response.output_item.added", map[string]any{
			"output_index": state.itemIndex,
			"item": map[string]any{
				"type":      "function_call",
				"id":        itemID,
				"call_id":   callID,
				"name":      state.name,
				"arguments": "",
			},
		})...)
	}
	if tc.Function.Arguments != "" {
		state.args.WriteString(tc.Function.Arguments)
		out = append(out, sseEvent("response.function_call_arguments.delta", map[string]any{
			"output_index": state.itemIndex,
			"delta":        tc.Function.Arguments,
		})...)
	}
	return out, nil
}

func (c *chatToResponsesStreamConverter) startEvents() []byte {
	var out []byte
	out = append(out, sseEvent("response.created", map[string]any{
		"response": map[string]any{
			"id":     c.responseID,
			"object": "response",
			"status": "in_progress",
			"usage": map[string]any{
				"input_tokens":  c.inputTokens,
				"output_tokens": 0,
			},
		},
	})...)
	out = append(out, sseEvent("response.in_progress", map[string]any{
		"response": map[string]any{
			"id":     c.responseID,
			"status": "in_progress",
		},
	})...)
	return out
}

func (c *chatToResponsesStreamConverter) messageSetup() []byte {
	return append(
		sseEvent("response.output_item.added", map[string]any{
			"output_index": c.messageItemIndex,
			"item": map[string]any{
				"type":    "message",
				"id":      "msg_0",
				"role":    "assistant",
				"content": []any{},
			},
		}),
		sseEvent("response.content_part.added", map[string]any{
			"output_index":  c.messageItemIndex,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})...,
	)
}

func (c *chatToResponsesStreamConverter) terminalEvents() ([]byte, error) {
	var out []byte
	if c.messageStarted {
		out = append(out, sseEvent("response.output_text.done", map[string]any{
			"output_index":  c.messageItemIndex,
			"content_index": 0,
			"text":          "",
		})...)
		out = append(out, sseEvent("response.content_part.done", map[string]any{
			"output_index":  c.messageItemIndex,
			"content_index": 0,
		})...)
		out = append(out, sseEvent("response.output_item.done", map[string]any{
			"output_index": c.messageItemIndex,
		})...)
	}
	for i := 0; i < len(c.calls); i++ {
		st := c.calls[i]
		if st == nil {
			continue
		}
		out = append(out, sseEvent("response.function_call_arguments.done", map[string]any{
			"output_index": st.itemIndex,
			"arguments":    st.args.String(),
		})...)
		out = append(out, sseEvent("response.output_item.done", map[string]any{
			"output_index": st.itemIndex,
		})...)
	}

	switch c.finishReason {
	case "stop", "tool_calls":
		reason := "end_turn"
		if c.finishReason == "tool_calls" {
			reason = "tool_use"
		}
		out = append(out, sseEvent("response.completed", map[string]any{
			"response": map[string]any{
				"id":          c.responseID,
				"object":      "response",
				"status":      "completed",
				"usage":       c.usageMap(),
				"stop_reason": reason,
			},
		})...)
	case "length":
		out = append(out, c.incompleteEvent("max_output_tokens")...)
	case "content_filter":
		out = append(out, c.incompleteEvent("content_filter")...)
	default:
		return nil, fmt.Errorf("unsupported Chat finish_reason %q", c.finishReason)
	}
	c.terminal = true
	return out, nil
}

func (c *chatToResponsesStreamConverter) handleDone() ([]byte, error) {
	if c.terminal {
		return nil, nil
	}
	return c.terminalEvents()
}

func (c *chatToResponsesStreamConverter) incompleteEvent(reason string) []byte {
	return sseEvent("response.incomplete", map[string]any{
		"response": map[string]any{
			"id":                 c.responseID,
			"object":             "response",
			"status":             "incomplete",
			"usage":              c.usageMap(),
			"incomplete_details": map[string]any{"reason": reason},
		},
	})
}

func (c *chatToResponsesStreamConverter) usageMap() map[string]any {
	u := map[string]any{
		"input_tokens":  c.inputTokens,
		"output_tokens": c.outputTokens,
		"total_tokens":  c.totalTokens,
	}
	if len(c.inDetails) > 0 {
		u["input_tokens_details"] = c.inDetails
	}
	if len(c.outDetails) > 0 {
		u["output_tokens_details"] = c.outDetails
	}
	return u
}

// ---------------------------------------------------------------------------
// Responses -> Chat
// ---------------------------------------------------------------------------

type responsesToChatStreamConverter struct {
	lines         []byte
	event         string
	data          []byte
	responseID    string
	model         string
	inputTokens   int
	outputTokens  int
	totalTokens   int
	inDetails     map[string]any
	outDetails    map[string]any
	started       bool
	messageOpened bool
	terminal      bool
	closed        bool
	calls         map[int]*responsesToolCallState
}

type responsesToolCallState struct {
	id        string
	name      string
	itemIndex int
}

func newResponsesToChatStreamConverter() StreamConverter {
	return &responsesToChatStreamConverter{
		calls: map[int]*responsesToolCallState{},
	}
}

func (c *responsesToChatStreamConverter) Write(p []byte) ([]byte, error) {
	if c.closed || len(p) == 0 {
		return nil, nil
	}
	c.lines = append(c.lines, p...)
	return c.drain(false)
}

func (c *responsesToChatStreamConverter) Close() ([]byte, error) {
	if c.closed {
		return nil, nil
	}
	c.closed = true
	return c.drain(true)
}

func (c *responsesToChatStreamConverter) drain(eof bool) ([]byte, error) {
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

func (c *responsesToChatStreamConverter) processLine(line []byte) ([]byte, error) {
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

type responsesStreamEvent struct {
	Response *struct {
		Usage             responsesStreamUsage `json:"usage"`
		StopReason        string               `json:"stop_reason"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type responsesStreamUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	InputDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (c *responsesToChatStreamConverter) processEvent() ([]byte, error) {
	if c.terminal {
		return nil, nil
	}
	event, data := c.event, append([]byte(nil), c.data...)
	c.event, c.data = "", nil
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil, nil
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	typ := event
	if typ == "" {
		typ, _ = v["type"].(string)
	}

	var wrapper responsesStreamEvent
	_ = json.Unmarshal(data, &wrapper)

	switch typ {
	case "response.created":
		response := getMap(v, "response")
		c.responseID, _ = response["id"].(string)
		c.model, _ = response["model"].(string)
		if wrapper.Response != nil {
			c.applyUsage(wrapper.Response.Usage)
		}
		c.started = true
		return c.chatChunk(map[string]any{"role": "assistant"}), nil
	case "response.in_progress":
		return nil, nil
	case "response.output_item.added":
		item := getMap(v, "item")
		kind, _ := item["type"].(string)
		switch kind {
		case "message":
			c.messageOpened = true
			return nil, nil
		case "function_call":
			idx := intNumber(v, "output_index")
			if idx < 0 {
				idx = len(c.calls)
			}
			callID, _ := item["call_id"].(string)
			if callID == "" {
				callID, _ = item["id"].(string)
			}
			name, _ := item["name"].(string)
			if callID == "" || name == "" {
				return nil, fmt.Errorf("function_call requires call_id and name")
			}
			c.calls[idx] = &responsesToolCallState{id: callID, name: name, itemIndex: idx}
			return c.chatChunk(map[string]any{"tool_calls": []map[string]any{{
				"index":    idx,
				"id":       callID,
				"type":     "function",
				"function": map[string]any{"name": name},
			}}}), nil
		case "refusal", "reasoning":
			return nil, fmt.Errorf("unsupported Responses output item type: %s", kind)
		default:
			return nil, fmt.Errorf("unsupported Responses output item type: %s", kind)
		}
	case "response.content_part.added":
		part := getMap(v, "part")
		partType, _ := part["type"].(string)
		if partType != "output_text" && partType != "text" {
			return nil, fmt.Errorf("unsupported Responses content part type: %s", partType)
		}
		if ann := part["annotations"]; ann != nil && nonEmptyJSON(toRaw(ann)) {
			return nil, fmt.Errorf("unsupported Responses content annotations")
		}
		return nil, nil
	case "response.output_text.delta":
		text, _ := v["delta"].(string)
		return c.chatChunk(map[string]any{"content": text}), nil
	case "response.output_text.done", "response.content_part.done", "response.output_item.done":
		return nil, nil
	case "response.function_call_arguments.delta":
		idx := intNumber(v, "output_index")
		delta, _ := v["delta"].(string)
		return c.chatChunk(map[string]any{"tool_calls": []map[string]any{{
			"index":    idx,
			"function": map[string]any{"arguments": delta},
		}}}), nil
	case "response.function_call_arguments.done":
		return nil, nil
	case "response.completed":
		if wrapper.Response != nil {
			c.applyUsage(wrapper.Response.Usage)
		}
		stopReason := ""
		if wrapper.Response != nil {
			stopReason = wrapper.Response.StopReason
		}
		return c.finish(stopReason), nil
	case "response.incomplete":
		reason := ""
		if wrapper.Response != nil && wrapper.Response.IncompleteDetails != nil {
			reason = wrapper.Response.IncompleteDetails.Reason
		}
		return c.finishIncomplete(reason), nil
	case "response.failed":
		return nil, fmt.Errorf("responses stream failed")
	default:
		return nil, fmt.Errorf("unsupported Responses stream event type: %s", typ)
	}
}

func (c *responsesToChatStreamConverter) applyUsage(u responsesStreamUsage) {
	if u.InputTokens > 0 {
		c.inputTokens = u.InputTokens
	}
	if u.OutputTokens > 0 {
		c.outputTokens = u.OutputTokens
	}
	if u.TotalTokens > 0 {
		c.totalTokens = u.TotalTokens
	}
	if u.InputDetails != nil && u.InputDetails.CachedTokens > 0 {
		c.inDetails = map[string]any{"cached_tokens": u.InputDetails.CachedTokens}
	}
	if u.OutputDetails != nil && u.OutputDetails.ReasoningTokens > 0 {
		c.outDetails = map[string]any{"reasoning_tokens": u.OutputDetails.ReasoningTokens}
	}
}

func (c *responsesToChatStreamConverter) chatChunk(delta map[string]any) []byte {
	chunk := map[string]any{
		"id":      c.responseID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   c.model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}},
	}
	b, _ := json.Marshal(chunk)
	return []byte(fmt.Sprintf("data: %s\n\n", b))
}

func (c *responsesToChatStreamConverter) finish(stopReason string) []byte {
	if c.terminal {
		return nil
	}
	c.terminal = true
	var out []byte
	out = append(out, c.usageChunk()...)
	finish := "stop"
	switch stopReason {
	case "tool_use":
		finish = "tool_calls"
	case "max_tokens":
		finish = "length"
	case "end_turn":
		finish = "stop"
	}
	if len(c.calls) > 0 && finish == "stop" {
		finish = "tool_calls"
	}
	out = append(out, c.finishChunk(finish)...)
	out = append(out, []byte("data: [DONE]\n\n")...)
	return out
}

func (c *responsesToChatStreamConverter) finishIncomplete(reason string) []byte {
	if c.terminal {
		return nil
	}
	c.terminal = true
	var out []byte
	out = append(out, c.usageChunk()...)
	finish := "length"
	if reason == "content_filter" {
		finish = "content_filter"
	}
	out = append(out, c.finishChunk(finish)...)
	out = append(out, []byte("data: [DONE]\n\n")...)
	return out
}

func (c *responsesToChatStreamConverter) usageChunk() []byte {
	u := map[string]any{
		"prompt_tokens":     c.inputTokens,
		"completion_tokens": c.outputTokens,
		"total_tokens":      c.totalTokens,
	}
	if len(c.inDetails) > 0 {
		u["prompt_tokens_details"] = c.inDetails
	}
	if len(c.outDetails) > 0 {
		u["completion_tokens_details"] = c.outDetails
	}
	chunk := map[string]any{
		"id":      c.responseID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   c.model,
		"choices": []map[string]any{},
		"usage":   u,
	}
	b, _ := json.Marshal(chunk)
	return []byte(fmt.Sprintf("data: %s\n\n", b))
}

func (c *responsesToChatStreamConverter) finishChunk(finish string) []byte {
	chunk := map[string]any{
		"id":      c.responseID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   c.model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finish,
		}},
	}
	b, _ := json.Marshal(chunk)
	return []byte(fmt.Sprintf("data: %s\n\n", b))
}

func toRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

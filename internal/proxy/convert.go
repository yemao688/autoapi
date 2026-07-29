package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type messagesRequestBody struct {
	Model           string          `json:"model"`
	MaxTokens       int             `json:"max_tokens"`
	Metadata        json.RawMessage `json:"metadata"`
	StopSeqs        json.RawMessage `json:"stop_sequences"`
	System          json.RawMessage `json:"system"`
	Messages        json.RawMessage `json:"messages"`
	Tools           json.RawMessage `json:"tools"`
	Temperature     *float64        `json:"temperature"`
	TopP            *float64        `json:"top_p"`
	Stream          bool            `json:"stream"`
	ReasoningEffort string          `json:"reasoning_effort"`
}

type responsesRequestBody struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           json.RawMessage `json:"input"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	Tools           json.RawMessage `json:"tools,omitempty"`
	ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Stream          bool            `json:"stream"`
	Reasoning       json.RawMessage `json:"reasoning,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

// reasoningEffortOf extracts the effort string from a Responses-style
// reasoning object, falling back to the reasoning_effort field value when the
// object carries no effort. reasoning.effort takes precedence. Dropping the
// effort during conversion would let the upstream silently apply its own
// default, so every request converter preserves it.
func reasoningEffortOf(raw json.RawMessage, fallback string) (string, error) {
	effort := fallback
	if len(raw) == 0 || string(raw) == "null" {
		return effort, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("reasoning must be an object")
	}
	if effortRaw, ok := obj["effort"]; ok {
		var s string
		if err := json.Unmarshal(effortRaw, &s); err != nil {
			return "", fmt.Errorf("reasoning.effort must be a string")
		}
		effort = s
	}
	return effort, nil
}

type messageItem struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type responseItem struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Output    json.RawMessage `json:"output,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type responseContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func errUnsupportedConversion(from, to Protocol) error {
	return fmt.Errorf("unsupported protocol conversion: %s to %s", from, to)
}

func messagesToResponsesRequest(body []byte, upstreamModel string) ([]byte, error) {
	var req messagesRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	metadata, err := normalizeJSONObjectField(req.Metadata, "metadata")
	if err != nil {
		return nil, err
	}
	if err := rejectUnsupportedStopSequences(req.StopSeqs); err != nil {
		return nil, err
	}
	instructions, err := messagesSystemToInstructions(req.System)
	if err != nil {
		return nil, err
	}
	input, err := messagesToResponsesInput(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := anthropicToolsToResponses(req.Tools)
	if err != nil {
		return nil, err
	}
	out := responsesRequestBody{Model: upstreamModel, Instructions: instructions, Input: input, Metadata: metadata, Tools: tools, Temperature: req.Temperature, TopP: req.TopP, Stream: req.Stream}
	if req.MaxTokens > 0 {
		out.MaxOutputTokens = &req.MaxTokens
	}
	if req.ReasoningEffort != "" {
		reasoning, err := json.Marshal(map[string]string{"effort": req.ReasoningEffort})
		if err != nil {
			return nil, err
		}
		out.Reasoning = reasoning
	}
	return json.Marshal(out)
}

func responsesToMessagesRequest(body []byte, upstreamModel string) ([]byte, error) {
	if err := rejectStatefulResponsesFields(body); err != nil {
		return nil, err
	}
	var req responsesRequestBody
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	messages, err := responsesInputToMessages(req.Input)
	if err != nil {
		return nil, err
	}
	tools, err := responsesToolsToAnthropic(req.Tools)
	if err != nil {
		return nil, err
	}
	metadata, err := normalizeJSONObjectField(req.Metadata, "metadata")
	if err != nil {
		return nil, err
	}
	toolChoice, err := responsesToolChoiceToAnthropic(req.ToolChoice)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"model": upstreamModel, "messages": json.RawMessage(messages), "stream": req.Stream}
	if req.Instructions != "" {
		out["system"] = req.Instructions
	}
	if len(metadata) > 0 {
		out["metadata"] = metadata
	}
	if req.MaxOutputTokens != nil {
		out["max_tokens"] = *req.MaxOutputTokens
	}
	if req.Temperature != nil {
		out["temperature"] = req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = req.TopP
	}
	if len(tools) > 0 {
		out["tools"] = json.RawMessage(tools)
	}
	if len(toolChoice) > 0 {
		out["tool_choice"] = json.RawMessage(toolChoice)
	}
	effort, err := reasoningEffortOf(req.Reasoning, req.ReasoningEffort)
	if err != nil {
		return nil, err
	}
	if effort != "" {
		out["reasoning_effort"] = effort
	}
	return json.Marshal(out)
}

func responsesToolChoiceToAnthropic(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `""` {
		return nil, nil
	}
	convertible, err := responsesToolChoiceConvertible(raw)
	if err != nil {
		return nil, err
	}
	if !convertible {
		return nil, errors.New("Responses tool_choice is not supported for Messages conversion unless it is auto")
	}
	return []byte(`{"type":"auto"}`), nil
}

func responsesToMessagesResponse(body []byte, clientModel string) ([]byte, error) {
	var resp struct {
		ID     string         `json:"id"`
		Status string         `json:"status"`
		Output []responseItem `json:"output"`
		Usage  struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	blocks, err := responsesOutputToMessagesBlocks(resp.Output)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"id":          resp.ID,
		"type":        "message",
		"role":        "assistant",
		"model":       clientModel,
		"content":     json.RawMessage(blocks),
		"stop_reason": responsesStatusToMessagesStop(resp.Status),
		"usage":       map[string]int{"input_tokens": resp.Usage.InputTokens, "output_tokens": resp.Usage.OutputTokens},
	}
	return json.Marshal(out)
}

func messagesToResponsesResponse(body []byte, clientModel string) ([]byte, error) {
	var resp struct {
		ID         string         `json:"id"`
		Content    []contentBlock `json:"content"`
		StopReason string         `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	output, err := messagesBlocksToResponsesOutput(resp.Content)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"id":      resp.ID,
		"object":  "response",
		"created": time.Now().Unix(),
		"model":   clientModel,
		"status":  messagesStopToResponsesStatus(resp.StopReason),
		"output":  json.RawMessage(output),
		"usage":   map[string]int{"input_tokens": resp.Usage.InputTokens, "output_tokens": resp.Usage.OutputTokens},
	}
	return json.Marshal(out)
}

func messagesSystemToInstructions(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	text, ok, err := flattenTypedTextBlocks(raw)
	if err != nil {
		return "", fmt.Errorf("messages system: %w", err)
	}
	if ok {
		return text, nil
	}
	return "", errors.New("messages system must be a string for Responses conversion")
}

func messagesToResponsesInput(raw json.RawMessage) ([]byte, error) {
	var messages []messageItem
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, err
	}
	var out []responseItem
	for _, msg := range messages {
		blocks, err := normalizeMessageContent(msg.Content)
		if err != nil {
			return nil, err
		}
		switch msg.Role {
		case "user":
			var content []responseContentBlock
			for _, b := range blocks {
				switch b.Type {
				case "text":
					content = append(content, responseContentBlock{Type: "input_text", Text: b.Text})
				case "tool_result":
					out = append(out, responseItem{Type: "function_call_output", CallID: b.ToolUseID, Output: wrapToolOutput(b.Content)})
				case "image":
					return nil, errors.New("image content blocks are not supported for protocol conversion")
				case "thinking":
					return nil, errors.New("thinking content blocks are not supported for protocol conversion")
				default:
					return nil, fmt.Errorf("unsupported content block type for protocol conversion: %s", b.Type)
				}
			}
			if len(content) > 0 {
				cb, _ := json.Marshal(content)
				out = append(out, responseItem{Role: "user", Content: cb})
			}
		case "assistant":
			var content []responseContentBlock
			for _, b := range blocks {
				switch b.Type {
				case "text":
					content = append(content, responseContentBlock{Type: "output_text", Text: b.Text})
				case "tool_use":
					arguments, err := jsonArguments(b.Input)
					if err != nil {
						return nil, err
					}
					out = append(out, responseItem{Type: "function_call", CallID: b.ID, Name: b.Name, Arguments: arguments})
				case "image":
					return nil, errors.New("image content blocks are not supported for protocol conversion")
				case "thinking":
					return nil, errors.New("thinking content blocks are not supported for protocol conversion")
				default:
					return nil, fmt.Errorf("unsupported content block type for protocol conversion: %s", b.Type)
				}
			}
			if len(content) > 0 {
				cb, _ := json.Marshal(content)
				out = append(out, responseItem{Role: "assistant", Content: cb})
			}
		default:
			return nil, fmt.Errorf("unsupported message role for protocol conversion: %s", msg.Role)
		}
	}
	return json.Marshal(out)
}

func responsesInputToMessages(raw json.RawMessage) ([]byte, error) {
	var items []responseItem
	if err := json.Unmarshal(raw, &items); err != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return json.Marshal([]messageItem{{Role: "user", Content: mustJSON([]contentBlock{{Type: "text", Text: s}})}})
		}
		return nil, err
	}
	var out []messageItem
	for _, item := range items {
		switch item.Type {
		case "function_call":
			input, err := parseFunctionArguments(item.Arguments)
			if err != nil {
				return nil, err
			}
			out = append(out, messageItem{Role: "assistant", Content: mustJSON([]contentBlock{{Type: "tool_use", ID: firstNonEmpty(item.CallID, item.ID), Name: item.Name, Input: input}})})
		case "function_call_output":
			out = append(out, messageItem{Role: "user", Content: mustJSON([]contentBlock{{Type: "tool_result", ToolUseID: item.CallID, Content: wrapToolOutput(item.Output)}})})
		default:
			if item.Type != "" && item.Type != "message" {
				return nil, fmt.Errorf("unsupported Responses input item type: %s", item.Type)
			}
			blocks, err := responsesContentToMessagesBlocks(item.Content)
			if err != nil {
				return nil, err
			}
			if len(blocks) > 0 {
				role := item.Role
				if role == "" {
					role = "user"
				}
				out = append(out, messageItem{Role: role, Content: mustJSON(blocks)})
			}
		}
	}
	return json.Marshal(out)
}

func normalizeMessageContent(raw json.RawMessage) ([]contentBlock, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []contentBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	for _, b := range blocks {
		if b.Type == "thinking" {
			return nil, errors.New("thinking content blocks are not supported for protocol conversion")
		}
		if b.Type == "image" || b.Type == "image_url" {
			return nil, errors.New("image content blocks are not supported for protocol conversion")
		}
	}
	return blocks, nil
}

func responsesContentToMessagesBlocks(raw json.RawMessage) ([]contentBlock, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []contentBlock{{Type: "text", Text: s}}, nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	out := make([]contentBlock, 0, len(blocks))
	for _, b := range blocks {
		var typ string
		if err := json.Unmarshal(b["type"], &typ); err != nil || typ == "" {
			return nil, errors.New("Responses content block type is required")
		}
		switch typ {
		case "input_text", "output_text", "text":
			text, err := requiredStringField(b, "text", "Responses text content")
			if err != nil {
				return nil, err
			}
			if err := rejectNonEmptyBlockMetadata(b, "annotations", "logprobs", "refusal"); err != nil {
				return nil, err
			}
			out = append(out, contentBlock{Type: "text", Text: text})
		case "refusal":
			if err := rejectNonEmptyBlockMetadata(b, "refusal"); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported Responses content block type: %s", typ)
		}
	}
	return out, nil
}

func normalizeJSONObjectField(raw json.RawMessage, field string) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%s must be an object", field)
	}
	return raw, nil
}

func rejectUnsupportedStopSequences(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var seqs []string
	if err := json.Unmarshal(raw, &seqs); err != nil {
		return fmt.Errorf("stop_sequences must be an array of strings")
	}
	for _, seq := range seqs {
		if seq != "" {
			return errors.New("messages stop_sequences are not safely representable for Responses conversion")
		}
	}
	return nil
}

func flattenTypedTextBlocks(raw json.RawMessage) (string, bool, error) {
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false, nil
	}
	text, err := joinTypedTextBlocks(blocks)
	if err != nil {
		return "", true, err
	}
	return text, true, nil
}

func joinTypedTextBlocks(blocks []map[string]json.RawMessage) (string, error) {
	if len(blocks) == 0 {
		return "", nil
	}
	var out string
	for _, block := range blocks {
		var typ string
		if err := json.Unmarshal(block["type"], &typ); err != nil || typ == "" {
			return "", errors.New("text content block type is required")
		}
		if typ != "text" && typ != "input_text" && typ != "output_text" {
			return "", fmt.Errorf("unsupported text content block type: %s", typ)
		}
		text, err := requiredStringField(block, "text", "text content")
		if err != nil {
			return "", err
		}
		if err := rejectNonEmptyBlockMetadata(block, "annotations", "logprobs", "refusal"); err != nil {
			return "", err
		}
		out += text
	}
	return out, nil
}

func requiredStringField(obj map[string]json.RawMessage, key, context string) (string, error) {
	var value string
	if err := json.Unmarshal(obj[key], &value); err != nil {
		return "", fmt.Errorf("%s %s must be a string", context, key)
	}
	return value, nil
}

func rejectNonEmptyBlockMetadata(obj map[string]json.RawMessage, fields ...string) error {
	for _, field := range fields {
		if nonEmptyJSON(obj[field]) {
			return fmt.Errorf("unsupported Responses content metadata %q", field)
		}
	}
	return nil
}

func anthropicToolsToResponses(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.InputSchema})
	}
	return json.Marshal(out)
}

func responsesToolsToAnthropic(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var tools []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if t.Type != "" && t.Type != "function" {
			return nil, fmt.Errorf("unsupported Responses tool type: %s", t.Type)
		}
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.Parameters})
	}
	return json.Marshal(out)
}

func responsesOutputToMessagesBlocks(items []responseItem) ([]byte, error) {
	var out []contentBlock
	for _, item := range items {
		switch item.Type {
		case "message", "":
			blocks, err := responsesContentToMessagesBlocks(item.Content)
			if err != nil {
				return nil, err
			}
			out = append(out, blocks...)
		case "function_call":
			input, err := parseFunctionArguments(item.Arguments)
			if err != nil {
				return nil, err
			}
			out = append(out, contentBlock{Type: "tool_use", ID: firstNonEmpty(item.CallID, item.ID), Name: item.Name, Input: input})
		default:
			return nil, fmt.Errorf("unsupported Responses output item type: %s", item.Type)
		}
	}
	return json.Marshal(out)
}

func messagesBlocksToResponsesOutput(blocks []contentBlock) ([]byte, error) {
	var out []responseItem
	var text []responseContentBlock
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text = append(text, responseContentBlock{Type: "output_text", Text: b.Text})
		case "tool_use":
			arguments, err := jsonArguments(b.Input)
			if err != nil {
				return nil, err
			}
			out = append(out, responseItem{Type: "function_call", CallID: b.ID, Name: b.Name, Arguments: arguments})
		case "thinking":
			return nil, errors.New("thinking content blocks are not supported for protocol conversion")
		case "image":
			return nil, errors.New("image content blocks are not supported for protocol conversion")
		default:
			return nil, fmt.Errorf("unsupported content block type for protocol conversion: %s", b.Type)
		}
	}
	if len(text) > 0 {
		out = append([]responseItem{{Type: "message", Role: "assistant", Content: mustJSON(text)}}, out...)
	}
	return json.Marshal(out)
}

func rejectStatefulResponsesFields(body []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		if _, ok := fields[field]; ok {
			return fmt.Errorf("stateful Responses fields are not supported: %s", field)
		}
	}
	for _, field := range []string{"background", "store"} {
		if raw, ok := fields[field]; ok {
			var v bool
			if json.Unmarshal(raw, &v) == nil && v {
				return fmt.Errorf("%s=true is not supported for protocol conversion", field)
			}
		}
	}
	return nil
}

func responsesStatusToMessagesStop(status string) string {
	switch status {
	case "completed":
		return "end_turn"
	case "incomplete":
		return "max_tokens"
	case "failed":
		return "error"
	default:
		return "end_turn"
	}
}

func messagesStopToResponsesStatus(stop string) string {
	switch stop {
	case "max_tokens":
		return "incomplete"
	case "error":
		return "failed"
	default:
		return "completed"
	}
}

func logDroppedFields(body []byte, fields ...string) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return
	}
	for _, field := range fields {
		if _, ok := m[field]; ok {
			slog.Debug("proxy: dropping unsupported field during protocol conversion", "field", field)
		}
	}
}

func wrapToolOutput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`""`)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return raw
	}
	return mustJSON(string(raw))
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func jsonArguments(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}", nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", fmt.Errorf("invalid function call arguments: %w", err)
	}
	b, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("invalid function call arguments: %w", err)
	}
	return string(b), nil
}

func parseFunctionArguments(arguments string) (json.RawMessage, error) {
	if arguments == "" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(arguments), &object); err != nil {
		return nil, fmt.Errorf("invalid Responses function_call arguments: %w", err)
	}
	return json.RawMessage(arguments), nil
}

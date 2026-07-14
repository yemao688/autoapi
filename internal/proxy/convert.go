package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type messagesRequestBody struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	System      json.RawMessage `json:"system"`
	Messages    json.RawMessage `json:"messages"`
	Tools       json.RawMessage `json:"tools"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	Stream      bool            `json:"stream"`
}

type responsesRequestBody struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           json.RawMessage `json:"input"`
	Tools           json.RawMessage `json:"tools,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	Stream          bool            `json:"stream"`
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
	out := responsesRequestBody{Model: upstreamModel, Instructions: instructions, Input: input, Tools: tools, Temperature: req.Temperature, TopP: req.TopP, Stream: req.Stream}
	if req.MaxTokens > 0 {
		out.MaxOutputTokens = &req.MaxTokens
	}
	logDroppedFields(body, "metadata", "stop_sequences")
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
	out := map[string]any{"model": upstreamModel, "messages": json.RawMessage(messages), "stream": req.Stream}
	if req.Instructions != "" {
		out["system"] = req.Instructions
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
	return json.Marshal(out)
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
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return "", errors.New("messages system content blocks are not supported for Responses conversion")
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
					out = append(out, responseItem{Type: "function_call_output", CallID: b.ToolUseID, Output: responseToolOutput(b.Content)})
				case "image":
					return nil, errors.New("image content blocks are not supported for protocol conversion")
				case "thinking":
					return nil, errors.New("thinking content blocks are not supported for protocol conversion")
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
				}
			}
			if len(content) > 0 {
				cb, _ := json.Marshal(content)
				out = append(out, responseItem{Role: "assistant", Content: cb})
			}
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
			out = append(out, messageItem{Role: "user", Content: mustJSON([]contentBlock{{Type: "tool_result", ToolUseID: item.CallID, Content: toolResultContent(item.Output)}})})
		default:
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
	var blocks []responseContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, err
	}
	out := make([]contentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "input_text" || b.Type == "output_text" || b.Type == "text" {
			out = append(out, contentBlock{Type: "text", Text: b.Text})
		}
	}
	return out, nil
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
			continue
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

func responseToolOutput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`""`)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return raw
	}
	return mustJSON(string(raw))
}

func toolResultContent(raw json.RawMessage) json.RawMessage {
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

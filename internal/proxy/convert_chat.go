package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func chatToResponsesRequest(body []byte, upstreamModel string) ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	out := map[string]any{"model": upstreamModel}
	copyNumber := func(from, to string) error {
		if raw, ok := req[from]; ok {
			if string(raw) == "null" {
				return nil
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return fmt.Errorf("%s: %w", from, err)
			}
			if _, ok := value.(float64); !ok {
				return fmt.Errorf("%s must be a number", from)
			}
			out[to] = value
		}
		return nil
	}
	if _, hasCompletion := req["max_completion_tokens"]; hasCompletion {
		if _, hasLegacy := req["max_tokens"]; hasLegacy {
			return nil, fmt.Errorf("max_completion_tokens and max_tokens cannot both be set")
		}
		if err := copyNumber("max_completion_tokens", "max_output_tokens"); err != nil {
			return nil, err
		}
	} else if err := copyNumber("max_tokens", "max_output_tokens"); err != nil {
		return nil, err
	}
	for _, key := range []string{"temperature", "top_p"} {
		if err := copyNumber(key, key); err != nil {
			return nil, err
		}
	}
	tools, err := chatToolsToResponses(req["tools"])
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	input, err := chatMessagesToResponses(req["messages"])
	if err != nil {
		return nil, err
	}
	out["input"] = input
	if raw, ok := req["stream"]; ok && string(raw) != "null" {
		var stream bool
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, fmt.Errorf("stream must be a boolean")
		}
		if stream {
			out["stream"] = true
		}
	}
	return json.Marshal(out)
}

func responsesToChatRequest(body []byte, upstreamModel string) ([]byte, error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	out := map[string]any{"model": upstreamModel}
	for _, field := range []string{"temperature", "top_p"} {
		if raw, ok := req[field]; ok && string(raw) != "null" {
			var value float64
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, fmt.Errorf("%s must be a number", field)
			}
			out[field] = value
		}
	}
	if raw, ok := req["max_output_tokens"]; ok && string(raw) != "null" {
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("max_output_tokens must be an integer")
		}
		out["max_completion_tokens"] = value
	}
	tools, err := responsesToolsToChat(req["tools"])
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		out["tools"] = tools
	}
	messages, err := responsesInputToChat(req["input"], req["instructions"])
	if err != nil {
		return nil, err
	}
	out["messages"] = messages
	if raw, ok := req["stream"]; ok && string(raw) != "null" {
		var stream bool
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, fmt.Errorf("stream must be a boolean")
		}
		if stream {
			out["stream"] = true
		}
	}
	return json.Marshal(out)
}

func chatToolsToResponses(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, fmt.Errorf("tools: %w", err)
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if err := rejectUnknownKeys(tool, "type", "function"); err != nil {
			return nil, err
		}
		var typ string
		if json.Unmarshal(tool["type"], &typ) != nil || typ != "function" {
			return nil, fmt.Errorf("only function tools are supported")
		}
		var fn map[string]json.RawMessage
		if json.Unmarshal(tool["function"], &fn) != nil {
			return nil, fmt.Errorf("function tool is required")
		}
		if err := rejectUnknownKeys(fn, "name", "description", "parameters", "strict"); err != nil {
			return nil, err
		}
		var name string
		if json.Unmarshal(fn["name"], &name) != nil || name == "" {
			return nil, fmt.Errorf("function name is required")
		}
		parameters := fn["parameters"]
		if len(parameters) == 0 || string(parameters) == "null" {
			parameters = json.RawMessage(`{"type":"object"}`)
		}
		var schema map[string]any
		if json.Unmarshal(parameters, &schema) != nil || schema == nil {
			return nil, fmt.Errorf("tool parameters must be an object")
		}
		item := map[string]any{"type": "function", "name": name, "parameters": schema}
		for _, key := range []string{"description", "strict"} {
			if value, ok := fn[key]; ok {
				item[key] = json.RawMessage(value)
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func responsesToolsToChat(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if err := rejectUnknownKeys(tool, "type", "name", "description", "parameters", "strict"); err != nil {
			return nil, err
		}
		var typ, name string
		if json.Unmarshal(tool["type"], &typ) != nil || typ != "function" || json.Unmarshal(tool["name"], &name) != nil || name == "" {
			return nil, fmt.Errorf("only valid function tools are supported")
		}
		var schema map[string]any
		parameters := tool["parameters"]
		if len(parameters) == 0 || string(parameters) == "null" {
			schema = map[string]any{"type": "object"}
		} else if json.Unmarshal(parameters, &schema) != nil || schema == nil {
			return nil, fmt.Errorf("tool parameters must be an object")
		}
		fn := map[string]any{"name": name, "parameters": schema}
		for _, key := range []string{"description", "strict"} {
			if value, ok := tool[key]; ok {
				fn[key] = json.RawMessage(value)
			}
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out, nil
}

func rejectUnknownKeys(obj map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key := range obj {
		if !set[key] {
			return fmt.Errorf("unsupported field %q", key)
		}
	}
	return nil
}

func chatMessagesToResponses(raw json.RawMessage) ([]map[string]any, error) {
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &messages); err != nil {
		return nil, fmt.Errorf("messages: %w", err)
	}
	seenCalls := map[string]bool{}
	results := map[string]bool{}
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		var role string
		if err := json.Unmarshal(message["role"], &role); err != nil || role == "" {
			return nil, fmt.Errorf("message role is required")
		}
		switch role {
		case "system", "developer", "user", "assistant":
			if content, ok := message["content"]; ok && string(content) != "null" {
				text, err := chatTextContent(content)
				if err != nil {
					return nil, err
				}
				if text != "" {
					contentType := "input_text"
					if role == "assistant" {
						contentType = "output_text"
					}
					out = append(out, map[string]any{"type": "message", "role": role, "content": []map[string]any{{"type": contentType, "text": text}}})
				}
			}
			if calls, ok := message["tool_calls"]; ok {
				var toolCalls []map[string]json.RawMessage
				if err := json.Unmarshal(calls, &toolCalls); err != nil {
					return nil, fmt.Errorf("tool_calls: %w", err)
				}
				for _, call := range toolCalls {
					id, name, args, err := chatFunctionCall(call)
					if err != nil {
						return nil, err
					}
					if seenCalls[id] {
						return nil, fmt.Errorf("duplicate tool call id %q", id)
					}
					seenCalls[id] = true
					out = append(out, map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(args)})
				}
			}
		case "tool":
			var callID string
			if err := json.Unmarshal(message["tool_call_id"], &callID); err != nil || callID == "" || !seenCalls[callID] || results[callID] {
				return nil, fmt.Errorf("tool result has invalid or duplicate tool_call_id")
			}
			content, ok := message["content"]
			if !ok {
				return nil, fmt.Errorf("tool result content is required")
			}
			var output string
			if err := json.Unmarshal(content, &output); err != nil {
				return nil, fmt.Errorf("tool result content must be a string")
			}
			results[callID] = true
			out = append(out, map[string]any{"type": "function_call_output", "call_id": callID, "output": output})
		default:
			return nil, fmt.Errorf("unsupported Chat message role %q", role)
		}
	}
	return out, nil
}

func chatTextContent(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("only text Chat content is supported")
	}
	var out bytes.Buffer
	for _, block := range blocks {
		var typ string
		if err := json.Unmarshal(block["type"], &typ); err != nil || (typ != "text" && typ != "input_text" && typ != "output_text") {
			return "", fmt.Errorf("only text Chat content is supported")
		}
		var value string
		if err := json.Unmarshal(block["text"], &value); err != nil {
			return "", fmt.Errorf("text content must be a string")
		}
		out.WriteString(value)
	}
	return out.String(), nil
}

func chatFunctionCall(call map[string]json.RawMessage) (string, string, json.RawMessage, error) {
	var id, typ string
	if json.Unmarshal(call["id"], &id) != nil || id == "" {
		return "", "", nil, fmt.Errorf("tool call id is required")
	}
	if json.Unmarshal(call["type"], &typ) != nil || typ != "function" {
		return "", "", nil, fmt.Errorf("only function tool calls are supported")
	}
	var fn map[string]json.RawMessage
	if json.Unmarshal(call["function"], &fn) != nil {
		return "", "", nil, fmt.Errorf("tool call function is required")
	}
	var name, arguments string
	if json.Unmarshal(fn["name"], &name) != nil || name == "" || json.Unmarshal(fn["arguments"], &arguments) != nil {
		return "", "", nil, fmt.Errorf("tool call function name and arguments are required")
	}
	if err := validateJSONObject([]byte(arguments)); err != nil {
		return "", "", nil, fmt.Errorf("tool call arguments: %w", err)
	}
	return id, name, json.RawMessage(arguments), nil
}

func responsesInputToChat(raw, instructions json.RawMessage) ([]map[string]any, error) {
	out := make([]map[string]any, 0)
	if len(instructions) > 0 && string(instructions) != "null" && string(instructions) != `""` {
		var text string
		if err := json.Unmarshal(instructions, &text); err != nil {
			return nil, fmt.Errorf("instructions must be a string")
		}
		out = append(out, map[string]any{"role": "developer", "content": text})
	}
	var input string
	if json.Unmarshal(raw, &input) == nil {
		out = append(out, map[string]any{"role": "user", "content": input})
		return out, nil
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("input must be a string or array")
	}
	seenCalls := map[string]bool{}
	results := map[string]bool{}
	activeCalls := make([]map[string]any, 0)
	hadCallGroup := false
	outputsStarted := false
	flushCalls := func() {
		if len(activeCalls) > 0 {
			calls := append([]map[string]any(nil), activeCalls...)
			out = append(out, map[string]any{"role": "assistant", "tool_calls": calls})
			hadCallGroup = true
			activeCalls = activeCalls[:0]
		}
	}
	for _, item := range items {
		var typ string
		_ = json.Unmarshal(item["type"], &typ)
		if typ == "function_call" {
			if outputsStarted {
				return nil, fmt.Errorf("function_call after function_call_output is not supported")
			}
			var callID, name, args string
			if json.Unmarshal(item["call_id"], &callID) != nil || callID == "" || json.Unmarshal(item["name"], &name) != nil || name == "" || json.Unmarshal(item["arguments"], &args) != nil {
				return nil, fmt.Errorf("function_call requires call_id, name and arguments")
			}
			if err := validateJSONObject([]byte(args)); err != nil {
				return nil, err
			}
			if seenCalls[callID] {
				return nil, fmt.Errorf("duplicate function call id %q", callID)
			}
			seenCalls[callID] = true
			activeCalls = append(activeCalls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": args}})
			continue
		}
		if typ == "function_call_output" {
			if !hadCallGroup && len(activeCalls) == 0 {
				return nil, fmt.Errorf("function_call_output has no preceding function_call group")
			}
			flushCalls()
			outputsStarted = true
			// After flushing, activeCalls must be empty; if any calls remain
			// (shouldn't because flushCalls empties them), that's an error.
			if len(activeCalls) > 0 {
				return nil, fmt.Errorf("function_call_output interleaved with pending function_call group")
			}
			var callID string
			if json.Unmarshal(item["call_id"], &callID) != nil || callID == "" || !seenCalls[callID] || results[callID] {
				return nil, fmt.Errorf("function_call_output has invalid or duplicate call_id")
			}
			var output string
			if err := json.Unmarshal(item["output"], &output); err != nil {
				return nil, fmt.Errorf("function_call_output output must be a string")
			}
			results[callID] = true
			out = append(out, map[string]any{"role": "tool", "tool_call_id": callID, "content": output})
			continue
		}
		if typ != "" && typ != "message" {
			return nil, fmt.Errorf("unsupported Responses input item type %q", typ)
		}
		if outputsStarted {
			return nil, fmt.Errorf("message interleaved after function_call_output")
		}
		if len(activeCalls) > 0 {
			return nil, fmt.Errorf("message interleaved with pending function_call group")
		}
		if hadCallGroup {
			return nil, fmt.Errorf("message after function_call group is not supported")
		}
		flushCalls()
		var role string
		if json.Unmarshal(item["role"], &role) != nil || role == "" {
			return nil, fmt.Errorf("Responses message role is required")
		}
		if role != "system" && role != "developer" && role != "user" && role != "assistant" {
			return nil, fmt.Errorf("unsupported Responses message role %q", role)
		}
		content, ok := item["content"]
		if !ok {
			return nil, fmt.Errorf("Responses message content is required")
		}
		text, err := responsesTextContent(content)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"role": role, "content": text})
	}
	flushCalls()
	return out, nil
}

func responsesTextContent(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("only text Responses content is supported")
	}
	var out bytes.Buffer
	for _, block := range blocks {
		var typ string
		if json.Unmarshal(block["type"], &typ) != nil || (typ != "input_text" && typ != "output_text") {
			return "", fmt.Errorf("only text Responses content is supported")
		}
		var value string
		if json.Unmarshal(block["text"], &value) != nil {
			return "", fmt.Errorf("Responses text content must be a string")
		}
		out.WriteString(value)
	}
	return out.String(), nil
}

func validateJSONObject(raw []byte) error {
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("tool arguments must be a JSON object")
	}
	return nil
}

func chatToResponsesResponse(body []byte, clientModel string) ([]byte, error) {
	var resp struct {
		ID      string `json:"id"`
		Choices []struct {
			Index   *int `json:"index"`
			Message struct {
				Role      string          `json:"role"`
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if err := validateChatResponseWire(body); err != nil {
		return nil, err
	}
	if len(resp.Choices) != 1 {
		return nil, fmt.Errorf("Chat response choices must contain exactly one choice")
	}
	choice := resp.Choices[0]
	if choice.Index == nil || *choice.Index != 0 {
		return nil, fmt.Errorf("Chat response choice index must be 0")
	}
	if choice.FinishReason == nil || *choice.FinishReason == "" {
		return nil, fmt.Errorf("Chat response finish_reason is required")
	}
	if choice.Message.Role != "assistant" {
		return nil, fmt.Errorf("unsupported Chat response role")
	}
	var messageFields map[string]json.RawMessage
	var rawEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawEnvelope); err != nil {
		return nil, err
	}
	var rawChoices []json.RawMessage
	if err := json.Unmarshal(rawEnvelope["choices"], &rawChoices); err != nil {
		return nil, err
	}
	var rawChoice map[string]json.RawMessage
	if err := json.Unmarshal(rawChoices[0], &rawChoice); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rawChoice["message"], &messageFields); err != nil {
		return nil, err
	}
	for _, forbidden := range []string{"refusal", "annotations"} {
		if _, ok := messageFields[forbidden]; ok {
			return nil, fmt.Errorf("unsupported Chat response field %q", forbidden)
		}
	}
	output := make([]map[string]any, 0)
	contentNull := len(choice.Message.Content) == 0 || string(choice.Message.Content) == "null"
	if contentNull && len(choice.Message.ToolCalls) == 0 {
		return nil, fmt.Errorf("Chat null content requires tool_calls")
	}
	if len(choice.Message.Content) > 0 && string(choice.Message.Content) != "null" {
		var content string
		if err := json.Unmarshal(choice.Message.Content, &content); err != nil {
			return nil, fmt.Errorf("Chat response content must be a string or null")
		}
		if content != "" {
			output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": content}}})
		}
	}
	seen := map[string]bool{}
	for callIndex, call := range choice.Message.ToolCalls {
		if call.Type != "function" || call.ID == "" || call.Function.Name == "" || seen[call.ID] {
			return nil, fmt.Errorf("invalid Chat function call")
		}
		if err := validateJSONObject([]byte(call.Function.Arguments)); err != nil {
			return nil, err
		}
		seen[call.ID] = true
		output = append(output, map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", callIndex), "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
	}
	status := "completed"
	var incompleteDetails any
	switch *choice.FinishReason {
	case "stop":
	case "tool_calls":
		if len(choice.Message.ToolCalls) == 0 {
			return nil, fmt.Errorf("tool_calls finish_reason without tool calls")
		}
	case "length", "content_filter":
		status = "incomplete"
		reason := "max_output_tokens"
		if *choice.FinishReason == "content_filter" {
			reason = "content_filter"
		}
		incompleteDetails = map[string]any{"reason": reason}
	default:
		return nil, fmt.Errorf("unsupported Chat finish_reason %q", *choice.FinishReason)
	}
	usage, err := chatUsageToResponses(resp.Usage)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"id": resp.ID, "model": clientModel, "status": status, "output": output, "usage": usage}
	if incompleteDetails != nil {
		out["incomplete_details"] = incompleteDetails
	}
	return json.Marshal(out)
}

func responsesToChatResponse(body []byte, clientModel string) ([]byte, error) {
	var resp struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type        string          `json:"type"`
				Text        string          `json:"text"`
				Annotations json.RawMessage `json:"annotations"`
				Refusal     json.RawMessage `json:"refusal"`
				Logprobs    json.RawMessage `json:"logprobs"`
			} `json:"content"`
			CallID    string `json:"call_id"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"output"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if err := validateResponsesOutputWire(body); err != nil {
		return nil, err
	}
	if resp.Status != "completed" && resp.Status != "incomplete" {
		return nil, fmt.Errorf("unsupported Responses status %q", resp.Status)
	}
	if resp.Status == "incomplete" && resp.IncompleteDetails.Reason != "max_output_tokens" && resp.IncompleteDetails.Reason != "content_filter" {
		return nil, fmt.Errorf("unsupported Responses incomplete reason %q", resp.IncompleteDetails.Reason)
	}
	var text string
	toolCalls := make([]map[string]any, 0)
	seen := map[string]bool{}
	messageCount := 0
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			messageCount++
			if messageCount > 1 {
				return nil, fmt.Errorf("multiple Responses message output items are not safely representable")
			}
			if item.Role != "assistant" {
				return nil, fmt.Errorf("unsupported Responses output role %q", item.Role)
			}
			for _, block := range item.Content {
				if nonEmptyJSON(block.Annotations) || nonEmptyJSON(block.Refusal) || nonEmptyJSON(block.Logprobs) {
					return nil, fmt.Errorf("unsupported Responses output metadata")
				}
				if block.Type != "output_text" {
					return nil, fmt.Errorf("unsupported Responses output content type %q", block.Type)
				}
				text += block.Text
			}
		case "function_call":
			callID := item.CallID
			if callID == "" || item.Name == "" || seen[callID] {
				return nil, fmt.Errorf("invalid Responses function call")
			}
			if err := validateJSONObject([]byte(item.Arguments)); err != nil {
				return nil, err
			}
			seen[callID] = true
			toolCalls = append(toolCalls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": item.Name, "arguments": item.Arguments}})
		default:
			return nil, fmt.Errorf("unsupported Responses output item type %q", item.Type)
		}
	}
	message := map[string]any{"role": "assistant"}
	if text != "" {
		message["content"] = text
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}
	if resp.Status == "incomplete" {
		finish = "length"
		if resp.IncompleteDetails.Reason == "content_filter" {
			finish = "content_filter"
		}
	}
	usage, err := responsesUsageToChat(resp.Usage)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"id": resp.ID, "model": clientModel, "choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}}, "usage": usage})
}

func nonEmptyJSON(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != "[]" && string(raw) != "{}" && string(raw) != `""`
}

func usageObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("usage must be an object")
	}
	return obj, nil
}

func numberField(obj map[string]json.RawMessage, key string) (int, error) {
	if raw, ok := obj[key]; ok && string(raw) != "null" {
		var n int
		if err := json.Unmarshal(raw, &n); err != nil {
			return 0, fmt.Errorf("usage.%s must be an integer", key)
		}
		return n, nil
	}
	return 0, nil
}

func detailsField(raw json.RawMessage, allowed string) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("usage details must be an object")
	}
	out := map[string]any{}
	for key, value := range obj {
		if key != allowed {
			if nonEmptyJSON(value) {
				return nil, fmt.Errorf("unsupported usage detail %q", key)
			}
			continue
		}
		var n int
		if err := json.Unmarshal(value, &n); err != nil {
			return nil, fmt.Errorf("usage detail %s must be an integer", key)
		}
		out[key] = n
	}
	return out, nil
}

func chatUsageToResponses(raw json.RawMessage) (map[string]any, error) {
	obj, err := usageObject(raw)
	if err != nil {
		return nil, err
	}
	prompt, err := numberField(obj, "prompt_tokens")
	if err != nil {
		return nil, err
	}
	completion, err := numberField(obj, "completion_tokens")
	if err != nil {
		return nil, err
	}
	total, err := numberField(obj, "total_tokens")
	if err != nil {
		return nil, err
	}
	inDetails, err := detailsField(obj["prompt_tokens_details"], "cached_tokens")
	if err != nil {
		return nil, err
	}
	outDetails, err := detailsField(obj["completion_tokens_details"], "reasoning_tokens")
	if err != nil {
		return nil, err
	}
	result := map[string]any{"input_tokens": prompt, "output_tokens": completion, "total_tokens": total}
	if inDetails != nil {
		result["input_tokens_details"] = inDetails
	}
	if outDetails != nil {
		result["output_tokens_details"] = outDetails
	}
	return result, nil
}

func responsesUsageToChat(raw json.RawMessage) (map[string]any, error) {
	obj, err := usageObject(raw)
	if err != nil {
		return nil, err
	}
	input, err := numberField(obj, "input_tokens")
	if err != nil {
		return nil, err
	}
	output, err := numberField(obj, "output_tokens")
	if err != nil {
		return nil, err
	}
	total, err := numberField(obj, "total_tokens")
	if err != nil {
		return nil, err
	}
	inDetails, err := detailsField(obj["input_tokens_details"], "cached_tokens")
	if err != nil {
		return nil, err
	}
	outDetails, err := detailsField(obj["output_tokens_details"], "reasoning_tokens")
	if err != nil {
		return nil, err
	}
	result := map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": total}
	if inDetails != nil {
		result["prompt_tokens_details"] = inDetails
	}
	if outDetails != nil {
		result["completion_tokens_details"] = outDetails
	}
	return result, nil
}

func validateChatResponseWire(raw []byte) error {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return fmt.Errorf("invalid Chat response")
	}
	var choices []json.RawMessage
	if json.Unmarshal(envelope["choices"], &choices) != nil {
		return fmt.Errorf("Chat choices must be an array")
	}
	for _, rawChoice := range choices {
		var choice map[string]json.RawMessage
		if json.Unmarshal(rawChoice, &choice) != nil {
			return fmt.Errorf("Chat choice must be an object")
		}
		if err := rejectWireKeys(choice, "index", "message", "finish_reason"); err != nil {
			return err
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(choice["message"], &message) != nil {
			return fmt.Errorf("Chat message must be an object")
		}
		if err := rejectWireKeys(message, "role", "content", "tool_calls"); err != nil {
			return err
		}
		if calls, ok := message["tool_calls"]; ok {
			var list []json.RawMessage
			if json.Unmarshal(calls, &list) != nil {
				return fmt.Errorf("tool_calls must be an array")
			}
			for _, rawCall := range list {
				var call map[string]json.RawMessage
				if json.Unmarshal(rawCall, &call) != nil {
					return fmt.Errorf("tool call must be an object")
				}
				if err := rejectWireKeys(call, "id", "type", "function"); err != nil {
					return err
				}
				var fn map[string]json.RawMessage
				if json.Unmarshal(call["function"], &fn) != nil {
					return fmt.Errorf("function must be an object")
				}
				if err := rejectWireKeys(fn, "name", "arguments"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateResponsesOutputWire(raw []byte) error {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return fmt.Errorf("invalid Responses response")
	}
	var output []json.RawMessage
	if json.Unmarshal(envelope["output"], &output) != nil {
		return fmt.Errorf("Responses output must be an array")
	}
	for _, rawItem := range output {
		var item map[string]json.RawMessage
		if json.Unmarshal(rawItem, &item) != nil {
			return fmt.Errorf("Responses output item must be an object")
		}
		var typ string
		_ = json.Unmarshal(item["type"], &typ)
		switch typ {
		case "message":
			if err := rejectWireKeys(item, "type", "role", "content", "id", "status"); err != nil {
				return err
			}
			var content []json.RawMessage
			if json.Unmarshal(item["content"], &content) != nil {
				return fmt.Errorf("Responses message content must be an array")
			}
			for _, rawBlock := range content {
				var block map[string]json.RawMessage
				if json.Unmarshal(rawBlock, &block) != nil {
					return fmt.Errorf("Responses content block must be an object")
				}
				if err := rejectWireKeys(block, "type", "text", "annotations", "logprobs", "refusal"); err != nil {
					return err
				}
				for _, key := range []string{"annotations", "logprobs", "refusal"} {
					if nonEmptyJSON(block[key]) {
						return fmt.Errorf("unsupported Responses content metadata %q", key)
					}
				}
			}
		case "function_call":
			if err := rejectWireKeys(item, "type", "id", "call_id", "name", "arguments", "status"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported Responses output item type %q", typ)
		}
	}
	return nil
}

func rejectWireKeys(obj map[string]json.RawMessage, allowed ...string) error {
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
	}
	for key := range obj {
		if !set[key] {
			return fmt.Errorf("unsupported response field %q", key)
		}
	}
	return nil
}

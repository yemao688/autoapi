package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func chatSSEEvent(delta string, finish string) string {
	choice := map[string]any{"index": 0, "delta": map[string]any{}}
	if delta != "" {
		choice["delta"] = map[string]any{"content": delta}
	}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	b, _ := json.Marshal(map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk", "choices": []any{choice}})
	return "data: " + string(b) + "\n\n"
}

func chatToolSSE(index int, id, name, args string) string {
	tc := map[string]any{"index": index}
	if id != "" {
		tc["id"] = id
	}
	if name != "" || args != "" {
		fn := map[string]any{}
		if name != "" {
			fn["name"] = name
		}
		if args != "" {
			fn["arguments"] = args
		}
		tc["function"] = fn
	}
	choice := map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{tc}}}
	b, _ := json.Marshal(map[string]any{"id": "chatcmpl-1", "choices": []any{choice}})
	return "data: " + string(b) + "\n\n"
}

func TestChatToResponsesStreamTextAndUsage(t *testing.T) {
	c := newChatToResponsesStreamConverter()
	input := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		chatSSEEvent("hello", "") +
		chatSSEEvent(" world", "stop") +
		"data: {\"id\":\"chatcmpl-1\",\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":1},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n" +
		"data: [DONE]\n\n"
	out, err := c.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, ev := range []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done", "response.completed"} {
		if !strings.Contains(s, ev) {
			t.Fatalf("missing event %s in %s", ev, s)
		}
	}
	if !strings.Contains(s, "\"stop_reason\":\"end_turn\"") {
		t.Fatalf("missing end_turn: %s", s)
	}
	if !strings.Contains(s, "\"input_tokens\":5") || !strings.Contains(s, "\"output_tokens\":2") || !strings.Contains(s, "\"total_tokens\":7") {
		t.Fatalf("usage not mapped: %s", s)
	}
	if !strings.Contains(s, "\"cached_tokens\":1") || !strings.Contains(s, "\"reasoning_tokens\":1") {
		t.Fatalf("usage details not preserved: %s", s)
	}
	if strings.Count(s, "event: response.output_text.delta") != 2 {
		t.Fatalf("expected 2 text deltas, got %d in %s", strings.Count(s, "event: response.output_text.delta"), s)
	}
}

func TestChatToResponsesStreamToolCalls(t *testing.T) {
	c := newChatToResponsesStreamConverter()
	input := chatToolSSE(0, "call_abc", "lookup", "") +
		chatToolSSE(0, "", "", "{\"q\":\"") +
		chatToolSSE(0, "", "", "x") +
		chatToolSSE(0, "", "", "\"}") +
		chatSSEEvent("", "tool_calls") +
		"data: [DONE]\n\n"
	out, err := c.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "\"type\":\"function_call\"") {
		t.Fatalf("missing function_call item: %s", s)
	}
	if !strings.Contains(s, "\"id\":\"fc_0\"") {
		t.Fatalf("function_call item id not fc_0: %s", s)
	}
	if !strings.Contains(s, "\"call_id\":\"call_abc\"") {
		t.Fatalf("call_id not preserved: %s", s)
	}
	if !strings.Contains(s, "response.function_call_arguments.delta") {
		t.Fatalf("missing function_call_arguments.delta: %s", s)
	}
	if !strings.Contains(s, "\"stop_reason\":\"tool_use\"") {
		t.Fatalf("missing tool_use stop_reason: %s", s)
	}
	if strings.Count(s, "event: response.function_call_arguments.delta") != 3 {
		t.Fatalf("expected 3 argument deltas, got %d in %s", strings.Count(s, "event: response.function_call_arguments.delta"), s)
	}
}

func TestChatToResponsesStreamLengthAndContentFilter(t *testing.T) {
	t.Run("length", func(t *testing.T) {
		c := newChatToResponsesStreamConverter()
		out, err := c.Write([]byte(chatSSEEvent("hi", "length") + "data: [DONE]\n\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "response.incomplete") || !strings.Contains(string(out), "max_output_tokens") {
			t.Fatalf("expected incomplete max_output_tokens: %s", out)
		}
	})
	t.Run("content_filter", func(t *testing.T) {
		c := newChatToResponsesStreamConverter()
		out, err := c.Write([]byte(chatSSEEvent("hi", "content_filter") + "data: [DONE]\n\n"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "response.incomplete") || !strings.Contains(string(out), "content_filter") {
			t.Fatalf("expected incomplete content_filter: %s", out)
		}
	})
}

func TestChatToResponsesStreamNoDoneCloseIsIncomplete(t *testing.T) {
	c := newChatToResponsesStreamConverter()
	_, err := c.Write([]byte(chatSSEEvent("hi", "stop")))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Close()
	if err == nil {
		t.Fatal("expected error for missing terminal event")
	}
	if strings.Contains(string(out), "response.completed") || strings.Contains(string(out), "response.incomplete") {
		t.Fatalf("close synthesized terminal after truncated stream: %s", out)
	}
}

func TestChatToResponsesStreamRejectsRefusalAndUnknownDelta(t *testing.T) {
	t.Run("refusal", func(t *testing.T) {
		c := newChatToResponsesStreamConverter()
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"refusal": "no"}}}})
		_, err := c.Write([]byte("data: " + string(b) + "\n\n"))
		if err == nil || !strings.Contains(err.Error(), "refusal") {
			t.Fatalf("refusal delta not rejected: %v", err)
		}
	})
	t.Run("unknown delta field", func(t *testing.T) {
		c := newChatToResponsesStreamConverter()
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning": "x"}}}})
		_, err := c.Write([]byte("data: " + string(b) + "\n\n"))
		if err == nil || !strings.Contains(err.Error(), "unsupported Chat stream delta field") {
			t.Fatalf("unknown delta field not rejected: %v", err)
		}
	})
}

func TestChatToResponsesStreamSSEFramingAndChunking(t *testing.T) {
	input := "event: message\r\ndata: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\r\n\r\n" +
		"event: message\r\ndata: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"b\"},\"finish_reason\":\"stop\"}]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n"
	var out []byte
	c := newChatToResponsesStreamConverter()
	for i := 0; i < len(input); i++ {
		b, err := c.Write([]byte{input[i]})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
	tail, err := c.Close()
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, tail...)
	if !strings.Contains(string(out), "response.completed") {
		t.Fatalf("chunked CRLF stream not converted: %s", out)
	}
}

func TestChatToResponsesStreamErrorDoesNotSynthesizeTerminal(t *testing.T) {
	c := newChatToResponsesStreamConverter()
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"audio": map[string]any{}}}}})
	_, err := c.Write([]byte("data: " + string(b) + "\n\n"))
	if err == nil {
		t.Fatal("expected conversion error")
	}
	out, _ := c.Close()
	if strings.Contains(string(out), "response.completed") || strings.Contains(string(out), "response.incomplete") {
		t.Fatalf("close synthesized terminal after error: %s", out)
	}
}

func responsesSSE(ev, payload string) string {
	return "event: " + ev + "\ndata: " + payload + "\n\n"
}

func TestResponsesToChatStreamTextAndUsage(t *testing.T) {
	c := newResponsesToChatStreamConverter()
	input := responsesSSE("response.created", `{"type":"response.created","response":{"id":"resp_1","model":"m","usage":{"input_tokens":3}}}`) +
		responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"message","id":"msg_1"}}`) +
		responsesSSE("response.content_part.added", `{"type":"response.content_part.added","part":{"type":"output_text","annotations":[]}}`) +
		responsesSSE("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`) +
		responsesSSE("response.output_text.done", `{"type":"response.output_text.done"}`) +
		responsesSSE("response.content_part.done", `{"type":"response.content_part.done"}`) +
		responsesSSE("response.output_item.done", `{"type":"response.output_item.done"}`) +
		responsesSSE("response.completed", `{"type":"response.completed","response":{"usage":{"output_tokens":2,"total_tokens":5,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":1}},"stop_reason":"end_turn"}}`)
	out, err := c.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "\"role\":\"assistant\"") {
		t.Fatalf("missing role delta: %s", s)
	}
	if !strings.Contains(s, "\"content\":\"hi\"") {
		t.Fatalf("missing content delta: %s", s)
	}
	if !strings.Contains(s, "\"prompt_tokens\":3") || !strings.Contains(s, "\"completion_tokens\":2") || !strings.Contains(s, "\"total_tokens\":5") {
		t.Fatalf("usage not mapped: %s", s)
	}
	if !strings.Contains(s, "\"cached_tokens\":1") || !strings.Contains(s, "\"reasoning_tokens\":1") {
		t.Fatalf("usage details not preserved: %s", s)
	}
	if !strings.Contains(s, "\"finish_reason\":\"stop\"") {
		t.Fatalf("missing stop finish_reason: %s", s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("missing [DONE]: %s", s)
	}
}

func TestResponsesToChatStreamToolUse(t *testing.T) {
	c := newResponsesToChatStreamConverter()
	input := responsesSSE("response.created", `{"type":"response.created","response":{"id":"resp_1","model":"m"}}`) +
		responsesSSE("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_0","call_id":"call_abc","name":"lookup"}}`) +
		responsesSSE("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"q\":\"x"}`) +
		responsesSSE("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"}"}`) +
		responsesSSE("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","output_index":0}`) +
		responsesSSE("response.output_item.done", `{"type":"response.output_item.done","output_index":0}`) +
		responsesSSE("response.completed", `{"type":"response.completed","response":{"usage":{"output_tokens":1},"stop_reason":"tool_use"}}`)
	out, err := c.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "\"id\":\"call_abc\"") {
		t.Fatalf("call_id not mapped to Chat tool id: %s", s)
	}
	if !strings.Contains(s, "\"name\":\"lookup\"") {
		t.Fatalf("tool name not emitted: %s", s)
	}
	args := accumulateChatToolArguments(s)
	if args[0] != `{"q":"x"}` {
		t.Fatalf("tool arguments not accumulated: got %q", args[0])
	}
	if !strings.Contains(s, "\"finish_reason\":\"tool_calls\"") {
		t.Fatalf("missing tool_calls finish_reason: %s", s)
	}
}

func TestResponsesToChatStreamIncomplete(t *testing.T) {
	t.Run("max_output_tokens", func(t *testing.T) {
		c := newResponsesToChatStreamConverter()
		input := responsesSSE("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
			responsesSSE("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`) +
			responsesSSE("response.incomplete", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`)
		out, err := c.Write([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "\"finish_reason\":\"length\"") {
			t.Fatalf("expected length finish_reason: %s", out)
		}
	})
	t.Run("content_filter", func(t *testing.T) {
		c := newResponsesToChatStreamConverter()
		input := responsesSSE("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
			responsesSSE("response.incomplete", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`)
		out, err := c.Write([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "\"finish_reason\":\"content_filter\"") {
			t.Fatalf("expected content_filter finish_reason: %s", out)
		}
	})
}

func TestResponsesToChatStreamRejectsFailedAndUnsupported(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		c := newResponsesToChatStreamConverter()
		_, err := c.Write([]byte(responsesSSE("response.failed", `{"type":"response.failed"}`)))
		if err == nil || !strings.Contains(err.Error(), "failed") {
			t.Fatalf("response.failed not rejected: %v", err)
		}
	})
	t.Run("refusal item", func(t *testing.T) {
		c := newResponsesToChatStreamConverter()
		_, err := c.Write([]byte(responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"refusal"}}`)))
		if err == nil || !strings.Contains(err.Error(), "unsupported Responses output item type") {
			t.Fatalf("refusal item not rejected: %v", err)
		}
	})
	t.Run("reasoning item", func(t *testing.T) {
		c := newResponsesToChatStreamConverter()
		_, err := c.Write([]byte(responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"reasoning"}}`)))
		if err == nil || !strings.Contains(err.Error(), "unsupported Responses output item type") {
			t.Fatalf("reasoning item not rejected: %v", err)
		}
	})
	t.Run("unknown content part", func(t *testing.T) {
		c := newResponsesToChatStreamConverter()
		_, err := c.Write([]byte(responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"message"}}`) +
			responsesSSE("response.content_part.added", `{"type":"response.content_part.added","part":{"type":"reasoning"}}`)))
		if err == nil || !strings.Contains(err.Error(), "unsupported Responses content part type") {
			t.Fatalf("reasoning content part not rejected: %v", err)
		}
	})
}

func TestResponsesToChatStreamNoTerminalCloseOmitsDone(t *testing.T) {
	c := newResponsesToChatStreamConverter()
	_, err := c.Write([]byte(responsesSSE("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
		responsesSSE("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Close()
	if err == nil {
		t.Fatal("expected error for missing terminal event")
	}
	if strings.Contains(string(out), "[DONE]") || strings.Contains(string(out), "finish_reason") {
		t.Fatalf("non-terminal close should not emit terminal: %s", out)
	}
}

func TestResponsesToChatStreamSSEFraming(t *testing.T) {
	input := "event: response.created\r\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"m\"}}\r\n\r\n" +
		"event: response.output_text.delta\r\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\r\n\r\n" +
		"event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1},\"stop_reason\":\"end_turn\"}}\r\n\r\n"
	var out []byte
	c := newResponsesToChatStreamConverter()
	for i := 0; i < len(input); i++ {
		b, err := c.Write([]byte{input[i]})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
	if !strings.Contains(string(out), "\"finish_reason\":\"stop\"") || !strings.Contains(string(out), "data: [DONE]") {
		t.Fatalf("CRLF chunked stream not converted: %s", out)
	}
}

func TestResponsesToChatStreamErrorDoesNotSynthesizeTerminal(t *testing.T) {
	c := newResponsesToChatStreamConverter()
	_, err := c.Write([]byte(responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"reasoning"}}`)))
	if err == nil {
		t.Fatal("expected conversion error")
	}
	out, _ := c.Close()
	if strings.Contains(string(out), "[DONE]") {
		t.Fatalf("close synthesized terminal after error: %s", out)
	}
}

func accumulateChatToolArguments(out string) map[int]string {
	args := map[int]string{}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var v struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Index    int `json:"index"`
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &v) != nil {
			continue
		}
		if len(v.Choices) == 0 {
			continue
		}
		for _, tc := range v.Choices[0].Delta.ToolCalls {
			args[tc.Index] += tc.Function.Arguments
		}
	}
	return args
}

func TestResponsesToChatStreamRejectsMultipleMessages(t *testing.T) {
	c := newResponsesToChatStreamConverter()
	input := responsesSSE("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
		responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"message","id":"msg1"}}`) +
		responsesSSE("response.output_text.delta", `{"type":"response.output_text.delta","delta":"hi"}`) +
		responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"message","id":"msg2"}}`)
	_, err := c.Write([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "multiple message") {
		t.Fatalf("expected multiple message error, got: %v", err)
	}
}

func TestResponsesToChatStreamRejectsMessageFunctionCallMix(t *testing.T) {
	c := newResponsesToChatStreamConverter()
	input := responsesSSE("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
		responsesSSE("response.output_item.added", `{"type":"response.output_item.added","item":{"type":"message","id":"msg1"}}`) +
		responsesSSE("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_0","call_id":"call_a","name":"lookup"}}`)
	_, err := c.Write([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "cannot be mixed") {
		t.Fatalf("expected mix error, got: %v", err)
	}
}

func TestChatToResponsesStreamToolCallIDConsistency(t *testing.T) {
	t.Run("id changes for same index", func(t *testing.T) {
		c := newChatToResponsesStreamConverter()
		input := chatToolSSE(0, "call_a", "lookup", "") + chatToolSSE(0, "call_b", "", "")
		_, err := c.Write([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "tool call id changed") {
			t.Fatalf("expected id changed error, got: %v", err)
		}
	})
	t.Run("duplicate ids across indices", func(t *testing.T) {
		c := newChatToResponsesStreamConverter()
		input := chatToolSSE(0, "call_x", "a", "") + chatToolSSE(1, "call_x", "b", "")
		_, err := c.Write([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "duplicate tool call id") {
			t.Fatalf("expected duplicate id error, got: %v", err)
		}
	})
}

func TestChatToResponsesStreamArgumentsDeltaRequiresFunctionCall(t *testing.T) {
	c := newChatToResponsesStreamConverter()
	tc := map[string]any{"index": 0, "function": map[string]any{"arguments": "{}"}}
	b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{tc}}}}})
	_, err := c.Write([]byte("data: " + string(b) + "\n\n"))
	if err == nil || !strings.Contains(err.Error(), "function_call_arguments.delta without function_call") {
		t.Fatalf("expected error for orphan arguments delta, got: %v", err)
	}
}

func TestResponsesToChatStreamFunctionCallRequiresCallID(t *testing.T) {
	c := newResponsesToChatStreamConverter()
	input := responsesSSE("response.created", `{"type":"response.created","response":{"id":"r","model":"m"}}`) +
		responsesSSE("response.output_item.added", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_0","name":"lookup"}}`)
	_, err := c.Write([]byte(input))
	if err == nil || !strings.Contains(err.Error(), "function_call requires call_id") {
		t.Fatalf("expected call_id required error, got: %v", err)
	}
}

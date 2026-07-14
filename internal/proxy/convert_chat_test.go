package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatToResponsesRequestPreservesRolesTokensAndTools(t *testing.T) {
	body := []byte(`{"model":"client","temperature":0.2,"top_p":0.8,"max_completion_tokens":42,"tools":[{"type":"function","function":{"name":"lookup","description":"find","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}],"messages":[{"role":"system","content":"system"},{"role":"developer","content":"developer"},{"role":"user","content":"hello"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}]}`)
	out, err := chatToResponsesRequest(body, "upstream")
	if err != nil {
		t.Fatalf("chatToResponsesRequest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "upstream" || got["max_output_tokens"] != float64(42) {
		t.Fatalf("header mapping: %#v", got)
	}
	input := got["input"].([]any)
	if len(input) != 5 {
		t.Fatalf("input length=%d: %#v", len(input), input)
	}
	if input[0].(map[string]any)["role"] != "system" || input[1].(map[string]any)["role"] != "developer" {
		t.Fatalf("roles not preserved: %#v", input)
	}
	if input[3].(map[string]any)["type"] != "function_call" || input[4].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("tool order/types: %#v", input)
	}
	if input[0].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] != "input_text" || input[2].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] != "input_text" {
		t.Fatalf("system/user content shape: %#v", input)
	}
}

func TestChatAssistantResponsesInputUsesOutputTextAndCallIDOnly(t *testing.T) {
	out, err := chatToResponsesRequest([]byte(`{"model":"m","messages":[{"role":"assistant","content":"history","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`), "u")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	input := got["input"].([]any)
	content := input[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if content["type"] != "output_text" {
		t.Fatalf("assistant content=%#v", content)
	}
	call := input[1].(map[string]any)
	if call["call_id"] != "c" {
		t.Fatalf("call=%#v", call)
	}
	if _, ok := call["id"]; ok {
		t.Fatalf("unexpected Responses item id: %#v", call)
	}
}

func TestResponsesToChatRequestInstructionsAndTools(t *testing.T) {
	body := []byte(`{"model":"client","instructions":"be concise","max_output_tokens":11,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},{"type":"function_call_output","call_id":"call_1","output":"ok"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)
	out, err := responsesToChatRequest(body, "upstream")
	if err != nil {
		t.Fatalf("responsesToChatRequest: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	msgs := got["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "developer" || msgs[0].(map[string]any)["content"] != "be concise" {
		t.Fatalf("instructions not mapped first: %#v", msgs)
	}
	if msgs[3].(map[string]any)["role"] != "tool" || msgs[3].(map[string]any)["tool_call_id"] != "call_1" {
		t.Fatalf("tool output not mapped: %#v", msgs)
	}
}

func TestResponsesFunctionCallRequiresCallIDAndToolOutputString(t *testing.T) {
	if _, err := responsesToChatRequest([]byte(`{"model":"m","input":[{"type":"function_call","id":"only-id","name":"f","arguments":"{}"} ]}`), "u"); err == nil {
		t.Fatal("function_call with only id accepted")
	}
	if _, err := responsesToChatRequest([]byte(`{"model":"m","input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":{}}]}`), "u"); err == nil {
		t.Fatal("object function_call_output accepted")
	}
}

func TestChatResponseToResponsesStatusUsageAndTools(t *testing.T) {
	body := []byte(`{"id":"chat_1","choices":[{"index":0,"message":{"role":"assistant","content":"done","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)
	out, err := chatToResponsesResponse(body, "client")
	if err != nil {
		t.Fatalf("chatToResponsesResponse: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["status"] != "completed" || got["model"] != "client" {
		t.Fatalf("status/model: %#v", got)
	}
	if got["usage"].(map[string]any)["input_tokens"] != float64(2) {
		t.Fatalf("usage: %#v", got["usage"])
	}
	output := got["output"].([]any)
	if output[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("function call output: %#v", output)
	}
}

func TestChatContentFilterMapsToResponsesIncomplete(t *testing.T) {
	out, err := chatToResponsesResponse([]byte(`{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"partial"},"finish_reason":"content_filter"}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"status":"incomplete"`) || !strings.Contains(string(out), `"reason":"content_filter"`) {
		t.Fatalf("output=%s", out)
	}
}

func TestResponsesToChatResponseFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"failed":            `{"id":"r","status":"failed","output":[]}`,
		"unknown item":      `{"id":"r","status":"completed","output":[{"type":"reasoning"}]}`,
		"bad arguments":     `{"id":"r","status":"completed","output":[{"type":"function_call","call_id":"c","name":"f","arguments":"[]"}]}`,
		"multiple messages": `{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"b"}]}]}`,
		"refusal":           `{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"no"}]}]}`,
		"annotations":       `{"id":"r","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a","annotations":[{}]}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := responsesToChatResponse([]byte(body), "m"); err == nil {
				t.Fatal("expected conversion error")
			}
		})
	}
}

func TestChatResponseFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"array content":  `{"id":"r","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"x"}]},"finish_reason":"stop"}]}`,
		"refusal":        `{"id":"r","choices":[{"index":0,"message":{"role":"assistant","content":null,"refusal":"no"},"finish_reason":"stop"}]}`,
		"wrong index":    `{"id":"r","choices":[{"index":1,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`,
		"missing finish": `{"id":"r","choices":[{"index":0,"message":{"role":"assistant","content":"x"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := chatToResponsesResponse([]byte(body), "m"); err == nil {
				t.Fatal("expected fail-closed response error")
			}
		})
	}
}

func TestChatRequestRejectsConflictingTokensAndInvalidCallIDs(t *testing.T) {
	if _, err := chatToResponsesRequest([]byte(`{"model":"m","max_tokens":1,"max_completion_tokens":2,"messages":[]}`), "u"); err == nil {
		t.Fatal("conflicting token fields accepted")
	}
	for _, body := range []string{
		`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"[]"}}]}]}`,
		`{"model":"m","messages":[{"role":"tool","tool_call_id":"missing","content":"x"}]}`,
	} {
		if _, err := chatToResponsesRequest([]byte(body), "u"); err == nil {
			t.Fatalf("invalid call body accepted: %s", body)
		}
	}
}

func TestChatToolCallsAndResultsRemainOrderedAndRequireSameBodyHistory(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"assistant","content":"working","tool_calls":[{"id":"a","type":"function","function":{"name":"one","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"two","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"one-result"},{"role":"tool","tool_call_id":"b","content":"two-result"}]}`)
	out, err := chatToResponsesRequest(body, "u")
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	var request map[string]any
	if err := json.Unmarshal(out, &request); err != nil {
		t.Fatal(err)
	}
	input := request["input"].([]any)
	if input[1].(map[string]any)["call_id"] != "a" || input[2].(map[string]any)["call_id"] != "b" || input[3].(map[string]any)["call_id"] != "a" || input[4].(map[string]any)["call_id"] != "b" {
		t.Fatalf("input order=%#v", input)
	}
	previousResponseBody := `{"model":"m","input":[{"type":"function_call_output","call_id":"historical","output":"x"}]}`
	if _, err := responsesToChatRequest([]byte(previousResponseBody), "u"); err == nil {
		t.Fatal("historical function_call_output accepted without same-body call")
	}
}

func TestChatConverterDoesNotAcceptUnknownTextSemantics(t *testing.T) {
	_, err := chatTextContent([]byte(`[{"type":"image_url","image_url":{"url":"x"}}]`))
	if err == nil || !strings.Contains(err.Error(), "text") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConversionAdapterChatEdgesAreNonStreaming(t *testing.T) {
	chatPrep, err := (conversionAdapter{from: ProtocolOpenAIChat, to: ProtocolOpenAIResponses}).PrepareAttempt([]byte(`{"model":"m","messages":[]}`), candidate{modelName: "upstream", ruleLabel: "m"})
	if err != nil {
		t.Fatalf("Chat adapter: %v", err)
	}
	if chatPrep.Path != "/v1/responses" || chatPrep.NewStreamConverter != nil || chatPrep.ConvertResponse == nil {
		t.Fatalf("Chat adapter preparation: %+v", chatPrep)
	}
	responsesPrep, err := (conversionAdapter{from: ProtocolOpenAIResponses, to: ProtocolOpenAIChat}).PrepareAttempt([]byte(`{"model":"m","input":"hi"}`), candidate{modelName: "upstream", ruleLabel: "m"})
	if err != nil {
		t.Fatalf("Responses adapter: %v", err)
	}
	if responsesPrep.Path != "/v1/chat/completions" || responsesPrep.NewStreamConverter != nil || responsesPrep.ConvertResponse == nil {
		t.Fatalf("Responses adapter preparation: %+v", responsesPrep)
	}
}

func TestFunctionToolStrictAndUnsupportedBehaviorFields(t *testing.T) {
	out, err := chatToResponsesRequest([]byte(`{"model":"m","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"},"strict":true}}],"messages":[]}`), "u")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"strict":true`) {
		t.Fatalf("strict was not preserved: %s", out)
	}
	if _, err := responsesToChatRequest([]byte(`{"model":"m","tools":[{"type":"function","name":"f","parameters":{"type":"object"},"output_schema":{}}],"input":"hi"}`), "u"); err == nil {
		t.Fatal("unsupported Responses tool behavior silently dropped")
	}
}

func TestUsageDetailsRoundTripAndUnsupportedDetails(t *testing.T) {
	chat, err := chatToResponsesResponse([]byte(`{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(chat), `"cached_tokens":4`) || !strings.Contains(string(chat), `"reasoning_tokens":5`) {
		t.Fatalf("details lost: %s", chat)
	}
	if _, err := chatToResponsesResponse([]byte(`{"id":"c","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"audio_tokens":1}}}`), "m"); err == nil {
		t.Fatal("unsupported usage details accepted")
	}
}

package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessagesToResponsesRequestConversion(t *testing.T) {
	body := []byte(`{
		"model":"client-model","max_tokens":123,"system":"be brief","temperature":0.2,"top_p":0.9,
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"x"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":{"result":"y"}}]}
		],
		"tools":[{"name":"lookup","description":"Lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]
	}`)
	out, err := messagesToResponsesRequest(body, "upstream-model")
	if err != nil {
		t.Fatalf("messagesToResponsesRequest: %v", err)
	}
	var got map[string]any
	mustUnmarshal(t, out, &got)
	if got["model"] != "upstream-model" || got["instructions"] != "be brief" {
		t.Fatalf("unexpected model/instructions: %s", out)
	}
	if int(got["max_output_tokens"].(float64)) != 123 {
		t.Fatalf("max_output_tokens not mapped: %s", out)
	}
	if !strings.Contains(string(out), `"function_call"`) || !strings.Contains(string(out), `"function_call_output"`) {
		t.Fatalf("tool roundtrip items missing: %s", out)
	}
	if !strings.Contains(string(out), `"parameters"`) || !strings.Contains(string(out), `"type":"function"`) {
		t.Fatalf("tools not mapped: %s", out)
	}
	if !strings.Contains(string(out), `"input_text"`) || !strings.Contains(string(out), `"output_text"`) {
		t.Fatalf("text blocks not mapped: %s", out)
	}
}

func TestResponsesToMessagesRequestConversion(t *testing.T) {
	body := []byte(`{
		"model":"client-model","instructions":"be brief","max_output_tokens":77,
		"tool_choice":"auto","metadata":{"client":"sdk"},
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"id":"item_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"},
			{"type":"function_call_output","call_id":"call_1","output":{"result":"y"}}
		],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]
	}`)
	out, err := responsesToMessagesRequest(body, "claude-upstream")
	if err != nil {
		t.Fatalf("responsesToMessagesRequest: %v", err)
	}
	if !strings.Contains(string(out), `"model":"claude-upstream"`) || !strings.Contains(string(out), `"system":"be brief"`) {
		t.Fatalf("model/system not mapped: %s", out)
	}
	if !strings.Contains(string(out), `"max_tokens":77`) || !strings.Contains(string(out), `"input_schema"`) {
		t.Fatalf("tokens/tools not mapped: %s", out)
	}
	if !strings.Contains(string(out), `"tool_choice":{"type":"auto"}`) || !strings.Contains(string(out), `"metadata":{"client":"sdk"}`) {
		t.Fatalf("tool_choice/metadata not mapped: %s", out)
	}
	if !strings.Contains(string(out), `"tool_use"`) || !strings.Contains(string(out), `"tool_result"`) {
		t.Fatalf("tool items missing: %s", out)
	}
	var got map[string]any
	mustUnmarshal(t, out, &got)
	messages := got["messages"].([]any)
	toolInput := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)["input"]
	if _, ok := toolInput.(map[string]any); !ok {
		t.Fatalf("tool_use input = %#v, want object", toolInput)
	}
	if id := messages[1].(map[string]any)["content"].([]any)[0].(map[string]any)["id"]; id != "call_1" {
		t.Fatalf("tool_use id = %#v, want call_1", id)
	}
}

func TestResponsesToMessagesResponseConversion(t *testing.T) {
	body := []byte(`{"id":"resp_1","model":"upstream","status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},{"id":"item_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}],"usage":{"input_tokens":3,"output_tokens":4}}`)
	out, err := responsesToMessagesResponse(body, "client-model")
	if err != nil {
		t.Fatalf("responsesToMessagesResponse: %v", err)
	}
	if !strings.Contains(string(out), `"model":"client-model"`) || !strings.Contains(string(out), `"stop_reason":"max_tokens"`) {
		t.Fatalf("model/status not mapped: %s", out)
	}
	if !strings.Contains(string(out), `"tool_use"`) || !strings.Contains(string(out), `"input_tokens":3`) || !strings.Contains(string(out), `"output_tokens":4`) {
		t.Fatalf("content/usage not mapped: %s", out)
	}
	var got map[string]any
	mustUnmarshal(t, out, &got)
	blocks := got["content"].([]any)
	toolInput := blocks[1].(map[string]any)["input"]
	if _, ok := toolInput.(map[string]any); !ok {
		t.Fatalf("tool_use input = %#v, want object", toolInput)
	}
	if id := blocks[1].(map[string]any)["id"]; id != "call_1" {
		t.Fatalf("tool_use id = %#v, want call_1", id)
	}
}

func TestMessagesToResponsesResponseConversion(t *testing.T) {
	body := []byte(`{"id":"msg_1","model":"upstream","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"x"}}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":6}}`)
	out, err := messagesToResponsesResponse(body, "client-model")
	if err != nil {
		t.Fatalf("messagesToResponsesResponse: %v", err)
	}
	if !strings.Contains(string(out), `"model":"client-model"`) || !strings.Contains(string(out), `"status":"completed"`) {
		t.Fatalf("model/status not mapped: %s", out)
	}
	if !strings.Contains(string(out), `"function_call"`) || !strings.Contains(string(out), `"input_tokens":5`) || !strings.Contains(string(out), `"output_tokens":6`) {
		t.Fatalf("output/usage not mapped: %s", out)
	}
	var got map[string]any
	mustUnmarshal(t, out, &got)
	functionCall := got["output"].([]any)[1].(map[string]any)
	arguments, ok := functionCall["arguments"].(string)
	if !ok {
		t.Fatalf("arguments = %#v, want string", functionCall["arguments"])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil || parsed["q"] != "x" {
		t.Fatalf("arguments = %q, parsed = %#v, err = %v", arguments, parsed, err)
	}
}

func TestStopReasonStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status string
		stop   string
	}{
		{"completed", "end_turn"},
		{"incomplete", "max_tokens"},
		{"failed", "error"},
		{"unknown", "end_turn"},
	} {
		if got := responsesStatusToMessagesStop(tc.status); got != tc.stop {
			t.Errorf("responsesStatusToMessagesStop(%q) = %q, want %q", tc.status, got, tc.stop)
		}
	}
	for _, tc := range []struct {
		stop   string
		status string
	}{
		{"max_tokens", "incomplete"},
		{"error", "failed"},
		{"end_turn", "completed"},
		{"unknown", "completed"},
	} {
		if got := messagesStopToResponsesStatus(tc.stop); got != tc.status {
			t.Errorf("messagesStopToResponsesStatus(%q) = %q, want %q", tc.stop, got, tc.status)
		}
	}
}

func TestProtocolConversionRejections(t *testing.T) {
	cases := []struct {
		name string
		fn   func() error
		want string
	}{
		{"thinking block", func() error {
			_, err := messagesToResponsesRequest([]byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"thinking","text":"x"}]}]}`), "u")
			return err
		}, "thinking"},
		{"image block", func() error {
			_, err := messagesToResponsesRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`), "u")
			return err
		}, "image"},
		{"stop_sequences", func() error {
			_, err := messagesToResponsesRequest([]byte(`{"model":"m","stop_sequences":["x"],"messages":[]}`), "u")
			return err
		}, "stop_sequences"},
		{"previous_response_id", func() error {
			_, err := responsesToMessagesRequest([]byte(`{"model":"m","previous_response_id":"r","input":[]}`), "u")
			return err
		}, "previous_response_id"},
		{"background", func() error {
			_, err := responsesToMessagesRequest([]byte(`{"model":"m","background":true,"input":[]}`), "u")
			return err
		}, "background"},
		{"invalid arguments", func() error {
			_, err := responsesToMessagesRequest([]byte(`{"model":"m","input":[{"type":"function_call","call_id":"c","arguments":"not-json"}]}`), "u")
			return err
		}, "invalid Responses function_call arguments"},
		{"assistant unknown block", func() error {
			_, err := messagesToResponsesRequest([]byte(`{"model":"m","messages":[{"role":"assistant","content":[{"type":"custom"}]}]}`), "u")
			return err
		}, "unsupported content block type"},
		{"user unknown block", func() error {
			_, err := messagesToResponsesRequest([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"custom"}]}]}`), "u")
			return err
		}, "unsupported content block type"},
		{"unknown Responses input item", func() error {
			_, err := responsesToMessagesRequest([]byte(`{"model":"m","input":[{"type":"reasoning"}]}`), "u")
			return err
		}, "unsupported Responses input item type"},
		{"unsupported Responses tool type", func() error {
			_, err := responsesToMessagesRequest([]byte(`{"model":"m","input":[],"tools":[{"type":"code_interpreter","name":"c"}]}`), "u")
			return err
		}, "unsupported Responses tool type"},
		{"unsupported Responses tool_choice", func() error {
			_, err := responsesToMessagesRequest([]byte(`{"model":"m","input":[],"tool_choice":"required"}`), "u")
			return err
		}, "tool_choice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func mustUnmarshal(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
}

func TestResponseConversionRejectsUnsupportedSemantics(t *testing.T) {
	cases := []struct {
		name string
		fn   func() error
		want string
	}{
		{"unknown Responses output item", func() error {
			_, err := responsesToMessagesResponse([]byte(`{"id":"r","status":"completed","output":[{"type":"reasoning","id":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}`), "c")
			return err
		}, "unsupported Responses output item type"},
		{"unknown Responses content block", func() error {
			_, err := responsesToMessagesResponse([]byte(`{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_image"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`), "c")
			return err
		}, "unsupported Responses content block type"},
		{"thinking Messages content block", func() error {
			_, err := messagesToResponsesResponse([]byte(`{"id":"m","content":[{"type":"thinking","thinking":"x"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), "c")
			return err
		}, "thinking"},
		{"unknown Messages content block", func() error {
			_, err := messagesToResponsesResponse([]byte(`{"id":"m","content":[{"type":"custom"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`), "c")
			return err
		}, "unsupported content block type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.fn()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

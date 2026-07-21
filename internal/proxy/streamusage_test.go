package proxy

import "testing"

func TestStreamUsageAccumulatorProducedOutput(t *testing.T) {
	tests := []struct {
		name, data string
		want       bool
	}{
		{"responses terminal", `data: {"type":"response.completed"}

`, false},
		{"responses text", `data: {"type":"response.output_text.delta","delta":"hi"}

`, true},
		{"responses tool", `data: {"type":"response.function_call_arguments.delta","delta":"{}"}

`, true},
		{"responses embedded output", `data: {"type":"response.completed","response":{"output":[{"type":"message"}]}}

`, true},
		{"chat done", "data: [DONE]\n\n", false},
		{"chat role", `data: {"choices":[{"delta":{"role":"assistant"}}]}

`, false},
		{"chat content", `data: {"choices":[{"delta":{"content":"hi"}}]}

`, true},
		{"chat tool", `data: {"choices":[{"delta":{"tool_calls":[{"id":"x"}]}}]}

`, true},
		{"chat refusal", `data: {"choices":[{"delta":{"refusal":"no"}}]}

`, true},
		{"chat reasoning_content", `data: {"choices":[{"delta":{"reasoning_content":"think"}}]}

`, true},
		{"chat reasoning", `data: {"choices":[{"delta":{"reasoning":"think"}}]}

`, true},
		{"responses reasoning text", `data: {"type":"response.reasoning_text.delta","delta":"think"}

`, true},
		{"responses content part", `data: {"type":"response.content_part.added","part":{"type":"output_text","text":""}}

`, true},
		{"responses empty content part", `data: {"type":"response.content_part.added","part":null}

`, false},
		{"anthropic lifecycle", `data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}

`, false},
		{"anthropic empty text block", `data: {"type":"content_block_start","content_block":{"type":"text","text":""}}

`, true},
		{"anthropic tool block", `data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"t"}}

`, true},
		{"chat null function call", `data: {"choices":[{"delta":{"function_call":null,"audio":null}}]}

`, false},
		{"anthropic content", `data: {"type":"content_block_delta","delta":{"text":"hi"}}

`, true},
		{"gemini empty", `data: {"candidates":[{"finishReason":"STOP"}]}

`, true},
		{"gemini parts", `data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}

`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a streamUsageAccumulator
			a.Feed([]byte(tt.data))
			if a.ProducedOutput() != tt.want {
				t.Fatalf("ProducedOutput()=%v, want %v", a.ProducedOutput(), tt.want)
			}
		})
	}
}

func TestStreamUsageAccumulatorProtocolErrors(t *testing.T) {
	tests := []struct {
		name, data, message string
	}{
		{"responses error", `data: {"type":"error","code":"overloaded","message":"server overloaded"}` + "\n\n", "server overloaded"},
		{"responses failed", `data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"provider failed"}}}` + "\n\n", "provider failed"},
		{"responses incomplete", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", "max_output_tokens"},
		{"anthropic error", `data: {"type":"error","error":{"type":"overloaded_error","message":"anthropic overloaded"}}` + "\n\n", "anthropic overloaded"},
		{"gemini error", `data: {"error":{"code":503,"message":"temporarily unavailable","status":"UNAVAILABLE"}}` + "\n\n", "temporarily unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &streamUsageAccumulator{}
			a.Feed([]byte(tt.data))
			if !a.Done() || !a.Errored() || a.Successful() {
				t.Fatalf("state: done=%v errored=%v successful=%v", a.Done(), a.Errored(), a.Successful())
			}
			if a.ErrorMessage() != tt.message {
				t.Fatalf("message=%q, want %q", a.ErrorMessage(), tt.message)
			}
		})
	}
}

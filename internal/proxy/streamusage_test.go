package proxy

import "testing"

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

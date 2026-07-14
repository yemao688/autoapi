package proxy

import (
	"strings"
	"testing"
)

func TestResponsesClientMessagesProviderStreamTextAndUsage(t *testing.T) {
	c := newMessagesToResponsesStreamConverter()
	input := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\"}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n" +
		"event: ping\ndata: {}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	out, err := c.Write([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "response.created") || !strings.Contains(string(out), "response.output_text.delta") || !strings.Contains(string(out), "\"input_tokens\":7") || !strings.Contains(string(out), "\"output_tokens\":3") || !strings.Contains(string(out), "end_turn") {
		t.Fatalf("unexpected converted stream: %s", out)
	}
	if strings.Contains(string(out), "ping") {
		t.Fatal("ping event was forwarded")
	}
}

func TestResponsesClientMessagesProviderStreamToolAndChunkedLines(t *testing.T) {
	c := newMessagesToResponsesStreamConverter()
	parts := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"tool_1\",\"name\":\"lookup\"}}\n\n",
		`event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"\"q\":\"a\""}}

`,
		`event: content_block_delta
data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"}"}}

`,
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\"}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	var out []byte
	for _, part := range parts {
		for _, b := range []byte(part) {
			got, err := c.Write([]byte{b})
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, got...)
		}
	}
	if !strings.Contains(string(out), "tool_1") || !strings.Contains(string(out), "response.function_call_arguments.delta") || !strings.Contains(string(out), "{\\\"q\\\":\\\"a\\\"}") {
		t.Fatalf("unexpected tool stream: %s", out)
	}
}

func TestResponsesClientMessagesProviderStreamCloseDoesNotSynthesizeTerminal(t *testing.T) {
	c := newMessagesToResponsesStreamConverter()
	out, err := c.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n"))
	if err != nil || len(out) != 0 {
		t.Fatalf("out=%q err=%v", out, err)
	}
	out, err = c.Close()
	if err != nil || strings.Contains(string(out), "response.completed") {
		t.Fatalf("close synthesized terminal: %q err=%v", out, err)
	}
}

func TestMessagesToResponsesStreamSSEFraming(t *testing.T) {
	c := newMessagesToResponsesStreamConverter()
	input := "event: message_start\r\ndata: {\"type\":\"message_start\",\r\ndata: \"message\":{}}\r\n\r\n"
	var out []byte
	for i := 0; i < len(input); i++ {
		got, err := c.Write([]byte{input[i]})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, got...)
	}
	if !strings.Contains(string(out), "response.created") {
		t.Fatalf("CRLF/multiple data lines were not parsed: %q", out)
	}
	c = newMessagesToResponsesStreamConverter()
	out, err := c.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{}}"))
	if err != nil {
		t.Fatal(err)
	}
	tail, err := c.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(out)+len(tail) == 0 || !strings.Contains(string(append(out, tail...)), "response.created") {
		t.Fatal("EOF tail event was dropped")
	}
}

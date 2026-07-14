package proxy

import (
	"encoding/json"
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

func TestResponsesToMessagesStreamTextGolden(t *testing.T) {
	c := newResponsesToMessagesStreamConverter()
	in := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt\",\"usage\":{\"input_tokens\":2}}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":3}}}\n\n"
	out, err := c.Write([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "event: message_start") != 1 || strings.Count(string(out), "event: content_block_start") != 1 || strings.Count(string(out), "event: content_block_delta") != 1 || strings.Count(string(out), "event: content_block_stop") != 1 || strings.Count(string(out), "event: message_stop") != 1 {
		t.Fatalf("unexpected events: %s", out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var v map[string]interface{}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResponsesToMessagesStreamTerminalAndUnknown(t *testing.T) {
	c := newResponsesToMessagesStreamConverter()
	out, err := c.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"bad\"}\n\nevent: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "bad") || !strings.Contains(string(out), "max_tokens") || strings.Contains(string(out), "completed") {
		t.Fatalf("invalid terminal conversion: %s", out)
	}
	c = newResponsesToMessagesStreamConverter()
	out, err = c.Write([]byte("event: response.reasoning.delta\ndata: {\"type\":\"response.reasoning.delta\",\"delta\":\"secret\"}\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "text_delta") || strings.Contains(string(out), "secret") {
		t.Fatalf("reasoning became text: %s", out)
	}
}

func TestResponsesToMessagesStreamTerminalSemantics(t *testing.T) {
	c := newResponsesToMessagesStreamConverter()
	out, err := c.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Index(got, "event: content_block_stop") > strings.Index(got, "event: message_delta") || strings.Count(got, "event: message_stop") != 1 {
		t.Fatalf("active block was not closed before terminal events: %s", got)
	}
	more, err := c.Write([]byte("event: response.completed\ndata: {}\n\n"))
	if err != nil || len(more) != 0 {
		t.Fatalf("duplicate terminal was not ignored: out=%q err=%v", more, err)
	}

	c = newResponsesToMessagesStreamConverter()
	if _, err := c.Write([]byte("event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n")); err == nil {
		t.Fatal("failed response was treated as successful conversion")
	}
	if _, err := c.Write([]byte("event: response.completed\ndata: {}\n\n")); err != nil {
		t.Fatalf("terminal guard did not suppress later events: %v", err)
	}
}

func TestResponsesToMessagesStreamIncompleteReasonError(t *testing.T) {
	c := newResponsesToMessagesStreamConverter()
	if _, err := c.Write([]byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"content_filter\"}}}\n\n")); err == nil || !strings.Contains(err.Error(), "content_filter") {
		t.Fatalf("unsupported incomplete reason did not return clear error: %v", err)
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

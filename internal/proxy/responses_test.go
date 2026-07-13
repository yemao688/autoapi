package proxy

import "testing"

func TestValidateResponsesRequest(t *testing.T) {
	if req, err := validateResponsesRequest([]byte(`{"model":"gpt-5","input":"hi","stream":true}`)); err != nil || !req.Stream {
		t.Fatalf("valid request: %#v, %v", req, err)
	}
	for _, body := range []string{
		`{"model":"gpt-5","previous_response_id":"resp_1"}`,
		`{"model":"gpt-5","background":true}`,
		`{"model":"gpt-5","conversation":"x"}`,
		`{"model":"gpt-5","store":true}`,
	} {
		if _, err := validateResponsesRequest([]byte(body)); err == nil {
			t.Errorf("expected stateful/unknown field rejection for %s", body)
		}
	}
	if _, err := validateResponsesRequest([]byte(`{"model":"gpt-5","background":false,"unknown_field":123}`)); err != nil {
		t.Fatalf("stateless unknown fields should pass: %v", err)
	}
}

func TestParseResponsesStreamTerminalAndUsage(t *testing.T) {
	var acc streamUsageAccumulator
	acc.Feed([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"usage\":{\"input_tokens\":12,\"output_tokens\":7}}\n\n"))
	in, out, _, _ := acc.Usage()
	if in != 12 || out != 7 || acc.TerminalState() != "completed" || !acc.Successful() {
		t.Fatalf("usage=(%d,%d), done=%v", in, out, acc.Done())
	}
}

func TestResponsesTerminalFailuresAreNotSuccessful(t *testing.T) {
	for _, typ := range []string{"response.failed", "response.incomplete"} {
		var acc streamUsageAccumulator
		acc.Feed([]byte("data: {\"type\":\"" + typ + "\"}\n\n"))
		if acc.Successful() {
			t.Fatalf("%s incorrectly successful", typ)
		}
	}
}

func TestResponsesCreatedDoesNotStopParsing(t *testing.T) {
	var acc streamUsageAccumulator
	acc.Feed([]byte("data: {\"type\":\"response.created\"}\n\n"))
	if acc.Done() || acc.TerminalState() != "" {
		t.Fatal("response.created must not be terminal")
	}
	acc.Feed([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":9}}}}\n\n"))
	in, out, cached, _ := acc.Usage()
	if in != 12 || out != 4 || cached != 9 || !acc.Successful() {
		t.Fatalf("usage=(%d,%d,%d), terminal=%q", in, out, cached, acc.TerminalState())
	}
}

func TestResponsesInputCachedTokens(t *testing.T) {
	var acc streamUsageAccumulator
	acc.Feed([]byte("data: {\"type\":\"response.completed\",\"usage\":{\"input_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":6}}}\n\n"))
	_, _, cached, _ := acc.Usage()
	if cached != 6 {
		t.Fatalf("cached tokens = %d, want 6", cached)
	}
}

func TestRewriteBodyModelPreservesRawValues(t *testing.T) {
	body := []byte(`{"model":"old","big":9007199254740993,"unknown":{"x":true}}`)
	out, err := rewriteBodyModel(body, "new")
	if err != nil || string(out) != `{"big":9007199254740993,"model":"new","unknown":{"x":true}}` {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestChar_RewriteBodyModelIdentityPreservesBytes(t *testing.T) {
	body := []byte("{\n  \"z\": 1,\n  \"model\": \"same\",\n  \"a\": {\"nested\": true}\n}")
	out, err := rewriteBodyModel(body, "same")
	if err != nil {
		t.Fatalf("rewriteBodyModel: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("body was re-encoded; got %q want %q", string(out), string(body))
	}
	if len(out) > 0 && &out[0] != &body[0] {
		t.Fatalf("expected identical body bytes to be returned without allocation")
	}
}

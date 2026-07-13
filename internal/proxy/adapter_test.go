package proxy

import (
	"encoding/json"
	"testing"
)

func TestNativeAdapterPrepareAttempt(t *testing.T) {
	body := []byte(`{"model":"requested","messages":[{"role":"user","content":"hi"}]}`)
	c := candidate{
		modelName:    "upstream-model",
		protocol:     ProtocolOpenAIChat,
		upstreamPath: "/v1/chat/completions",
	}

	prep, err := (nativeAdapter{}).PrepareAttempt(body, c)
	if err != nil {
		t.Fatalf("PrepareAttempt: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(prep.Body, &got); err != nil {
		t.Fatalf("unmarshal prepared body: %v", err)
	}
	if got["model"] != "upstream-model" {
		t.Fatalf("model = %v, want upstream-model", got["model"])
	}
	if prep.Path != "/v1/chat/completions" {
		t.Fatalf("Path = %q, want /v1/chat/completions", prep.Path)
	}
	if prep.ConversionMode != "native" {
		t.Fatalf("ConversionMode = %q, want native", prep.ConversionMode)
	}
}

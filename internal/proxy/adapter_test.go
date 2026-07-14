package proxy

import (
	"bytes"
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
	if prep.ConversionMode != ConversionModeNative {
		t.Fatalf("ConversionMode = %q, want native", prep.ConversionMode)
	}
}

func TestNativeAdapterPrepareAttemptWithUnknownProtocol(t *testing.T) {
	body := []byte(`{"model":"requested","input":"hello"}`)
	c := candidate{
		modelName:    "upstream-model",
		protocol:     ProtocolUnknown,
		upstreamPath: "/v1/embeddings",
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
	if prep.Path != "/v1/embeddings" {
		t.Fatalf("Path = %q, want /v1/embeddings", prep.Path)
	}
}

func TestNativeAdapterPrepareAttemptEmptyModelAndInvalidJSON(t *testing.T) {
	t.Run("empty modelName", func(t *testing.T) {
		body := []byte(`{"model":"requested"}`)
		prep, err := (nativeAdapter{}).PrepareAttempt(body, candidate{upstreamPath: "/v1/chat/completions"})
		if err != nil {
			t.Fatalf("PrepareAttempt: %v", err)
		}
		if !bytes.Equal(prep.Body, body) {
			t.Fatalf("body = %s, want unchanged %s", prep.Body, body)
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		body := []byte(`{"model":`)
		prep, err := (nativeAdapter{}).PrepareAttempt(body, candidate{modelName: "upstream-model", upstreamPath: "/v1/chat/completions"})
		if err != nil {
			t.Fatalf("PrepareAttempt: %v", err)
		}
		if !bytes.Equal(prep.Body, body) {
			t.Fatalf("body = %s, want unchanged %s", prep.Body, body)
		}
	})
}

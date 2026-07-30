package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoapi/internal/toolconfig"
)

func TestListToolProvidersMapsClaudeTierExtrasAndAppliesThem(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	path := toolconfig.DefaultConfigPath(toolconfig.ToolClaude, homeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{
  "env": {
    "ANTHROPIC_BASE_URL": "https://claude.example",
    "ANTHROPIC_AUTH_TOKEN": "claude-secret",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4-5",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-5",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-4-5"
  },
  "model": "claude-sonnet-4-5"
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	views, err := svc.ListToolProviders("claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].InDB || views[0].Preset.ProviderID != "anthropic" {
		t.Fatalf("unexpected Claude views: %+v", views)
	}
	want := map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "claude-haiku-4-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "claude-opus-4-5",
	}
	for key, value := range want {
		if views[0].Preset.Extra[key] != value {
			t.Fatalf("view Extra[%q] = %q, want %q: %+v", key, views[0].Preset.Extra[key], value, views[0].Preset)
		}
	}

	updated := views[0].Preset
	updated.Extra["ANTHROPIC_DEFAULT_SONNET_MODEL"] = ""
	if _, err := svc.UpdateEnabledToolPreset(updated, ""); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contentString := string(content)
	if strings.Contains(contentString, "ANTHROPIC_DEFAULT_SONNET_MODEL") {
		t.Fatalf("cleared Claude tier override remained after apply: %s", content)
	}
	for _, value := range []string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"claude-haiku-4-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"claude-opus-4-5",
	} {
		if !strings.Contains(contentString, value) {
			t.Fatalf("Claude apply lost %q: %s", value, content)
		}
	}
}

func TestListToolProvidersMapsCodexWireAPIAndAppliesDefault(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	path := toolconfig.DefaultConfigPath(toolconfig.ToolCodex, homeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`model_provider = "acme-ai"
model = "acme-ai/acme-model"
disable_response_storage = true

[model_providers.acme-ai]
name = "Acme AI"
base_url = "https://codex.example"
wire_api = "chat"
requires_openai_auth = true
`), 0o644); err != nil {
		t.Fatal(err)
	}

	views, err := svc.ListToolProviders("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].InDB || views[0].Preset.ProviderID != "acme-ai" {
		t.Fatalf("unexpected Codex views: %+v", views)
	}
	if views[0].Preset.Extra["wire_api"] != "chat" {
		t.Fatalf("Codex view Extra[wire_api] = %q, want chat: %+v", views[0].Preset.Extra["wire_api"], views[0].Preset)
	}

	updated := views[0].Preset
	updated.Extra["wire_api"] = ""
	if _, err := svc.UpdateEnabledToolPreset(updated, ""); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contentString := string(content)
	if !strings.Contains(contentString, `wire_api = "responses"`) {
		t.Fatalf("cleared Codex wire_api did not revert to responses: %s", content)
	}
	if strings.Contains(contentString, `wire_api = "chat"`) {
		t.Fatalf("Codex chat wire_api remained after clearing: %s", content)
	}
}

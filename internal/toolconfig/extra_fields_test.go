package toolconfig

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func claudeExtraPreset() PresetPlaintext {
	preset := testPreset("")
	preset.Preset.ProviderID = "anthropic"
	preset.Extra = map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "claude-haiku-4-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "claude-opus-4-5",
	}
	return preset
}

func TestClaudeExtraTierOverridesUpsertPresent(t *testing.T) {
	home := t.TempDir()
	commitPlan(t, ClaudeAdapter{}, claudeExtraPreset(), home)
	content := string(readFile(t, DefaultConfigPath(ToolClaude, home)))
	for _, value := range []string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"claude-haiku-4-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"claude-sonnet-4-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"claude-opus-4-5",
	} {
		if !strings.Contains(content, value) {
			t.Fatalf("Claude config missing %q: %s", value, content)
		}
	}
}

func TestClaudeExtraTierOverridesAbsentOnUpsertWithout(t *testing.T) {
	home := t.TempDir()
	path := DefaultConfigPath(ToolClaude, home)
	writeFile(t, path, `{"env":{"ANTHROPIC_DEFAULT_HAIKU_MODEL":"old-haiku","ANTHROPIC_DEFAULT_SONNET_MODEL":"old-sonnet","ANTHROPIC_DEFAULT_OPUS_MODEL":"old-opus"}}`, 0o644)
	preset := claudeExtraPreset()
	preset.Extra = nil
	commitPlan(t, ClaudeAdapter{}, preset, home)
	content := string(readFile(t, path))
	for _, key := range claudeTierModelKeys {
		if strings.Contains(content, key) {
			t.Fatalf("cleared Claude key remains %q: %s", key, content)
		}
	}
}

func TestClaudeExtraTierOverrideClearedOnReupsert(t *testing.T) {
	home := t.TempDir()
	commitPlan(t, ClaudeAdapter{}, claudeExtraPreset(), home)
	preset := claudeExtraPreset()
	preset.Extra["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = ""
	commitPlan(t, ClaudeAdapter{}, preset, home)
	content := string(readFile(t, DefaultConfigPath(ToolClaude, home)))
	if strings.Contains(content, "ANTHROPIC_DEFAULT_HAIKU_MODEL") {
		t.Fatalf("cleared Claude tier override remains: %s", content)
	}
	for _, value := range []string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"claude-sonnet-4-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"claude-opus-4-5",
	} {
		if !strings.Contains(content, value) {
			t.Fatalf("Claude config lost %q after re-upsert: %s", value, content)
		}
	}
}

func TestClaudeExtraTierOverridesRemoval(t *testing.T) {
	home := t.TempDir()
	commitPlan(t, ClaudeAdapter{}, claudeExtraPreset(), home)
	changeSet, err := (ClaudeAdapter{}).PlanRemoval(home, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	content := string(readFile(t, DefaultConfigPath(ToolClaude, home)))
	for _, key := range claudeTierModelKeys {
		if strings.Contains(content, key) {
			t.Fatalf("removed Claude key remains %q: %s", key, content)
		}
	}
}

func TestClaudeExtraTierOverridesReadManagedRoundTrip(t *testing.T) {
	home := t.TempDir()
	preset := claudeExtraPreset()
	commitPlan(t, ClaudeAdapter{}, preset, home)
	managed, err := (ClaudeAdapter{}).ReadManaged(home, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range preset.Extra {
		if managed.Fields[key] != want {
			t.Fatalf("managed field %q = %q, want %q: %+v", key, managed.Fields[key], want, managed)
		}
	}
	raw, err := (ClaudeAdapter{}).ReadManagedRaw(home, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if !raw.Present || raw.Model != "acme-model" {
		t.Fatalf("unexpected raw Claude section: %+v", raw)
	}
}

func TestCodexWireAPIDefaultResponses(t *testing.T) {
	home := t.TempDir()
	commitPlan(t, CodexAdapter{}, testPreset(""), home)
	content := string(readFile(t, DefaultConfigPath(ToolCodex, home)))
	if !strings.Contains(content, `wire_api = "responses"`) {
		t.Fatalf("Codex config did not keep default wire_api=responses: %s", content)
	}
	managed, err := (CodexAdapter{}).ReadManaged(home, "acme-ai")
	if err != nil {
		t.Fatal(err)
	}
	if managed.Fields["wire_api"] != "responses" {
		t.Fatalf("managed wire_api = %q, want responses: %+v", managed.Fields["wire_api"], managed)
	}
}

func TestCodexWireAPIChatWritten(t *testing.T) {
	home := t.TempDir()
	preset := testPreset("")
	preset.Extra = map[string]string{"wire_api": "chat"}
	commitPlan(t, CodexAdapter{}, preset, home)
	content := string(readFile(t, DefaultConfigPath(ToolCodex, home)))
	if !strings.Contains(content, `wire_api = "chat"`) {
		t.Fatalf("Codex config did not write chat wire_api: %s", content)
	}
	managed, err := (CodexAdapter{}).ReadManaged(home, "acme-ai")
	if err != nil {
		t.Fatal(err)
	}
	if managed.Fields["wire_api"] != "chat" {
		t.Fatalf("managed wire_api = %q, want chat: %+v", managed.Fields["wire_api"], managed)
	}
}

func TestCodexWireAPIInvalidRejected(t *testing.T) {
	home := t.TempDir()
	preset := testPreset("")
	preset.Extra = map[string]string{"wire_api": "bogus"}
	if _, err := (CodexAdapter{}).Plan(preset, home); !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("invalid Codex wire_api error = %v", err)
	}
}

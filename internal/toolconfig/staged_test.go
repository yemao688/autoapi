package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func stagedDirectPreset(tool Tool, providerID, name, baseURL, key string) PresetPlaintext {
	return PresetPlaintext{
		Preset: Preset{
			Tool:       tool,
			Kind:       PresetDirect,
			Name:       name,
			ProviderID: providerID,
			BaseURL:    baseURL,
			Models:     []PresetModel{{Name: "model", Default: true}},
		},
		APIKey: key,
	}
}

func commitStagedPlan(t *testing.T, changeSet *ChangeSet, homeDir string) {
	t.Helper()
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(homeDir, "backups")}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanToolConfigChangeCodexStagesMultipleProviders(t *testing.T) {
	homeDir := t.TempDir()
	configPath := DefaultConfigPath(ToolCodex, homeDir)
	authPath := filepath.Join(homeDir, ".codex", "auth.json")
	writeFile(t, configPath, `# retain this comment
theme = "dark"
model_provider = "park-me"
model = "old-model"

[unmanaged]
# retain unmanaged table
value = "keep"

[model_providers.existing] # existing provider
name = "Old Existing"
base_url = "https://old.example"
custom = "preserve"

[model_providers.remove-me]
name = "Remove"
base_url = "https://remove.example"

[model_providers.park-me]
name = "Park"
base_url = "https://park.example"
`, 0o644)
	writeFile(t, authPath, `{
  // retain auth comment
  "OTHER_KEY": "keep",
  "OPENAI_API_KEY": "old-key"
}`, 0o644)

	newPreset := stagedDirectPreset(ToolCodex, "new-provider", "New", "https://new.example", "new-key")
	newPreset.Models = nil
	existingPreset := stagedDirectPreset(ToolCodex, "existing", "Edited Existing", "https://edited.example", "existing-key")
	existingPreset.Models = nil
	changes := []ToolProviderChange{
		{Action: "upsert", Preset: newPreset},
		{Action: "upsert", Preset: existingPreset},
		{Action: "remove", Preset: stagedDirectPreset(ToolCodex, "remove-me", "Remove", "https://remove.example", "")},
		{Action: "park", Preset: stagedDirectPreset(ToolCodex, "park-me", "Park", "https://park.example", "")},
	}
	changeSet, err := PlanToolConfigChange(ToolCodex, changes, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(changeSet.Changes) != 2 || changeSet.Changes[1].Resource != ResCodexAuth || changeSet.Changes[1].Mode != 0o600 {
		t.Fatalf("staged Codex changes = %+v", changeSet.Changes)
	}
	commitStagedPlan(t, changeSet, homeDir)

	configAfter := string(readFile(t, configPath))
	for _, want := range []string{"retain this comment", "retain unmanaged table", `value = "keep"`, "new-provider", "https://edited.example", `custom = "preserve"`} {
		if !strings.Contains(configAfter, want) {
			t.Fatalf("Codex config omitted %q: %s", want, configAfter)
		}
	}
	for _, removed := range []string{"remove-me", "park-me"} {
		if strings.Contains(configAfter, removed) {
			t.Fatalf("Codex config retained %q: %s", removed, configAfter)
		}
	}
	var values map[string]any
	if err := toml.Unmarshal([]byte(configAfter), &values); err != nil {
		t.Fatal(err)
	}
	if pointer := tomlString(values["model_provider"]); pointer != "existing" {
		t.Fatalf("unexpected model_provider pointer after staged upserts: %q", pointer)
	}
	authAfter := string(readFile(t, authPath))
	if strings.Contains(authAfter, "OPENAI_API_KEY") || !strings.Contains(authAfter, "OTHER_KEY") || !strings.Contains(authAfter, "retain auth comment") {
		t.Fatalf("Codex auth aggregation was incorrect: %s", authAfter)
	}
	if mode := fileMode(t, authPath); mode != 0o600 {
		t.Fatalf("Codex auth mode = %o, want 600", mode)
	}

	removePointer, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{{
		Action: "remove",
		Preset: stagedDirectPreset(ToolCodex, "existing", "Edited Existing", "https://edited.example", ""),
	}}, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	commitStagedPlan(t, removePointer, homeDir)
	var afterPointer map[string]any
	if err := toml.Unmarshal(readFile(t, configPath), &afterPointer); err != nil {
		t.Fatal(err)
	}
	if _, exists := afterPointer["model_provider"]; exists {
		t.Fatalf("model_provider pointer remained after removal: %#v", afterPointer)
	}

	addKey, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{{
		Action: "upsert",
		Preset: stagedDirectPreset(ToolCodex, "new-provider", "New", "https://new.example", "final-key"),
	}}, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	commitStagedPlan(t, addKey, homeDir)
	if authAfter := string(readFile(t, authPath)); !strings.Contains(authAfter, "final-key") || strings.Contains(authAfter, "old-key") {
		t.Fatalf("Codex auth key was not added/replaced: %s", authAfter)
	}
}

func TestPlanToolConfigChangeRejectsCodexReservedAndDuplicateIDs(t *testing.T) {
	reserved := stagedDirectPreset(ToolCodex, "openai", "OpenAI", "https://example.test", "")
	if _, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{{Action: "upsert", Preset: reserved}}, t.TempDir()); !errors.Is(err, ErrConflict) {
		t.Fatalf("reserved provider error = %v", err)
	}
	duplicate := stagedDirectPreset(ToolCodex, "same", "Same", "https://example.test", "")
	if _, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{
		{Action: "upsert", Preset: duplicate},
		{Action: "remove", Preset: duplicate},
	}, t.TempDir()); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate provider error = %v", err)
	}
}

func TestPlanToolConfigChangeClaudeUpsertThenRemove(t *testing.T) {
	homeDir := t.TempDir()
	path := DefaultConfigPath(ToolClaude, homeDir)
	writeFile(t, path, `{
  // retain settings comment
  "env": {
    "KEEP_ENV": "yes"
  },
  "permissions": {"allow": ["Bash"]}
}`, 0o644)

	preset := stagedDirectPreset(ToolClaude, "anthropic", "Anthropic", "https://claude.example", "claude-key")
	upsert, err := PlanToolConfigChange(ToolClaude, []ToolProviderChange{{Action: "upsert", Preset: preset}}, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(upsert.Changes) != 1 {
		t.Fatalf("Claude upsert changes = %+v", upsert.Changes)
	}
	commitStagedPlan(t, upsert, homeDir)

	remove, err := PlanToolConfigChange(ToolClaude, []ToolProviderChange{{
		Action: "remove",
		Preset: PresetPlaintext{Preset: preset.Preset},
	}}, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	commitStagedPlan(t, remove, homeDir)
	after := string(readFile(t, path))
	for _, removed := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", `"model"`} {
		if strings.Contains(after, removed) {
			t.Fatalf("Claude managed key remains %q: %s", removed, after)
		}
	}
	for _, want := range []string{"retain settings comment", "KEEP_ENV", "permissions"} {
		if !strings.Contains(after, want) {
			t.Fatalf("Claude removal omitted %q: %s", want, after)
		}
	}
}

func TestPlanToolConfigChangeRejectsUnsupportedToolAndAction(t *testing.T) {
	preset := stagedDirectPreset(ToolCodex, "provider", "Provider", "https://example.test", "")
	if _, err := PlanToolConfigChange(ToolOpencode, []ToolProviderChange{{Action: "upsert", Preset: preset}}, t.TempDir()); !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("opencode error = %v", err)
	}
	if _, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{{Action: "invalid", Preset: preset}}, t.TempDir()); !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("action error = %v", err)
	}
}

func TestPlanToolConfigChangeClaudeRejectsNonAnthropic(t *testing.T) {
	homeDir := t.TempDir()
	path := DefaultConfigPath(ToolClaude, homeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	preset := stagedDirectPreset(ToolClaude, "other", "Other", "https://example.test", "")
	if _, err := PlanToolConfigChange(ToolClaude, []ToolProviderChange{{Action: "upsert", Preset: preset}}, homeDir); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("non-anthropic error = %v", err)
	}
}

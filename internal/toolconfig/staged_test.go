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
	changeSet, err := PlanToolConfigChange(ToolCodex, changes, "", homeDir)
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
	}}, "", homeDir)
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
	}}, "", homeDir)
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
	if _, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{{Action: "upsert", Preset: reserved}}, "", t.TempDir()); !errors.Is(err, ErrConflict) {
		t.Fatalf("reserved provider error = %v", err)
	}
	duplicate := stagedDirectPreset(ToolCodex, "same", "Same", "https://example.test", "")
	if _, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{
		{Action: "upsert", Preset: duplicate},
		{Action: "remove", Preset: duplicate},
	}, "", t.TempDir()); !errors.Is(err, ErrConflict) {
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
	upsert, err := PlanToolConfigChange(ToolClaude, []ToolProviderChange{{Action: "upsert", Preset: preset}}, "", homeDir)
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
	}}, "", homeDir)
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
	if _, err := PlanToolConfigChange(ToolOpencode, []ToolProviderChange{{Action: "upsert", Preset: preset}}, "", t.TempDir()); !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("opencode error = %v", err)
	}
	if _, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{{Action: "invalid", Preset: preset}}, "", t.TempDir()); !errors.Is(err, ErrInvalidPreset) {
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
	if _, err := PlanToolConfigChange(ToolClaude, []ToolProviderChange{{Action: "upsert", Preset: preset}}, "", homeDir); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("non-anthropic error = %v", err)
	}
}

func TestPlanToolConfigChangeClaudeCommonConfigDeepMerge(t *testing.T) {
	homeDir := t.TempDir()
	path := DefaultConfigPath(ToolClaude, homeDir)
	writeFile(t, path, `{
  // retain settings comment
  "env": {
    "KEEP_ENV": "yes",
    "ANTHROPIC_BASE_URL": "https://managed.example",
    "ANTHROPIC_AUTH_TOKEN": "managed-token",
    "ANTHROPIC_MODEL": "managed-model",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "haiku-model",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "sonnet-model",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "opus-model"
  },
  "permissions": {"allow": ["Bash"]},
  "array": ["old"],
  "model": "managed-root"
}`, 0o644)

	common := `{
  "env": {"KEEP_ENV": "updated", "COMMON_FLAG": true},
  "permissions": {"deny": ["rm -rf"]},
  "array": ["new"]
}`
	changeSet, err := PlanToolConfigChange(ToolClaude, nil, common, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	after := string(changeSet.Changes[0].After)
	for _, want := range []string{
		"retain settings comment",
		`"KEEP_ENV": "updated"`,
		`"COMMON_FLAG": true`,
		`"allow"`,
		`"deny"`,
		`"array": ["new"]`,
		`"ANTHROPIC_BASE_URL": "https://managed.example"`,
		`"ANTHROPIC_AUTH_TOKEN": "managed-token"`,
		`"ANTHROPIC_MODEL": "managed-model"`,
		`"ANTHROPIC_DEFAULT_HAIKU_MODEL": "haiku-model"`,
		`"ANTHROPIC_DEFAULT_SONNET_MODEL": "sonnet-model"`,
		`"ANTHROPIC_DEFAULT_OPUS_MODEL": "opus-model"`,
		`"model": "managed-root"`,
	} {
		if !strings.Contains(after, want) {
			t.Fatalf("Claude common merge omitted %q: %s", want, after)
		}
	}
}

func TestPlanToolConfigChangeClaudeCommonConfigRejectsInvalidAndSensitive(t *testing.T) {
	homeDir := t.TempDir()
	path := DefaultConfigPath(ToolClaude, homeDir)
	writeFile(t, path, `{ "env": {"ANTHROPIC_BASE_URL": "https://managed.example"} }`, 0o644)
	preset := stagedDirectPreset(ToolClaude, "anthropic", "Anthropic", "https://managed.example", "managed-token")

	for _, test := range []struct {
		name    string
		snippet string
		want    string
		isErr   error
	}{
		{name: "managed collision", snippet: `{"env":{"ANTHROPIC_BASE_URL":"override"}}`, want: "managed key path", isErr: ErrConflict},
		{name: "nested secret", snippet: `{"options":{"nested":{"apiKey":"secret"}}}`, want: "secrets belong in preset key fields", isErr: ErrInvalidPreset},
		{name: "invalid JSON", snippet: `{"options":}`, want: "invalid JSON", isErr: ErrInvalidPreset},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanToolConfigChange(ToolClaude, []ToolProviderChange{{Action: "upsert", Preset: preset}}, test.snippet, homeDir)
			if err == nil || !strings.Contains(err.Error(), test.want) || !errors.Is(err, test.isErr) {
				t.Fatalf("common config error = %v", err)
			}
		})
	}
}

func TestPlanToolConfigChangeClaudeCommonConfigEmptyIsNoOp(t *testing.T) {
	homeDir := t.TempDir()
	path := DefaultConfigPath(ToolClaude, homeDir)
	before := `{"permissions":{"allow":["Bash"]}}
`
	writeFile(t, path, before, 0o644)
	changeSet, err := PlanToolConfigChange(ToolClaude, nil, " \n\t", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(changeSet.Changes[0].After); got != before {
		t.Fatalf("empty Claude common config changed file: %q", got)
	}
}

func TestPlanToolConfigChangeCodexCommonConfigMergesTopLevel(t *testing.T) {
	homeDir := t.TempDir()
	path := DefaultConfigPath(ToolCodex, homeDir)
	writeFile(t, path, `# retain this comment
profile = "old" # retain profile comment
existing = true

[model_providers.existing]
name = "Existing"
base_url = "https://existing.example"
`, 0o644)
	common := `profile = "fast"
new_flag = true
features = ["a", "b"]
options = { mode = "fast", count = 2 }
`
	changeSet, err := PlanToolConfigChange(ToolCodex, nil, common, homeDir)
	if err != nil {
		t.Fatal(err)
	}
	after := string(changeSet.Changes[0].After)
	if _, err := readTOMLBytes(changeSet.Changes[0].After); err != nil {
		t.Fatalf("merged Codex config is invalid: %v\n%s", err, after)
	}
	for _, want := range []string{
		"retain this comment",
		`profile = "fast" # retain profile comment`,
		`new_flag = true`,
		`features = ["a", "b"]`,
		`options = { mode = "fast", count = 2 }`,
		"model_providers.existing",
	} {
		if !strings.Contains(after, want) {
			t.Fatalf("Codex common merge omitted %q: %s", want, after)
		}
	}
}

func TestPlanToolConfigChangeCodexCommonConfigRejectsInvalid(t *testing.T) {
	for _, test := range []struct {
		name    string
		snippet string
		want    string
		isErr   error
	}{
		{name: "table section", snippet: "[profiles]\nname = \"fast\"\n", want: "table sections are not supported in v1", isErr: ErrInvalidPreset},
		{name: "managed key", snippet: "model = \"managed\"\n", want: "managed and cannot be overridden", isErr: ErrConflict},
		{name: "sensitive key", snippet: "options = { authToken = \"secret\" }\n", want: "secrets belong in preset key fields", isErr: ErrInvalidPreset},
		{name: "invalid TOML", snippet: "profile = [\n", want: "invalid TOML", isErr: ErrInvalidPreset},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanToolConfigChange(ToolCodex, nil, test.snippet, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), test.want) || !errors.Is(err, test.isErr) {
				t.Fatalf("common config error = %v", err)
			}
		})
	}
}

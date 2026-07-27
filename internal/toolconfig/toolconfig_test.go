package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func testPreset(vendor string) PresetPlaintext {
	return PresetPlaintext{
		Preset: Preset{
			Kind:    PresetDirect,
			Name:    "Acme AI",
			Vendor:  vendor,
			BaseURL: "https://api.example.test/v1",
			Models: []PresetModel{{
				Name:       "acme-model",
				Default:    true,
				Limit:      &ModelLimit{Context: 8192, Output: 1024},
				Modalities: []string{"text", "image"},
				Reasoning:  true,
				Variants: map[string]PresetVariant{
					"fast": {ReasoningEffort: "low", Include: []string{"summary"}},
				},
			}},
		},
		APIKey: "secret-key",
	}
}

func commitPlan(t *testing.T, adapter Adapter, preset PresetPlaintext, home string) *ChangeSet {
	t.Helper()
	changeSet, err := adapter.Plan(preset, home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	return changeSet
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestDetectMissingAndPresent(t *testing.T) {
	home := t.TempDir()
	openCode := OpenCodeAdapter{}
	claude := ClaudeAdapter{}
	codex := CodexAdapter{}

	wantOpenCode := filepath.Join(home, ".config", "opencode", "opencode.json")
	wantClaude := filepath.Join(home, ".claude", "settings.json")
	wantCodex := filepath.Join(home, ".codex", "config.toml")
	if status := openCode.Detect(home); status.ConfigPath != wantOpenCode || status.ConfigExists || status.Installed {
		t.Fatalf("missing opencode status: %+v", status)
	}
	if status := claude.Detect(home); status.ConfigPath != wantClaude || status.ConfigExists || status.Installed {
		t.Fatalf("missing Claude status: %+v", status)
	}
	if status := codex.Detect(home); status.ConfigPath != wantCodex || status.ConfigExists || status.Installed || status.ExtraPaths["auth_json"] != "" {
		t.Fatalf("missing Codex status: %+v", status)
	}

	writeFile(t, wantOpenCode, `{}`, 0o644)
	writeFile(t, wantClaude, `{}`, 0o644)
	writeFile(t, wantCodex, ``, 0o644)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{}`, 0o600)
	omoJSON := filepath.Join(filepath.Dir(wantOpenCode), "oh-my-opencode-slim.json")
	omoJSONC := filepath.Join(filepath.Dir(wantOpenCode), "oh-my-opencode-slim.jsonc")
	writeFile(t, omoJSON, `{}`, 0o644)
	writeFile(t, omoJSONC, `{}`, 0o644)
	if status := openCode.Detect(home); !status.ConfigExists || !status.Installed || status.ConfigPath != wantOpenCode || status.ExtraPaths["omo_config"] != omoJSONC {
		t.Fatalf("present opencode status: %+v", status)
	}
	if status := claude.Detect(home); !status.ConfigExists || !status.Installed || status.ConfigPath != wantClaude {
		t.Fatalf("present Claude status: %+v", status)
	}
	if status := codex.Detect(home); !status.ConfigExists || !status.Installed || status.ConfigPath != wantCodex || status.ExtraPaths["auth_json"] != filepath.Join(home, ".codex", "auth.json") {
		t.Fatalf("present Codex status: %+v", status)
	}
}

func TestOpenCodePlanCommitPreservesLeavesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := DefaultConfigPath(ToolOpencode, home)
	fixture := `{
  // keep me
  "unmanaged": {"enabled": true},
  "provider": {
    "acme-ai": {"headers": {"X-Keep": "yes"}},
    "other": {"npm": "custom"}
  }
}`
	writeFile(t, path, fixture, 0o640)
	preset := testPreset("@ai-sdk/openai-compatible")
	adapter := OpenCodeAdapter{}
	commitPlan(t, adapter, preset, home)
	first := readFile(t, path)
	if !strings.Contains(string(first), "keep me") || !strings.Contains(string(first), `"unmanaged"`) || !strings.Contains(string(first), `"X-Keep"`) {
		t.Fatalf("unmanaged opencode content was lost: %s", first)
	}
	commitPlan(t, adapter, preset, home)
	second := readFile(t, path)
	if string(first) != string(second) {
		t.Fatalf("opencode plan/commit is not idempotent:\nfirst: %s\nsecond: %s", first, second)
	}
	managed, err := adapter.ReadManaged(home, "acme-ai")
	if err != nil {
		t.Fatal(err)
	}
	if !managed.Present || managed.ProviderID != "acme-ai" || managed.BaseURL != preset.BaseURL || managed.Model != "acme-ai/acme-model" {
		t.Fatalf("unexpected opencode managed section: %+v", managed)
	}
	if managed.Fields["apiKey"] != MaskSecret(preset.APIKey) || managed.Fields["models_count"] != "1" {
		t.Fatalf("opencode secret/count projection is wrong: %+v", managed.Fields)
	}
	if strings.Contains(strings.Join([]string{managed.Fields["apiKey"], managed.Fields["models"]}, " "), preset.APIKey) {
		t.Fatalf("opencode ReadManaged leaked secret: %+v", managed.Fields)
	}

	for _, vendor := range []string{"@ai-sdk/openai", "@ai-sdk/openai-compatible"} {
		variant := testPreset(vendor)
		commitPlan(t, adapter, variant, home)
		managed, err := adapter.ReadManaged(home, "acme-ai")
		if err != nil {
			t.Fatal(err)
		}
		if managed.Fields["npm"] != vendor {
			t.Fatalf("vendor shape was not written: got %q want %q", managed.Fields["npm"], vendor)
		}
	}
}

func TestClaudePlanCommitPreservesCommentsAndMasksToken(t *testing.T) {
	home := t.TempDir()
	path := DefaultConfigPath(ToolClaude, home)
	writeFile(t, path, `{
  // keep me
  "permissions": {"allow": ["Bash"]}
}`, 0o644)
	adapter := ClaudeAdapter{}
	preset := testPreset("")
	commitPlan(t, adapter, preset, home)
	first := readFile(t, path)
	if !strings.Contains(string(first), "keep me") || !strings.Contains(string(first), "permissions") {
		t.Fatalf("unmanaged Claude content was lost: %s", first)
	}
	commitPlan(t, adapter, preset, home)
	if got := string(readFile(t, path)); got != string(first) {
		t.Fatalf("Claude plan/commit is not idempotent:\nfirst: %s\nsecond: %s", first, got)
	}
	managed, err := adapter.ReadManaged(home, "acme-ai")
	if err != nil {
		t.Fatal(err)
	}
	if !managed.Present || managed.BaseURL != preset.BaseURL || managed.Model != "acme-model" || managed.Fields["ANTHROPIC_AUTH_TOKEN"] != MaskSecret(preset.APIKey) {
		t.Fatalf("unexpected Claude managed section: %+v", managed)
	}
}

func TestCodexPlanCommitSplicesCommentsAndAuth(t *testing.T) {
	home := t.TempDir()
	configPath := DefaultConfigPath(ToolCodex, home)
	authPath := filepath.Join(home, ".codex", "auth.json")
	config := `# keep me
theme = "dark" # keep inline

[unmanaged]
# keep unmanaged section
value = "untouched"

[model_providers.other] # other provider
name = "Other"
base_url = "https://other.example"
`
	writeFile(t, configPath, config, 0o640)
	writeFile(t, authPath, `{
  "OTHER_KEY": "keep",
  "OPENAI_API_KEY": "old"
}`, 0o644)
	preset := testPreset("")
	adapter := CodexAdapter{}
	commitPlan(t, adapter, preset, home)
	configAfter := readFile(t, configPath)
	for _, want := range []string{"# keep me", "# keep unmanaged section", `value = "untouched"`, `requires_openai_auth = true`, `wire_api = "responses"`} {
		if !strings.Contains(string(configAfter), want) {
			t.Fatalf("Codex config lost %q: %s", want, configAfter)
		}
	}
	if strings.Count(string(configAfter), "[model_providers.acme-ai]") != 1 {
		t.Fatalf("Codex provider table was duplicated: %s", configAfter)
	}
	var configValues map[string]any
	if err := toml.Unmarshal(configAfter, &configValues); err != nil {
		t.Fatal(err)
	}
	if configValues["model_provider"] != "acme-ai" || configValues["model"] != "acme-model" || configValues["disable_response_storage"] != true {
		t.Fatalf("unexpected Codex top-level values: %#v", configValues)
	}
	authAfter := readFile(t, authPath)
	if !strings.Contains(string(authAfter), `"OTHER_KEY": "keep"`) || !strings.Contains(string(authAfter), "secret-key") {
		t.Fatalf("Codex auth was not patched safely: %s", authAfter)
	}
	if mode := fileMode(t, authPath); mode != 0o600 {
		t.Fatalf("Codex auth mode = %o, want 600", mode)
	}
	commitPlan(t, adapter, preset, home)
	if got := string(readFile(t, configPath)); got != string(configAfter) {
		t.Fatalf("Codex config is not idempotent:\nfirst: %s\nsecond: %s", configAfter, got)
	}
	managed, err := adapter.ReadManaged(home, "acme-ai")
	if err != nil {
		t.Fatal(err)
	}
	if !managed.Present || managed.BaseURL != preset.BaseURL || managed.Model != "acme-model" || managed.Fields["OPENAI_API_KEY"] != MaskSecret(preset.APIKey) {
		t.Fatalf("unexpected Codex managed section: %+v", managed)
	}

	beforeNoKey := append([]byte(nil), authAfter...)
	noKey := preset
	noKey.APIKey = ""
	commitPlan(t, adapter, noKey, home)
	if got := readFile(t, authPath); string(got) != string(beforeNoKey) {
		t.Fatalf("key-less Codex preset touched auth.json: %s", got)
	}
}

func TestCodexReservedProviderIDs(t *testing.T) {
	for _, providerID := range []string{"openai", "ollama", "oss", "amazon-bedrock", "lmstudio", "ollama-chat"} {
		preset := testPreset("")
		preset.ProviderID = providerID
		if _, err := (CodexAdapter{}).Plan(preset, t.TempDir()); !errors.Is(err, ErrConflict) {
			t.Fatalf("provider %q: expected ErrConflict, got %v", providerID, err)
		}
	}
}

func TestUnsafeShapesAndDuplicateManagedJSONKeys(t *testing.T) {
	tests := []struct {
		name    string
		adapter Adapter
		content string
	}{
		{
			name:    "opencode provider array",
			adapter: OpenCodeAdapter{},
			content: `{"provider": []}`,
		},
		{
			name:    "opencode duplicate provider",
			adapter: OpenCodeAdapter{},
			content: `{"provider": {}, "provider": {}}`,
		},
		{
			name:    "opencode duplicate options",
			adapter: OpenCodeAdapter{},
			content: `{"provider": {"acme-ai": {"options": {}, "options": {}}}}`,
		},
		{
			name:    "opencode duplicate model leaf",
			adapter: OpenCodeAdapter{},
			content: `{"provider": {"acme-ai": {"models": {"m": {}, "m": {}}}}}`,
		},
		{
			name:    "Claude env scalar",
			adapter: ClaudeAdapter{},
			content: `{"env": "unsafe"}`,
		},
		{
			name:    "Claude duplicate env",
			adapter: ClaudeAdapter{},
			content: `{"env": {}, "env": {}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, DefaultConfigPath(test.adapter.Tool(), home), test.content, 0o644)
			if _, err := test.adapter.Plan(testPreset(""), home); !errors.Is(err, ErrUnsafeShape) {
				t.Fatalf("expected ErrUnsafeShape, got %v", err)
			}
		})
	}
}

func TestReadManagedRequiresProviderID(t *testing.T) {
	home := t.TempDir()
	for _, adapter := range []Adapter{OpenCodeAdapter{}, ClaudeAdapter{}, CodexAdapter{}} {
		if _, err := adapter.ReadManaged(home, ""); !errors.Is(err, ErrInvalidPreset) {
			t.Fatalf("%s: expected ErrInvalidPreset, got %v", adapter.Tool(), err)
		}
	}
}

func TestSymlinkPlanTargetsResolvedFile(t *testing.T) {
	home := t.TempDir()
	path := DefaultConfigPath(ToolOpencode, home)
	target := filepath.Join(home, "real-opencode.json")
	writeFile(t, target, `{ "unmanaged": true }`, 0o644)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	changeSet, err := (OpenCodeAdapter{}).Plan(testPreset(""), home)
	if err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(changeSet.Changes) != 1 || changeSet.Changes[0].Path != resolvedTarget {
		t.Fatalf("Plan did not resolve symlink: %+v", changeSet.Changes)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	linkTarget, err := os.Readlink(path)
	if err != nil || linkTarget != target {
		t.Fatalf("symlink was replaced: target=%q err=%v", linkTarget, err)
	}
	if !strings.Contains(string(readFile(t, target)), "acme-ai") {
		t.Fatalf("resolved target was not written")
	}
}

func TestExportSnippets(t *testing.T) {
	preset := testPreset("")
	for _, adapter := range []Adapter{OpenCodeAdapter{}, ClaudeAdapter{}, CodexAdapter{}} {
		snippet, err := adapter.ExportSnippet(preset)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(snippet.Content, preset.BaseURL) {
			t.Fatalf("%s snippet omitted base URL: %+v", adapter.Tool(), snippet)
		}
		if snippet.TargetPath == "" || snippet.Format == "" {
			t.Fatalf("%s snippet omitted target metadata: %+v", adapter.Tool(), snippet)
		}
	}
	codexSnippet, err := (CodexAdapter{}).ExportSnippet(preset)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(codexSnippet.Notes, `~/.codex/auth.json`) || !strings.Contains(codexSnippet.Notes, `OPENAI_API_KEY`) {
		t.Fatalf("Codex auth guidance missing: %+v", codexSnippet)
	}
}

func TestResolveManagedPathUsesNearestSymlinkAncestorAndContainsHome(t *testing.T) {
	home := t.TempDir()
	realDir := filepath.Join(home, "real-config")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(home, "linked-config")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(linkedDir, "settings.json")
	resolved, exists, err := resolveManagedPath(missing, home)
	if err != nil {
		t.Fatal(err)
	}
	realDirResolved, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(realDirResolved, "settings.json")
	if exists || resolved != wantPath {
		t.Fatalf("missing symlinked path = %q, %v; want %q, false", resolved, exists, wantPath)
	}
	writeFile(t, filepath.Join(realDir, "settings.json"), `{}`, 0o644)
	resolved, exists, err = resolveManagedPath(missing, home)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || resolved != wantPath {
		t.Fatalf("existing symlinked path = %q, %v", resolved, exists)
	}

	outside := t.TempDir()
	outsideLink := filepath.Join(home, "outside")
	if err := os.Symlink(outside, outsideLink); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveManagedPath(filepath.Join(outsideLink, "config.json"), home); !errors.Is(err, ErrInvalidPreset) || !strings.Contains(err.Error(), "path escapes home") {
		t.Fatalf("outside symlink was not rejected: %v", err)
	}
}

func TestPlanDoesNotCreateParents(t *testing.T) {
	for _, test := range []struct {
		name    string
		adapter Adapter
		parent  func(string) string
	}{
		{name: "opencode", adapter: OpenCodeAdapter{}, parent: func(home string) string { return filepath.Join(home, ".config", "opencode") }},
		{name: "claude", adapter: ClaudeAdapter{}, parent: func(home string) string { return filepath.Join(home, ".claude") }},
		{name: "codex", adapter: CodexAdapter{}, parent: func(home string) string { return filepath.Join(home, ".codex") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if _, err := test.adapter.Plan(testPreset(""), home); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(test.parent(home)); !os.IsNotExist(err) {
				t.Fatalf("Plan created %s: %v", test.parent(home), err)
			}
		})
	}
}

func TestReadManagedRawIsPlaintextButManagedIsMasked(t *testing.T) {
	for _, adapter := range []Adapter{OpenCodeAdapter{}, ClaudeAdapter{}, CodexAdapter{}} {
		t.Run(string(adapter.Tool()), func(t *testing.T) {
			home := t.TempDir()
			preset := testPreset("")
			commitPlan(t, adapter, preset, home)
			raw, err := adapter.ReadManagedRaw(home, "acme-ai")
			if err != nil {
				t.Fatal(err)
			}
			if !raw.Present || raw.APIKey != preset.APIKey || raw.BaseURL != preset.BaseURL || len(raw.Models) == 0 {
				t.Fatalf("unexpected raw section: %+v", raw)
			}
			managed, err := adapter.ReadManaged(home, "acme-ai")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(managed.Fields["apiKey"]+managed.Fields["ANTHROPIC_AUTH_TOKEN"]+managed.Fields["OPENAI_API_KEY"], preset.APIKey) {
				t.Fatalf("masked section leaked secret: %+v", managed)
			}
		})
	}
}

func TestOpenCodeReadManagedHandlesMissingOptions(t *testing.T) {
	home := t.TempDir()
	path := DefaultConfigPath(ToolOpencode, home)
	writeFile(t, path, `{"provider":{"acme-ai":{"models":{}}}}`, 0o644)
	managed, err := (OpenCodeAdapter{}).ReadManaged(home, "acme-ai")
	if err != nil {
		t.Fatal(err)
	}
	if !managed.Present || managed.BaseURL != "" {
		t.Fatalf("unexpected managed section: %+v", managed)
	}
	raw, err := (OpenCodeAdapter{}).ReadManagedRaw(home, "acme-ai")
	if err != nil || !raw.Present || raw.APIKey != "" {
		t.Fatalf("unexpected raw section: %+v, err=%v", raw, err)
	}
}

func TestOpenCodeModelsShapeAndWhitespaceFile(t *testing.T) {
	home := t.TempDir()
	path := DefaultConfigPath(ToolOpencode, home)
	writeFile(t, path, " \r\n\t", 0o644)
	if _, err := (OpenCodeAdapter{}).Plan(testPreset(""), home); err != nil {
		t.Fatalf("whitespace-only JSON was not treated as empty: %v", err)
	}
	writeFile(t, path, `{"provider":{"acme-ai":{"models":[]}}}`, 0o644)
	if _, err := (OpenCodeAdapter{}).Plan(testPreset(""), home); !errors.Is(err, ErrUnsafeShape) {
		t.Fatalf("models array was not rejected: %v", err)
	}
}

func TestOpenCodeProviderScopedPointerClearing(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "other provider survives", want: `"model": "other/keep"`},
		{name: "same provider clears", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			pointer := "other/keep"
			if test.want == "" {
				pointer = "acme-ai/old"
			}
			writeFile(t, DefaultConfigPath(ToolOpencode, home), `{"model":"`+pointer+`"}`, 0o644)
			preset := testPreset("")
			preset.Models = nil
			commitPlan(t, OpenCodeAdapter{}, preset, home)
			content := string(readFile(t, DefaultConfigPath(ToolOpencode, home)))
			if test.want != "" && !strings.Contains(content, `"model":"other/keep"`) {
				t.Fatalf("pointer was unexpectedly changed: %s", content)
			}
			if test.want == "" && strings.Contains(content, `"model"`) {
				t.Fatalf("provider-scoped pointer was not cleared: %s", content)
			}
		})
	}
}

func TestClaudeNoDefaultLeavesGlobalPointers(t *testing.T) {
	home := t.TempDir()
	path := DefaultConfigPath(ToolClaude, home)
	writeFile(t, path, `{"model":"global-root","env":{"ANTHROPIC_MODEL":"global-env"}}`, 0o644)
	preset := testPreset("")
	preset.Models = nil
	commitPlan(t, ClaudeAdapter{}, preset, home)
	content := string(readFile(t, path))
	if !strings.Contains(content, `"model":"global-root"`) || !strings.Contains(content, `"ANTHROPIC_MODEL":"global-env"`) {
		t.Fatalf("Claude global pointers changed by no-default preset: %s", content)
	}
}

func TestCodexProviderIDCharset(t *testing.T) {
	preset := testPreset("")
	preset.ProviderID = "not.a-bare-key"
	if _, err := (CodexAdapter{}).Plan(preset, t.TempDir()); !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("invalid provider ID was accepted: %v", err)
	}
}

func TestCodexCRLFMultilineHeaderFailsClosedOrRemainsCorrect(t *testing.T) {
	fixture := "description = \"\"\"line\r\n[model_providers.acme-ai]\r\nname = \"inside\"\r\n\"\"\"\r\nmodel_provider = \"old\"\r\nmodel = \"old-model\"\r\ndisable_response_storage = false\r\n"
	result, err := spliceCodexConfig([]byte(fixture), "acme-ai", "Acme AI", "https://api.example.test/v1", "acme-model")
	if err != nil {
		return
	}
	values, err := readTOMLBytes(result)
	if err != nil {
		t.Fatalf("splice returned unparsable TOML: %v", err)
	}
	if values["model_provider"] != "acme-ai" || values["model"] != "acme-model" || !strings.Contains(tomlString(values["description"]), "[model_providers.acme-ai]") {
		t.Fatalf("splice silently corrupted multiline TOML: %s", result)
	}
}

func TestParentDirectorySymlinkCommitTargetsRealDirectory(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config")
	realDir := filepath.Join(home, "real-opencode")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(configDir, "opencode")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	changeSet, err := (OpenCodeAdapter{}).Plan(testPreset(""), home)
	if err != nil {
		t.Fatal(err)
	}
	realDirResolved, err := filepath.EvalSymlinks(realDir)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(realDirResolved, "opencode.json")
	if changeSet.Changes[0].Path != wantPath {
		t.Fatalf("resolved parent path = %q, want %q", changeSet.Changes[0].Path, wantPath)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(wantPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(linkDir); err != nil {
		t.Fatal(err)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

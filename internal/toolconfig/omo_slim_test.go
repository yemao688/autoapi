package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const omoSlimFixture = `{
  // keep me
  "preset": "balanced",
  "presets": {
    "balanced": {
      "orchestrator": {
        "model": "autoapi-compatible/old-model",
        "variant": "slow",
        "displayName": "chief",
        "skills": ["keep-skill"],
        "mcps": ["keep-mcp"],
        "temperature": 0.5
      },
      "oracle": {
        "model": "autoapi-compatible/oracle-model",
        "variant": "careful"
      }
    },
    "fast": {
      "orchestrator": {
        "model": "autoapi-compatible/fast-model",
        "variant": "quick"
      },
      "oracle": {
        "model": "autoapi-compatible/oracle-fast",
        "variant": "quick"
      }
    }
  },
  "agents": {
    "custom": {
      "model": "autoapi-compatible/custom-model",
      "variant": "custom",
      "displayName": "helper",
      "skills": ["simplify"],
      "mcps": ["*"],
      "prompt": "keep prompt",
      "orchestratorPrompt": "@custom\n- route things"
    }
  },
  "disabled_agents": ["observer"],
  "disabled_skills": ["codemap"],
  "disabled_mcps": ["context7"]
}
`

func writeOmoSlimFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".config", "opencode", "oh-my-opencode-slim.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(omoSlimFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func TestDetectOmoSlimConfigPrefersJSONC(t *testing.T) {
	home, jsonc := writeOmoSlimFixture(t)
	json := filepath.Join(filepath.Dir(jsonc), "oh-my-opencode-slim.json")
	if err := os.WriteFile(json, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := DetectOmoSlimConfig(home); !ok || got != jsonc {
		t.Fatalf("DetectOmoSlimConfig = %q, %v; want %q, true", got, ok, jsonc)
	}
	if _, ok := DetectOmoSlimConfig(t.TempDir()); ok {
		t.Fatal("missing OMO Slim config was detected")
	}
}

func TestReadOmoSlimConfigRoundTripProjection(t *testing.T) {
	home, path := writeOmoSlimFixture(t)
	config, err := ReadOmoSlimConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Path != resolvedPath || config.ActivePreset != "balanced" {
		t.Fatalf("unexpected config identity: %+v", config)
	}
	wantOrchestrator := OmoSlimAgent{
		Model:       "autoapi-compatible/old-model",
		Variant:     "slow",
		DisplayName: "chief",
		Skills:      []string{"keep-skill"},
		Mcps:        []string{"keep-mcp"},
	}
	if !reflect.DeepEqual(config.Agents["orchestrator"], wantOrchestrator) {
		t.Fatalf("built-in agent not read: %+v", config.Agents["orchestrator"])
	}
	wantCustom := OmoSlimAgent{
		Model:       "autoapi-compatible/custom-model",
		Variant:     "custom",
		DisplayName: "helper",
		Skills:      []string{"simplify"},
		Mcps:        []string{"*"},
	}
	if !reflect.DeepEqual(config.Agents["custom"], wantCustom) {
		t.Fatalf("custom agent not read: %+v", config.Agents["custom"])
	}
	wantCustomFull := OmoSlimCustomAgent{
		Model:              "autoapi-compatible/custom-model",
		Variant:            "custom",
		DisplayName:        "helper",
		Skills:             []string{"simplify"},
		Mcps:               []string{"*"},
		Prompt:             "keep prompt",
		OrchestratorPrompt: "@custom\n- route things",
	}
	if !reflect.DeepEqual(config.CustomAgents["custom"], wantCustomFull) {
		t.Fatalf("custom agent full record not read: %+v", config.CustomAgents["custom"])
	}
	if len(config.DisabledAgents) != 1 || config.DisabledAgents[0] != "observer" {
		t.Fatalf("disabled agents = %#v", config.DisabledAgents)
	}
	if len(config.DisabledSkills) != 1 || config.DisabledSkills[0] != "codemap" {
		t.Fatalf("disabled skills = %#v", config.DisabledSkills)
	}
	if len(config.DisabledMcps) != 1 || config.DisabledMcps[0] != "context7" {
		t.Fatalf("disabled mcps = %#v", config.DisabledMcps)
	}
}

func TestPlanOmoSlimChangePreservesJSONCAndChecksOpencode(t *testing.T) {
	home, omoSlimPath := writeOmoSlimFixture(t)
	openCodePath := DefaultConfigPath(ToolOpencode, home)
	if err := os.MkdirAll(filepath.Dir(openCodePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(openCodePath, []byte(`{
  "provider": {"autoapi": {"models": {"kimi-k3": {}}}},
  "keep": true
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(omoSlimPath)
	if err != nil {
		t.Fatal(err)
	}
	changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{
		Agents: map[string]OmoSlimAgent{
			"orchestrator": {Model: "autoapi-compatible/kimi-k3", Variant: "balanced"},
		},
		CustomAgents: map[string]OmoSlimCustomAgent{
			"custom": {
				Model:              "autoapi-compatible/kimi-k3",
				Variant:            "fast",
				DisplayName:        "helper",
				Skills:             []string{"simplify"},
				Mcps:               []string{"*"},
				Prompt:             "keep prompt",
				OrchestratorPrompt: "@custom\n- route things",
			},
		},
		DisabledAgents: []string{"observer", "council"},
	}, []string{"autoapi-compatible/kimi-k3"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(omoSlimPath); string(got) != string(before) {
		t.Fatal("PlanOmoSlimChange modified the live file")
	}
	if len(changeSet.Changes) != 1 || changeSet.Changes[0].Resource != ResOmoSlimConfig {
		t.Fatalf("unexpected changes: %+v", changeSet.Changes)
	}
	if len(changeSet.Checks) != 1 || changeSet.Checks[0].Resource != ResOpencodeConfig || changeSet.Checks[0].Path != openCodePath {
		t.Fatalf("unexpected checks: %+v", changeSet.Checks)
	}
	if !strings.Contains(string(changeSet.Changes[0].After), "// keep me") || !strings.Contains(string(changeSet.Changes[0].After), "keep prompt") || !strings.Contains(string(changeSet.Changes[0].After), "keep-skill") {
		t.Fatal("unmanaged OMO Slim content was not retained in rendered output")
	}

	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoSlimConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	wantOrchestrator := OmoSlimAgent{
		Model:   "autoapi-compatible/kimi-k3",
		Variant: "balanced",
		// DisplayName was not included in the change payload: empty string
		// means "clear the override", so the leaf is deleted. Nil Skills/Mcps
		// leave those leaves untouched.
		Skills: []string{"keep-skill"},
		Mcps:   []string{"keep-mcp"},
	}
	if !reflect.DeepEqual(config.Agents["orchestrator"], wantOrchestrator) {
		t.Fatalf("built-in patch not committed: %+v", config.Agents["orchestrator"])
	}
	if !strings.Contains(string(changeSet.Changes[0].After), `"temperature": 0.5`) {
		t.Fatal("unmanaged temperature leaf was dropped from rendered output")
	}
	if got := config.Agents["custom"]; got.Model != "autoapi-compatible/kimi-k3" || got.Variant != "fast" || got.DisplayName != "helper" {
		t.Fatalf("custom patch not committed: %+v", got)
	}
	if got := config.CustomAgents["custom"]; got.Prompt != "keep prompt" || got.OrchestratorPrompt != "@custom\n- route things" {
		t.Fatalf("custom prompt leaves not committed: %+v", got)
	}
	if len(config.DisabledAgents) != 2 || config.DisabledAgents[1] != "council" {
		t.Fatalf("disabled agent patch not committed: %#v", config.DisabledAgents)
	}
	committed, err := os.ReadFile(omoSlimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(committed), "// keep me") {
		t.Fatal("JSONC comment was not preserved after Commit")
	}
}

func TestPlanOmoSlimChangeSwitchesPreset(t *testing.T) {
	home, _ := writeOmoSlimFixture(t)
	changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{
		ActivePreset: stringPtr("fast"),
		Agents: map[string]OmoSlimAgent{
			"oracle": {Model: "autoapi-compatible/kimi-k3", Variant: "fast"},
		},
	}, []string{"autoapi-compatible/kimi-k3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoSlimConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if config.ActivePreset != "fast" || config.Agents["oracle"].Model != "autoapi-compatible/kimi-k3" {
		t.Fatalf("preset switch not committed: %+v", config)
	}
}

func TestPlanOmoSlimPresetUpsertNewAndDuplicate(t *testing.T) {
	t.Run("new empty preset", func(t *testing.T) {
		home, path := writeOmoSlimFixture(t)
		changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{PresetOps: []OmoSlimPresetOp{{
			Operation: OmoSlimPresetUpsert,
			Name:      "empty",
		}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
			t.Fatal(err)
		}
		presets, err := ListOmoSlimPresets(home)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(presets, []string{"balanced", "empty", "fast"}) {
			t.Fatalf("presets = %#v", presets)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "// keep me") {
			t.Fatal("unmanaged comment was not preserved")
		}
	})

	t.Run("duplicate agents", func(t *testing.T) {
		home, _ := writeOmoSlimFixture(t)
		agents := map[string]OmoSlimAgent{
			"orchestrator": {Model: "autoapi-compatible/fast-model", Variant: "quick"},
			"oracle":       {Model: "autoapi-compatible/oracle-fast", Variant: "quick"},
		}
		changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{PresetOps: []OmoSlimPresetOp{{
			Operation: OmoSlimPresetUpsert,
			Name:      "copy",
			Agents:    agents,
		}}}, []string{"autoapi-compatible/fast-model", "autoapi-compatible/oracle-fast"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
			t.Fatal(err)
		}
		presetAgents, err := ListOmoSlimPresetAgents(home)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(presetAgents["copy"], agents) {
			t.Fatalf("copied agents = %#v, want %#v", presetAgents["copy"], agents)
		}
	})
}

func TestPlanOmoSlimPresetRenameUpdatesActiveAndRejectsConflicts(t *testing.T) {
	home, _ := writeOmoSlimFixture(t)
	changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{PresetOps: []OmoSlimPresetOp{{
		Operation: OmoSlimPresetRename,
		Name:      "balanced",
		NewName:   "renamed",
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoSlimConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if config.ActivePreset != "renamed" || config.Agents["orchestrator"].Variant != "slow" {
		t.Fatalf("renamed active preset = %+v", config)
	}
	if _, err := PlanOmoSlimChange(home, OmoSlimChange{PresetOps: []OmoSlimPresetOp{{
		Operation: OmoSlimPresetRename,
		Name:      "renamed",
		NewName:   "fast",
	}}}, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("rename conflict error = %v", err)
	}
}

func TestPlanOmoSlimPresetDeleteClearsActiveAndRejectsInvalidNames(t *testing.T) {
	home, _ := writeOmoSlimFixture(t)
	changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{PresetOps: []OmoSlimPresetOp{{
		Operation: OmoSlimPresetDelete,
		Name:      "balanced",
	}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoSlimConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if config.ActivePreset != "" || config.Agents == nil {
		t.Fatalf("active preset was not cleared: %+v", config)
	}

	cases := []OmoSlimPresetOp{
		{Operation: OmoSlimPresetDelete, Name: "missing"},
		{Operation: OmoSlimPresetRename, Name: "missing", NewName: "new"},
		{Operation: OmoSlimPresetUpsert, Name: "   "},
		{Operation: OmoSlimPresetUpsert, Name: "bad\nname"},
	}
	for _, op := range cases {
		freshHome, _ := writeOmoSlimFixture(t)
		if _, err := PlanOmoSlimChange(freshHome, OmoSlimChange{PresetOps: []OmoSlimPresetOp{op}}, nil); err == nil {
			t.Fatalf("operation %+v unexpectedly succeeded", op)
		}
	}
}

func TestPlanOmoSlimPresetRejectsDuplicateJSONKeys(t *testing.T) {
	home, path := writeOmoSlimFixture(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(content), `"fast": {`, `"fast": {}, "fast": {`, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = PlanOmoSlimChange(home, OmoSlimChange{PresetOps: []OmoSlimPresetOp{{
		Operation: OmoSlimPresetUpsert,
		Name:      "new",
	}}}, nil)
	if err == nil || !errors.Is(err, ErrUnsafeShape) {
		t.Fatalf("duplicate key error = %v", err)
	}
}

func TestPlanOmoSlimChangeReportsAllMissingModels(t *testing.T) {
	home, _ := writeOmoSlimFixture(t)
	_, err := PlanOmoSlimChange(home, OmoSlimChange{Agents: map[string]OmoSlimAgent{
		"oracle": {Model: "missing-oracle"},
		"fixer":  {Model: "missing-fixer"},
	}}, []string{"autoapi-compatible/kimi-k3"})
	if !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("error = %v, want ErrInvalidPreset", err)
	}
	for _, reference := range []string{"oracle=missing-oracle", "fixer=missing-fixer"} {
		if !strings.Contains(err.Error(), reference) {
			t.Errorf("error %q omitted %q", err, reference)
		}
	}
}

func TestPlanOmoSlimChangeWritesAgentArraysAndClearsLeaves(t *testing.T) {
	home, omoSlimPath := writeOmoSlimFixture(t)
	changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{
		Agents: map[string]OmoSlimAgent{
			"orchestrator": {
				Model:       "autoapi-compatible/kimi-k3",
				Variant:     "", // clear the variant leaf
				DisplayName: "", // clear the displayName leaf
				Skills:      []string{"*", "!codemap"},
				Mcps:        []string{},
			},
		},
	}, []string{"autoapi-compatible/kimi-k3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoSlimConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	want := OmoSlimAgent{
		Model:  "autoapi-compatible/kimi-k3",
		Skills: []string{"*", "!codemap"},
		Mcps:   []string{},
	}
	if !reflect.DeepEqual(config.Agents["orchestrator"], want) {
		t.Fatalf("agent leaves not written as expected: %+v", config.Agents["orchestrator"])
	}
	committed, err := os.ReadFile(omoSlimPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(committed), `"variant": "slow"`) || strings.Contains(string(committed), `"chief"`) {
		t.Fatal("cleared leaves still present in committed output")
	}
	if !strings.Contains(string(committed), `"temperature": 0.5`) {
		t.Fatal("unmanaged temperature leaf was dropped")
	}
}

func TestPlanOmoSlimChangeCustomAgentsReplaceDropsStaleAndPreservesBuiltIn(t *testing.T) {
	home, omoSlimPath := writeOmoSlimFixture(t)
	// Add a built-in override under agents; the replace must preserve it.
	raw, err := os.ReadFile(omoSlimPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), `"agents": {`, "\"agents\": {\n    \"oracle\": {\"displayName\": \"advisor\"},", 1)
	if err := os.WriteFile(omoSlimPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	changeSet, err := PlanOmoSlimChange(home, OmoSlimChange{
		CustomAgents: map[string]OmoSlimCustomAgent{
			"database": {
				Model:              "autoapi-compatible/kimi-k3",
				DisplayName:        "db",
				Prompt:             "You are a database specialist.",
				OrchestratorPrompt: "@database\n- Delegate SQL work.",
			},
		},
	}, []string{"autoapi-compatible/kimi-k3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoSlimConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, stale := config.CustomAgents["custom"]; stale {
		t.Fatal("stale custom agent survived the replace")
	}
	if got := config.CustomAgents["database"]; got.Model != "autoapi-compatible/kimi-k3" || got.DisplayName != "db" || got.Prompt == "" {
		t.Fatalf("new custom agent not written: %+v", got)
	}
	committed, err := os.ReadFile(omoSlimPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(committed), `"advisor"`) {
		t.Fatal("built-in override under agents was dropped by the replace")
	}
}

func TestValidateOmoSlimCustomAgentsRules(t *testing.T) {
	valid := []string{"autoapi-compatible/kimi-k3"}
	cases := []struct {
		name   string
		agents map[string]OmoSlimCustomAgent
		want   string
	}{
		{
			name:   "built-in name rejected",
			agents: map[string]OmoSlimCustomAgent{"oracle": {Model: "autoapi-compatible/kimi-k3"}},
			want:   "built-in",
		},
		{
			name:   "unknown model rejected",
			agents: map[string]OmoSlimCustomAgent{"custom": {Model: "nope"}},
			want:   "custom=nope",
		},
		{
			name:   "display name collides with built-in",
			agents: map[string]OmoSlimCustomAgent{"custom": {DisplayName: "oracle"}},
			want:   "collides with a built-in",
		},
		{
			name: "display name duplicated",
			agents: map[string]OmoSlimCustomAgent{
				"a": {DisplayName: "helper"},
				"b": {DisplayName: "helper"},
			},
			want: "used by both",
		},
		{
			name:   "orchestratorPrompt must self-mention",
			agents: map[string]OmoSlimCustomAgent{"custom": {OrchestratorPrompt: "do things"}},
			want:   "must start with @custom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := writeOmoSlimFixture(t)
			_, err := PlanOmoSlimChange(home, OmoSlimChange{CustomAgents: tc.agents}, valid)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestListKnownSkills(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{
		filepath.Join(".config", "opencode", "skills", "simplify"),
		filepath.Join(".agents", "skills", "vitest"),
	} {
		if err := os.MkdirAll(filepath.Join(home, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skills, err := ListKnownSkills(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skills, []string{"simplify", "vitest"}) {
		t.Fatalf("skills = %#v", skills)
	}
	if got, err := ListKnownSkills(t.TempDir()); err != nil || len(got) != 0 {
		t.Fatalf("missing dirs must yield an empty list, got %#v, %v", got, err)
	}
}

func TestListMcpNamesIncludesBundledAndConfigured(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "opencode", "opencode.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcp": {"dbx": {}, "agent-browser": {}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := ListMcpNames(home)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"agent-browser", "context7", "dbx", "gh_grep", "websearch"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("mcp names = %#v, want %#v", names, want)
	}
	// A missing config still yields the bundled set.
	missing, err := ListMcpNames(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(missing, []string{"context7", "gh_grep", "websearch"}) {
		t.Fatalf("bundled-only mcp names = %#v", missing)
	}
}

func stringPtr(value string) *string { return &value }

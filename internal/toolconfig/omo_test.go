package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const omoFixture = `{
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

func writeOmoFixture(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".config", "opencode", "oh-my-opencode-slim.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(omoFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func TestDetectOmoConfigPrefersJSONC(t *testing.T) {
	home, jsonc := writeOmoFixture(t)
	json := filepath.Join(filepath.Dir(jsonc), "oh-my-opencode-slim.json")
	if err := os.WriteFile(json, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := DetectOmoConfig(home); !ok || got != jsonc {
		t.Fatalf("DetectOmoConfig = %q, %v; want %q, true", got, ok, jsonc)
	}
	if _, ok := DetectOmoConfig(t.TempDir()); ok {
		t.Fatal("missing OMO config was detected")
	}
}

func TestReadOmoConfigRoundTripProjection(t *testing.T) {
	home, path := writeOmoFixture(t)
	config, err := ReadOmoConfig(home)
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
	wantOrchestrator := OmoAgent{
		Model:       "autoapi-compatible/old-model",
		Variant:     "slow",
		DisplayName: "chief",
		Skills:      []string{"keep-skill"},
		Mcps:        []string{"keep-mcp"},
	}
	if !reflect.DeepEqual(config.Agents["orchestrator"], wantOrchestrator) {
		t.Fatalf("built-in agent not read: %+v", config.Agents["orchestrator"])
	}
	wantCustom := OmoAgent{
		Model:       "autoapi-compatible/custom-model",
		Variant:     "custom",
		DisplayName: "helper",
		Skills:      []string{"simplify"},
		Mcps:        []string{"*"},
	}
	if !reflect.DeepEqual(config.Agents["custom"], wantCustom) {
		t.Fatalf("custom agent not read: %+v", config.Agents["custom"])
	}
	wantCustomFull := OmoCustomAgent{
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

func TestPlanOmoChangePreservesJSONCAndChecksOpencode(t *testing.T) {
	home, omoPath := writeOmoFixture(t)
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
	before, err := os.ReadFile(omoPath)
	if err != nil {
		t.Fatal(err)
	}
	changeSet, err := PlanOmoChange(home, OmoChange{
		Agents: map[string]OmoAgent{
			"orchestrator": {Model: "autoapi-compatible/kimi-k3", Variant: "balanced"},
		},
		CustomAgents: map[string]OmoCustomAgent{
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
	if got, _ := os.ReadFile(omoPath); string(got) != string(before) {
		t.Fatal("PlanOmoChange modified the live file")
	}
	if len(changeSet.Changes) != 1 || changeSet.Changes[0].Resource != ResOmoConfig {
		t.Fatalf("unexpected changes: %+v", changeSet.Changes)
	}
	if len(changeSet.Checks) != 1 || changeSet.Checks[0].Resource != ResOpencodeConfig || changeSet.Checks[0].Path != openCodePath {
		t.Fatalf("unexpected checks: %+v", changeSet.Checks)
	}
	if !strings.Contains(string(changeSet.Changes[0].After), "// keep me") || !strings.Contains(string(changeSet.Changes[0].After), "keep prompt") || !strings.Contains(string(changeSet.Changes[0].After), "keep-skill") {
		t.Fatal("unmanaged OMO content was not retained in rendered output")
	}

	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	wantOrchestrator := OmoAgent{
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
	committed, err := os.ReadFile(omoPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(committed), "// keep me") {
		t.Fatal("JSONC comment was not preserved after Commit")
	}
}

func TestPlanOmoChangeSwitchesPreset(t *testing.T) {
	home, _ := writeOmoFixture(t)
	changeSet, err := PlanOmoChange(home, OmoChange{
		ActivePreset: stringPtr("fast"),
		Agents: map[string]OmoAgent{
			"oracle": {Model: "autoapi-compatible/kimi-k3", Variant: "fast"},
		},
	}, []string{"autoapi-compatible/kimi-k3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Commit(changeSet, CommitOpts{BackupRoot: filepath.Join(home, "backups")}); err != nil {
		t.Fatal(err)
	}
	config, err := ReadOmoConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if config.ActivePreset != "fast" || config.Agents["oracle"].Model != "autoapi-compatible/kimi-k3" {
		t.Fatalf("preset switch not committed: %+v", config)
	}
}

func TestPlanOmoChangeReportsAllMissingModels(t *testing.T) {
	home, _ := writeOmoFixture(t)
	_, err := PlanOmoChange(home, OmoChange{Agents: map[string]OmoAgent{
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

func TestPlanOmoChangeWritesAgentArraysAndClearsLeaves(t *testing.T) {
	home, omoPath := writeOmoFixture(t)
	changeSet, err := PlanOmoChange(home, OmoChange{
		Agents: map[string]OmoAgent{
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
	config, err := ReadOmoConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	want := OmoAgent{
		Model:  "autoapi-compatible/kimi-k3",
		Skills: []string{"*", "!codemap"},
		Mcps:   []string{},
	}
	if !reflect.DeepEqual(config.Agents["orchestrator"], want) {
		t.Fatalf("agent leaves not written as expected: %+v", config.Agents["orchestrator"])
	}
	committed, err := os.ReadFile(omoPath)
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

func TestPlanOmoChangeCustomAgentsReplaceDropsStaleAndPreservesBuiltIn(t *testing.T) {
	home, omoPath := writeOmoFixture(t)
	// Add a built-in override under agents; the replace must preserve it.
	raw, err := os.ReadFile(omoPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), `"agents": {`, "\"agents\": {\n    \"oracle\": {\"displayName\": \"advisor\"},", 1)
	if err := os.WriteFile(omoPath, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	changeSet, err := PlanOmoChange(home, OmoChange{
		CustomAgents: map[string]OmoCustomAgent{
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
	config, err := ReadOmoConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, stale := config.CustomAgents["custom"]; stale {
		t.Fatal("stale custom agent survived the replace")
	}
	if got := config.CustomAgents["database"]; got.Model != "autoapi-compatible/kimi-k3" || got.DisplayName != "db" || got.Prompt == "" {
		t.Fatalf("new custom agent not written: %+v", got)
	}
	committed, err := os.ReadFile(omoPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(committed), `"advisor"`) {
		t.Fatal("built-in override under agents was dropped by the replace")
	}
}

func TestValidateOmoCustomAgentsRules(t *testing.T) {
	valid := []string{"autoapi-compatible/kimi-k3"}
	cases := []struct {
		name   string
		agents map[string]OmoCustomAgent
		want   string
	}{
		{
			name:   "built-in name rejected",
			agents: map[string]OmoCustomAgent{"oracle": {Model: "autoapi-compatible/kimi-k3"}},
			want:   "built-in",
		},
		{
			name:   "unknown model rejected",
			agents: map[string]OmoCustomAgent{"custom": {Model: "nope"}},
			want:   "custom=nope",
		},
		{
			name:   "display name collides with built-in",
			agents: map[string]OmoCustomAgent{"custom": {DisplayName: "oracle"}},
			want:   "collides with a built-in",
		},
		{
			name: "display name duplicated",
			agents: map[string]OmoCustomAgent{
				"a": {DisplayName: "helper"},
				"b": {DisplayName: "helper"},
			},
			want: "used by both",
		},
		{
			name:   "orchestratorPrompt must self-mention",
			agents: map[string]OmoCustomAgent{"custom": {OrchestratorPrompt: "do things"}},
			want:   "must start with @custom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := writeOmoFixture(t)
			_, err := PlanOmoChange(home, OmoChange{CustomAgents: tc.agents}, valid)
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

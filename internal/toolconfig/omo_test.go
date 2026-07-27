package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
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
        "skills": ["keep-skill"],
        "mcps": {"keep": true}
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
      "prompt": "keep prompt"
    }
  },
  "disabled_agents": ["observer"]
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
	if config.Agents["orchestrator"] != (OmoAgent{Model: "autoapi-compatible/old-model", Variant: "slow"}) {
		t.Fatalf("built-in agent not read: %+v", config.Agents["orchestrator"])
	}
	if config.Agents["custom"] != (OmoAgent{Model: "autoapi-compatible/custom-model", Variant: "custom"}) {
		t.Fatalf("custom agent not read: %+v", config.Agents["custom"])
	}
	if len(config.DisabledAgents) != 1 || config.DisabledAgents[0] != "observer" {
		t.Fatalf("disabled agents = %#v", config.DisabledAgents)
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
			"custom":       {Model: "autoapi-compatible/kimi-k3", Variant: "fast"},
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
	if config.Agents["orchestrator"] != (OmoAgent{Model: "autoapi-compatible/kimi-k3", Variant: "balanced"}) {
		t.Fatalf("built-in patch not committed: %+v", config.Agents["orchestrator"])
	}
	if config.Agents["custom"] != (OmoAgent{Model: "autoapi-compatible/kimi-k3", Variant: "fast"}) {
		t.Fatalf("custom patch not committed: %+v", config.Agents["custom"])
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

func stringPtr(value string) *string { return &value }

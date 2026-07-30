package toolconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexCatalogPreset(providerID, name, baseURL string, models []PresetModel) PresetPlaintext {
	return PresetPlaintext{Preset: Preset{
		Tool:       ToolCodex,
		Kind:       PresetDirect,
		Name:       name,
		ProviderID: providerID,
		BaseURL:    baseURL,
		Models:     models,
	}}
}

func findCodexCatalogChange(t *testing.T, changeSet *ChangeSet) FileChange {
	t.Helper()
	for _, change := range changeSet.Changes {
		if change.Resource == codexModelCatalogResource {
			return change
		}
	}
	t.Fatalf("Codex model catalog change missing: %+v", changeSet.Changes)
	return FileChange{}
}

func TestPlanToolConfigChangeCodexBuildsModelCatalog(t *testing.T) {
	homeDir := t.TempDir()
	configPath := DefaultConfigPath(ToolCodex, homeDir)
	writeFile(t, configPath, `# keep this comment
unmanaged = true

[model_providers.existing]
name = "Existing"
base_url = "https://existing.example"
`, 0o644)
	changes := []ToolProviderChange{
		{Action: "upsert", Preset: codexCatalogPreset("first", "First", "https://first.example", []PresetModel{
			{
				Name:       "alpha",
				Reasoning:  true,
				Limit:      &ModelLimit{Context: 4096, Output: 512},
				Modalities: []string{"text", "image"},
			},
			{Name: "shared", Reasoning: true},
		})},
		{Action: "upsert", Preset: codexCatalogPreset("second", "Second", "https://second.example", []PresetModel{
			{Name: "shared", Reasoning: false, Limit: &ModelLimit{Context: 999}, Modalities: []string{"audio"}},
			{Name: "beta"},
		})},
	}

	first, err := PlanToolConfigChange(ToolCodex, changes, "", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changes) != 2 {
		t.Fatalf("Codex catalog changes = %+v, want config and catalog", first.Changes)
	}
	configAfter := string(first.Changes[0].After)
	if !strings.Contains(configAfter, "# keep this comment") || !strings.Contains(configAfter, `model_catalog_json = "autoapi-model-catalog.json"`) {
		t.Fatalf("Codex config lost comment or pointer: %s", configAfter)
	}
	catalogChange := findCodexCatalogChange(t, first)
	resolvedCatalogPath, _, err := snapshotFile(filepath.Join(homeDir, ".codex", codexModelCatalogFilename), homeDir)
	if err != nil {
		t.Fatal(err)
	}
	if catalogChange.Mode != 0o644 || catalogChange.Secret || catalogChange.Path != resolvedCatalogPath {
		t.Fatalf("unexpected catalog file change: %+v", catalogChange)
	}

	var document struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(catalogChange.After, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Models) != 3 {
		t.Fatalf("catalog model count = %d, want 3: %s", len(document.Models), catalogChange.After)
	}
	for i, want := range []string{"alpha", "shared", "beta"} {
		if got := document.Models[i]["slug"]; got != want {
			t.Fatalf("catalog model %d slug = %v, want %q", i, got, want)
		}
	}
	alpha := document.Models[0]
	if alpha["display_name"] != "alpha" || alpha["description"] != "alpha" || alpha["base_instructions"] != "You are Codex, a coding agent." || alpha["supports_reasoning_summaries"] != true || alpha["context_window"] != float64(4096) || alpha["max_context_window"] != float64(4096) {
		t.Fatalf("unexpected alpha catalog entry: %#v", alpha)
	}
	if got := alpha["input_modalities"]; got == nil || len(got.([]any)) != 2 || got.([]any)[0] != "text" || got.([]any)[1] != "image" {
		t.Fatalf("unexpected alpha modalities: %#v", got)
	}
	shared := document.Models[1]
	if shared["supports_reasoning_summaries"] != true || shared["input_modalities"].([]any)[0] != "text" {
		t.Fatalf("duplicate shared model did not keep first metadata: %#v", shared)
	}
	if _, exists := shared["context_window"]; exists {
		t.Fatalf("shared model unexpectedly has context_window: %#v", shared)
	}
	beta := document.Models[2]
	if beta["input_modalities"].([]any)[0] != "text" {
		t.Fatalf("default beta modalities = %#v", beta["input_modalities"])
	}
	if _, exists := beta["context_window"]; exists {
		t.Fatalf("beta unexpectedly has context_window: %#v", beta)
	}

	commitStagedPlan(t, first, homeDir)
	second, err := PlanToolConfigChange(ToolCodex, changes, "", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	secondCatalog := findCodexCatalogChange(t, second)
	if string(second.Changes[0].After) != string(first.Changes[0].After) {
		t.Fatalf("config plan is not idempotent:\nfirst: %s\nsecond: %s", first.Changes[0].After, second.Changes[0].After)
	}
	if string(secondCatalog.After) != string(catalogChange.After) {
		t.Fatalf("catalog plan is not idempotent:\nfirst: %s\nsecond: %s", catalogChange.After, secondCatalog.After)
	}
}

func TestPlanToolConfigChangeCodexRemovesCatalogPointerWithoutTouchingStaleFile(t *testing.T) {
	homeDir := t.TempDir()
	configPath := DefaultConfigPath(ToolCodex, homeDir)
	catalogPath := filepath.Join(homeDir, ".codex", codexModelCatalogFilename)
	staleCatalog := `{"models":[{"slug":"stale"}]}
`
	writeFile(t, configPath, `# keep this comment
model_catalog_json = "autoapi-model-catalog.json"
model_provider = "remove-me"

[model_providers.remove-me]
name = "Remove"
base_url = "https://remove.example"

[model_providers.park-me]
name = "Park"
base_url = "https://park.example"
`, 0o644)
	writeFile(t, catalogPath, staleCatalog, 0o644)

	changeSet, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{
		{Action: "remove", Preset: codexCatalogPreset("remove-me", "Remove", "https://remove.example", nil)},
		{Action: "park", Preset: codexCatalogPreset("park-me", "Park", "https://park.example", nil)},
	}, "", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(changeSet.Changes) != 1 {
		t.Fatalf("empty catalog changes = %+v, want config only", changeSet.Changes)
	}
	if strings.Contains(string(changeSet.Changes[0].After), "model_catalog_json") {
		t.Fatalf("catalog pointer remained: %s", changeSet.Changes[0].After)
	}
	commitStagedPlan(t, changeSet, homeDir)
	if got := string(readFile(t, catalogPath)); got != staleCatalog {
		t.Fatalf("stale catalog was touched: %q", got)
	}
}

func TestPlanToolConfigChangeCodexCommonConfigCannotOverrideCatalogPointer(t *testing.T) {
	homeDir := t.TempDir()
	if _, err := PlanToolConfigChange(ToolCodex, nil, `model_catalog_json = "other.json"
`, homeDir); err == nil || !strings.Contains(err.Error(), "managed and cannot be overridden") {
		t.Fatalf("catalog pointer common-config error = %v", err)
	}
}

func TestCodexModelCatalogHasTrailingLF(t *testing.T) {
	homeDir := t.TempDir()
	changeSet, err := PlanToolConfigChange(ToolCodex, []ToolProviderChange{{
		Action: "upsert",
		Preset: codexCatalogPreset("provider", "Provider", "https://provider.example", []PresetModel{{Name: "model"}}),
	}}, "", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	catalogChange := findCodexCatalogChange(t, changeSet)
	if !strings.HasSuffix(string(catalogChange.After), "\n") || (len(catalogChange.After) > 1 && catalogChange.After[len(catalogChange.After)-2] == '\n') {
		t.Fatalf("catalog trailing newline is not exactly one LF: %q", catalogChange.After[len(catalogChange.After)-10:])
	}
	if _, err := os.Stat(catalogChange.Path); !os.IsNotExist(err) {
		t.Fatalf("planning created catalog file: %v", err)
	}
}

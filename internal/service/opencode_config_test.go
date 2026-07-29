package service

import (
	"errors"
	"os"
	"strings"
	"testing"

	"autoapi/internal/toolconfig"
)

func totalPlanPreset(providerID, name, baseURL string) toolconfig.Preset {
	return toolconfig.Preset{
		Tool:       toolconfig.ToolOpencode,
		Kind:       toolconfig.PresetDirect,
		Name:       name,
		ProviderID: providerID,
		Vendor:     toolconfig.VendorOpenAICompatible,
		BaseURL:    baseURL,
		Models:     []toolconfig.PresetModel{{Name: "model", Default: true}},
	}
}

func TestOpencodeTotalUpsertNewEnabledDoesNotCreateDBRow(t *testing.T) {
	svc, db, home := newToolConfigTestService(t)
	path := writeToolConfigFixture(t, home, `{"provider":{},"other":true}`)
	plan := OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{
		Action:       "upsert",
		Preset:       totalPlanPreset("new-provider", "New Provider", "https://new.example"),
		PlaintextKey: "new-key",
	}}}
	if err := svc.ApplyOpencodeConfigChange(plan, true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "new-provider") || !strings.Contains(string(content), "new-key") {
		t.Fatalf("upsert output = %s", content)
	}
	presets, err := db.ListToolPresets("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) != 0 {
		t.Fatalf("enabled upsert created parked rows: %+v", presets)
	}
}

func TestOpencodeTotalUpsertPreservesExistingFileKey(t *testing.T) {
	svc, _, home := newToolConfigTestService(t)
	path := writeToolConfigFixture(t, home, `{"provider":{"live":{"name":"Old","options":{"baseURL":"https://old.example","apiKey":"file-key"},"models":{}}}}`)
	plan := OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{
		Action: "upsert",
		Preset: totalPlanPreset("live", "Edited", "https://edited.example"),
	}}}
	if err := svc.ApplyOpencodeConfigChange(plan, true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "file-key") || !strings.Contains(string(content), "edited.example") {
		t.Fatalf("existing file key was lost: %s", content)
	}
}

func TestOpencodeTotalUpsertUsesParkedDBKey(t *testing.T) {
	svc, _, home := newToolConfigTestService(t)
	writeToolConfigFixture(t, home, `{"provider":{}}`)
	preset := totalPlanPreset("parked", "Parked", "https://parked.example")
	if _, err := svc.CreateToolPreset(preset, "db-key"); err != nil {
		t.Fatal(err)
	}
	plan := OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{Action: "upsert", Preset: preset}}}
	if err := svc.ApplyOpencodeConfigChange(plan, true); err != nil {
		t.Fatal(err)
	}
	raw, err := (toolconfig.OpenCodeAdapter{}).ReadManagedRaw(home, "parked")
	if err != nil || raw.APIKey != "db-key" {
		t.Fatalf("parked DB key was not rendered: raw=%+v err=%v", raw, err)
	}
}

func TestOpencodeTotalParkEnabledEncryptsFileKey(t *testing.T) {
	svc, db, home := newToolConfigTestService(t)
	path := writeToolConfigFixture(t, home, `{"provider":{"live":{"name":"Live","options":{"baseURL":"https://live.example","apiKey":"live-key"},"models":{"model":{"name":"model"}}}},"model":"live/model"}`)
	preset := totalPlanPreset("live", "Live Edited", "https://edited.example")
	if err := svc.ApplyOpencodeConfigChange(OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{Action: "park", Preset: preset}}}, true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "live-key") || strings.Contains(string(content), "\"live\"") {
		t.Fatalf("park left provider in file: %s", content)
	}
	rows, err := db.ListToolPresets("opencode")
	if err != nil || len(rows) != 1 {
		t.Fatalf("parked rows = %+v err=%v", rows, err)
	}
	key, err := svc.decryptToolKey(rows[0].APIKeyEnc)
	if err != nil || key != "live-key" {
		t.Fatalf("parked key roundtrip = %q err=%v", key, err)
	}
}

func TestOpencodeTotalParkAlreadyParkedUpdatesDBOnly(t *testing.T) {
	svc, db, home := newToolConfigTestService(t)
	original := `{"provider":{},"other":true}`
	path := writeToolConfigFixture(t, home, original)
	preset := totalPlanPreset("parked", "Parked", "https://old.example")
	created, err := svc.CreateToolPreset(preset, "old-key")
	if err != nil {
		t.Fatal(err)
	}
	updated := *created
	updated.Name = "Edited Parked"
	updated.BaseURL = "https://edited.example"
	if err := svc.ApplyOpencodeConfigChange(OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{Action: "park", Preset: updated}}}, true); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != original {
		t.Fatalf("already parked config changed: got=%q want=%q", content, original)
	}
	row, err := db.GetToolPreset(created.ID)
	if err != nil || row == nil || row.Name != "Edited Parked" {
		t.Fatalf("parked row was not updated: row=%+v err=%v", row, err)
	}
	key, err := svc.decryptToolKey(row.APIKeyEnc)
	if err != nil || key != "old-key" {
		t.Fatalf("parked DB key changed: %q err=%v", key, err)
	}
}

func TestOpencodeTotalRemoveDeletesDBRow(t *testing.T) {
	svc, db, home := newToolConfigTestService(t)
	path := writeToolConfigFixture(t, home, `{"provider":{"remove":{"name":"Remove","options":{"baseURL":"https://remove.example"},"models":{}}}}`)
	preset := totalPlanPreset("remove", "Remove", "https://remove.example")
	created, err := svc.CreateToolPreset(preset, "remove-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyOpencodeConfigChange(OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{Action: "remove", Preset: *created}}}, true); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetToolPreset(created.ID)
	if err != nil || row != nil {
		t.Fatalf("removed DB row = %+v err=%v", row, err)
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "remove") {
		t.Fatalf("removed provider remains: %s", content)
	}
}

func TestOpencodeTotalPreviewCombinesGlobalsAndHasNoSideEffects(t *testing.T) {
	svc, db, home := newToolConfigTestService(t)
	path := writeToolConfigFixture(t, home, `{"provider":{},"other":true}`)
	preset := totalPlanPreset("parked", "Parked", "https://parked.example")
	created, err := svc.CreateToolPreset(preset, "db-key")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	plan := OpencodeConfigPlan{
		Providers: []OpencodeProviderPlan{{Action: "upsert", Preset: *created}},
		Globals:   toolconfig.OpencodeGlobalSettings{Theme: "system"},
	}
	preview, err := svc.PreviewOpencodeConfigChange(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.After, "parked") || !strings.Contains(preview.After, "system") {
		t.Fatalf("combined preview = %s", preview.After)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("preview changed config: before=%q after=%q", before, after)
	}
	row, err := db.GetToolPreset(created.ID)
	if err != nil || row == nil || row.APIKeyEnc != created.APIKeyEnc {
		t.Fatalf("preview changed DB row: row=%+v err=%v", row, err)
	}
}

func TestOpencodeTotalRejectsDuplicateAndDrift(t *testing.T) {
	svc, db, home := newToolConfigTestService(t)
	path := writeToolConfigFixture(t, home, `{"provider":{}}`)
	preset := totalPlanPreset("duplicate", "Duplicate", "https://duplicate.example")
	duplicate := OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{Action: "upsert", Preset: preset}, {Action: "park", Preset: preset}}}
	if _, err := svc.PreviewOpencodeConfigChange(duplicate); !errors.Is(err, toolconfig.ErrConflict) {
		t.Fatalf("duplicate plan error = %v", err)
	}
	if err := svc.ApplyOpencodeConfigChange(OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{Action: "upsert", Preset: preset, PlaintextKey: "key"}}}, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"provider":{},"external":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyOpencodeConfigChange(OpencodeConfigPlan{Providers: []OpencodeProviderPlan{{Action: "park", Preset: preset}}}, false); !errors.Is(err, toolconfig.ErrDrifted) {
		t.Fatalf("drift error = %v", err)
	}
	rows, err := db.ListToolPresets("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("drift created DB rows: %+v", rows)
	}
}

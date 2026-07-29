package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoapi/internal/model"
	"autoapi/internal/toolconfig"
)

func writeWorkbenchFile(t *testing.T, path, content string, mode os.FileMode) {
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

func workbenchPreset(tool toolconfig.Tool, providerID, name, baseURL string, kind toolconfig.PresetKind) toolconfig.Preset {
	return toolconfig.Preset{
		Tool:       tool,
		Kind:       kind,
		Name:       name,
		ProviderID: providerID,
		BaseURL:    baseURL,
		Models:     []toolconfig.PresetModel{{Name: "model", Default: true}},
	}
}

func TestToolWorkbenchKeyResolutionPrecedence(t *testing.T) {
	svc, db, homeDir := newToolConfigTestService(t)
	path := toolconfig.DefaultConfigPath(toolconfig.ToolClaude, homeDir)
	writeWorkbenchFile(t, path, `{"env":{"ANTHROPIC_BASE_URL":"https://file.example","ANTHROPIC_AUTH_TOKEN":"file-key"}}`, 0o644)
	dbPreset := workbenchPreset(toolconfig.ToolClaude, "anthropic", "Anthropic", "https://db.example", toolconfig.PresetDirect)
	created, err := svc.CreateToolPreset(dbPreset, "db-key")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetToolPreset(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := toolconfig.RawManagedSection{APIKey: "file-key"}
	if got, err := svc.resolveToolKey("explicit-key", raw, row); err != nil || got != "explicit-key" {
		t.Fatalf("explicit key = %q, err=%v", got, err)
	}
	if got, err := svc.resolveToolKey("", raw, row); err != nil || got != "file-key" {
		t.Fatalf("file key = %q, err=%v", got, err)
	}
	if got, err := svc.resolveToolKey("", toolconfig.RawManagedSection{}, row); err != nil || got != "db-key" {
		t.Fatalf("database key = %q, err=%v", got, err)
	}
}

func TestToolWorkbenchAutoapiPresetsStageForCodexAndClaude(t *testing.T) {
	svc, db, homeDir := newToolConfigTestService(t)
	key, err := db.CreateAPIKey(model.ApiKeyInput{Name: "relay"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		tool       toolconfig.Tool
		providerID string
		wantURL    string
	}{
		{name: "codex", tool: toolconfig.ToolCodex, providerID: "relay-codex", wantURL: "http://127.0.0.1:8344/v1"},
		{name: "claude", tool: toolconfig.ToolClaude, providerID: "anthropic", wantURL: "http://127.0.0.1:8344"},
	} {
		t.Run(test.name, func(t *testing.T) {
			preset := workbenchPreset(test.tool, test.providerID, "Relay", "", toolconfig.PresetAutoapi)
			preset.APIKeyID = key.ID
			previews, err := svc.PreviewToolConfigChange(string(test.tool), ToolConfigPlan{Providers: []ToolProviderPlan{{Action: "upsert", Preset: preset}}})
			if err != nil {
				t.Fatal(err)
			}
			var previewText string
			for _, preview := range previews {
				previewText += preview.After
			}
			if len(previews) == 0 || !strings.Contains(previewText, test.wantURL) || !strings.Contains(previewText, key.ID) {
				t.Fatalf("autoapi %s preview = %+v", test.name, previews)
			}
		})
	}
	_ = homeDir
}

func TestPresetPlaintextAutoapiWithoutRunningProxy(t *testing.T) {
	t.Run("LAN disabled uses settings port", func(t *testing.T) {
		svc, db, _ := newToolConfigTestService(t)
		svc.proxy = nil
		settings, err := db.GetSettings()
		if err != nil {
			t.Fatal(err)
		}
		settings.Server.Port = 19090
		settings.Server.LANEnabled = false
		settings.Server.LANAddress = ""
		if err := db.SaveSettings(*settings); err != nil {
			t.Fatal(err)
		}
		key, err := db.CreateAPIKey(model.ApiKeyInput{Name: "relay"})
		if err != nil {
			t.Fatal(err)
		}
		preset := workbenchPreset(toolconfig.ToolCodex, "relay-codex", "Relay", "", toolconfig.PresetAutoapi)
		preset.APIKeyID = key.ID
		plain, err := svc.presetPlaintext(preset)
		if err != nil {
			t.Fatal(err)
		}
		if plain.BaseURL != "http://127.0.0.1:19090/v1" {
			t.Fatalf("relay base URL = %q", plain.BaseURL)
		}
	})

	t.Run("LAN enabled uses selected address", func(t *testing.T) {
		svc, db, _ := newToolConfigTestService(t)
		svc.proxy = nil
		addresses, err := localIPv4Addrs()
		if err != nil || len(addresses) == 0 {
			t.Skip("no usable local IPv4 address")
		}
		settings, err := db.GetSettings()
		if err != nil {
			t.Fatal(err)
		}
		settings.Server.Port = 19091
		settings.Server.LANEnabled = true
		settings.Server.LANAddress = addresses[0]
		if err := db.SaveSettings(*settings); err != nil {
			t.Fatal(err)
		}
		key, err := db.CreateAPIKey(model.ApiKeyInput{Name: "relay"})
		if err != nil {
			t.Fatal(err)
		}
		preset := workbenchPreset(toolconfig.ToolClaude, "anthropic", "Relay", "", toolconfig.PresetAutoapi)
		preset.APIKeyID = key.ID
		plain, err := svc.presetPlaintext(preset)
		if err != nil {
			t.Fatal(err)
		}
		want := "http://" + addresses[0] + ":19091"
		if plain.BaseURL != want {
			t.Fatalf("relay base URL = %q, want %q", plain.BaseURL, want)
		}
	})
}

func TestPresetPlaintextAutoapiMissingAPIKeyIDWithoutRunningProxy(t *testing.T) {
	svc, _, _ := newToolConfigTestService(t)
	svc.proxy = nil
	preset := workbenchPreset(toolconfig.ToolClaude, "anthropic", "Relay", "", toolconfig.PresetAutoapi)
	if _, err := svc.presetPlaintext(preset); err == nil || err.Error() != "service: 请重新选择访问密钥" {
		t.Fatalf("missing API key error = %v", err)
	}
}

func TestToolWorkbenchPreviewFileCounts(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	codexPreset := workbenchPreset(toolconfig.ToolCodex, "codex-provider", "Codex", "https://codex.example", toolconfig.PresetDirect)
	previews, err := svc.PreviewToolConfigChange("codex", ToolConfigPlan{Providers: []ToolProviderPlan{{Action: "upsert", Preset: codexPreset, PlaintextKey: "codex-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 2 {
		t.Fatalf("Codex preview count = %d, want 2", len(previews))
	}
	claudePreset := workbenchPreset(toolconfig.ToolClaude, "anthropic", "Anthropic", "https://claude.example", toolconfig.PresetDirect)
	previews, err = svc.PreviewToolConfigChange("claude", ToolConfigPlan{Providers: []ToolProviderPlan{{Action: "upsert", Preset: claudePreset, PlaintextKey: "claude-key"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 1 {
		t.Fatalf("Claude preview count = %d, want 1", len(previews))
	}
	if _, err := os.Stat(filepath.Join(homeDir, ".codex", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("preview created Codex auth file: %v", err)
	}
}

func TestToolWorkbenchApplySyncsParkAndRemove(t *testing.T) {
	svc, db, homeDir := newToolConfigTestService(t)
	configPath := toolconfig.DefaultConfigPath(toolconfig.ToolCodex, homeDir)
	authPath := filepath.Join(homeDir, ".codex", "auth.json")
	writeWorkbenchFile(t, configPath, `model_provider = "park"

[model_providers.park]
name = "Park"
base_url = "https://park.example"

[model_providers.remove]
name = "Remove"
base_url = "https://remove.example"
`, 0o644)
	writeWorkbenchFile(t, authPath, `{"OPENAI_API_KEY":"file-key"}`, 0o644)
	park := workbenchPreset(toolconfig.ToolCodex, "park", "Park", "https://park.example", toolconfig.PresetDirect)
	remove := workbenchPreset(toolconfig.ToolCodex, "remove", "Remove", "https://remove.example", toolconfig.PresetDirect)
	parkRow, err := svc.CreateToolPreset(park, "old-db-key")
	if err != nil {
		t.Fatal(err)
	}
	removeRow, err := svc.CreateToolPreset(remove, "remove-db-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyToolConfigChange("codex", ToolConfigPlan{Providers: []ToolProviderPlan{
		{Action: "park", Preset: *parkRow},
		{Action: "remove", Preset: *removeRow},
	}}, true); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListToolPresets("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != parkRow.ID {
		t.Fatalf("park/remove DB rows = %+v", rows)
	}
	key, err := svc.decryptToolKey(rows[0].APIKeyEnc)
	if err != nil || key != "file-key" {
		t.Fatalf("parked key = %q, err=%v", key, err)
	}
	if content, err := os.ReadFile(configPath); err != nil || strings.Contains(string(content), "model_providers") || strings.Contains(string(content), "model_provider") {
		t.Fatalf("park/remove config = %s, err=%v", content, err)
	}
	if content, err := os.ReadFile(authPath); err != nil || strings.Contains(string(content), "OPENAI_API_KEY") {
		t.Fatalf("park/remove auth = %s, err=%v", content, err)
	}
	beforeParkedUpdate, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	parkUpdated := *parkRow
	parkUpdated.Name = "Parked Edited"
	parkUpdated.BaseURL = "https://parked-edited.example"
	if err := svc.ApplyToolConfigChange("codex", ToolConfigPlan{Providers: []ToolProviderPlan{{Action: "park", Preset: parkUpdated}}}, true); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(configPath); err != nil || string(after) != string(beforeParkedUpdate) {
		t.Fatalf("already parked config changed: %s, err=%v", after, err)
	}
	row, err := db.GetToolPreset(parkRow.ID)
	if err != nil || row == nil || row.Name != "Parked Edited" {
		t.Fatalf("already parked row was not updated: row=%+v err=%v", row, err)
	}
	if err := svc.ApplyToolConfigChange("codex", ToolConfigPlan{Providers: []ToolProviderPlan{{Action: "remove", Preset: *row}}}, true); err != nil {
		t.Fatal(err)
	}
	if row, err := db.GetToolPreset(parkRow.ID); err != nil || row != nil {
		t.Fatalf("already parked removal row = %+v err=%v", row, err)
	}
}

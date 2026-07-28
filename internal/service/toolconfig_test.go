package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoapi/internal/store"
	"autoapi/internal/toolconfig"
)

type toolConfigTestProxy struct {
	url string
}

func (p toolConfigTestProxy) IsRunning() bool        { return true }
func (p toolConfigTestProxy) URL() string            { return p.url }
func (p toolConfigTestProxy) ActiveConnections() int { return 0 }

func newToolConfigTestService(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	dbDir := t.TempDir()
	db, err := store.New(context.Background(), store.StoreDeps{DSN: filepath.Join(dbDir, "autoapi.db")})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc := New(db, toolConfigTestProxy{url: "http://0.0.0.0:8344"}, filepath.Join(homeDir, ".autoapi"))
	return svc, db, homeDir
}

func writeToolConfigFixture(t *testing.T, homeDir, content string) string {
	t.Helper()
	path := toolconfig.DefaultConfigPath(toolconfig.ToolOpencode, homeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if content == "" {
		content = `{"provider":{"existing":{"name":"Existing","options":{"baseURL":"https://existing.example"}}},"mcp":{"keep":true}}
`
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func directTestPreset() toolconfig.Preset {
	return toolconfig.Preset{
		Tool:       toolconfig.ToolOpencode,
		Kind:       toolconfig.PresetDirect,
		Name:       "Direct Test",
		ProviderID: "direct-test",
		Vendor:     toolconfig.VendorOpenAICompatible,
		BaseURL:    "https://direct.example/v1",
		Models: []toolconfig.PresetModel{{
			Name:    "direct-model",
			Default: true,
		}},
	}
}

func boolPtrTest(value bool) *bool {
	return &value
}

func TestGetOpencodeLiveStateReadsDisk(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	writeToolConfigFixture(t, homeDir, `{"provider":{"acme-ai":{}},"model":"acme-ai/acme-model"}`)

	state, err := svc.GetOpencodeLiveState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Model != "acme-ai/acme-model" || state.OmoSlimConfigured {
		t.Fatalf("state without OMO Slim: %+v", state)
	}

	omoSlimPath := filepath.Join(homeDir, ".config", "opencode", "oh-my-opencode-slim.jsonc")
	if err := os.MkdirAll(filepath.Dir(omoSlimPath), 0o755); err != nil {
		t.Fatal(err)
	}
	omoSlim := `{"preset":"balanced","presets":{"balanced":{"orchestrator":{"model":"acme-ai/acme-model","variant":"high"}}},"disabled_agents":["oracle"]}`
	if err := os.WriteFile(omoSlimPath, []byte(omoSlim), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err = svc.GetOpencodeLiveState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.OmoSlimConfigured || state.OmoSlimActivePreset != "balanced" || state.OmoSlimAgentCount != 1 || state.OmoSlimDisabledCount != 1 {
		t.Fatalf("state with OMO Slim: %+v", state)
	}
}

func TestListToolProvidersMirrorsDatabaseAndFile(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	writeToolConfigFixture(t, homeDir, `{"provider":{
		"acme-ai": {"options": {"baseURL": "https://acme.example/v1", "apiKey": "sk-acme"}, "models": {"m1": {}, "m2": {}}},
		"zeta": {"options": {"baseURL": "https://zeta.example"}},
		"file-only": {"name": "File Only", "options": {"baseURL": "https://file.example", "apiKey": "sk-file"}}
	}}`)

	existing := directTestPreset()
	existing.ProviderID = "acme-ai"
	existing.Name = "Acme"
	if _, err := svc.CreateToolPreset(existing, ""); err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	parked := directTestPreset()
	parked.ProviderID = "parked"
	parked.Name = "Parked"
	if _, err := svc.CreateToolPreset(parked, ""); err != nil {
		t.Fatalf("CreateToolPreset parked: %v", err)
	}

	views, err := svc.ListToolProviders("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 4 {
		t.Fatalf("views = %#v", views)
	}
	if views[0].Preset.ProviderID != "acme-ai" || !views[0].Enabled || !views[0].InDB {
		t.Fatalf("enabled DB view: %+v", views[0])
	}
	if views[1].Preset.ProviderID != "file-only" || !views[1].Enabled || views[1].InDB || views[1].Preset.APIKeyEnc != "sk-file" {
		t.Fatalf("file-only view did not retain service-local key: %+v", views[1])
	}
	if views[2].Preset.ProviderID != "zeta" || !views[2].Enabled || views[2].InDB {
		t.Fatalf("second file-only view: %+v", views[2])
	}
	if views[3].Preset.ProviderID != "parked" || views[3].Enabled || !views[3].InDB {
		t.Fatalf("parked view: %+v", views[3])
	}
}

func TestRevealToolProviderKeyUsesFileThenDatabase(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	writeToolConfigFixture(t, homeDir, `{"provider":{"live":{"options":{"apiKey":"file-secret"}}}}`)
	key, err := svc.RevealToolProviderKey("opencode", "live")
	if err != nil || key != "file-secret" {
		t.Fatalf("file key = %q, err=%v", key, err)
	}

	parked := directTestPreset()
	parked.ProviderID = "parked-key"
	parked.Name = "Parked Key"
	if _, err := svc.CreateToolPreset(parked, "db-secret"); err != nil {
		t.Fatal(err)
	}
	key, err = svc.RevealToolProviderKey("opencode", "parked-key")
	if err != nil || key != "db-secret" {
		t.Fatalf("database key = %q, err=%v", key, err)
	}

	autoapi := directTestPreset()
	autoapi.Kind = toolconfig.PresetAutoapi
	autoapi.ProviderID = "relay"
	autoapi.Name = "Relay"
	if _, err := svc.CreateToolPreset(autoapi, "relay-secret"); err != nil {
		t.Fatal(err)
	}
	key, err = svc.RevealToolProviderKey("opencode", "relay")
	if err != nil || key != "" {
		t.Fatalf("autoapi key = %q, err=%v", key, err)
	}
}

func TestOpencodeGlobalSettingsPreviewAndApply(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	path := writeToolConfigFixture(t, homeDir, `{
  // keep this comment
  "model": "old/model",
  "other": true
}`)
	settings := toolconfig.OpencodeGlobalSettings{Model: "new/model", Theme: "system", Autoupdate: boolPtrTest(true)}
	preview, err := svc.PreviewOpencodeGlobalChange(settings)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Path != resolvedPath || !strings.Contains(preview.Before, "keep this comment") || !strings.Contains(preview.After, "new/model") {
		t.Fatalf("preview = %+v", preview)
	}
	if err := svc.ApplyOpencodeGlobalChange(settings, true); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "new/model") || !strings.Contains(string(content), "keep this comment") || !strings.Contains(string(content), `"other": true`) {
		t.Fatalf("applied global settings lost content: %s", content)
	}
}

func TestEnableToolPresetDirectPersistsFileAndState(t *testing.T) {
	svc, db, homeDir := newToolConfigTestService(t)
	configPath := writeToolConfigFixture(t, homeDir, "")

	preset, err := svc.CreateToolPreset(directTestPreset(), "sk-direct-secret")
	if err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	result, err := svc.EnableToolPreset(preset.ID)
	if err != nil {
		t.Fatalf("EnableToolPreset: %v", err)
	}
	if result.Tool != string(toolconfig.ToolOpencode) || result.ConfigPath != configPath || len(result.BackupPaths) != 1 {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read applied config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `"https://direct.example/v1"`) || !strings.Contains(content, `"sk-direct-secret"`) {
		t.Fatalf("applied config missing managed values: %s", content)
	}
	if !strings.Contains(content, `"mcp":{"keep":true}`) {
		t.Fatalf("unmanaged config was not preserved: %s", content)
	}

	state, err := db.GetToolState(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolState: %v", err)
	}
	if state.ActivePresetID != 0 || state.ConfigPath != configPath || state.AppliedAt == 0 {
		t.Fatalf("unexpected tool state: %+v", state)
	}
	resolvedConfigPath, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		t.Fatalf("resolve applied config path: %v", err)
	}
	fileStates, err := db.GetToolFileStates(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolFileStates: %v", err)
	}
	if len(fileStates) != 1 || fileStates[0].Resource != toolconfig.ResOpencodeConfig || fileStates[0].Path != resolvedConfigPath || fileStates[0].AppliedFileHash == "" {
		t.Fatalf("unexpected file states: %+v", fileStates)
	}
}

func TestDisableToolPresetUpsertsAndEncryptsManagedKey(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	configPath := writeToolConfigFixture(t, homeDir, `{"provider":{"imported":{"npm":"@ai-sdk/openai-compatible","name":"Imported","options":{"baseURL":"https://import.example/v1","apiKey":"import-secret"},"models":{"import-model":{"name":"import-model"}}}},"model":"imported/import-model"}`)

	result, err := svc.DisableToolPreset("opencode", "imported")
	if err != nil {
		t.Fatalf("DisableToolPreset: %v", err)
	}
	if result.Tool != "opencode" {
		t.Fatalf("unexpected result: %+v", result)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "imported") {
		t.Fatalf("provider remained in config: %s", data)
	}
	presets, err := svc.GetToolPresets("opencode")
	if err != nil || len(presets) != 1 {
		t.Fatalf("presets = %#v, err=%v", presets, err)
	}
	preset := presets[0]
	if preset.APIKeyEnc == "" {
		t.Fatal("expected encrypted API key")
	}
	plaintext, err := svc.decryptToolKey(preset.APIKeyEnc)
	if err != nil {
		t.Fatalf("decrypt imported key: %v", err)
	}
	if plaintext != "import-secret" {
		t.Fatalf("decrypted imported key = %q", plaintext)
	}
}

func TestUpdateEnabledToolPresetSynthesizedPreservesOnFileKey(t *testing.T) {
	svc, db, homeDir := newToolConfigTestService(t)
	configPath := writeToolConfigFixture(t, homeDir, `{"provider":{"file-only":{"name":"File Only","options":{"baseURL":"https://file.example","apiKey":"on-file-secret"}}}}`)
	views, err := svc.ListToolProviders("opencode")
	if err != nil || len(views) != 1 || views[0].InDB {
		t.Fatalf("initial mirror = %#v, err=%v", views, err)
	}
	updated := views[0].Preset
	updated.Name = "Edited File Provider"
	updated.BaseURL = "https://edited.example"
	stored, err := svc.UpdateEnabledToolPreset(updated, "")
	if err != nil {
		t.Fatalf("UpdateEnabledToolPreset: %v", err)
	}
	if stored == nil || stored.ID == 0 {
		t.Fatalf("synthesized row was not created: %+v", stored)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "on-file-secret") || !strings.Contains(string(content), "edited.example") {
		t.Fatalf("write-through lost existing key or update: %s", content)
	}
	persisted, err := db.GetToolPreset(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := svc.decryptToolKey(persisted.APIKeyEnc)
	if err != nil || key != "on-file-secret" {
		t.Fatalf("persisted key = %q, err=%v", key, err)
	}
}

func TestDeleteToolPresetRejectsEnabledProvider(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	writeToolConfigFixture(t, homeDir, `{"provider":{"direct-test":{"options":{"baseURL":"https://live.example"}}}}`)
	preset, err := svc.CreateToolPreset(directTestPreset(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteToolPreset(preset.ID); !errors.Is(err, toolconfig.ErrConflict) || !strings.Contains(err.Error(), "请先在列表中禁用") {
		t.Fatalf("enabled delete error = %v", err)
	}
	if _, err := svc.DisableToolPreset("opencode", "direct-test"); err != nil {
		t.Fatalf("DisableToolPreset: %v", err)
	}
	if err := svc.DeleteToolPreset(preset.ID); err != nil {
		t.Fatalf("delete parked preset: %v", err)
	}
}

func TestRestoreToolBackupRestoresPreviousContent(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	configPath := writeToolConfigFixture(t, homeDir, "")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read original fixture: %v", err)
	}
	preset, err := svc.CreateToolPreset(directTestPreset(), "sk-restore-secret")
	if err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	result, err := svc.EnableToolPreset(preset.ID)
	if err != nil {
		t.Fatalf("EnableToolPreset: %v", err)
	}
	if len(result.BackupPaths) != 1 {
		t.Fatalf("expected one backup, got %+v", result.BackupPaths)
	}
	if err := os.WriteFile(configPath, []byte(`{"provider":{},"changed":true}
`), 0o644); err != nil {
		t.Fatalf("write changed config: %v", err)
	}
	if err := svc.RestoreToolBackup(string(toolconfig.ToolOpencode), string(toolconfig.ResOpencodeConfig), result.BackupPaths[0]); err != nil {
		t.Fatalf("RestoreToolBackup: %v", err)
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored content mismatch: got=%q want=%q", restored, original)
	}
}

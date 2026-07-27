package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoapi/internal/model"
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
		Vendor:     "@ai-sdk/openai-compatible",
		BaseURL:    "https://direct.example/v1",
		Models: []toolconfig.PresetModel{{
			Name:    "direct-model",
			Default: true,
		}},
	}
}

func TestGetOpencodeLiveStateReadsDisk(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	writeToolConfigFixture(t, homeDir, `{"provider":{"acme-ai":{}},"model":"acme-ai/acme-model"}`)

	state, err := svc.GetOpencodeLiveState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Model != "acme-ai/acme-model" || state.OmoConfigured {
		t.Fatalf("state without OMO: %+v", state)
	}

	omoPath := filepath.Join(homeDir, ".config", "opencode", "oh-my-opencode-slim.jsonc")
	if err := os.MkdirAll(filepath.Dir(omoPath), 0o755); err != nil {
		t.Fatal(err)
	}
	omo := `{"preset":"balanced","presets":{"balanced":{"orchestrator":{"model":"acme-ai/acme-model","variant":"high"}}},"disabled_agents":["oracle"]}`
	if err := os.WriteFile(omoPath, []byte(omo), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err = svc.GetOpencodeLiveState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.OmoConfigured || state.OmoActivePreset != "balanced" || state.OmoAgentCount != 1 || state.OmoDisabledCount != 1 {
		t.Fatalf("state with OMO: %+v", state)
	}
}

func TestApplyToolPresetDirectPersistsFileAndState(t *testing.T) {
	svc, db, homeDir := newToolConfigTestService(t)
	configPath := writeToolConfigFixture(t, homeDir, "")

	preset, err := svc.CreateToolPreset(directTestPreset(), "sk-direct-secret")
	if err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	result, err := svc.ApplyToolPreset(preset.ID, false)
	if err != nil {
		t.Fatalf("ApplyToolPreset: %v", err)
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
	if state.ActivePresetID != preset.ID || state.ConfigPath != configPath || state.AppliedAt == 0 {
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

func TestApplyToolPresetDriftRequiresExplicitOverride(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	configPath := writeToolConfigFixture(t, homeDir, "")
	preset, err := svc.CreateToolPreset(directTestPreset(), "sk-drift-secret")
	if err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	if _, err := svc.ApplyToolPreset(preset.ID, false); err != nil {
		t.Fatalf("initial ApplyToolPreset: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(`{"provider":{},"external":true}
`), 0o644); err != nil {
		t.Fatalf("write drift: %v", err)
	}
	if _, err := svc.ApplyToolPreset(preset.ID, false); !errors.Is(err, toolconfig.ErrDrifted) {
		t.Fatalf("expected ErrDrifted, got %v", err)
	}
	if _, err := svc.ApplyToolPreset(preset.ID, true); err != nil {
		t.Fatalf("override ApplyToolPreset: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read overridden config: %v", err)
	}
	if !strings.Contains(string(data), `"https://direct.example/v1"`) {
		t.Fatalf("override did not apply preset: %s", data)
	}
}

func TestApplyToolPresetAutoapiValidatesKeyAndUsesRelayAddress(t *testing.T) {
	svc, db, homeDir := newToolConfigTestService(t)
	configPath := writeToolConfigFixture(t, homeDir, "")
	autoapi := toolconfig.Preset{
		Tool:     toolconfig.ToolOpencode,
		Kind:     toolconfig.PresetAutoapi,
		Name:     "Autoapi Test",
		APIKeyID: "missing-key",
		Models:   []toolconfig.PresetModel{{Name: "relay-model", Default: true}},
	}
	created, err := svc.CreateToolPreset(autoapi, "")
	if err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	if _, err := svc.ApplyToolPreset(created.ID, false); err == nil || !strings.Contains(err.Error(), "请重新选择访问密钥") {
		t.Fatalf("expected readable missing-key error, got %v", err)
	}

	key, err := db.CreateAPIKey(modelAPIKeyInput())
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	created.APIKeyID = key.ID
	if _, err := svc.UpdateToolPreset(*created, ""); err != nil {
		t.Fatalf("UpdateToolPreset: %v", err)
	}
	plain, err := svc.presetPlaintext(*created)
	if err != nil {
		t.Fatalf("presetPlaintext: %v", err)
	}
	if !strings.HasSuffix(plain.BaseURL, ":8344/v1") {
		t.Fatalf("unexpected relay BaseURL: %q", plain.BaseURL)
	}
	if _, err := svc.ApplyToolPreset(created.ID, false); err != nil {
		t.Fatalf("ApplyToolPreset autoapi: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read autoapi config: %v", err)
	}
	if !strings.Contains(string(data), plain.BaseURL) {
		t.Fatalf("autoapi BaseURL missing from config: %s", data)
	}
}

func TestImportToolPresetEncryptsManagedKey(t *testing.T) {
	svc, _, homeDir := newToolConfigTestService(t)
	writeToolConfigFixture(t, homeDir, `{"provider":{"imported":{"npm":"@ai-sdk/openai-compatible","name":"Imported","options":{"baseURL":"https://import.example/v1","apiKey":"import-secret"},"models":{"import-model":{"name":"import-model"}}}},"model":"imported/import-model"}
`)

	preset, err := svc.ImportToolPreset(string(toolconfig.ToolOpencode), "imported", "Imported Preset")
	if err != nil {
		t.Fatalf("ImportToolPreset: %v", err)
	}
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
	result, err := svc.ApplyToolPreset(preset.ID, false)
	if err != nil {
		t.Fatalf("ApplyToolPreset: %v", err)
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

func modelAPIKeyInput() model.ApiKeyInput {
	return model.ApiKeyInput{Name: "tool-access-test-key"}
}

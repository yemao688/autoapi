package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"autoapi/internal/toolconfig"
)

func TestToolPresetCRUDRoundTrip(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name   string
		preset toolconfig.Preset
	}{
		{
			name: "codex direct",
			preset: toolconfig.Preset{
				Tool:       toolconfig.ToolCodex,
				Kind:       toolconfig.PresetDirect,
				Name:       "zeta",
				ProviderID: "provider-z",
				Vendor:     "responses",
				BaseURL:    "https://codex.example.test/v1",
				APIKeyEnc:  "enc-z",
				APIKeyID:   "key-z",
				Models:     []toolconfig.PresetModel{{Name: "model-z", Reasoning: true, Variants: map[string]toolconfig.PresetVariant{"high": {ReasoningEffort: "high"}}}},
				Extra:      map[string]string{"wire_api": "responses"},
			},
		},
		{
			name: "opencode autoapi",
			preset: toolconfig.Preset{
				Tool:       toolconfig.ToolOpencode,
				Kind:       toolconfig.PresetAutoapi,
				Name:       "alpha",
				ProviderID: "autoapi",
				Vendor:     toolconfig.VendorOpenAICompatible,
				BaseURL:    "http://127.0.0.1:8344/v1",
				APIKeyEnc:  "enc-a",
				APIKeyID:   "key-a",
				Models: []toolconfig.PresetModel{{
					Name:       "model-a",
					Limit:      &toolconfig.ModelLimit{Context: 128000, Output: 4096},
					Modalities: []string{"text", "image"},
					Variants:   map[string]toolconfig.PresetVariant{"balanced": {ReasoningEffort: "medium"}},
					Default:    true,
				}},
				Extra: map[string]string{"source": "relay", "region": "local"},
			},
		},
		{
			name: "opencode direct",
			preset: toolconfig.Preset{
				Tool:   toolconfig.ToolOpencode,
				Kind:   toolconfig.PresetDirect,
				Name:   "beta",
				Models: []toolconfig.PresetModel{{Name: "model-b"}},
				Extra:  map[string]string{},
			},
		},
	}

	created := make([]toolconfig.Preset, 0, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.preset
			if err := s.CreateToolPreset(&p); err != nil {
				t.Fatalf("CreateToolPreset: %v", err)
			}
			if p.ID == 0 || p.CreatedAt == 0 || p.UpdatedAt == 0 {
				t.Fatalf("expected generated identity and timestamps: %+v", p)
			}

			got, err := s.GetToolPreset(p.ID)
			if err != nil {
				t.Fatalf("GetToolPreset: %v", err)
			}
			if !reflect.DeepEqual(got, &p) {
				t.Fatalf("round trip mismatch: got=%+v want=%+v", got, p)
			}
			created = append(created, p)
		})
	}

	all, err := s.ListToolPresets("")
	if err != nil {
		t.Fatalf("ListToolPresets all: %v", err)
	}
	if len(all) != len(cases) {
		t.Fatalf("expected %d presets, got %d", len(cases), len(all))
	}
	if all[0].Tool != toolconfig.ToolCodex || all[0].Name != "zeta" || all[1].Name != "alpha" || all[2].Name != "beta" {
		t.Fatalf("unexpected tool/name ordering: %+v", all)
	}

	opencode, err := s.ListToolPresets(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("ListToolPresets filtered: %v", err)
	}
	if len(opencode) != 2 || opencode[0].Name != "alpha" || opencode[1].Name != "beta" {
		t.Fatalf("unexpected filtered presets: %+v", opencode)
	}

	updated := created[1]
	oldUpdatedAt := updated.UpdatedAt
	updated.Name = "alpha-updated"
	updated.BaseURL = "http://127.0.0.1:9444/v1"
	updated.Models = []toolconfig.PresetModel{{Name: "updated-model", Limit: &toolconfig.ModelLimit{Context: 32000, Output: 2048}}}
	updated.Extra = map[string]string{"source": "updated"}
	if err := s.UpdateToolPreset(&updated); err != nil {
		t.Fatalf("UpdateToolPreset: %v", err)
	}
	if updated.UpdatedAt <= oldUpdatedAt {
		t.Fatalf("expected UpdatedAt to bump: old=%d new=%d", oldUpdatedAt, updated.UpdatedAt)
	}
	got, err := s.GetToolPreset(updated.ID)
	if err != nil {
		t.Fatalf("GetToolPreset after update: %v", err)
	}
	if !reflect.DeepEqual(got, &updated) {
		t.Fatalf("updated round trip mismatch: got=%+v want=%+v", got, updated)
	}
}

func TestToolPresetUniqueConflictAndDelete(t *testing.T) {
	s := newTestStore(t)
	p := &toolconfig.Preset{Tool: toolconfig.ToolClaude, Kind: toolconfig.PresetDirect, Name: "shared"}
	if err := s.CreateToolPreset(p); err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	other := &toolconfig.Preset{Tool: toolconfig.ToolClaude, Kind: toolconfig.PresetDirect, Name: "other"}
	if err := s.CreateToolPreset(other); err != nil {
		t.Fatalf("CreateToolPreset other: %v", err)
	}
	conflict := &toolconfig.Preset{Tool: toolconfig.ToolClaude, Kind: toolconfig.PresetAutoapi, Name: "shared"}
	if err := s.CreateToolPreset(conflict); !errors.Is(err, toolconfig.ErrConflict) {
		t.Fatalf("expected toolconfig.ErrConflict, got %v", err)
	}
	other.Name = "shared"
	if err := s.UpdateToolPreset(other); !errors.Is(err, toolconfig.ErrConflict) {
		t.Fatalf("expected toolconfig.ErrConflict from update, got %v", err)
	}

	if err := s.DeleteToolPreset(p.ID); err != nil {
		t.Fatalf("DeleteToolPreset: %v", err)
	}
	got, err := s.GetToolPreset(p.ID)
	if err != nil {
		t.Fatalf("GetToolPreset after delete: %v", err)
	}
	if got != nil {
		t.Fatalf("expected deleted preset to be absent, got %+v", got)
	}
	if err := s.DeleteToolPreset(p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting absent preset, got %v", err)
	}
}

func TestToolStateAbsentAndUpsert(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetToolState(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolState absent: %v", err)
	}
	if !reflect.DeepEqual(got, &toolconfig.ToolState{}) {
		t.Fatalf("expected zero-valued absent state, got %+v", got)
	}

	state := &toolconfig.ToolState{
		Tool:           toolconfig.ToolOpencode,
		ActivePresetID: 42,
		ConfigPath:     "/tmp/opencode.json",
		AppliedAt:      100,
	}
	if err := s.SaveToolState(state); err != nil {
		t.Fatalf("SaveToolState: %v", err)
	}
	got, err = s.GetToolState(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolState after save: %v", err)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("saved state mismatch: got=%+v want=%+v", got, state)
	}

	updated := &toolconfig.ToolState{
		Tool:           toolconfig.ToolOpencode,
		ActivePresetID: 77,
		ConfigPath:     "/tmp/updated-opencode.json",
		AppliedAt:      200,
	}
	if err := s.SaveToolState(updated); err != nil {
		t.Fatalf("SaveToolState upsert: %v", err)
	}
	got, err = s.GetToolState(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolState after upsert: %v", err)
	}
	if !reflect.DeepEqual(got, updated) {
		t.Fatalf("upserted state mismatch: got=%+v want=%+v", got, updated)
	}
}

func TestDeleteToolPresetClearsActiveState(t *testing.T) {
	s := newTestStore(t)
	p := &toolconfig.Preset{Tool: toolconfig.ToolClaude, Kind: toolconfig.PresetDirect, Name: "active"}
	if err := s.CreateToolPreset(p); err != nil {
		t.Fatalf("CreateToolPreset: %v", err)
	}
	if err := s.SaveToolState(&toolconfig.ToolState{Tool: toolconfig.ToolClaude, ActivePresetID: p.ID, ConfigPath: "/tmp/settings.json"}); err != nil {
		t.Fatalf("SaveToolState: %v", err)
	}
	if err := s.DeleteToolPreset(p.ID); err != nil {
		t.Fatalf("DeleteToolPreset: %v", err)
	}
	state, err := s.GetToolState(string(toolconfig.ToolClaude))
	if err != nil {
		t.Fatalf("GetToolState: %v", err)
	}
	if state.ActivePresetID != 0 || state.ConfigPath != "/tmp/settings.json" {
		t.Fatalf("expected active preset cleared while retaining state, got %+v", state)
	}
}

func TestToolFileStateUpsertAndList(t *testing.T) {
	s := newTestStore(t)
	cases := []toolconfig.ToolFileState{
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.ResOpencodeOmoSlim, Path: "/tmp/omo-slim.json", AppliedFileHash: "hash-omo-slim", AppliedAt: 20},
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.ResOpencodeConfig, Path: "/tmp/opencode.json", AppliedFileHash: "hash-config", AppliedAt: 10},
		{Tool: toolconfig.ToolClaude, Resource: toolconfig.ResClaudeSettings, Path: "/tmp/settings.json", AppliedFileHash: "hash-claude", AppliedAt: 30},
	}
	for _, tc := range cases {
		state := tc
		if err := s.SaveToolFileState(&state); err != nil {
			t.Fatalf("SaveToolFileState(%s): %v", tc.Resource, err)
		}
	}

	opencode, err := s.GetToolFileStates(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolFileStates: %v", err)
	}
	if len(opencode) != 2 || opencode[0].Resource != toolconfig.ResOpencodeConfig || opencode[1].Resource != toolconfig.ResOpencodeOmoSlim {
		t.Fatalf("expected resource ordering, got %+v", opencode)
	}

	updated := &toolconfig.ToolFileState{
		Tool:            toolconfig.ToolOpencode,
		Resource:        toolconfig.ResOpencodeConfig,
		Path:            "/tmp/opencode-updated.json",
		AppliedFileHash: "hash-updated",
		AppliedAt:       40,
	}
	if err := s.SaveToolFileState(updated); err != nil {
		t.Fatalf("SaveToolFileState upsert: %v", err)
	}
	opencode, err = s.GetToolFileStates(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolFileStates after upsert: %v", err)
	}
	if len(opencode) != 2 || !reflect.DeepEqual(opencode[0], *updated) {
		t.Fatalf("expected updated config state first, got %+v", opencode)
	}
}

func TestSaveToolApplyState(t *testing.T) {
	s := newTestStore(t)
	state := &toolconfig.ToolState{
		Tool:           toolconfig.ToolOpencode,
		ActivePresetID: 12,
		ConfigPath:     "/tmp/opencode.json",
		AppliedAt:      100,
	}
	files := []toolconfig.ToolFileState{
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.ResOpencodeOmoSlim, Path: "/tmp/omo-slim.json", AppliedFileHash: "omo-slim-1", AppliedAt: 100},
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.Resource("opencode/extra"), Path: "/tmp/extra.json", AppliedFileHash: "extra-1", AppliedAt: 100},
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.ResOpencodeConfig, Path: "/tmp/opencode.json", AppliedFileHash: "config-1", AppliedAt: 100},
	}
	if err := s.SaveToolApplyState(state, files); err != nil {
		t.Fatalf("SaveToolApplyState: %v", err)
	}

	gotState, err := s.GetToolState(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolState: %v", err)
	}
	if !reflect.DeepEqual(gotState, state) {
		t.Fatalf("saved tool state mismatch: got=%+v want=%+v", gotState, state)
	}
	gotFiles, err := s.GetToolFileStates(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolFileStates: %v", err)
	}
	wantFiles := []toolconfig.ToolFileState{files[2], files[1], files[0]}
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Fatalf("saved file states mismatch: got=%+v want=%+v", gotFiles, wantFiles)
	}

	updatedState := &toolconfig.ToolState{
		Tool:           toolconfig.ToolOpencode,
		ActivePresetID: 99,
		ConfigPath:     "/tmp/opencode-updated.json",
		AppliedAt:      200,
	}
	updatedFiles := []toolconfig.ToolFileState{
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.ResOpencodeOmoSlim, Path: "/tmp/omo-slim-updated.json", AppliedFileHash: "omo-slim-2", AppliedAt: 200},
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.Resource("opencode/extra"), Path: "/tmp/extra-updated.json", AppliedFileHash: "extra-2", AppliedAt: 200},
		{Tool: toolconfig.ToolOpencode, Resource: toolconfig.ResOpencodeConfig, Path: "/tmp/opencode-updated.json", AppliedFileHash: "config-2", AppliedAt: 200},
	}
	if err := s.SaveToolApplyState(updatedState, updatedFiles); err != nil {
		t.Fatalf("SaveToolApplyState update: %v", err)
	}

	gotState, err = s.GetToolState(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolState after update: %v", err)
	}
	if !reflect.DeepEqual(gotState, updatedState) {
		t.Fatalf("updated tool state mismatch: got=%+v want=%+v", gotState, updatedState)
	}
	gotFiles, err = s.GetToolFileStates(string(toolconfig.ToolOpencode))
	if err != nil {
		t.Fatalf("GetToolFileStates after update: %v", err)
	}
	wantFiles = []toolconfig.ToolFileState{updatedFiles[2], updatedFiles[1], updatedFiles[0]}
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Fatalf("updated file states mismatch: got=%+v want=%+v", gotFiles, wantFiles)
	}
}

func TestToolAccessMigrationIsIdempotent(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "tool-access.db")

	first, err := New(context.Background(), StoreDeps{DSN: dsn})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := New(context.Background(), StoreDeps{DSN: dsn})
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer second.Close()

	if _, err := second.ListToolPresets(""); err != nil {
		t.Fatalf("ListToolPresets after reopening: %v", err)
	}
}

func TestToolAccessMigrationDoesNotSkipPartialSchema(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "partial-tool-access.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open partial database: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE tool_presets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  tool TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'direct',
  name TEXT NOT NULL,
  provider_id TEXT NOT NULL DEFAULT '',
  vendor TEXT NOT NULL DEFAULT '',
  base_url TEXT NOT NULL DEFAULT '',
  api_key_enc TEXT NOT NULL DEFAULT '',
  api_key_id TEXT NOT NULL DEFAULT '',
  models_json TEXT NOT NULL DEFAULT '[]',
  extra_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(tool, name)
);`); err != nil {
		db.Close()
		t.Fatalf("create partial schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close partial database: %v", err)
	}

	opened, err := New(context.Background(), StoreDeps{DSN: dsn})
	if err != nil {
		t.Fatalf("reopen partial schema: %v", err)
	}
	defer opened.Close()
	if _, err := opened.GetToolState(string(toolconfig.ToolOpencode)); err != nil {
		t.Fatalf("tool_state missing after partial migration recovery: %v", err)
	}
	if _, err := opened.GetToolFileStates(string(toolconfig.ToolOpencode)); err != nil {
		t.Fatalf("tool_file_state missing after partial migration recovery: %v", err)
	}
}

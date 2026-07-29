package toolconfig

import (
	"errors"
	"testing"
)

func TestAutoapiBaseURL(t *testing.T) {
	tests := []struct {
		tool Tool
		want string
	}{
		{ToolOpencode, "http://10.0.0.2:8344/v1"},
		{ToolCodex, "http://10.0.0.2:8344/v1"},
		{ToolClaude, "http://10.0.0.2:8344"},
	}
	for _, tt := range tests {
		if got := AutoapiBaseURL(tt.tool, " http://10.0.0.2:8344/// "); got != tt.want {
			t.Errorf("AutoapiBaseURL(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
	if got := AutoapiBaseURL(ToolOpencode, ""); got != "" {
		t.Fatalf("empty relay address = %q, want empty", got)
	}
	if got := AutoapiBaseURL(ToolOpencode, "///"); got != "" {
		t.Fatalf("slash-only relay address = %q, want empty", got)
	}
}

func TestBuildAutoapiPresetDefaultsFirstModel(t *testing.T) {
	models := []PresetModel{{Name: "first"}, {Name: "second"}}
	preset, err := BuildAutoapiPreset(ToolOpencode, "Autoapi", "http://relay:8344/", "token-id", models)
	if err != nil {
		t.Fatal(err)
	}
	if preset.Kind != PresetAutoapi || preset.ProviderID != "autoapi" {
		t.Fatalf("unexpected preset identity: %+v", preset)
	}
	if preset.BaseURL != "http://relay:8344/v1" || preset.APIKeyID != "token-id" || preset.APIKey != "token-id" {
		t.Fatalf("unexpected relay fields: %+v", preset)
	}
	if preset.Vendor != VendorOpenAICompatible || !preset.Models[0].Default || preset.Models[1].Default {
		t.Fatalf("unexpected model/vendor fields: %+v", preset)
	}
	if models[0].Default {
		t.Fatal("builder mutated caller's model slice")
	}

	for _, tool := range []Tool{ToolCodex, ToolClaude} {
		got, err := BuildAutoapiPreset(tool, "Autoapi", "http://relay:8344", "token-id", []PresetModel{{Name: "model"}})
		if err != nil {
			t.Fatal(err)
		}
		if got.Vendor != "" {
			t.Fatalf("%s vendor = %q, want empty", tool, got.Vendor)
		}
	}
}

func TestBuildAutoapiPresetValidation(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
		addr string
		key  string
		mods []PresetModel
	}{
		{"name", ToolOpencode, "http://relay", "key", []PresetModel{{Name: "model"}}},
		{"address", ToolOpencode, "", "key", []PresetModel{{Name: "model"}}},
		{"key", ToolOpencode, "http://relay", "", []PresetModel{{Name: "model"}}},
		{"models", ToolOpencode, "http://relay", "key", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := "Autoapi"
			if tt.name == "name" {
				name = ""
			}
			_, err := BuildAutoapiPreset(tt.tool, name, tt.addr, tt.key, tt.mods)
			if !errors.Is(err, ErrInvalidPreset) {
				t.Fatalf("error = %v, want ErrInvalidPreset", err)
			}
		})
	}
}

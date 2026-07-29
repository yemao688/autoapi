package toolconfig

import (
	"errors"
	"strings"
	"testing"
)

func TestExportSnippetDispatchesByTool(t *testing.T) {
	for _, tool := range []Tool{ToolOpencode, ToolCodex, ToolClaude} {
		preset := PresetPlaintext{Preset: Preset{
			Tool:    tool,
			Kind:    PresetDirect,
			Name:    "Example",
			BaseURL: "https://api.example.test/v1",
			Models:  []PresetModel{{Name: "model", Default: true}},
		}, APIKey: "secret"}
		snippet, err := ExportSnippet(preset, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if snippet.TargetPath == "" || snippet.Format == "" || !strings.Contains(snippet.Content, preset.BaseURL) {
			t.Fatalf("%s produced incomplete snippet: %+v", tool, snippet)
		}
	}
}

func TestExportSnippetRejectsUnsupportedTool(t *testing.T) {
	_, err := ExportSnippet(PresetPlaintext{Preset: Preset{Tool: Tool("unknown")}}, t.TempDir())
	if !errors.Is(err, ErrInvalidPreset) {
		t.Fatalf("error = %v, want ErrInvalidPreset", err)
	}
}

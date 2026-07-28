package toolconfig

import "fmt"

// ExportSnippet dispatches snippet rendering to the adapter for p.Tool.
// homeDir only steers TargetPath guidance (see Adapter.ExportSnippet).
func ExportSnippet(p PresetPlaintext, homeDir string) (Snippet, error) {
	switch p.Tool {
	case ToolOpencode:
		return (OpenCodeAdapter{}).ExportSnippet(p, homeDir)
	case ToolCodex:
		return (CodexAdapter{}).ExportSnippet(p, homeDir)
	case ToolClaude:
		return (ClaudeAdapter{}).ExportSnippet(p, homeDir)
	default:
		return Snippet{}, fmt.Errorf("%w: unsupported tool %q", ErrInvalidPreset, p.Tool)
	}
}

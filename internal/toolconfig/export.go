package toolconfig

import "fmt"

// ExportSnippet dispatches snippet rendering to the adapter for p.Tool.
func ExportSnippet(p PresetPlaintext) (Snippet, error) {
	switch p.Tool {
	case ToolOpencode:
		return (OpenCodeAdapter{}).ExportSnippet(p)
	case ToolCodex:
		return (CodexAdapter{}).ExportSnippet(p)
	case ToolClaude:
		return (ClaudeAdapter{}).ExportSnippet(p)
	default:
		return Snippet{}, fmt.Errorf("%w: unsupported tool %q", ErrInvalidPreset, p.Tool)
	}
}

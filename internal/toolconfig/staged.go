package toolconfig

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// ToolProviderChange is one ordered provider mutation in a complete tool
// configuration plan. Park has the same file effect as remove; the service
// layer keeps its preset in the database for later re-enabling.
type ToolProviderChange struct {
	Action string
	Preset PresetPlaintext
}

// PlanToolConfigChange snapshots a tool's managed files once and renders all
// provider mutations into one multi-file ChangeSet.
func PlanToolConfigChange(tool Tool, changes []ToolProviderChange, homeDir string) (*ChangeSet, error) {
	switch tool {
	case ToolCodex:
		return planCodexToolConfigChange(changes, homeDir)
	case ToolClaude:
		return planClaudeToolConfigChange(changes, homeDir)
	case ToolOpencode:
		return nil, fmt.Errorf("toolconfig: opencode uses its dedicated staged planner: %w", ErrInvalidPreset)
	default:
		return nil, fmt.Errorf("toolconfig: unsupported staged tool %q: %w", tool, ErrInvalidPreset)
	}
}

func planCodexToolConfigChange(changes []ToolProviderChange, homeDir string) (*ChangeSet, error) {
	homeDir = absoluteHomeDir(homeDir)
	configPath := DefaultConfigPath(ToolCodex, homeDir)
	resolvedConfigPath, configBefore, err := snapshotFile(configPath, homeDir)
	if err != nil {
		return nil, err
	}
	authPath := filepath.Join(homeDir, ".codex", "auth.json")
	resolvedAuthPath, authBefore, err := snapshotFile(authPath, homeDir)
	if err != nil {
		return nil, err
	}

	configAfter := configBefore
	authAfter := authBefore
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if err := validateStagedAction(change.Action); err != nil {
			return nil, err
		}
		providerID, err := stagedProviderID(change.Preset.Preset)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[providerID]; exists {
			return nil, fmt.Errorf("%w: duplicate provider %q in plan", ErrConflict, providerID)
		}
		seen[providerID] = struct{}{}

		var nextConfig, nextAuth []byte
		switch change.Action {
		case "upsert":
			nextConfig, nextAuth, err = spliceCodexUpsert(configAfter, authAfter, change.Preset)
		case "park", "remove":
			nextConfig, nextAuth, err = spliceCodexStagedRemove(configAfter, authAfter, providerID)
		}
		if err != nil {
			return nil, err
		}
		configAfter, authAfter = nextConfig, nextAuth
	}
	if _, err := readTOMLBytes(configAfter); err != nil {
		return nil, fmt.Errorf("validate staged Codex config: %w", err)
	}

	configChange := changeForSnapshot(ResCodexConfig, resolvedConfigPath, false, configBefore)
	configChange.After = configAfter
	fileChanges := []FileChange{configChange}
	if !bytes.Equal(authBefore, authAfter) {
		authChange := changeForSnapshot(ResCodexAuth, resolvedAuthPath, true, authBefore)
		authChange.After = authAfter
		fileChanges = append(fileChanges, authChange)
	}
	return &ChangeSet{Tool: ToolCodex, Changes: fileChanges}, nil
}

func planClaudeToolConfigChange(changes []ToolProviderChange, homeDir string) (*ChangeSet, error) {
	homeDir = absoluteHomeDir(homeDir)
	configPath := DefaultConfigPath(ToolClaude, homeDir)
	resolvedPath, before, err := snapshotFile(configPath, homeDir)
	if err != nil {
		return nil, err
	}

	after := before
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if err := validateStagedAction(change.Action); err != nil {
			return nil, err
		}
		providerID, err := stagedProviderID(change.Preset.Preset)
		if err != nil {
			return nil, err
		}
		if providerID != "anthropic" {
			return nil, fmt.Errorf("Claude provider %q: %w", providerID, ErrConfigNotFound)
		}
		if _, exists := seen[providerID]; exists {
			return nil, fmt.Errorf("%w: duplicate provider %q in plan", ErrConflict, providerID)
		}
		seen[providerID] = struct{}{}

		switch change.Action {
		case "upsert":
			after, err = spliceClaudeUpsert(after, change.Preset)
		case "park", "remove":
			after, err = spliceClaudeStagedRemove(after, providerID)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := parseJSONBytes(after); err != nil {
		return nil, fmt.Errorf("validate staged Claude settings: %w", err)
	}
	change := changeForSnapshot(ResClaudeSettings, resolvedPath, false, before)
	change.After = after
	return &ChangeSet{Tool: ToolClaude, Changes: []FileChange{change}}, nil
}

func validateStagedAction(action string) error {
	switch action {
	case "upsert", "park", "remove":
		return nil
	default:
		return fmt.Errorf("%w: unknown staged provider action %q", ErrInvalidPreset, action)
	}
}

func stagedProviderID(preset Preset) (string, error) {
	if strings.TrimSpace(preset.ProviderID) == "" && strings.TrimSpace(preset.Name) == "" {
		return "", fmt.Errorf("%w: provider ID or name is required", ErrInvalidPreset)
	}
	providerID := providerKey(preset)
	if strings.TrimSpace(providerID) == "" {
		return "", fmt.Errorf("%w: provider ID is required", ErrInvalidPreset)
	}
	return providerID, nil
}

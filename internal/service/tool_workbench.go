package service

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"autoapi/internal/toolconfig"
)

// ToolProviderPlan is one staged provider operation for Codex or Claude.
// Action is "upsert", "park", or "remove".
type ToolProviderPlan struct {
	Action       string
	Preset       toolconfig.Preset
	PlaintextKey string
}

// ToolConfigPlan is the complete staged state for a generic tool workbench.
type ToolConfigPlan struct {
	Providers []ToolProviderPlan
}

// ToolFilePreview contains the exact before and after bytes for one managed
// file. The preview intentionally contains plaintext keys for this local,
// single-user workbench, matching the existing opencode preview behavior.
type ToolFilePreview struct {
	Path   string
	Before string
	After  string
}

type toolPresetSync struct {
	upserts   []toolconfig.Preset
	keys      []string
	removeIDs []int64
}

func (s *Service) planToolConfigChange(tool string, plan ToolConfigPlan) (*toolconfig.ChangeSet, *toolPresetSync, error) {
	toolID := toolconfig.Tool(tool)
	if toolID != toolconfig.ToolCodex && toolID != toolconfig.ToolClaude {
		if toolID == toolconfig.ToolOpencode {
			return nil, nil, fmt.Errorf("service: opencode uses its dedicated staged planner: %w", toolconfig.ErrInvalidPreset)
		}
		return nil, nil, fmt.Errorf("service: unsupported staged tool %q: %w", tool, toolconfig.ErrInvalidPreset)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	existing, err := s.store.ListToolPresets(tool)
	if err != nil {
		return nil, nil, err
	}
	existingByProvider := make(map[string][]toolconfig.Preset, len(existing))
	for _, preset := range existing {
		providerID := toolconfig.ProviderKey(preset)
		existingByProvider[providerID] = append(existingByProvider[providerID], preset)
	}

	adapter, err := adapterFor(toolID)
	if err != nil {
		return nil, nil, err
	}
	changes := make([]toolconfig.ToolProviderChange, 0, len(plan.Providers))
	sync := &toolPresetSync{}
	seen := make(map[string]struct{}, len(plan.Providers))
	for _, staged := range plan.Providers {
		providerID, err := stagedToolProviderID(staged.Preset)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := seen[providerID]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate %s provider %q in plan", toolconfig.ErrConflict, tool, providerID)
		}
		seen[providerID] = struct{}{}
		if staged.Action != "upsert" && staged.Action != "park" && staged.Action != "remove" {
			return nil, nil, fmt.Errorf("%w: unknown %s provider action %q", toolconfig.ErrInvalidPreset, tool, staged.Action)
		}

		preset := staged.Preset
		preset.Tool = toolID
		preset.ProviderID = providerID
		dbRows := existingByProvider[providerID]
		var existingRow *toolconfig.Preset
		if len(dbRows) > 0 {
			row := dbRows[0]
			existingRow = &row
		}

		var raw toolconfig.RawManagedSection
		if staged.Action == "upsert" || staged.Action == "park" {
			raw, err = adapter.ReadManagedRaw(homeDir, providerID)
			if err != nil && !errors.Is(err, toolconfig.ErrConfigNotFound) {
				return nil, nil, err
			}
		}

		switch staged.Action {
		case "upsert":
			plaintext, err := s.resolveToolUpsertPlaintext(preset, staged.PlaintextKey, raw, existingRow)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, toolconfig.ToolProviderChange{Action: "upsert", Preset: plaintext})
		case "park":
			if err := validateStagedToolPreset(preset); err != nil {
				return nil, nil, err
			}
			key, err := s.resolveToolKey(staged.PlaintextKey, raw, existingRow)
			if err != nil {
				return nil, nil, err
			}
			if existingRow != nil {
				preset.ID = existingRow.ID
				preset.Kind = existingRow.Kind
				preset.APIKeyID = existingRow.APIKeyID
				preset.CreatedAt = existingRow.CreatedAt
				preset.UpdatedAt = existingRow.UpdatedAt
			}
			sync.upserts = append(sync.upserts, preset)
			sync.keys = append(sync.keys, key)
			changes = append(changes, toolconfig.ToolProviderChange{
				Action: "park",
				Preset: toolconfig.PresetPlaintext{Preset: preset},
			})
		case "remove":
			changes = append(changes, toolconfig.ToolProviderChange{
				Action: "remove",
				Preset: toolconfig.PresetPlaintext{Preset: preset},
			})
			for _, row := range dbRows {
				sync.removeIDs = append(sync.removeIDs, row.ID)
			}
		}
	}

	changeSet, err := toolconfig.PlanToolConfigChange(toolID, changes, homeDir)
	if err != nil {
		return nil, nil, err
	}
	return changeSet, sync, nil
}

func stagedToolProviderID(preset toolconfig.Preset) (string, error) {
	if strings.TrimSpace(preset.ProviderID) == "" && strings.TrimSpace(preset.Name) == "" {
		return "", fmt.Errorf("%w: provider ID or name is required", toolconfig.ErrInvalidPreset)
	}
	providerID := toolconfig.ProviderKey(preset)
	if strings.TrimSpace(providerID) == "" {
		return "", fmt.Errorf("%w: provider ID is required", toolconfig.ErrInvalidPreset)
	}
	return providerID, nil
}

func validateStagedToolPreset(preset toolconfig.Preset) error {
	if strings.TrimSpace(preset.Name) == "" {
		return fmt.Errorf("%w: name is empty", toolconfig.ErrInvalidPreset)
	}
	if preset.Kind == toolconfig.PresetDirect && strings.TrimSpace(preset.BaseURL) == "" {
		return fmt.Errorf("%w: direct preset %q has an empty base URL", toolconfig.ErrInvalidPreset, preset.Name)
	}
	return nil
}

func (s *Service) resolveToolUpsertPlaintext(preset toolconfig.Preset, explicit string, raw toolconfig.RawManagedSection, existing *toolconfig.Preset) (toolconfig.PresetPlaintext, error) {
	if preset.Kind == toolconfig.PresetAutoapi {
		plain, err := s.presetPlaintext(preset)
		if err != nil {
			return toolconfig.PresetPlaintext{}, err
		}
		key, err := s.resolveToolKey(explicit, raw, existing)
		if err != nil {
			return toolconfig.PresetPlaintext{}, err
		}
		if key != "" {
			plain.APIKey = key
		}
		return plain, nil
	}
	if err := validateStagedToolPreset(preset); err != nil {
		return toolconfig.PresetPlaintext{}, err
	}
	key, err := s.resolveToolKey(explicit, raw, existing)
	if err != nil {
		return toolconfig.PresetPlaintext{}, err
	}
	return toolconfig.PresetPlaintext{Preset: preset, APIKey: key}, nil
}

func (s *Service) resolveToolKey(explicit string, raw toolconfig.RawManagedSection, existing *toolconfig.Preset) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if raw.APIKey != "" {
		return raw.APIKey, nil
	}
	if existing == nil || existing.APIKeyEnc == "" {
		return "", nil
	}
	return s.decryptToolKey(existing.APIKeyEnc)
}

// PreviewToolConfigChange renders every file in a complete staged Codex or
// Claude plan without writing files or changing the parked-provider mirror.
func (s *Service) PreviewToolConfigChange(tool string, plan ToolConfigPlan) ([]ToolFilePreview, error) {
	changeSet, _, err := s.planToolConfigChange(tool, plan)
	if err != nil {
		return nil, err
	}
	if len(changeSet.Changes) == 0 {
		return nil, fmt.Errorf("service: %s config plan produced no changes", tool)
	}
	previews := make([]ToolFilePreview, 0, len(changeSet.Changes))
	for _, change := range changeSet.Changes {
		previews = append(previews, ToolFilePreview{
			Path:   change.Path,
			Before: string(change.Before),
			After:  string(change.After),
		})
	}
	return previews, nil
}

// ApplyToolConfigChange commits all staged files and only then synchronizes
// parked-provider rows. Park keeps an encrypted DB row while remove deletes
// matching rows, exactly as the existing opencode workbench does.
func (s *Service) ApplyToolConfigChange(tool string, plan ToolConfigPlan, allowDrift bool) error {
	changeSet, sync, err := s.planToolConfigChange(tool, plan)
	if err != nil {
		return err
	}
	if len(changeSet.Changes) == 0 {
		return fmt.Errorf("service: %s config plan produced no changes", tool)
	}
	configPath := changeSet.Changes[0].Path
	toolID := toolconfig.Tool(tool)
	if _, err := s.commitToolChangeSet(toolID, changeSet, allowDrift, 0, configPath); err != nil {
		return err
	}
	for i, key := range sync.keys {
		if key == "" {
			sync.upserts[i].APIKeyEnc = ""
			continue
		}
		encoded, err := s.encryptToolKey(key)
		if err != nil {
			return err
		}
		sync.upserts[i].APIKeyEnc = encoded
	}
	return s.store.SyncToolPresets(sync.upserts, sync.removeIDs)
}

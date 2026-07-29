package service

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"autoapi/internal/toolconfig"
)

type OpencodeGlobalSettings = toolconfig.OpencodeGlobalSettings

// One staged provider operation. Action is "upsert", "park", or "remove".
type OpencodeProviderPlan struct {
	Action       string
	Preset       toolconfig.Preset
	PlaintextKey string
}

// OpencodeConfigPlan is the complete staged opencode workbench state.
type OpencodeConfigPlan struct {
	Providers []OpencodeProviderPlan
	Globals   OpencodeGlobalSettings
}

type opencodePresetSync struct {
	upserts   []toolconfig.Preset
	keys      []string
	removeIDs []int64
}

func (s *Service) planOpencodeConfigChange(plan OpencodeConfigPlan) (*toolconfig.ChangeSet, *opencodePresetSync, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	existing, err := s.store.ListToolPresets(string(toolconfig.ToolOpencode))
	if err != nil {
		return nil, nil, err
	}
	existingByProvider := make(map[string][]toolconfig.Preset, len(existing))
	for _, preset := range existing {
		existingByProvider[toolconfig.ProviderKey(preset)] = append(existingByProvider[toolconfig.ProviderKey(preset)], preset)
	}

	adapter := toolconfig.NewOpenCodeAdapter()
	changes := make([]toolconfig.OpencodeProviderChange, 0, len(plan.Providers))
	sync := &opencodePresetSync{}
	seen := make(map[string]struct{}, len(plan.Providers))
	for _, staged := range plan.Providers {
		providerID, err := stagedProviderID(staged)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := seen[providerID]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate opencode provider %q in plan", toolconfig.ErrConflict, providerID)
		}
		seen[providerID] = struct{}{}
		if staged.Action != "upsert" && staged.Action != "park" && staged.Action != "remove" {
			return nil, nil, fmt.Errorf("%w: unknown opencode provider action %q", toolconfig.ErrInvalidPreset, staged.Action)
		}

		preset := staged.Preset
		preset.Tool = toolconfig.ToolOpencode
		preset.ProviderID = providerID
		normalizeToolPresetVendor(&preset)
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
			plaintext, err := s.resolveOpencodeUpsertPlaintext(preset, staged.PlaintextKey, raw, existingRow)
			if err != nil {
				return nil, nil, err
			}
			changes = append(changes, toolconfig.OpencodeProviderChange{Action: "upsert", Preset: plaintext})
		case "park":
			if err := validateStagedOpencodePreset(preset); err != nil {
				return nil, nil, err
			}
			key, err := s.resolveOpencodeKey(staged.PlaintextKey, raw, existingRow)
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
			changes = append(changes, toolconfig.OpencodeProviderChange{
				Action: "remove",
				Preset: toolconfig.PresetPlaintext{Preset: preset},
			})
		case "remove":
			changes = append(changes, toolconfig.OpencodeProviderChange{
				Action: "remove",
				Preset: toolconfig.PresetPlaintext{Preset: preset},
			})
			for _, row := range dbRows {
				sync.removeIDs = append(sync.removeIDs, row.ID)
			}
		}
	}
	changeSet, err := toolconfig.PlanOpencodeConfigChange(homeDir, changes, plan.Globals)
	if err != nil {
		return nil, nil, err
	}
	return changeSet, sync, nil
}

func stagedProviderID(staged OpencodeProviderPlan) (string, error) {
	if strings.TrimSpace(staged.Preset.ProviderID) == "" && strings.TrimSpace(staged.Preset.Name) == "" {
		return "", fmt.Errorf("%w: opencode provider ID or name is required", toolconfig.ErrInvalidPreset)
	}
	providerID := toolconfig.ProviderKey(staged.Preset)
	if strings.TrimSpace(providerID) == "" {
		return "", fmt.Errorf("%w: opencode provider ID is required", toolconfig.ErrInvalidPreset)
	}
	return providerID, nil
}

func validateStagedOpencodePreset(preset toolconfig.Preset) error {
	if strings.TrimSpace(preset.Name) == "" {
		return fmt.Errorf("%w: name is empty", toolconfig.ErrInvalidPreset)
	}
	if preset.Kind == toolconfig.PresetDirect && strings.TrimSpace(preset.BaseURL) == "" {
		return fmt.Errorf("%w: direct preset %q has an empty base URL", toolconfig.ErrInvalidPreset, preset.Name)
	}
	return nil
}

func (s *Service) resolveOpencodeUpsertPlaintext(preset toolconfig.Preset, explicit string, raw toolconfig.RawManagedSection, existing *toolconfig.Preset) (toolconfig.PresetPlaintext, error) {
	if preset.Kind == toolconfig.PresetAutoapi {
		plain, err := s.presetPlaintext(preset)
		if err != nil {
			return toolconfig.PresetPlaintext{}, err
		}
		key, err := s.resolveOpencodeKey(explicit, raw, existing)
		if err != nil {
			return toolconfig.PresetPlaintext{}, err
		}
		if key != "" {
			plain.APIKey = key
		}
		return plain, nil
	}
	if err := validateStagedOpencodePreset(preset); err != nil {
		return toolconfig.PresetPlaintext{}, err
	}
	key, err := s.resolveOpencodeKey(explicit, raw, existing)
	if err != nil {
		return toolconfig.PresetPlaintext{}, err
	}
	return toolconfig.PresetPlaintext{Preset: preset, APIKey: key}, nil
}

func (s *Service) resolveOpencodeKey(explicit string, raw toolconfig.RawManagedSection, existing *toolconfig.Preset) (string, error) {
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

// PreviewOpencodeConfigChange renders the complete staged opencode document
// without writing the file or changing parked-provider rows.
func (s *Service) PreviewOpencodeConfigChange(plan OpencodeConfigPlan) (OmoSlimPreview, error) {
	changeSet, _, err := s.planOpencodeConfigChange(plan)
	if err != nil {
		return OmoSlimPreview{}, err
	}
	if len(changeSet.Changes) == 0 {
		return OmoSlimPreview{}, fmt.Errorf("service: opencode config plan produced no changes")
	}
	change := changeSet.Changes[0]
	return OmoSlimPreview{Path: change.Path, Before: string(change.Before), After: string(change.After)}, nil
}

// ApplyOpencodeConfigChange commits the complete staged document and only then
// synchronizes parked-provider rows in one store transaction.
func (s *Service) ApplyOpencodeConfigChange(plan OpencodeConfigPlan, allowDrift bool) error {
	changeSet, sync, err := s.planOpencodeConfigChange(plan)
	if err != nil {
		return err
	}
	if len(changeSet.Changes) == 0 {
		return fmt.Errorf("service: opencode config plan produced no changes")
	}
	configPath := changeSet.Changes[0].Path
	if _, err := s.commitToolChangeSet(toolconfig.ToolOpencode, changeSet, allowDrift, 0, configPath); err != nil {
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

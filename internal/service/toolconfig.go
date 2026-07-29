package service

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/toolconfig"
)

// ToolApplyResult summarizes a successful tool configuration apply.
type ToolApplyResult struct {
	Tool        string
	ConfigPath  string
	BackupPaths []string
	HotReload   bool
	RestartHint string
}

// ToolProviderView is the UI-facing row: a preset plus its live mirror state.
type ToolProviderView struct {
	Preset  toolconfig.Preset
	Enabled bool // provider currently present in the tool's config file
	InDB    bool // false for synthesized rows that exist only in the file
}

// DriftState reports the current drift state of one managed tool resource.
type DriftState struct {
	Resource toolconfig.Resource
	Path     string
	Drifted  bool
	Missing  bool
}

// ToolBackupInfo describes one backup file available for restoration.
type ToolBackupInfo struct {
	Resource toolconfig.Resource
	Path     string
	ModTime  time.Time
}

const (
	toolRestartHint   = "restart"
	toolHotReloadHint = "hot_reload"
)

// ListToolStatuses detects every supported tool and adds the persisted active
// preset and per-resource drift state.
func (s *Service) ListToolStatuses() ([]toolconfig.ToolStatus, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	statuses := make([]toolconfig.ToolStatus, 0, len(toolconfig.SupportedTools))
	for _, tool := range toolconfig.SupportedTools {
		adapter, err := adapterFor(tool)
		if err != nil {
			return nil, err
		}
		status := adapter.Detect(homeDir)
		state, err := s.store.GetToolState(string(tool))
		if err != nil {
			return nil, fmt.Errorf("service: get %s tool state: %w", tool, err)
		}
		status.ActivePresetID = state.ActivePresetID

		fileStates, err := s.store.GetToolFileStates(string(tool))
		if err != nil {
			return nil, fmt.Errorf("service: get %s file states: %w", tool, err)
		}
		for _, fileState := range fileStates {
			if fileState.Path == "" {
				status.Drifted = true
				continue
			}
			currentHash, err := toolconfig.HashFile(fileState.Path)
			if err != nil {
				return nil, fmt.Errorf("service: hash %s resource %s: %w", tool, fileState.Resource, err)
			}
			if currentHash != fileState.AppliedFileHash {
				status.Drifted = true
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// CreateToolPreset encrypts an optional plaintext key and persists a preset.
func (s *Service) CreateToolPreset(p toolconfig.Preset, plaintextKey string) (*toolconfig.Preset, error) {
	normalizeToolPresetVendor(&p)
	if plaintextKey != "" {
		encoded, err := s.encryptToolKey(plaintextKey)
		if err != nil {
			return nil, err
		}
		p.APIKeyEnc = encoded
	}
	if err := s.store.CreateToolPreset(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// UpdateToolPreset updates a preset. An empty plaintext key preserves the
// existing encrypted key.
func (s *Service) UpdateToolPreset(p toolconfig.Preset, plaintextKey string) (*toolconfig.Preset, error) {
	normalizeToolPresetVendor(&p)
	existing, err := s.store.GetToolPreset(p.ID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("service: tool preset %d not found", p.ID)
	}
	if plaintextKey == "" {
		p.APIKeyEnc = existing.APIKeyEnc
	} else {
		encoded, err := s.encryptToolKey(plaintextKey)
		if err != nil {
			return nil, err
		}
		p.APIKeyEnc = encoded
	}
	if p.CreatedAt == 0 {
		p.CreatedAt = existing.CreatedAt
	}
	if err := s.store.UpdateToolPreset(&p); err != nil {
		return nil, err
	}
	return s.store.GetToolPreset(p.ID)
}

func (s *Service) GetToolPresets(tool string) ([]toolconfig.Preset, error) {
	return s.store.ListToolPresets(tool)
}

func (s *Service) DeleteToolPreset(id int64) error {
	preset, err := s.store.GetToolPreset(id)
	if err != nil {
		return err
	}
	if preset == nil {
		return fmt.Errorf("service: tool preset %d not found", id)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("service: resolve home dir: %w", err)
	}
	adapter, err := adapterFor(preset.Tool)
	if err != nil {
		return err
	}
	providerIDs, err := providerIDsForTool(preset.Tool, adapter, homeDir)
	if err != nil {
		return err
	}
	providerID := toolconfig.ProviderKey(*preset)
	for _, candidate := range providerIDs {
		if candidate == providerID {
			return fmt.Errorf("%w: 该供应商当前已启用，请先在列表中禁用", toolconfig.ErrConflict)
		}
	}
	return s.store.DeleteToolPreset(id)
}

// ListToolProviders returns the live mirror of a tool config and its parked DB
// presets. Provider entries present only on disk are synthesized as ID 0 rows.
func (s *Service) ListToolProviders(tool string) ([]ToolProviderView, error) {
	toolID := toolconfig.Tool(tool)
	adapter, err := adapterFor(toolID)
	if err != nil {
		return nil, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	providerIDs, err := providerIDsForTool(toolID, adapter, homeDir)
	if err != nil {
		return nil, err
	}
	fileProviders := make(map[string]struct{}, len(providerIDs))
	for _, providerID := range providerIDs {
		fileProviders[providerID] = struct{}{}
	}
	presets, err := s.store.ListToolPresets(tool)
	if err != nil {
		return nil, err
	}
	views := make([]ToolProviderView, 0, len(presets)+len(providerIDs))
	claimed := make(map[string]struct{}, len(presets))
	for _, preset := range presets {
		normalizeToolPresetVendor(&preset)
		providerID := toolconfig.ProviderKey(preset)
		_, enabled := fileProviders[providerID]
		if enabled {
			claimed[providerID] = struct{}{}
		}
		views = append(views, ToolProviderView{Preset: preset, Enabled: enabled, InDB: true})
	}
	for _, providerID := range providerIDs {
		if _, exists := claimed[providerID]; exists {
			continue
		}
		raw, err := adapter.ReadManagedRaw(homeDir, providerID)
		if err != nil {
			return nil, err
		}
		if !raw.Present {
			continue
		}
		name := raw.Name
		if name == "" {
			name = providerID
		}
		vendor := ""
		if toolID == toolconfig.ToolOpencode {
			vendor = toolconfig.NormalizeVendor(raw.Vendor)
		}
		views = append(views, ToolProviderView{
			Preset: toolconfig.Preset{
				Tool:       toolID,
				Kind:       toolconfig.PresetDirect,
				Name:       name,
				ProviderID: providerID,
				Vendor:     vendor,
				BaseURL:    raw.BaseURL,
				Models:     raw.Models,
				APIKeyEnc:  raw.APIKey,
			},
			Enabled: true,
			InDB:    false,
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].Enabled != views[j].Enabled {
			return views[i].Enabled
		}
		leftName := strings.ToLower(views[i].Preset.Name)
		rightName := strings.ToLower(views[j].Preset.Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return toolconfig.ProviderKey(views[i].Preset) < toolconfig.ProviderKey(views[j].Preset)
	})
	return views, nil
}

// RevealToolProviderKey returns a provider key only after an explicit user
// request from the provider editor. The plaintext value is never logged or
// included in a redacted provider view.
func (s *Service) RevealToolProviderKey(tool, providerID string) (string, error) {
	toolID := toolconfig.Tool(tool)
	adapter, err := adapterFor(toolID)
	if err != nil {
		return "", err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("service: resolve home dir: %w", err)
	}
	raw, err := adapter.ReadManagedRaw(homeDir, providerID)
	if err != nil && !errors.Is(err, toolconfig.ErrConfigNotFound) {
		return "", err
	}
	if err == nil && raw.Present {
		return raw.APIKey, nil
	}

	presets, err := s.store.ListToolPresets(string(toolID))
	if err != nil {
		return "", err
	}
	for _, preset := range presets {
		if toolconfig.ProviderKey(preset) != providerID {
			continue
		}
		if preset.Kind == toolconfig.PresetAutoapi || preset.APIKeyEnc == "" {
			return "", nil
		}
		return s.decryptToolKey(preset.APIKeyEnc)
	}
	return "", nil
}

// EnableToolPreset writes a parked DB preset into the live tool config.
func (s *Service) EnableToolPreset(id int64) (ToolApplyResult, error) {
	preset, err := s.store.GetToolPreset(id)
	if err != nil {
		return ToolApplyResult{}, err
	}
	if preset == nil {
		return ToolApplyResult{}, fmt.Errorf("service: tool preset %d not found", id)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ToolApplyResult{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	adapter, err := adapterFor(preset.Tool)
	if err != nil {
		return ToolApplyResult{}, err
	}
	plaintext, err := s.presetPlaintext(*preset)
	if err != nil {
		return ToolApplyResult{}, err
	}
	changeSet, err := adapter.Plan(plaintext, homeDir)
	if err != nil {
		return ToolApplyResult{}, err
	}
	configPath := adapter.Detect(homeDir).ConfigPath
	return s.commitToolChangeSet(preset.Tool, changeSet, true, 0, configPath)
}

// DisableToolPreset snapshots the live provider into the DB and removes it
// from the tool config. The DB copy remains available for a later enable.
func (s *Service) DisableToolPreset(tool, providerID string) (ToolApplyResult, error) {
	toolID := toolconfig.Tool(tool)
	adapter, err := adapterFor(toolID)
	if err != nil {
		return ToolApplyResult{}, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ToolApplyResult{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	raw, err := adapter.ReadManagedRaw(homeDir, providerID)
	if err != nil {
		return ToolApplyResult{}, err
	}
	existingPresets, err := s.store.ListToolPresets(tool)
	if err != nil {
		return ToolApplyResult{}, err
	}
	var existing *toolconfig.Preset
	for i := range existingPresets {
		if toolconfig.ProviderKey(existingPresets[i]) == providerID {
			existing = &existingPresets[i]
			break
		}
	}
	if !raw.Present {
		if existing != nil {
			return ToolApplyResult{Tool: tool}, nil
		}
		return ToolApplyResult{}, fmt.Errorf("service: managed configuration for %s is not present: %w", tool, toolconfig.ErrConfigNotFound)
	}

	if existing != nil {
		updated := *existing
		if raw.Name != "" {
			updated.Name = raw.Name
		}
		if toolID == toolconfig.ToolOpencode {
			updated.Vendor = toolconfig.NormalizeVendor(raw.Vendor)
		}
		updated.BaseURL = raw.BaseURL
		updated.Models = raw.Models
		if raw.APIKey != "" {
			updated.APIKeyEnc, err = s.encryptToolKey(raw.APIKey)
			if err != nil {
				return ToolApplyResult{}, err
			}
		}
		if err := s.store.UpdateToolPreset(&updated); err != nil {
			return ToolApplyResult{}, err
		}
	} else {
		name := raw.Name
		if name == "" {
			name = providerID
		}
		preset := toolconfig.Preset{
			Tool:       toolID,
			Kind:       toolconfig.PresetDirect,
			Name:       name,
			ProviderID: providerID,
			BaseURL:    raw.BaseURL,
			Models:     raw.Models,
		}
		if toolID == toolconfig.ToolOpencode {
			preset.Vendor = toolconfig.NormalizeVendor(raw.Vendor)
		}
		if raw.APIKey != "" {
			preset.APIKeyEnc, err = s.encryptToolKey(raw.APIKey)
			if err != nil {
				return ToolApplyResult{}, err
			}
		}
		if err := s.store.CreateToolPreset(&preset); err != nil {
			return ToolApplyResult{}, err
		}
	}

	changeSet, err := adapter.PlanRemoval(homeDir, providerID)
	if err != nil {
		return ToolApplyResult{}, err
	}
	configPath := adapter.Detect(homeDir).ConfigPath
	return s.commitToolChangeSet(toolID, changeSet, true, 0, configPath)
}

// UpdateEnabledToolPreset writes an enabled provider through to disk and then
// reconciles its DB row. ID 0 is used for a provider that exists only on disk.
func (s *Service) UpdateEnabledToolPreset(p toolconfig.Preset, plaintextKey string) (*toolconfig.Preset, error) {
	toolID := p.Tool
	adapter, err := adapterFor(toolID)
	if err != nil {
		return nil, err
	}
	if p.Kind == toolconfig.PresetAutoapi {
		if p.ID == 0 {
			return nil, fmt.Errorf("service: autoapi tool preset must already exist in the database")
		}
		if _, err := s.UpdateToolPreset(p, plaintextKey); err != nil {
			return nil, err
		}
		if _, err := s.EnableToolPreset(p.ID); err != nil {
			return nil, err
		}
		return s.store.GetToolPreset(p.ID)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	var existing *toolconfig.Preset
	if p.ID != 0 {
		existing, err = s.store.GetToolPreset(p.ID)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			return nil, fmt.Errorf("service: tool preset %d not found", p.ID)
		}
	}
	var resolvedPlaintextKey string
	if plaintextKey != "" {
		resolvedPlaintextKey = plaintextKey
		p.APIKeyEnc, err = s.encryptToolKey(plaintextKey)
		if err != nil {
			return nil, err
		}
	} else if existing != nil && existing.APIKeyEnc != "" {
		resolvedPlaintextKey, err = s.decryptToolKey(existing.APIKeyEnc)
		if err != nil {
			return nil, err
		}
		p.APIKeyEnc = existing.APIKeyEnc
	} else {
		raw, rawErr := adapter.ReadManagedRaw(homeDir, toolconfig.ProviderKey(p))
		if rawErr != nil {
			return nil, rawErr
		}
		if raw.APIKey != "" {
			resolvedPlaintextKey = raw.APIKey
			p.APIKeyEnc, err = s.encryptToolKey(raw.APIKey)
			if err != nil {
				return nil, err
			}
		} else {
			p.APIKeyEnc = ""
		}
	}
	normalizeToolPresetVendor(&p)
	plaintext := toolconfig.PresetPlaintext{Preset: p, APIKey: resolvedPlaintextKey}
	changeSet, err := adapter.Plan(plaintext, homeDir)
	if err != nil {
		return nil, err
	}
	configPath := adapter.Detect(homeDir).ConfigPath
	if _, err := s.commitToolChangeSet(toolID, changeSet, true, 0, configPath); err != nil {
		return nil, err
	}
	if existing == nil {
		if err := s.store.CreateToolPreset(&p); err != nil {
			return nil, err
		}
	} else {
		if p.CreatedAt == 0 {
			p.CreatedAt = existing.CreatedAt
		}
		if err := s.store.UpdateToolPreset(&p); err != nil {
			return nil, err
		}
	}
	return s.store.GetToolPreset(p.ID)
}

// CheckToolDrift returns per-resource drift details for a tool.
func (s *Service) CheckToolDrift(tool string) ([]DriftState, error) {
	if _, err := adapterFor(toolconfig.Tool(tool)); err != nil {
		return nil, err
	}
	states, err := s.store.GetToolFileStates(tool)
	if err != nil {
		return nil, err
	}
	out := make([]DriftState, 0, len(states))
	for _, state := range states {
		if state.Path == "" {
			out = append(out, DriftState{Resource: state.Resource, Path: state.Path, Drifted: true, Missing: true})
			continue
		}
		currentHash, err := toolconfig.HashFile(state.Path)
		if err != nil {
			return nil, fmt.Errorf("service: hash %s resource %s: %w", tool, state.Resource, err)
		}
		out = append(out, DriftState{
			Resource: state.Resource,
			Path:     state.Path,
			Drifted:  currentHash != state.AppliedFileHash,
			Missing:  currentHash == "",
		})
	}
	return out, nil
}

// ExportToolSnippet decrypts a preset only for the pure export renderer.
func (s *Service) ExportToolSnippet(id int64) (toolconfig.Snippet, error) {
	preset, err := s.store.GetToolPreset(id)
	if err != nil {
		return toolconfig.Snippet{}, err
	}
	if preset == nil {
		return toolconfig.Snippet{}, fmt.Errorf("service: tool preset %d not found", id)
	}
	plaintext, err := s.presetPlaintext(*preset)
	if err != nil {
		return toolconfig.Snippet{}, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return toolconfig.Snippet{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	return toolconfig.ExportSnippet(plaintext, homeDir)
}

// OmoSlimConfigView is the UI-facing projection of the OMO Slim config plus the
// closed-choice lists the editor needs. It is a single return value because
// the Wails binding only supports at most one data value plus error.
type OmoSlimConfigView struct {
	Path              string
	ActivePreset      string
	Agents            map[string]toolconfig.OmoSlimAgent
	CustomAgents      map[string]toolconfig.OmoSlimCustomAgent
	DisabledAgents    []string
	DisabledSkills    []string
	DisabledMcps      []string
	KnownPresets      []string
	ValidModels       []string
	AvailableVariants []string
	PresetAgents      map[string]map[string]toolconfig.OmoSlimAgent
	KnownSkills       []string
	KnownMcps         []string
}

func (s *Service) GetOmoSlimConfig() (OmoSlimConfigView, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return OmoSlimConfigView{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	config, err := toolconfig.ReadOmoSlimConfig(homeDir)
	if err != nil {
		return OmoSlimConfigView{}, err
	}
	validModels, err := toolconfig.ListProviderModels(homeDir)
	if err != nil {
		return OmoSlimConfigView{}, err
	}
	knownPresets, err := toolconfig.ListOmoSlimPresets(homeDir)
	if err != nil {
		return OmoSlimConfigView{}, err
	}
	variants, err := toolconfig.ListProviderVariants(homeDir)
	if err != nil {
		return OmoSlimConfigView{}, err
	}
	presetAgents, err := toolconfig.ListOmoSlimPresetAgents(homeDir)
	if err != nil {
		return OmoSlimConfigView{}, err
	}
	knownSkills, err := toolconfig.ListKnownSkills(homeDir)
	if err != nil {
		return OmoSlimConfigView{}, err
	}
	knownMcps, err := toolconfig.ListMcpNames(homeDir)
	if err != nil {
		return OmoSlimConfigView{}, err
	}
	return OmoSlimConfigView{
		Path:              config.Path,
		ActivePreset:      config.ActivePreset,
		Agents:            config.Agents,
		CustomAgents:      config.CustomAgents,
		DisabledAgents:    config.DisabledAgents,
		DisabledSkills:    config.DisabledSkills,
		DisabledMcps:      config.DisabledMcps,
		KnownPresets:      knownPresets,
		ValidModels:       validModels,
		AvailableVariants: variants,
		PresetAgents:      presetAgents,
		KnownSkills:       knownSkills,
		KnownMcps:         knownMcps,
	}, nil
}

// OmoSlimPreview renders the result of an OMO Slim change without writing anything, so
// the UI can show the exact file content before the user confirms a write.
type OmoSlimPreview struct {
	Path   string
	Before string
	After  string
}

// PreviewToolOmoSlimChange plans an OMO Slim change and returns the resulting file
// content. Nothing is written and no drift state is touched.
func (s *Service) PreviewToolOmoSlimChange(ch toolconfig.OmoSlimChange) (OmoSlimPreview, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return OmoSlimPreview{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	validModels, err := toolconfig.ListProviderModels(homeDir)
	if err != nil {
		return OmoSlimPreview{}, err
	}
	changeSet, err := toolconfig.PlanOmoSlimChange(homeDir, ch, validModels)
	if err != nil {
		return OmoSlimPreview{}, err
	}
	if len(changeSet.Changes) == 0 {
		return OmoSlimPreview{}, fmt.Errorf("service: OMO Slim plan produced no changes")
	}
	change := changeSet.Changes[0]
	before, _ := os.ReadFile(change.Path)
	return OmoSlimPreview{
		Path:   change.Path,
		Before: string(before),
		After:  string(change.After),
	}, nil
}

// GetOpencodeGlobalSettings reads the curated top-level opencode settings.
func (s *Service) GetOpencodeGlobalSettings() (toolconfig.OpencodeGlobalSettings, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return toolconfig.OpencodeGlobalSettings{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	return toolconfig.ReadOpencodeGlobalSettings(homeDir)
}

// OpencodeLiveState is the on-disk snapshot shown on the opencode card: the
// effective model pointer plus a compact OMO Slim overview. It is read live on
// every call and intentionally bypasses the DB-tracked state so drift
// becomes a concrete comparison instead of an abstract badge.
type OpencodeLiveState struct {
	Model                string
	OmoSlimConfigured    bool
	OmoSlimActivePreset  string
	OmoSlimAgentCount    int
	OmoSlimDisabledCount int
}

func (s *Service) GetOpencodeLiveState() (OpencodeLiveState, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return OpencodeLiveState{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	state := OpencodeLiveState{}
	model, err := toolconfig.ReadModelPointer(homeDir)
	if err != nil {
		return OpencodeLiveState{}, err
	}
	state.Model = model
	config, err := toolconfig.ReadOmoSlimConfig(homeDir)
	if err != nil {
		if errors.Is(err, toolconfig.ErrConfigNotFound) {
			return state, nil
		}
		return OpencodeLiveState{}, err
	}
	state.OmoSlimConfigured = true
	state.OmoSlimActivePreset = config.ActivePreset
	state.OmoSlimAgentCount = len(config.Agents)
	state.OmoSlimDisabledCount = len(config.DisabledAgents)
	return state, nil
}

func (s *Service) ApplyOmoSlimConfig(change toolconfig.OmoSlimChange, allowDrift bool) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("service: resolve home dir: %w", err)
	}
	validModels, err := toolconfig.ListProviderModels(homeDir)
	if err != nil {
		return err
	}
	changeSet, err := toolconfig.PlanOmoSlimChange(homeDir, change, validModels)
	if err != nil {
		return err
	}
	state, err := s.store.GetToolState(string(toolconfig.ToolOpencode))
	if err != nil {
		return err
	}
	if state.Tool == "" {
		state.Tool = toolconfig.ToolOpencode
	}
	if state.ConfigPath == "" {
		state.ConfigPath, _ = toolconfig.ResolveConfigPath(toolconfig.ToolOpencode, homeDir)
	}
	_, err = s.commitToolChangeSet(toolconfig.ToolOpencode, changeSet, allowDrift, state.ActivePresetID, state.ConfigPath)
	return err
}

const legacyOmoSlimBackupDir = "opencode-omo"

// ListToolBackups lists both legacy central backups and timestamped source
// siblings for every resource owned by the tool.
func (s *Service) ListToolBackups(tool string) ([]ToolBackupInfo, error) {
	toolID := toolconfig.Tool(tool)
	adapter, err := adapterFor(toolID)
	if err != nil {
		return nil, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	root := toolBackupRoot(homeDir)
	entries := make([]ToolBackupInfo, 0)
	seen := make(map[string]struct{})
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return filepath.SkipDir
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		resourcePath := filepath.ToSlash(filepath.Dir(rel))
		resource := toolconfig.Resource(resourcePath)
		if resourcePath == legacyOmoSlimBackupDir {
			resource = toolconfig.ResOmoSlimConfig
		}
		if !resourceBelongsToTool(toolID, resource) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if _, exists := seen[path]; !exists {
			seen[path] = struct{}{}
			entries = append(entries, ToolBackupInfo{Resource: resource, Path: path, ModTime: info.ModTime()})
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("service: list %s tool backups: %w", tool, err)
	}
	for _, resource := range backupResourcesForTool(toolID) {
		targetPath, targetErr := s.targetPathForResource(toolID, resource, homeDir, adapter)
		if targetErr != nil {
			continue
		}
		targetPath = resolveBackupTargetPath(targetPath)
		dirEntries, readErr := os.ReadDir(filepath.Dir(targetPath))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return nil, fmt.Errorf("service: list %s source backups: %w", tool, readErr)
		}
		for _, entry := range dirEntries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(filepath.Dir(targetPath), entry.Name())
			if !toolconfig.IsSourceBackup(targetPath, path) {
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil, infoErr
			}
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			entries = append(entries, ToolBackupInfo{Resource: resource, Path: path, ModTime: info.ModTime()})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Resource != entries[j].Resource {
			return entries[i].Resource < entries[j].Resource
		}
		if !entries[i].ModTime.Equal(entries[j].ModTime) {
			return entries[i].ModTime.After(entries[j].ModTime)
		}
		return entries[i].Path < entries[j].Path
	})
	return entries, nil
}

// RestoreToolBackup applies one backup as an explicit overwrite and persists
// the resulting resource hash while retaining the current active preset.
func (s *Service) RestoreToolBackup(tool, resource, backupPath string) error {
	toolID := toolconfig.Tool(tool)
	adapter, err := adapterFor(toolID)
	if err != nil {
		return err
	}
	if !resourceBelongsToTool(toolID, toolconfig.Resource(resource)) {
		return fmt.Errorf("service: resource %q does not belong to tool %q", resource, tool)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("service: resolve home dir: %w", err)
	}
	targetPath, err := s.targetPathForResource(toolID, toolconfig.Resource(resource), homeDir, adapter)
	if err != nil {
		return err
	}
	backupRoot := toolBackupRoot(homeDir)
	if err := validateBackupPath(backupRoot, toolconfig.Resource(resource), backupPath, targetPath); err != nil {
		return err
	}
	after, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("service: read tool backup: %w", err)
	}
	before, err := os.ReadFile(targetPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("service: read current tool file: %w", err)
	}
	if os.IsNotExist(err) {
		before = nil
	}
	beforeHash, err := toolconfig.HashFile(targetPath)
	if err != nil {
		return fmt.Errorf("service: hash current tool file: %w", err)
	}
	state, err := s.store.GetToolState(tool)
	if err != nil {
		return err
	}
	if state.Tool == "" {
		state.Tool = toolID
	}
	if state.ConfigPath == "" {
		state.ConfigPath = adapter.Detect(homeDir).ConfigPath
	}
	changeSet := &toolconfig.ChangeSet{
		Tool: toolID,
		Changes: []toolconfig.FileChange{{
			Resource:   toolconfig.Resource(resource),
			Path:       targetPath,
			Secret:     toolconfig.Resource(resource) == toolconfig.ResCodexAuth,
			Before:     before,
			BeforeHash: beforeHash,
			After:      after,
			Mode:       0o644,
		}},
	}
	_, err = s.commitToolChangeSet(toolID, changeSet, true, state.ActivePresetID, state.ConfigPath)
	return err
}

func (s *Service) presetPlaintext(p toolconfig.Preset) (toolconfig.PresetPlaintext, error) {
	if p.Kind == toolconfig.PresetAutoapi {
		if p.APIKeyID == "" {
			return toolconfig.PresetPlaintext{}, fmt.Errorf("service: 请重新选择访问密钥")
		}
		keys, err := s.store.ListAPIKeys()
		if err != nil {
			return toolconfig.PresetPlaintext{}, fmt.Errorf("service: list access keys: %w", err)
		}
		found := false
		for _, key := range keys {
			if key.ID == p.APIKeyID {
				found = true
				break
			}
		}
		if !found {
			return toolconfig.PresetPlaintext{}, fmt.Errorf("service: 请重新选择访问密钥")
		}
		if s.proxy == nil || !s.proxy.IsRunning() {
			return toolconfig.PresetPlaintext{}, fmt.Errorf("service: relay is not running")
		}
		settings, err := s.store.GetSettings()
		if err != nil {
			return toolconfig.PresetPlaintext{}, fmt.Errorf("service: get settings: %w", err)
		}
		var serverSettings model.ServerSettings
		if settings != nil {
			serverSettings = settings.Server
		}
		relayAddr := resolveAPIAddress(s.proxy.URL(), serverSettings)
		if relayAddr == "" {
			return toolconfig.PresetPlaintext{}, fmt.Errorf("service: relay address is unavailable")
		}
		plain, err := toolconfig.BuildAutoapiPreset(p.Tool, p.Name, relayAddr, p.APIKeyID, p.Models, p.Vendor)
		if err != nil {
			return toolconfig.PresetPlaintext{}, err
		}
		plain.BaseURL = toolconfig.AutoapiBaseURLForVendor(p.Tool, relayAddr, p.Vendor)
		if plain.APIKey == "" {
			plain.APIKey = p.APIKeyID
		}
		plain.ID = p.ID
		plain.Tool = p.Tool
		plain.Kind = p.Kind
		plain.Name = p.Name
		plain.ProviderID = p.ProviderID
		plain.APIKeyID = p.APIKeyID
		plain.CreatedAt = p.CreatedAt
		plain.UpdatedAt = p.UpdatedAt
		return plain, nil
	}

	plain := toolconfig.PresetPlaintext{Preset: p}
	if p.APIKeyEnc != "" {
		key, err := s.decryptToolKey(p.APIKeyEnc)
		if err != nil {
			return toolconfig.PresetPlaintext{}, err
		}
		plain.APIKey = key
	}
	return plain, nil
}

func (s *Service) commitToolChangeSet(tool toolconfig.Tool, changeSet *toolconfig.ChangeSet, allowDrift bool, activePresetID int64, configPath string) (ToolApplyResult, error) {
	if changeSet == nil {
		return ToolApplyResult{}, fmt.Errorf("service: nil tool change set")
	}
	states, err := s.store.GetToolFileStates(string(tool))
	if err != nil {
		return ToolApplyResult{}, err
	}
	expected := make(map[toolconfig.Resource]string, len(states))
	for _, state := range states {
		expected[state.Resource] = state.AppliedFileHash
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ToolApplyResult{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	result, err := toolconfig.Commit(changeSet, toolconfig.CommitOpts{
		BackupRoot:     toolBackupRoot(homeDir),
		KeepBackups:    20,
		ExpectedHashes: expected,
		AllowDrift:     allowDrift,
	})
	if err != nil {
		return ToolApplyResult{}, err
	}
	appliedAt := time.Now().UnixMilli()
	fileStates := make([]toolconfig.ToolFileState, 0, len(result.Files))
	backupPaths := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		fileStates = append(fileStates, toolconfig.ToolFileState{
			Tool:            tool,
			Resource:        file.Resource,
			Path:            file.Path,
			AppliedFileHash: file.Hash,
			AppliedAt:       appliedAt,
		})
		if file.BackupPath != "" {
			backupPaths = append(backupPaths, file.BackupPath)
		}
	}
	if configPath == "" {
		configPath, _ = toolconfig.ResolveConfigPath(tool, homeDir)
	}
	if err := s.store.SaveToolApplyState(&toolconfig.ToolState{
		Tool:           tool,
		ActivePresetID: activePresetID,
		ConfigPath:     configPath,
		AppliedAt:      appliedAt,
	}, fileStates); err != nil {
		return ToolApplyResult{}, err
	}
	hotReload := tool == toolconfig.ToolClaude
	restartHint := toolRestartHint
	if hotReload {
		restartHint = toolHotReloadHint
	}
	return ToolApplyResult{
		Tool:        string(tool),
		ConfigPath:  configPath,
		BackupPaths: backupPaths,
		HotReload:   hotReload,
		RestartHint: restartHint,
	}, nil
}

func (s *Service) targetPathForResource(tool toolconfig.Tool, resource toolconfig.Resource, homeDir string, adapter toolconfig.Adapter) (string, error) {
	states, err := s.store.GetToolFileStates(string(tool))
	if err != nil {
		return "", err
	}
	for _, state := range states {
		if state.Resource == resource && state.Path != "" {
			return state.Path, nil
		}
	}
	switch resource {
	case toolconfig.ResOpencodeConfig, toolconfig.ResCodexConfig, toolconfig.ResClaudeSettings:
		path, _ := toolconfig.ResolveConfigPath(tool, homeDir)
		if path == "" {
			return "", fmt.Errorf("service: no path for resource %s", resource)
		}
		return resolveBackupTargetPath(path), nil
	case toolconfig.ResOpencodeOmoSlim, toolconfig.ResOmoSlimConfig:
		if path, ok := toolconfig.DetectOmoSlimConfig(homeDir); ok {
			return resolveBackupTargetPath(path), nil
		}
	case toolconfig.ResCodexAuth:
		status := adapter.Detect(homeDir)
		if path := status.ExtraPaths["auth_json"]; path != "" {
			return resolveBackupTargetPath(path), nil
		}
	}
	return "", fmt.Errorf("service: no target path for resource %s", resource)
}

func (s *Service) encryptToolKey(plaintext string) (string, error) {
	ciphertext, nonce, err := s.Encrypt([]byte(plaintext))
	if err != nil {
		return "", err
	}
	data := make([]byte, 0, len(nonce)+len(ciphertext))
	data = append(data, nonce...)
	data = append(data, ciphertext...)
	return base64.StdEncoding.EncodeToString(data), nil
}

func (s *Service) decryptToolKey(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("service: decode tool API key: %w", err)
	}
	block, err := aes.NewCipher(s.encKey)
	if err != nil {
		return "", fmt.Errorf("service: create tool API key cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("service: create tool API key GCM: %w", err)
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("service: tool API key ciphertext is truncated")
	}
	plaintext, err := s.Decrypt(data[gcm.NonceSize():], data[:gcm.NonceSize()])
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func toolBackupRoot(homeDir string) string {
	return filepath.Join(homeDir, ".autoapi", "toolconfig-backups")
}

func resourceBelongsToTool(tool toolconfig.Tool, resource toolconfig.Resource) bool {
	prefix := string(tool) + "/"
	if strings.HasPrefix(string(resource), prefix) && len(resource) > len(prefix) {
		return true
	}
	return tool == toolconfig.ToolOpencode && resource == toolconfig.ResOmoSlimConfig
}

func validateBackupPath(backupRoot string, resource toolconfig.Resource, backupPath string, targets ...string) error {
	backupAbs, err := filepath.Abs(backupPath)
	if err != nil {
		return fmt.Errorf("service: resolve backup path: %w", err)
	}
	info, err := os.Stat(backupAbs)
	if err != nil {
		return fmt.Errorf("service: stat backup path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("service: backup path is not a regular file")
	}
	resourceDirs := []string{filepath.Join(backupRoot, filepath.FromSlash(string(resource)))}
	if resource == toolconfig.ResOmoSlimConfig {
		resourceDirs = append(resourceDirs, filepath.Join(backupRoot, filepath.FromSlash(legacyOmoSlimBackupDir)))
	}
	for _, resourceDir := range resourceDirs {
		rootAbs, err := filepath.Abs(resourceDir)
		if err != nil {
			return fmt.Errorf("service: resolve backup directory: %w", err)
		}
		rel, err := filepath.Rel(rootAbs, backupAbs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return nil
		}
	}
	if len(targets) == 0 || !toolconfig.IsSourceBackup(resolveBackupTargetPath(targets[0]), backupAbs) {
		return fmt.Errorf("service: backup path is outside resource directory")
	}
	return nil
}

func backupResourcesForTool(tool toolconfig.Tool) []toolconfig.Resource {
	switch tool {
	case toolconfig.ToolOpencode:
		return []toolconfig.Resource{toolconfig.ResOpencodeConfig, toolconfig.ResOmoSlimConfig, toolconfig.ResOpencodeOmoSlim}
	case toolconfig.ToolCodex:
		return []toolconfig.Resource{toolconfig.ResCodexConfig, toolconfig.ResCodexAuth}
	case toolconfig.ToolClaude:
		return []toolconfig.Resource{toolconfig.ResClaudeSettings}
	default:
		return nil
	}
}

func resolveBackupTargetPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(filepath.Clean(resolvedDir), filepath.Base(path))
	}
	return path
}

func adapterFor(tool toolconfig.Tool) (toolconfig.Adapter, error) {
	switch tool {
	case toolconfig.ToolOpencode:
		return toolconfig.NewOpenCodeAdapter(), nil
	case toolconfig.ToolCodex:
		return toolconfig.NewCodexAdapter(), nil
	case toolconfig.ToolClaude:
		return toolconfig.NewClaudeAdapter(), nil
	default:
		return nil, fmt.Errorf("service: unsupported tool %q", tool)
	}
}

func normalizeToolPresetVendor(p *toolconfig.Preset) {
	if p != nil && p.Tool == toolconfig.ToolOpencode {
		p.Vendor = toolconfig.NormalizeVendor(p.Vendor)
	}
}

func providerIDsForTool(tool toolconfig.Tool, adapter toolconfig.Adapter, homeDir string) ([]string, error) {
	switch tool {
	case toolconfig.ToolOpencode:
		return toolconfig.ListOpenCodeProviderIDs(homeDir)
	case toolconfig.ToolCodex:
		return toolconfig.ListCodexProviderIDs(homeDir)
	case toolconfig.ToolClaude:
		raw, err := adapter.ReadManagedRaw(homeDir, "anthropic")
		if err != nil {
			return nil, err
		}
		if raw.Present {
			return []string{"anthropic"}, nil
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("service: unsupported tool %q", tool)
	}
}

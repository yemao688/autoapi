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
	return s.store.DeleteToolPreset(id)
}

// ApplyToolPreset plans and commits a provider preset, then persists the
// resulting hashes and backup paths as one database operation.
func (s *Service) ApplyToolPreset(id int64, allowDrift bool) (ToolApplyResult, error) {
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
	return s.commitToolChangeSet(preset.Tool, changeSet, allowDrift, id, configPath)
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

// ImportToolPreset reconstructs a direct preset from the adapter's raw,
// plaintext managed section. The plaintext key is encrypted before storage.
func (s *Service) ImportToolPreset(tool, providerID, name string) (*toolconfig.Preset, error) {
	toolID := toolconfig.Tool(tool)
	adapter, err := adapterFor(toolID)
	if err != nil {
		return nil, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	raw, err := adapter.ReadManagedRaw(homeDir, providerID)
	if err != nil {
		return nil, err
	}
	if !raw.Present {
		return nil, fmt.Errorf("service: managed configuration for %s is not present: %w", tool, toolconfig.ErrConfigNotFound)
	}
	if raw.ProviderID == "" {
		raw.ProviderID = providerID
	}
	preset := toolconfig.Preset{
		Tool:       toolID,
		Kind:       toolconfig.PresetDirect,
		Name:       name,
		ProviderID: raw.ProviderID,
		BaseURL:    raw.BaseURL,
		Models:     raw.Models,
	}
	if raw.APIKey != "" {
		preset.APIKeyEnc, err = s.encryptToolKey(raw.APIKey)
		if err != nil {
			return nil, err
		}
	}
	if err := s.store.CreateToolPreset(&preset); err != nil {
		return nil, err
	}
	return &preset, nil
}

// ImportCandidate describes one provider entry discovered in a tool's
// existing on-disk config, for batch import. It is secret-free: HasKey is a
// presence boolean and plaintext keys never leave the backend.
type ImportCandidate struct {
	ProviderID      string
	BaseURL         string
	HasKey          bool
	Models          []string
	AlreadyImported bool
	SuggestedName   string
}

// ListImportCandidates enumerates the provider entries present in a tool's
// existing config so the UI can offer batch import. opencode/codex list
// every provider entry; claude exposes at most one candidate (its single
// env block) under the conventional provider ID "anthropic". Entries whose
// provider ID already belongs to a stored preset are marked AlreadyImported.
func (s *Service) ListImportCandidates(tool string) ([]ImportCandidate, error) {
	toolID := toolconfig.Tool(tool)
	adapter, err := adapterFor(toolID)
	if err != nil {
		return nil, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("service: resolve home dir: %w", err)
	}

	var providerIDs []string
	switch toolID {
	case toolconfig.ToolOpencode:
		providerIDs, err = toolconfig.ListOpenCodeProviderIDs(homeDir)
	case toolconfig.ToolCodex:
		providerIDs, err = toolconfig.ListCodexProviderIDs(homeDir)
	case toolconfig.ToolClaude:
		// Claude manages a single env block with no provider key; probe the
		// conventional ID and surface at most one candidate.
		raw, rerr := adapter.ReadManagedRaw(homeDir, "anthropic")
		if rerr != nil {
			return nil, rerr
		}
		if raw.Present {
			providerIDs = []string{"anthropic"}
		}
	default:
		return nil, fmt.Errorf("service: unsupported tool %q", tool)
	}
	if err != nil {
		return nil, err
	}
	if len(providerIDs) == 0 {
		return []ImportCandidate{}, nil
	}

	existing, err := s.store.ListToolPresets(string(toolID))
	if err != nil {
		return nil, err
	}
	usedProviderIDs := make(map[string]bool, len(existing))
	usedNames := make(map[string]bool, len(existing))
	for _, p := range existing {
		usedProviderIDs[p.ProviderID] = true
		usedNames[p.Name] = true
	}

	candidates := make([]ImportCandidate, 0, len(providerIDs))
	for _, providerID := range providerIDs {
		raw, rerr := adapter.ReadManagedRaw(homeDir, providerID)
		if rerr != nil {
			return nil, rerr
		}
		if !raw.Present {
			continue
		}
		models := make([]string, 0, len(raw.Models))
		for _, m := range raw.Models {
			models = append(models, m.Name)
		}
		suggested := providerID
		for i := 2; usedNames[suggested]; i++ {
			suggested = fmt.Sprintf("%s-%d", providerID, i)
		}
		candidates = append(candidates, ImportCandidate{
			ProviderID:      providerID,
			BaseURL:         raw.BaseURL,
			HasKey:          raw.APIKey != "",
			Models:          models,
			AlreadyImported: usedProviderIDs[providerID],
			SuggestedName:   suggested,
		})
	}
	return candidates, nil
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

// OmoConfigView is the UI-facing projection of the OMO config plus the
// closed-choice lists the editor needs. It is a single return value because
// the Wails binding only supports at most one data value plus error.
type OmoConfigView struct {
	Path              string
	ActivePreset      string
	Agents            map[string]toolconfig.OmoAgent
	DisabledAgents    []string
	KnownPresets      []string
	ValidModels       []string
	AvailableVariants []string
	PresetAgents      map[string]map[string]toolconfig.OmoAgent
}

func (s *Service) GetOmoConfig() (OmoConfigView, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return OmoConfigView{}, fmt.Errorf("service: resolve home dir: %w", err)
	}
	config, err := toolconfig.ReadOmoConfig(homeDir)
	if err != nil {
		return OmoConfigView{}, err
	}
	validModels, err := toolconfig.ListProviderModels(homeDir)
	if err != nil {
		return OmoConfigView{}, err
	}
	knownPresets, err := toolconfig.ListOmoPresets(homeDir)
	if err != nil {
		return OmoConfigView{}, err
	}
	variants, err := toolconfig.ListProviderVariants(homeDir)
	if err != nil {
		return OmoConfigView{}, err
	}
	presetAgents, err := toolconfig.ListOmoPresetAgents(homeDir)
	if err != nil {
		return OmoConfigView{}, err
	}
	return OmoConfigView{
		Path:              config.Path,
		ActivePreset:      config.ActivePreset,
		Agents:            config.Agents,
		DisabledAgents:    config.DisabledAgents,
		KnownPresets:      knownPresets,
		ValidModels:       validModels,
		AvailableVariants: variants,
		PresetAgents:      presetAgents,
	}, nil
}

// OpencodeLiveState is the on-disk snapshot shown on the opencode card: the
// effective model pointer plus a compact OMO overview. It is read live on
// every call and intentionally bypasses the DB-tracked state so drift
// becomes a concrete comparison instead of an abstract badge.
type OpencodeLiveState struct {
	Model            string
	OmoConfigured    bool
	OmoActivePreset  string
	OmoAgentCount    int
	OmoDisabledCount int
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
	config, err := toolconfig.ReadOmoConfig(homeDir)
	if err != nil {
		if errors.Is(err, toolconfig.ErrConfigNotFound) {
			return state, nil
		}
		return OpencodeLiveState{}, err
	}
	state.OmoConfigured = true
	state.OmoActivePreset = config.ActivePreset
	state.OmoAgentCount = len(config.Agents)
	state.OmoDisabledCount = len(config.DisabledAgents)
	return state, nil
}

func (s *Service) ApplyOmoConfig(change toolconfig.OmoChange, allowDrift bool) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("service: resolve home dir: %w", err)
	}
	validModels, err := toolconfig.ListProviderModels(homeDir)
	if err != nil {
		return err
	}
	changeSet, err := toolconfig.PlanOmoChange(homeDir, change, validModels)
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

// ListToolBackups lists backups below the per-tool resource directories.
func (s *Service) ListToolBackups(tool string) ([]ToolBackupInfo, error) {
	toolID := toolconfig.Tool(tool)
	if _, err := adapterFor(toolID); err != nil {
		return nil, err
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("service: resolve home dir: %w", err)
	}
	root := toolBackupRoot(homeDir)
	entries := make([]ToolBackupInfo, 0)
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
		resource := toolconfig.Resource(filepath.ToSlash(filepath.Dir(rel)))
		if !resourceBelongsToTool(toolID, resource) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, ToolBackupInfo{Resource: resource, Path: path, ModTime: info.ModTime()})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("service: list %s tool backups: %w", tool, err)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Resource != entries[j].Resource {
			return entries[i].Resource < entries[j].Resource
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
	backupRoot := toolBackupRoot(homeDir)
	if err := validateBackupPath(backupRoot, toolconfig.Resource(resource), backupPath); err != nil {
		return err
	}
	after, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("service: read tool backup: %w", err)
	}
	targetPath, err := s.targetPathForResource(toolID, toolconfig.Resource(resource), homeDir, adapter)
	if err != nil {
		return err
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
			Before:     before,
			BeforeHash: beforeHash,
			After:      after,
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
		relayAddr := resolveAPIAddress(s.proxy.URL())
		if relayAddr == "" {
			return toolconfig.PresetPlaintext{}, fmt.Errorf("service: relay address is unavailable")
		}
		plain, err := toolconfig.BuildAutoapiPreset(p.Tool, p.Name, relayAddr, p.APIKeyID, p.Models)
		if err != nil {
			return toolconfig.PresetPlaintext{}, err
		}
		plain.BaseURL = toolconfig.AutoapiBaseURL(p.Tool, relayAddr)
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
		return path, nil
	case toolconfig.ResOpencodeOMO, toolconfig.ResOmoConfig:
		if path, ok := toolconfig.DetectOmoConfig(homeDir); ok {
			return path, nil
		}
	case toolconfig.ResCodexAuth:
		status := adapter.Detect(homeDir)
		if path := status.ExtraPaths["auth_json"]; path != "" {
			return path, nil
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
	return tool == toolconfig.ToolOpencode && resource == toolconfig.ResOmoConfig
}

func validateBackupPath(backupRoot string, resource toolconfig.Resource, backupPath string) error {
	resourceDir := filepath.Join(backupRoot, filepath.FromSlash(string(resource)))
	rootAbs, err := filepath.Abs(resourceDir)
	if err != nil {
		return fmt.Errorf("service: resolve backup directory: %w", err)
	}
	backupAbs, err := filepath.Abs(backupPath)
	if err != nil {
		return fmt.Errorf("service: resolve backup path: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, backupAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("service: backup path is outside resource directory")
	}
	info, err := os.Stat(backupAbs)
	if err != nil {
		return fmt.Errorf("service: stat backup path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("service: backup path is not a regular file")
	}
	return nil
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

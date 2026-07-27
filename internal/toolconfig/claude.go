package toolconfig

import (
	"encoding/json"
	"fmt"

	"github.com/tailscale/hujson"
)

// ClaudeAdapter manages the environment overrides and default model in
// Claude Code's settings.json. Only the named leaves are owned by Autoapi.
type ClaudeAdapter struct{}

func NewClaudeAdapter() Adapter { return ClaudeAdapter{} }

func (ClaudeAdapter) Tool() Tool { return ToolClaude }

func (ClaudeAdapter) Detect(homeDir string) ToolStatus {
	return detectClaude(homeDir)
}

func claudeDefaultModel(models []PresetModel) string {
	return presetDefaultModel(models)
}

func (ClaudeAdapter) Plan(p PresetPlaintext, homeDir string) (*ChangeSet, error) {
	if err := validatePreset(p); err != nil {
		return nil, err
	}
	homeDir = absoluteHomeDir(homeDir)
	configPath := DefaultConfigPath(ToolClaude, homeDir)
	resolvedPath, before, err := snapshotFile(configPath, homeDir)
	if err != nil {
		return nil, err
	}
	doc, err := parseJSONBytes(before)
	if err != nil {
		return nil, fmt.Errorf("parse Claude settings: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, fmt.Errorf("parse Claude settings: %w", err)
	}
	if err := requireUniqueKeys(root, "env", "model"); err != nil {
		return nil, err
	}
	env, envPresent, err := requireObjectMember(root, "env")
	if err != nil {
		return nil, err
	}
	if !envPresent {
		env = &hujson.Object{}
		if err := setObjectMember(root, "env", hujson.Value{Value: env}); err != nil {
			return nil, err
		}
	}
	if err := requireUniqueKeys(env, "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"); err != nil {
		return nil, err
	}

	if err := setClaudeString(env, "ANTHROPIC_BASE_URL", p.BaseURL); err != nil {
		return nil, err
	}
	if p.APIKey == "" {
		if err := removeObjectMember(env, "ANTHROPIC_AUTH_TOKEN"); err != nil {
			return nil, err
		}
	} else if err := setClaudeString(env, "ANTHROPIC_AUTH_TOKEN", p.APIKey); err != nil {
		return nil, err
	}
	if defaultModel := claudeDefaultModel(p.Models); defaultModel != "" {
		if err := setClaudeString(env, "ANTHROPIC_MODEL", defaultModel); err != nil {
			return nil, err
		}
		if err := setClaudeString(root, "model", defaultModel); err != nil {
			return nil, err
		}
	} else {
		// Claude's pointers are global. Unlike OpenCode's provider-scoped
		// pointer, a no-default preset leaves both existing pointers untouched.
	}

	change := changeForSnapshot(ResClaudeSettings, resolvedPath, false, before)
	change.After = doc.Pack()
	return &ChangeSet{Tool: ToolClaude, Changes: []FileChange{change}}, nil
}

func setClaudeString(object *hujson.Object, name, value string) error {
	literal, err := jsonValue(value)
	if err != nil {
		return err
	}
	return setObjectMember(object, name, literal)
}

func (ClaudeAdapter) ReadManaged(homeDir, providerID string) (ManagedSection, error) {
	root, env, present, err := loadClaudeManaged(homeDir, providerID)
	if err != nil {
		return ManagedSection{}, err
	}
	if !present {
		return ManagedSection{}, nil
	}

	section := ManagedSection{
		Present:    true,
		ProviderID: providerID,
		Model:      objectString(root, "model"),
		Fields:     map[string]string{},
	}
	if env != nil {
		section.BaseURL = objectString(env, "ANTHROPIC_BASE_URL")
		if value := objectString(env, "ANTHROPIC_BASE_URL"); value != "" {
			section.Fields["ANTHROPIC_BASE_URL"] = value
		}
		if value := objectString(env, "ANTHROPIC_AUTH_TOKEN"); value != "" {
			section.Fields["ANTHROPIC_AUTH_TOKEN"] = MaskSecret(value)
		}
		if value := objectString(env, "ANTHROPIC_MODEL"); value != "" {
			section.Fields["ANTHROPIC_MODEL"] = value
		}
	}
	return section, nil
}

// ReadManagedRaw returns plaintext credentials for backend reconciliation.
// NEVER expose this result through Wails bindings or logs.
func (ClaudeAdapter) ReadManagedRaw(homeDir, providerID string) (RawManagedSection, error) {
	root, env, present, err := loadClaudeManaged(homeDir, providerID)
	if err != nil {
		return RawManagedSection{}, err
	}
	if !present {
		return RawManagedSection{}, nil
	}
	model := objectString(root, "model")
	models := []PresetModel(nil)
	if model != "" {
		models = []PresetModel{{Name: model, Default: true}}
	}
	return RawManagedSection{
		Present:    true,
		ProviderID: providerID,
		BaseURL:    objectString(env, "ANTHROPIC_BASE_URL"),
		APIKey:     objectString(env, "ANTHROPIC_AUTH_TOKEN"),
		Model:      model,
		Models:     models,
	}, nil
}

func loadClaudeManaged(homeDir, providerID string) (*hujson.Object, *hujson.Object, bool, error) {
	if providerID == "" {
		return nil, nil, false, fmt.Errorf("%w: provider ID is required", ErrInvalidPreset)
	}
	homeDir = absoluteHomeDir(homeDir)
	path := DefaultConfigPath(ToolClaude, homeDir)
	_, data, err := snapshotFile(path, homeDir)
	if err != nil {
		return nil, nil, false, err
	}
	if data == nil {
		return nil, nil, false, nil
	}
	doc, err := parseJSONBytes(data)
	if err != nil {
		return nil, nil, false, fmt.Errorf("parse Claude settings: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, nil, false, fmt.Errorf("parse Claude settings: %w", err)
	}
	if err := requireUniqueKeys(root, "env", "model"); err != nil {
		return nil, nil, false, err
	}
	env, envPresent, err := requireObjectMember(root, "env")
	if err != nil {
		return nil, nil, false, err
	}
	if envPresent {
		if err := requireUniqueKeys(env, "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"); err != nil {
			return nil, nil, false, err
		}
	}
	if !envPresent && objectValue(root, "model") == nil {
		return root, nil, false, nil
	}
	return root, env, true, nil
}

func (ClaudeAdapter) ExportSnippet(p PresetPlaintext) (Snippet, error) {
	if err := validatePreset(p); err != nil {
		return Snippet{}, err
	}
	env := map[string]string{}
	if p.BaseURL != "" {
		env["ANTHROPIC_BASE_URL"] = p.BaseURL
	}
	if p.APIKey != "" {
		env["ANTHROPIC_AUTH_TOKEN"] = p.APIKey
	}
	fragment := map[string]any{"env": env}
	if defaultModel := claudeDefaultModel(p.Models); defaultModel != "" {
		env["ANTHROPIC_MODEL"] = defaultModel
		fragment["model"] = defaultModel
	}
	data, err := json.MarshalIndent(fragment, "", "  ")
	if err != nil {
		return Snippet{}, err
	}
	return Snippet{
		TargetPath: "~/.claude/settings.json",
		Format:     "json",
		Content:    string(data) + "\n",
		Notes:      "Paste this fragment into ~/.claude/settings.json under the top-level object.",
	}, nil
}

package toolconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/tailscale/hujson"
)

// omoSlimBuiltinAgents are the agent names OMO Slim ships with. They may be overridden
// per preset (presets.<preset>.<name>) or at the top level (agents.<name>);
// any other name under agents is a custom agent definition.
var omoSlimBuiltinAgents = map[string]bool{
	"orchestrator": true,
	"oracle":       true,
	"librarian":    true,
	"explorer":     true,
	"designer":     true,
	"fixer":        true,
	"observer":     true,
	"council":      true,
}

// OmoSlimAgent is the managed projection of one preset-level agent override.
// Empty string fields mean "no override" (the leaf is deleted on write);
// nil Skills/Mcps mean the leaf is left untouched, while a non-nil slice
// (including an empty one) replaces it.
type OmoSlimAgent struct {
	Model       string   `json:"model"`
	Variant     string   `json:"variant"`
	DisplayName string   `json:"displayName"`
	Skills      []string `json:"skills,omitempty"`
	Mcps        []string `json:"mcps,omitempty"`
}

// OmoSlimCustomAgent is a full custom agent definition from the top-level agents
// object. Only custom agents may carry prompt/orchestratorPrompt; built-in
// prompt overrides live in markdown files OMO Slim manages itself.
type OmoSlimCustomAgent struct {
	Model              string   `json:"model"`
	Variant            string   `json:"variant"`
	DisplayName        string   `json:"displayName"`
	Skills             []string `json:"skills,omitempty"`
	Mcps               []string `json:"mcps,omitempty"`
	Prompt             string   `json:"prompt"`
	OrchestratorPrompt string   `json:"orchestratorPrompt"`
}

// OmoSlimConfig is the managed projection of an oh-my-opencode-slim config.
type OmoSlimConfig struct {
	Path           string
	ActivePreset   string
	Agents         map[string]OmoSlimAgent
	CustomAgents   map[string]OmoSlimCustomAgent
	DisabledAgents []string
	DisabledSkills []string
	DisabledMcps   []string
}

// OmoSlimPresetOp is one ordered operation on the top-level presets object.
// Upsert creates a missing preset or patches the supplied managed agent leaves
// on an existing preset; rename moves the existing preset from Name to NewName;
// delete removes Name. Agents are supplied by the caller for upsert so the UI
// can duplicate a preset without exposing the raw document.
type OmoSlimPresetOp struct {
	Operation string
	Name      string
	NewName   string
	Agents    map[string]OmoSlimAgent
}

const (
	OmoSlimPresetUpsert = "upsert"
	OmoSlimPresetRename = "rename"
	OmoSlimPresetDelete = "delete"
)

// OmoSlimChange uses per-field leaf semantics: agents listed in Agents are
// upserted into the target preset (empty strings delete that leaf, nil
// Skills/Mcps leave it untouched), CustomAgents fully replaces the set of
// custom agent definitions when non-nil, and each Disabled* leaf is replaced
// only when non-nil. PresetOps are applied in list order before ActivePreset
// and the ordinary Agents patch.
type OmoSlimChange struct {
	ActivePreset   *string
	Agents         map[string]OmoSlimAgent
	CustomAgents   map[string]OmoSlimCustomAgent
	DisabledAgents []string
	DisabledSkills []string
	DisabledMcps   []string
	PresetOps      []OmoSlimPresetOp
}

// DetectOmoSlimConfig returns the preferred OMO Slim config path without creating it.
func DetectOmoSlimConfig(homeDir string) (string, bool) {
	dir := filepath.Join(absoluteHomeDir(homeDir), ".config", "opencode")
	jsonc := filepath.Join(dir, "oh-my-opencode-slim.jsonc")
	if pathExists(jsonc) {
		return jsonc, true
	}
	json := filepath.Join(dir, "oh-my-opencode-slim.json")
	if pathExists(json) {
		return json, true
	}
	return "", false
}

// ReadOmoSlimConfig reads the active preset, built-in and custom agent projections,
// and the disabled_agents leaf. Custom agents override same-named built-ins.
func ReadOmoSlimConfig(homeDir string) (OmoSlimConfig, error) {
	path, _, _, root, err := readOmoSlimDocument(homeDir)
	if err != nil {
		return OmoSlimConfig{}, err
	}
	config, err := readOmoSlimConfigRoot(root)
	if err != nil {
		return OmoSlimConfig{}, err
	}
	config.Path = path
	return config, nil
}

// ListOmoSlimPresets returns the sorted names of every preset declared in the OMO Slim
// config's presets object. It is used by the UI to offer a closed preset
// switcher (free-form preset names are rejected by PlanOmoSlimChange anyway).
func ListOmoSlimPresets(homeDir string) ([]string, error) {
	_, _, _, root, err := readOmoSlimDocument(homeDir)
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			// Match ListProviderModels: a missing config yields no choices,
			// not an error; the strict read path still fails closed.
			return nil, nil
		}
		return nil, err
	}
	presets, present, err := omoSlimObjectMember(root, "presets", true)
	if err != nil {
		return nil, err
	}
	if !present {
		// A config without presets is unusual (ReadOmoSlimConfig fails closed on
		// it) but the lister stays tolerant and simply offers no choices.
		return nil, nil
	}
	result := make([]string, 0, len(presets.Members))
	for _, member := range presets.Members {
		result = append(result, memberName(member))
	}
	sort.Strings(result)
	return result, nil
}

// ListOmoSlimPresetAgents returns the built-in agent projection of every preset
// declared in the OMO Slim config, keyed by preset name. It powers the preset
// switch preview in the UI. Custom agents (the top-level agents object) are
// not part of the projection because they exist independently of any preset.
func ListOmoSlimPresetAgents(homeDir string) (map[string]map[string]OmoSlimAgent, error) {
	_, _, _, root, err := readOmoSlimDocument(homeDir)
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			return nil, nil
		}
		return nil, err
	}
	presets, present, err := omoSlimObjectMember(root, "presets", true)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	result := make(map[string]map[string]OmoSlimAgent, len(presets.Members))
	for _, member := range presets.Members {
		name := memberName(member)
		object, ok := member.Value.Value.(*hujson.Object)
		if !ok {
			return nil, omoSlimShape("presets.%s is not an object", name)
		}
		agents, err := readOmoSlimAgents(object, "presets."+name)
		if err != nil {
			return nil, err
		}
		result[name] = agents
	}
	return result, nil
}

// PlanOmoSlimChange renders a hujson leaf patch for OMO Slim and a read-only check for
// the opencode provider file. It never writes either file.
func PlanOmoSlimChange(homeDir string, ch OmoSlimChange, validModels []string) (*ChangeSet, error) {
	if err := validateOmoSlimModels(ch.Agents, validModels); err != nil {
		return nil, err
	}
	if err := validateOmoSlimPresetOps(ch.PresetOps, validModels); err != nil {
		return nil, err
	}
	if ch.CustomAgents != nil {
		if err := validateOmoSlimCustomAgents(ch.CustomAgents, validModels); err != nil {
			return nil, err
		}
	}
	if ch.ActivePreset != nil {
		if err := validateOmoSlimPresetName(*ch.ActivePreset); err != nil {
			return nil, fmt.Errorf("active OMO Slim preset: %w", err)
		}
	}

	path, before, doc, root, err := readOmoSlimDocument(homeDir)
	if err != nil {
		return nil, err
	}
	currentPreset, err := omoSlimActivePreset(root)
	if err != nil {
		return nil, err
	}

	presets, present, err := omoSlimObjectMember(root, "presets", true)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, omoSlimShape("missing presets object")
	}
	if err := validateOmoSlimPresetMembers(presets); err != nil {
		return nil, err
	}

	activePreset := currentPreset
	for _, op := range ch.PresetOps {
		var err error
		activePreset, err = applyOmoSlimPresetOp(presets, activePreset, op)
		if err != nil {
			return nil, err
		}
	}
	targetPreset := activePreset
	if ch.ActivePreset != nil {
		targetPreset = *ch.ActivePreset
		target, targetPresent, err := omoSlimObjectMember(presets, targetPreset, true)
		if err != nil {
			return nil, fmt.Errorf("preset %q: %w", targetPreset, err)
		}
		if !targetPresent || target == nil {
			return nil, omoSlimShape("preset %q is missing", targetPreset)
		}
		if err := setOmoSlimString(root, "preset", targetPreset); err != nil {
			return nil, err
		}
	} else if activePreset != currentPreset {
		if err := setOmoSlimString(root, "preset", activePreset); err != nil {
			return nil, err
		}
	}

	if targetPreset != "" {
		target, present, err := omoSlimObjectMember(presets, targetPreset, true)
		if err != nil {
			return nil, fmt.Errorf("preset %q: %w", targetPreset, err)
		}
		if !present {
			return nil, omoSlimShape("preset %q is missing", targetPreset)
		}

		for _, name := range sortedOmoSlimAgentNames(ch.Agents) {
			if err := setOmoSlimAgent(target, name, ch.Agents[name]); err != nil {
				return nil, err
			}
		}
	}
	if len(ch.Agents) > 0 && targetPreset == "" {
		return nil, omoSlimShape("cannot patch agents without an active preset")
	}

	if ch.CustomAgents != nil {
		if err := writeOmoSlimCustomAgents(root, ch.CustomAgents); err != nil {
			return nil, err
		}
	}

	for _, leaf := range []struct {
		name  string
		value []string
	}{
		{"disabled_agents", ch.DisabledAgents},
		{"disabled_skills", ch.DisabledSkills},
		{"disabled_mcps", ch.DisabledMcps},
	} {
		if leaf.value == nil {
			continue
		}
		value, err := jsonValue(leaf.value)
		if err != nil {
			return nil, err
		}
		if err := setObjectMember(root, leaf.name, value); err != nil {
			return nil, err
		}
	}

	change := changeForSnapshot(ResOmoSlimConfig, path, false, before)
	change.After, err = packFormatted(doc)
	if err != nil {
		return nil, err
	}
	checks, err := omoSlimOpenCodeChecks(homeDir)
	if err != nil {
		return nil, err
	}
	return &ChangeSet{
		Tool:    ToolOpencode,
		Changes: []FileChange{change},
		Checks:  checks,
	}, nil
}

func readOmoSlimDocument(homeDir string) (string, []byte, hujson.Value, *hujson.Object, error) {
	homeDir = absoluteHomeDir(homeDir)
	path, ok := DetectOmoSlimConfig(homeDir)
	if !ok {
		return "", nil, hujson.Value{}, nil, fmt.Errorf("%w: OMO Slim config not found", ErrConfigNotFound)
	}
	resolved, before, err := snapshotFile(path, homeDir)
	if err != nil {
		return "", nil, hujson.Value{}, nil, err
	}
	if before == nil {
		return "", nil, hujson.Value{}, nil, fmt.Errorf("%s: %w", path, ErrConfigNotFound)
	}
	doc, err := parseJSONBytes(before)
	if err != nil {
		return "", nil, hujson.Value{}, nil, fmt.Errorf("parse OMO Slim config: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return "", nil, hujson.Value{}, nil, fmt.Errorf("parse OMO Slim config: %w", err)
	}
	if err := requireUniqueKeys(root, "preset", "presets", "agents", "disabled_agents", "disabled_skills", "disabled_mcps"); err != nil {
		return "", nil, hujson.Value{}, nil, err
	}
	if err := requireUniqueObjectMembers(root); err != nil {
		return "", nil, hujson.Value{}, nil, err
	}
	return resolved, before, doc, root, nil
}

func readOmoSlimConfigRoot(root *hujson.Object) (OmoSlimConfig, error) {
	activePreset, err := omoSlimActivePreset(root)
	if err != nil {
		return OmoSlimConfig{}, err
	}
	presets, present, err := omoSlimObjectMember(root, "presets", true)
	if err != nil {
		return OmoSlimConfig{}, err
	}
	if !present {
		return OmoSlimConfig{}, omoSlimShape("missing presets object")
	}
	builtIn := map[string]OmoSlimAgent{}
	if activePreset != "" {
		active, present, err := omoSlimObjectMember(presets, activePreset, true)
		if err != nil {
			return OmoSlimConfig{}, fmt.Errorf("preset %q: %w", activePreset, err)
		}
		if !present {
			return OmoSlimConfig{}, omoSlimShape("active preset %q is missing", activePreset)
		}
		builtIn, err = readOmoSlimAgents(active, "presets."+activePreset)
		if err != nil {
			return OmoSlimConfig{}, err
		}
	}
	agents := make(map[string]OmoSlimAgent, len(builtIn))
	for name, agent := range builtIn {
		agents[name] = agent
	}
	customAgents := map[string]OmoSlimCustomAgent{}
	if value := objectMemberValue(root, "agents"); value != nil {
		custom, _, err := omoSlimObjectMember(root, "agents", true)
		if err != nil {
			return OmoSlimConfig{}, err
		}
		customAgents, err = readOmoSlimCustomAgents(custom, "agents")
		if err != nil {
			return OmoSlimConfig{}, err
		}
		for name, customAgent := range customAgents {
			agents[name] = OmoSlimAgent{
				Model:       customAgent.Model,
				Variant:     customAgent.Variant,
				DisplayName: customAgent.DisplayName,
				Skills:      customAgent.Skills,
				Mcps:        customAgent.Mcps,
			}
		}
	}
	disabled, err := readOmoSlimStringArray(root, "disabled_agents")
	if err != nil {
		return OmoSlimConfig{}, err
	}
	disabledSkills, err := readOmoSlimStringArray(root, "disabled_skills")
	if err != nil {
		return OmoSlimConfig{}, err
	}
	disabledMcps, err := readOmoSlimStringArray(root, "disabled_mcps")
	if err != nil {
		return OmoSlimConfig{}, err
	}
	return OmoSlimConfig{
		ActivePreset:   activePreset,
		Agents:         agents,
		CustomAgents:   customAgents,
		DisabledAgents: disabled,
		DisabledSkills: disabledSkills,
		DisabledMcps:   disabledMcps,
	}, nil
}

func omoSlimActivePreset(root *hujson.Object) (string, error) {
	preset, present, err := omoSlimStringMember(root, "preset")
	if err != nil {
		return "", err
	}
	if !present {
		return "", omoSlimShape("missing active preset")
	}
	return preset, nil
}

func omoSlimObjectMember(object *hujson.Object, name string, required bool) (*hujson.Object, bool, error) {
	child, present, err := requireObjectMember(object, name)
	if err != nil {
		return nil, false, err
	}
	if !present {
		if required {
			return nil, false, nil
		}
		return nil, false, nil
	}
	if err := requireUniqueObjectMembers(child); err != nil {
		return nil, false, err
	}
	return child, true, nil
}

func omoSlimStringMember(object *hujson.Object, name string) (string, bool, error) {
	value := objectMemberValue(object, name)
	if value == nil {
		return "", false, nil
	}
	literal, ok := value.Value.(hujson.Literal)
	if !ok || literal.Kind() != hujson.Kind('"') {
		return "", true, omoSlimShape("JSON key %q is not a string", name)
	}
	return literal.String(), true, nil
}

// readOmoSlimAgentFields reads the shared agent override leaves from one agent
// object. Unknown leaves (temperature, options, permission, ...) are ignored
// but preserved by the leaf-level write path.
func readOmoSlimAgentFields(agent *hujson.Object, path string) (OmoSlimAgent, error) {
	if err := requireUniqueKeys(agent, "model", "variant", "displayName", "skills", "mcps", "prompt", "orchestratorPrompt"); err != nil {
		return OmoSlimAgent{}, err
	}
	model, _, err := omoSlimStringMember(agent, "model")
	if err != nil {
		return OmoSlimAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	variant, _, err := omoSlimStringMember(agent, "variant")
	if err != nil {
		return OmoSlimAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	displayName, _, err := omoSlimStringMember(agent, "displayName")
	if err != nil {
		return OmoSlimAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	skills, err := readOmoSlimStringArray(agent, "skills")
	if err != nil {
		return OmoSlimAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	mcps, err := readOmoSlimStringArray(agent, "mcps")
	if err != nil {
		return OmoSlimAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	return OmoSlimAgent{Model: model, Variant: variant, DisplayName: displayName, Skills: skills, Mcps: mcps}, nil
}

func readOmoSlimAgents(object *hujson.Object, path string) (map[string]OmoSlimAgent, error) {
	if object == nil {
		return map[string]OmoSlimAgent{}, nil
	}
	result := make(map[string]OmoSlimAgent, len(object.Members))
	for _, member := range object.Members {
		name := memberName(member)
		agent, ok := member.Value.Value.(*hujson.Object)
		if !ok {
			return nil, omoSlimShape("%s.%s is not an object", path, name)
		}
		fields, err := readOmoSlimAgentFields(agent, path+"."+name)
		if err != nil {
			return nil, err
		}
		result[name] = fields
	}
	return result, nil
}

// readOmoSlimCustomAgents reads the top-level agents object, splitting custom
// definitions (full fields) from built-in overrides (shared fields only;
// prompt/orchestratorPrompt are not supported for built-ins in JSON).
func readOmoSlimCustomAgents(object *hujson.Object, path string) (map[string]OmoSlimCustomAgent, error) {
	if object == nil {
		return map[string]OmoSlimCustomAgent{}, nil
	}
	result := make(map[string]OmoSlimCustomAgent, len(object.Members))
	for _, member := range object.Members {
		name := memberName(member)
		if omoSlimBuiltinAgents[name] {
			// Built-in overrides under agents are not managed here; the
			// write path preserves them untouched.
			continue
		}
		agent, ok := member.Value.Value.(*hujson.Object)
		if !ok {
			return nil, omoSlimShape("%s.%s is not an object", path, name)
		}
		fields, err := readOmoSlimAgentFields(agent, path+"."+name)
		if err != nil {
			return nil, err
		}
		prompt, _, err := omoSlimStringMember(agent, "prompt")
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", path, name, err)
		}
		orchestratorPrompt, _, err := omoSlimStringMember(agent, "orchestratorPrompt")
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", path, name, err)
		}
		result[name] = OmoSlimCustomAgent{
			Model:              fields.Model,
			Variant:            fields.Variant,
			DisplayName:        fields.DisplayName,
			Skills:             fields.Skills,
			Mcps:               fields.Mcps,
			Prompt:             prompt,
			OrchestratorPrompt: orchestratorPrompt,
		}
	}
	return result, nil
}

// readOmoSlimStringArray reads a string-array leaf from an object. A missing leaf
// yields nil; a present non-array or non-string element fails closed.
func readOmoSlimStringArray(object *hujson.Object, name string) ([]string, error) {
	value := objectMemberValue(object, name)
	if value == nil {
		return nil, nil
	}
	array, ok := value.Value.(*hujson.Array)
	if !ok {
		return nil, omoSlimShape("%s is not an array", name)
	}
	result := make([]string, 0, len(array.Elements))
	for _, element := range array.Elements {
		literal, ok := element.Value.(hujson.Literal)
		if !ok || literal.Kind() != hujson.Kind('"') {
			return nil, omoSlimShape("%s contains a non-string value", name)
		}
		result = append(result, literal.String())
	}
	return result, nil
}

func validateOmoSlimModels(agents map[string]OmoSlimAgent, validModels []string) error {
	valid := make(map[string]struct{}, len(validModels))
	for _, model := range validModels {
		valid[model] = struct{}{}
	}
	missing := make([]string, 0)
	for _, name := range sortedOmoSlimAgentNames(agents) {
		model := agents[name].Model
		if model == "" {
			continue
		}
		if _, ok := valid[model]; !ok {
			missing = append(missing, fmt.Sprintf("%s=%s", name, model))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing OMO Slim model references: %s", ErrInvalidPreset, strings.Join(missing, ", "))
	}
	return nil
}

func validateOmoSlimPresetName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: preset name is empty", ErrInvalidPreset)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: preset name contains a control character", ErrInvalidPreset)
		}
	}
	return nil
}

func validateOmoSlimPresetOps(ops []OmoSlimPresetOp, validModels []string) error {
	for index, op := range ops {
		switch op.Operation {
		case OmoSlimPresetUpsert:
			if err := validateOmoSlimPresetName(op.Name); err != nil {
				return fmt.Errorf("preset operation %d: %w", index, err)
			}
			if err := validateOmoSlimModels(op.Agents, validModels); err != nil {
				return fmt.Errorf("preset operation %d: %w", index, err)
			}
		case OmoSlimPresetRename:
			if err := validateOmoSlimPresetName(op.Name); err != nil {
				return fmt.Errorf("preset operation %d: rename source: %w", index, err)
			}
			if err := validateOmoSlimPresetName(op.NewName); err != nil {
				return fmt.Errorf("preset operation %d: rename target: %w", index, err)
			}
		case OmoSlimPresetDelete:
			if err := validateOmoSlimPresetName(op.Name); err != nil {
				return fmt.Errorf("preset operation %d: %w", index, err)
			}
		default:
			return fmt.Errorf("%w: unknown OMO Slim preset operation %q", ErrInvalidPreset, op.Operation)
		}
	}
	return nil
}

func validateOmoSlimPresetMembers(presets *hujson.Object) error {
	if err := requireUniqueObjectMembers(presets); err != nil {
		return err
	}
	for _, member := range presets.Members {
		name := memberName(member)
		if err := validateOmoSlimPresetName(name); err != nil {
			return err
		}
		preset, ok := member.Value.Value.(*hujson.Object)
		if !ok {
			return omoSlimShape("presets.%s is not an object", name)
		}
		if err := requireUniqueObjectMembers(preset); err != nil {
			return err
		}
		for _, agentMember := range preset.Members {
			agent, ok := agentMember.Value.Value.(*hujson.Object)
			if !ok {
				return omoSlimShape("presets.%s.%s is not an object", name, memberName(agentMember))
			}
			if err := requireUniqueObjectMembers(agent); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyOmoSlimPresetOp(presets *hujson.Object, activePreset string, op OmoSlimPresetOp) (string, error) {
	switch op.Operation {
	case OmoSlimPresetUpsert:
		preset, present, err := omoSlimObjectMember(presets, op.Name, true)
		if err != nil {
			return activePreset, fmt.Errorf("preset %q: %w", op.Name, err)
		}
		if !present {
			preset = &hujson.Object{}
			if err := setObjectMember(presets, op.Name, hujson.Value{Value: preset}); err != nil {
				return activePreset, err
			}
		}
		for _, name := range sortedOmoSlimAgentNames(op.Agents) {
			if err := setOmoSlimAgent(preset, name, op.Agents[name]); err != nil {
				return activePreset, fmt.Errorf("preset %q: %w", op.Name, err)
			}
		}
		return activePreset, nil
	case OmoSlimPresetRename:
		sourceIndex, err := findUniqueObjectMember(presets, op.Name)
		if err != nil {
			return activePreset, err
		}
		if sourceIndex < 0 {
			return activePreset, fmt.Errorf("%w: preset %q does not exist", ErrInvalidPreset, op.Name)
		}
		targetIndex, err := findUniqueObjectMember(presets, op.NewName)
		if err != nil {
			return activePreset, err
		}
		if targetIndex >= 0 {
			return activePreset, fmt.Errorf("%w: preset %q already exists", ErrConflict, op.NewName)
		}
		// Rename the key in place so the preset's value, comments, and ordering
		// stay byte-for-byte equivalent apart from the key text.
		presets.Members[sourceIndex].Name.Value = hujson.String(op.NewName)
		if activePreset == op.Name {
			return op.NewName, nil
		}
		return activePreset, nil
	case OmoSlimPresetDelete:
		if _, present, err := omoSlimObjectMember(presets, op.Name, true); err != nil {
			return activePreset, err
		} else if !present {
			return activePreset, fmt.Errorf("%w: preset %q does not exist", ErrInvalidPreset, op.Name)
		}
		if err := removeObjectMember(presets, op.Name); err != nil {
			return activePreset, err
		}
		if activePreset == op.Name {
			return "", nil
		}
		return activePreset, nil
	default:
		return activePreset, fmt.Errorf("%w: unknown OMO Slim preset operation %q", ErrInvalidPreset, op.Operation)
	}
}

func sortedOmoSlimAgentNames(agents map[string]OmoSlimAgent) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func setOmoSlimString(object *hujson.Object, name, value string) error {
	literal, err := jsonValue(value)
	if err != nil {
		return err
	}
	return setObjectMember(object, name, literal)
}

// setOmoSlimAgent upserts one preset-level agent override. Empty string fields
// delete the corresponding leaf ("no override"); nil Skills/Mcps leave that
// leaf untouched, a non-nil slice replaces it. Unknown leaves (temperature,
// options, permission, prompt, ...) are preserved.
func setOmoSlimAgent(parent *hujson.Object, name string, agent OmoSlimAgent) error {
	agentObject, present, err := requireObjectMember(parent, name)
	if err != nil {
		return fmt.Errorf("agent %q: %w", name, err)
	}
	if !present {
		agentObject = &hujson.Object{}
		if err := setObjectMember(parent, name, hujson.Value{Value: agentObject}); err != nil {
			return err
		}
	}
	if err := requireUniqueKeys(agentObject, "model", "variant", "displayName", "skills", "mcps"); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"model", agent.Model},
		{"variant", agent.Variant},
		{"displayName", agent.DisplayName},
	} {
		if field.value == "" {
			deleteObjectMember(agentObject, field.name)
			continue
		}
		if err := setOmoSlimString(agentObject, field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name  string
		value []string
	}{
		{"skills", agent.Skills},
		{"mcps", agent.Mcps},
	} {
		if field.value == nil {
			continue
		}
		array, err := jsonValue(field.value)
		if err != nil {
			return err
		}
		if err := setObjectMember(agentObject, field.name, array); err != nil {
			return err
		}
	}
	return nil
}

// writeOmoSlimCustomAgents replaces the set of custom agent definitions under the
// top-level agents object. Built-in overrides already present are preserved
// untouched; every other existing entry is removed first.
func writeOmoSlimCustomAgents(root *hujson.Object, agents map[string]OmoSlimCustomAgent) error {
	var custom *hujson.Object
	if value := objectMemberValue(root, "agents"); value != nil {
		var err error
		custom, _, err = omoSlimObjectMember(root, "agents", true)
		if err != nil {
			return err
		}
		stale := make([]string, 0, len(custom.Members))
		for _, member := range custom.Members {
			if name := memberName(member); !omoSlimBuiltinAgents[name] {
				stale = append(stale, name)
			}
		}
		for _, name := range stale {
			deleteObjectMember(custom, name)
		}
	} else if len(agents) > 0 {
		custom = &hujson.Object{}
		if err := setObjectMember(root, "agents", hujson.Value{Value: custom}); err != nil {
			return err
		}
	}
	if custom == nil {
		return nil
	}
	for _, name := range sortedOmoSlimCustomAgentNames(agents) {
		agent := agents[name]
		agentObject := &hujson.Object{}
		if err := setObjectMember(custom, name, hujson.Value{Value: agentObject}); err != nil {
			return err
		}
		for _, field := range []struct {
			name  string
			value string
		}{
			{"model", agent.Model},
			{"variant", agent.Variant},
			{"displayName", agent.DisplayName},
			{"prompt", agent.Prompt},
			{"orchestratorPrompt", agent.OrchestratorPrompt},
		} {
			if field.value == "" {
				continue
			}
			if err := setOmoSlimString(agentObject, field.name, field.value); err != nil {
				return err
			}
		}
		for _, field := range []struct {
			name  string
			value []string
		}{
			{"skills", agent.Skills},
			{"mcps", agent.Mcps},
		} {
			if field.value == nil {
				continue
			}
			array, err := jsonValue(field.value)
			if err != nil {
				return err
			}
			if err := setObjectMember(agentObject, field.name, array); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateOmoSlimCustomAgents enforces the schema rules for custom agent
// definitions: non-empty non-built-in names, known models, display names that
// are unique and collide with neither built-in nor custom agent names, and an
// orchestratorPrompt that begins with the agent's own @mention.
func validateOmoSlimCustomAgents(agents map[string]OmoSlimCustomAgent, validModels []string) error {
	valid := make(map[string]struct{}, len(validModels))
	for _, model := range validModels {
		valid[model] = struct{}{}
	}
	displayNames := make(map[string]string, len(agents))
	for _, name := range sortedOmoSlimCustomAgentNames(agents) {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: custom agent name is empty", ErrInvalidPreset)
		}
		if omoSlimBuiltinAgents[name] {
			return fmt.Errorf("%w: %q is a built-in agent and cannot be redefined", ErrInvalidPreset, name)
		}
		agent := agents[name]
		if agent.Model != "" {
			if _, ok := valid[agent.Model]; !ok {
				return fmt.Errorf("%w: missing OMO Slim model references: %s=%s", ErrInvalidPreset, name, agent.Model)
			}
		}
		if agent.DisplayName != "" {
			if omoSlimBuiltinAgents[agent.DisplayName] {
				return fmt.Errorf("%w: display name %q collides with a built-in agent", ErrInvalidPreset, agent.DisplayName)
			}
			if _, exists := agents[agent.DisplayName]; exists {
				return fmt.Errorf("%w: display name %q collides with custom agent %q", ErrInvalidPreset, agent.DisplayName, agent.DisplayName)
			}
			if other, taken := displayNames[agent.DisplayName]; taken {
				return fmt.Errorf("%w: display name %q used by both %q and %q", ErrInvalidPreset, agent.DisplayName, other, name)
			}
			displayNames[agent.DisplayName] = name
		}
		if agent.OrchestratorPrompt != "" && !strings.HasPrefix(agent.OrchestratorPrompt, "@"+name) {
			return fmt.Errorf("%w: orchestratorPrompt for %q must start with @%s", ErrInvalidPreset, name, name)
		}
	}
	return nil
}

func sortedOmoSlimCustomAgentNames(agents map[string]OmoSlimCustomAgent) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ListKnownSkills enumerates installed OpenCode skills from the two standard
// skill directories (~/.config/opencode/skills and ~/.agents/skills), giving
// the UI closed-list candidates for agent skills arrays. Missing directories
// are tolerated.
func ListKnownSkills(homeDir string) ([]string, error) {
	homeDir = absoluteHomeDir(homeDir)
	seen := map[string]bool{}
	for _, rel := range []string{
		filepath.Join(".config", "opencode", "skills"),
		filepath.Join(".agents", "skills"),
	} {
		entries, err := os.ReadDir(filepath.Join(homeDir, rel))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				seen[entry.Name()] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func omoSlimOpenCodeChecks(homeDir string) ([]FileCheck, error) {
	path, _ := ResolveConfigPath(ToolOpencode, homeDir)
	hash, err := HashFile(path)
	if err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, nil
	}
	return []FileCheck{{
		Resource:     ResOpencodeConfig,
		Path:         path,
		ExpectedHash: hash,
	}}, nil
}

func omoSlimShape(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrUnsafeShape)...)
}

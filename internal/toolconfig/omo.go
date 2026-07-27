package toolconfig

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

// OmoAgent is the managed projection of one OMO agent.
type OmoAgent struct {
	Model   string
	Variant string
}

// OmoConfig is the managed projection of an oh-my-opencode-slim config.
type OmoConfig struct {
	Path           string
	ActivePreset   string
	Agents         map[string]OmoAgent
	DisabledAgents []string
}

// OmoChange uses set semantics for agent leaves and whole-leaf replacement for
// disabled_agents. A nil DisabledAgents means that leaf is left untouched.
type OmoChange struct {
	ActivePreset   *string
	Agents         map[string]OmoAgent
	DisabledAgents []string
}

// DetectOmoConfig returns the preferred OMO config path without creating it.
func DetectOmoConfig(homeDir string) (string, bool) {
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

// ReadOmoConfig reads the active preset, built-in and custom agent projections,
// and the disabled_agents leaf. Custom agents override same-named built-ins.
func ReadOmoConfig(homeDir string) (OmoConfig, error) {
	path, _, _, root, err := readOmoDocument(homeDir)
	if err != nil {
		return OmoConfig{}, err
	}
	config, err := readOmoConfigRoot(root)
	if err != nil {
		return OmoConfig{}, err
	}
	config.Path = path
	return config, nil
}

// ListOmoPresets returns the sorted names of every preset declared in the OMO
// config's presets object. It is used by the UI to offer a closed preset
// switcher (free-form preset names are rejected by PlanOmoChange anyway).
func ListOmoPresets(homeDir string) ([]string, error) {
	_, _, _, root, err := readOmoDocument(homeDir)
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			// Match ListProviderModels: a missing config yields no choices,
			// not an error; the strict read path still fails closed.
			return nil, nil
		}
		return nil, err
	}
	presets, present, err := omoObjectMember(root, "presets", true)
	if err != nil {
		return nil, err
	}
	if !present {
		// A config without presets is unusual (ReadOmoConfig fails closed on
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

// ListOmoPresetAgents returns the built-in agent projection of every preset
// declared in the OMO config, keyed by preset name. It powers the preset
// switch preview in the UI. Custom agents (the top-level agents object) are
// not part of the projection because they exist independently of any preset.
func ListOmoPresetAgents(homeDir string) (map[string]map[string]OmoAgent, error) {
	_, _, _, root, err := readOmoDocument(homeDir)
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			return nil, nil
		}
		return nil, err
	}
	presets, present, err := omoObjectMember(root, "presets", true)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	result := make(map[string]map[string]OmoAgent, len(presets.Members))
	for _, member := range presets.Members {
		name := memberName(member)
		object, ok := member.Value.Value.(*hujson.Object)
		if !ok {
			return nil, omoShape("presets.%s is not an object", name)
		}
		agents, err := readOmoAgents(object, "presets."+name)
		if err != nil {
			return nil, err
		}
		result[name] = agents
	}
	return result, nil
}

// PlanOmoChange renders a hujson leaf patch for OMO and a read-only check for
// the opencode provider file. It never writes either file.
func PlanOmoChange(homeDir string, ch OmoChange, validModels []string) (*ChangeSet, error) {
	if err := validateOmoModels(ch.Agents, validModels); err != nil {
		return nil, err
	}
	if ch.ActivePreset != nil && strings.TrimSpace(*ch.ActivePreset) == "" {
		return nil, fmt.Errorf("%w: active OMO preset is empty", ErrInvalidPreset)
	}

	path, before, doc, root, err := readOmoDocument(homeDir)
	if err != nil {
		return nil, err
	}
	currentPreset, err := omoActivePreset(root)
	if err != nil {
		return nil, err
	}
	targetPreset := currentPreset
	if ch.ActivePreset != nil {
		targetPreset = *ch.ActivePreset
		if err := setOmoString(root, "preset", targetPreset); err != nil {
			return nil, err
		}
	}

	presets, present, err := omoObjectMember(root, "presets", true)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, omoShape("missing presets object")
	}
	target, present, err := omoObjectMember(presets, targetPreset, true)
	if err != nil {
		return nil, fmt.Errorf("preset %q: %w", targetPreset, err)
	}
	if !present {
		return nil, omoShape("preset %q is missing", targetPreset)
	}

	var custom *hujson.Object
	if value := objectMemberValue(root, "agents"); value != nil {
		custom, _, err = omoObjectMember(root, "agents", true)
		if err != nil {
			return nil, err
		}
	} else if len(ch.Agents) > 0 {
		custom = &hujson.Object{}
		if err := setObjectMember(root, "agents", hujson.Value{Value: custom}); err != nil {
			return nil, err
		}
	}

	for _, name := range sortedOmoAgentNames(ch.Agents) {
		agent := ch.Agents[name]
		parent := target
		if custom != nil && objectMemberValue(custom, name) != nil {
			parent = custom
		} else if objectMemberValue(target, name) == nil {
			parent = custom
			if parent == nil {
				custom = &hujson.Object{}
				if err := setObjectMember(root, "agents", hujson.Value{Value: custom}); err != nil {
					return nil, err
				}
				parent = custom
			}
		}
		if err := setOmoAgent(parent, name, agent); err != nil {
			return nil, err
		}
	}

	if ch.DisabledAgents != nil {
		value, err := jsonValue(ch.DisabledAgents)
		if err != nil {
			return nil, err
		}
		if err := setObjectMember(root, "disabled_agents", value); err != nil {
			return nil, err
		}
	}

	change := changeForSnapshot(ResOmoConfig, path, false, before)
	change.After = doc.Pack()
	checks, err := omoOpenCodeChecks(homeDir)
	if err != nil {
		return nil, err
	}
	return &ChangeSet{
		Tool:    ToolOpencode,
		Changes: []FileChange{change},
		Checks:  checks,
	}, nil
}

func readOmoDocument(homeDir string) (string, []byte, hujson.Value, *hujson.Object, error) {
	homeDir = absoluteHomeDir(homeDir)
	path, ok := DetectOmoConfig(homeDir)
	if !ok {
		return "", nil, hujson.Value{}, nil, fmt.Errorf("%w: OMO config not found", ErrConfigNotFound)
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
		return "", nil, hujson.Value{}, nil, fmt.Errorf("parse OMO config: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return "", nil, hujson.Value{}, nil, fmt.Errorf("parse OMO config: %w", err)
	}
	if err := requireUniqueKeys(root, "preset", "presets", "agents", "disabled_agents"); err != nil {
		return "", nil, hujson.Value{}, nil, err
	}
	return resolved, before, doc, root, nil
}

func readOmoConfigRoot(root *hujson.Object) (OmoConfig, error) {
	activePreset, err := omoActivePreset(root)
	if err != nil {
		return OmoConfig{}, err
	}
	presets, present, err := omoObjectMember(root, "presets", true)
	if err != nil {
		return OmoConfig{}, err
	}
	if !present {
		return OmoConfig{}, omoShape("missing presets object")
	}
	active, present, err := omoObjectMember(presets, activePreset, true)
	if err != nil {
		return OmoConfig{}, fmt.Errorf("preset %q: %w", activePreset, err)
	}
	if !present {
		return OmoConfig{}, omoShape("active preset %q is missing", activePreset)
	}
	builtIn, err := readOmoAgents(active, "presets."+activePreset)
	if err != nil {
		return OmoConfig{}, err
	}
	agents := make(map[string]OmoAgent, len(builtIn))
	for name, agent := range builtIn {
		agents[name] = agent
	}
	if value := objectMemberValue(root, "agents"); value != nil {
		custom, _, err := omoObjectMember(root, "agents", true)
		if err != nil {
			return OmoConfig{}, err
		}
		customAgents, err := readOmoAgents(custom, "agents")
		if err != nil {
			return OmoConfig{}, err
		}
		for name, agent := range customAgents {
			agents[name] = agent
		}
	}
	disabled, err := readOmoDisabledAgents(root)
	if err != nil {
		return OmoConfig{}, err
	}
	return OmoConfig{
		ActivePreset:   activePreset,
		Agents:         agents,
		DisabledAgents: disabled,
	}, nil
}

func omoActivePreset(root *hujson.Object) (string, error) {
	preset, present, err := omoStringMember(root, "preset")
	if err != nil {
		return "", err
	}
	if !present || strings.TrimSpace(preset) == "" {
		return "", omoShape("missing active preset")
	}
	return preset, nil
}

func omoObjectMember(object *hujson.Object, name string, required bool) (*hujson.Object, bool, error) {
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

func omoStringMember(object *hujson.Object, name string) (string, bool, error) {
	value := objectMemberValue(object, name)
	if value == nil {
		return "", false, nil
	}
	literal, ok := value.Value.(hujson.Literal)
	if !ok || literal.Kind() != hujson.Kind('"') {
		return "", true, omoShape("JSON key %q is not a string", name)
	}
	return literal.String(), true, nil
}

func readOmoAgents(object *hujson.Object, path string) (map[string]OmoAgent, error) {
	if object == nil {
		return map[string]OmoAgent{}, nil
	}
	result := make(map[string]OmoAgent, len(object.Members))
	for _, member := range object.Members {
		name := memberName(member)
		agent, ok := member.Value.Value.(*hujson.Object)
		if !ok {
			return nil, omoShape("%s.%s is not an object", path, name)
		}
		if err := requireUniqueKeys(agent, "model", "variant"); err != nil {
			return nil, err
		}
		model, _, err := omoStringMember(agent, "model")
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", path, name, err)
		}
		variant, _, err := omoStringMember(agent, "variant")
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", path, name, err)
		}
		result[name] = OmoAgent{Model: model, Variant: variant}
	}
	return result, nil
}

func readOmoDisabledAgents(root *hujson.Object) ([]string, error) {
	value := objectMemberValue(root, "disabled_agents")
	if value == nil {
		return nil, nil
	}
	array, ok := value.Value.(*hujson.Array)
	if !ok {
		return nil, omoShape("disabled_agents is not an array")
	}
	result := make([]string, 0, len(array.Elements))
	for _, element := range array.Elements {
		literal, ok := element.Value.(hujson.Literal)
		if !ok || literal.Kind() != hujson.Kind('"') {
			return nil, omoShape("disabled_agents contains a non-string value")
		}
		result = append(result, literal.String())
	}
	return result, nil
}

func validateOmoModels(agents map[string]OmoAgent, validModels []string) error {
	valid := make(map[string]struct{}, len(validModels))
	for _, model := range validModels {
		valid[model] = struct{}{}
	}
	missing := make([]string, 0)
	for _, name := range sortedOmoAgentNames(agents) {
		model := agents[name].Model
		if model == "" {
			continue
		}
		if _, ok := valid[model]; !ok {
			missing = append(missing, fmt.Sprintf("%s=%s", name, model))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing OMO model references: %s", ErrInvalidPreset, strings.Join(missing, ", "))
	}
	return nil
}

func sortedOmoAgentNames(agents map[string]OmoAgent) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func setOmoString(object *hujson.Object, name, value string) error {
	literal, err := jsonValue(value)
	if err != nil {
		return err
	}
	return setObjectMember(object, name, literal)
}

func setOmoAgent(parent *hujson.Object, name string, agent OmoAgent) error {
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
	if err := requireUniqueKeys(agentObject, "model", "variant"); err != nil {
		return err
	}
	if err := setOmoString(agentObject, "model", agent.Model); err != nil {
		return err
	}
	return setOmoString(agentObject, "variant", agent.Variant)
}

func omoOpenCodeChecks(homeDir string) ([]FileCheck, error) {
	path := DefaultConfigPath(ToolOpencode, absoluteHomeDir(homeDir))
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

func omoShape(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrUnsafeShape)...)
}

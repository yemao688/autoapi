package toolconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

// omoBuiltinAgents are the agent names OMO ships with. They may be overridden
// per preset (presets.<preset>.<name>) or at the top level (agents.<name>);
// any other name under agents is a custom agent definition.
var omoBuiltinAgents = map[string]bool{
	"orchestrator": true,
	"oracle":       true,
	"librarian":    true,
	"explorer":     true,
	"designer":     true,
	"fixer":        true,
	"observer":     true,
	"council":      true,
}

// OmoAgent is the managed projection of one preset-level agent override.
// Empty string fields mean "no override" (the leaf is deleted on write);
// nil Skills/Mcps mean the leaf is left untouched, while a non-nil slice
// (including an empty one) replaces it.
type OmoAgent struct {
	Model       string   `json:"model"`
	Variant     string   `json:"variant"`
	DisplayName string   `json:"displayName"`
	Skills      []string `json:"skills,omitempty"`
	Mcps        []string `json:"mcps,omitempty"`
}

// OmoCustomAgent is a full custom agent definition from the top-level agents
// object. Only custom agents may carry prompt/orchestratorPrompt; built-in
// prompt overrides live in markdown files OMO manages itself.
type OmoCustomAgent struct {
	Model              string   `json:"model"`
	Variant            string   `json:"variant"`
	DisplayName        string   `json:"displayName"`
	Skills             []string `json:"skills,omitempty"`
	Mcps               []string `json:"mcps,omitempty"`
	Prompt             string   `json:"prompt"`
	OrchestratorPrompt string   `json:"orchestratorPrompt"`
}

// OmoConfig is the managed projection of an oh-my-opencode-slim config.
type OmoConfig struct {
	Path           string
	ActivePreset   string
	Agents         map[string]OmoAgent
	CustomAgents   map[string]OmoCustomAgent
	DisabledAgents []string
	DisabledSkills []string
	DisabledMcps   []string
}

// OmoChange uses per-field leaf semantics: agents listed in Agents are
// upserted into the target preset (empty strings delete that leaf, nil
// Skills/Mcps leave it untouched), CustomAgents fully replaces the set of
// custom agent definitions when non-nil, and each Disabled* leaf is replaced
// only when non-nil.
type OmoChange struct {
	ActivePreset   *string
	Agents         map[string]OmoAgent
	CustomAgents   map[string]OmoCustomAgent
	DisabledAgents []string
	DisabledSkills []string
	DisabledMcps   []string
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
	if ch.CustomAgents != nil {
		if err := validateOmoCustomAgents(ch.CustomAgents, validModels); err != nil {
			return nil, err
		}
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

	for _, name := range sortedOmoAgentNames(ch.Agents) {
		if err := setOmoAgent(target, name, ch.Agents[name]); err != nil {
			return nil, err
		}
	}

	if ch.CustomAgents != nil {
		if err := writeOmoCustomAgents(root, ch.CustomAgents); err != nil {
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
	if err := requireUniqueKeys(root, "preset", "presets", "agents", "disabled_agents", "disabled_skills", "disabled_mcps"); err != nil {
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
	customAgents := map[string]OmoCustomAgent{}
	if value := objectMemberValue(root, "agents"); value != nil {
		custom, _, err := omoObjectMember(root, "agents", true)
		if err != nil {
			return OmoConfig{}, err
		}
		customAgents, err = readOmoCustomAgents(custom, "agents")
		if err != nil {
			return OmoConfig{}, err
		}
		for name, customAgent := range customAgents {
			agents[name] = OmoAgent{
				Model:       customAgent.Model,
				Variant:     customAgent.Variant,
				DisplayName: customAgent.DisplayName,
				Skills:      customAgent.Skills,
				Mcps:        customAgent.Mcps,
			}
		}
	}
	disabled, err := readOmoStringArray(root, "disabled_agents")
	if err != nil {
		return OmoConfig{}, err
	}
	disabledSkills, err := readOmoStringArray(root, "disabled_skills")
	if err != nil {
		return OmoConfig{}, err
	}
	disabledMcps, err := readOmoStringArray(root, "disabled_mcps")
	if err != nil {
		return OmoConfig{}, err
	}
	return OmoConfig{
		ActivePreset:   activePreset,
		Agents:         agents,
		CustomAgents:   customAgents,
		DisabledAgents: disabled,
		DisabledSkills: disabledSkills,
		DisabledMcps:   disabledMcps,
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

// readOmoAgentFields reads the shared agent override leaves from one agent
// object. Unknown leaves (temperature, options, permission, ...) are ignored
// but preserved by the leaf-level write path.
func readOmoAgentFields(agent *hujson.Object, path string) (OmoAgent, error) {
	if err := requireUniqueKeys(agent, "model", "variant", "displayName", "skills", "mcps", "prompt", "orchestratorPrompt"); err != nil {
		return OmoAgent{}, err
	}
	model, _, err := omoStringMember(agent, "model")
	if err != nil {
		return OmoAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	variant, _, err := omoStringMember(agent, "variant")
	if err != nil {
		return OmoAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	displayName, _, err := omoStringMember(agent, "displayName")
	if err != nil {
		return OmoAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	skills, err := readOmoStringArray(agent, "skills")
	if err != nil {
		return OmoAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	mcps, err := readOmoStringArray(agent, "mcps")
	if err != nil {
		return OmoAgent{}, fmt.Errorf("%s: %w", path, err)
	}
	return OmoAgent{Model: model, Variant: variant, DisplayName: displayName, Skills: skills, Mcps: mcps}, nil
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
		fields, err := readOmoAgentFields(agent, path+"."+name)
		if err != nil {
			return nil, err
		}
		result[name] = fields
	}
	return result, nil
}

// readOmoCustomAgents reads the top-level agents object, splitting custom
// definitions (full fields) from built-in overrides (shared fields only;
// prompt/orchestratorPrompt are not supported for built-ins in JSON).
func readOmoCustomAgents(object *hujson.Object, path string) (map[string]OmoCustomAgent, error) {
	if object == nil {
		return map[string]OmoCustomAgent{}, nil
	}
	result := make(map[string]OmoCustomAgent, len(object.Members))
	for _, member := range object.Members {
		name := memberName(member)
		if omoBuiltinAgents[name] {
			// Built-in overrides under agents are not managed here; the
			// write path preserves them untouched.
			continue
		}
		agent, ok := member.Value.Value.(*hujson.Object)
		if !ok {
			return nil, omoShape("%s.%s is not an object", path, name)
		}
		fields, err := readOmoAgentFields(agent, path+"."+name)
		if err != nil {
			return nil, err
		}
		prompt, _, err := omoStringMember(agent, "prompt")
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", path, name, err)
		}
		orchestratorPrompt, _, err := omoStringMember(agent, "orchestratorPrompt")
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", path, name, err)
		}
		result[name] = OmoCustomAgent{
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

// readOmoStringArray reads a string-array leaf from an object. A missing leaf
// yields nil; a present non-array or non-string element fails closed.
func readOmoStringArray(object *hujson.Object, name string) ([]string, error) {
	value := objectMemberValue(object, name)
	if value == nil {
		return nil, nil
	}
	array, ok := value.Value.(*hujson.Array)
	if !ok {
		return nil, omoShape("%s is not an array", name)
	}
	result := make([]string, 0, len(array.Elements))
	for _, element := range array.Elements {
		literal, ok := element.Value.(hujson.Literal)
		if !ok || literal.Kind() != hujson.Kind('"') {
			return nil, omoShape("%s contains a non-string value", name)
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

// setOmoAgent upserts one preset-level agent override. Empty string fields
// delete the corresponding leaf ("no override"); nil Skills/Mcps leave that
// leaf untouched, a non-nil slice replaces it. Unknown leaves (temperature,
// options, permission, prompt, ...) are preserved.
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
		if err := setOmoString(agentObject, field.name, field.value); err != nil {
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

// writeOmoCustomAgents replaces the set of custom agent definitions under the
// top-level agents object. Built-in overrides already present are preserved
// untouched; every other existing entry is removed first.
func writeOmoCustomAgents(root *hujson.Object, agents map[string]OmoCustomAgent) error {
	var custom *hujson.Object
	if value := objectMemberValue(root, "agents"); value != nil {
		var err error
		custom, _, err = omoObjectMember(root, "agents", true)
		if err != nil {
			return err
		}
		stale := make([]string, 0, len(custom.Members))
		for _, member := range custom.Members {
			if name := memberName(member); !omoBuiltinAgents[name] {
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
	for _, name := range sortedOmoCustomAgentNames(agents) {
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
			if err := setOmoString(agentObject, field.name, field.value); err != nil {
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

// validateOmoCustomAgents enforces the schema rules for custom agent
// definitions: non-empty non-built-in names, known models, display names that
// are unique and collide with neither built-in nor custom agent names, and an
// orchestratorPrompt that begins with the agent's own @mention.
func validateOmoCustomAgents(agents map[string]OmoCustomAgent, validModels []string) error {
	valid := make(map[string]struct{}, len(validModels))
	for _, model := range validModels {
		valid[model] = struct{}{}
	}
	displayNames := make(map[string]string, len(agents))
	for _, name := range sortedOmoCustomAgentNames(agents) {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: custom agent name is empty", ErrInvalidPreset)
		}
		if omoBuiltinAgents[name] {
			return fmt.Errorf("%w: %q is a built-in agent and cannot be redefined", ErrInvalidPreset, name)
		}
		agent := agents[name]
		if agent.Model != "" {
			if _, ok := valid[agent.Model]; !ok {
				return fmt.Errorf("%w: missing OMO model references: %s=%s", ErrInvalidPreset, name, agent.Model)
			}
		}
		if agent.DisplayName != "" {
			if omoBuiltinAgents[agent.DisplayName] {
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

func sortedOmoCustomAgentNames(agents map[string]OmoCustomAgent) []string {
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

func omoOpenCodeChecks(homeDir string) ([]FileCheck, error) {
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

func omoShape(format string, args ...any) error {
	return fmt.Errorf(format+": %w", append(args, ErrUnsafeShape)...)
}

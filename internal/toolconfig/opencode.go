package toolconfig

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// OpenCodeAdapter manages the provider entry and default model pointer in
// opencode.json. It patches owned leaves and leaves the rest of the provider
// entry, including comments, untouched.
type OpenCodeAdapter struct{}

type OpencodeAdapter = OpenCodeAdapter

func NewOpenCodeAdapter() Adapter { return OpenCodeAdapter{} }

func NewOpencodeAdapter() Adapter { return OpenCodeAdapter{} }

func (OpenCodeAdapter) Tool() Tool { return ToolOpencode }

func (OpenCodeAdapter) Detect(homeDir string) ToolStatus {
	return detectOpencode(homeDir)
}

type openCodeOptions struct {
	BaseURL string `json:"baseURL,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
}

type openCodeModalities struct {
	Input []string `json:"input,omitempty"`
}

type openCodeModel struct {
	Name       string                   `json:"name,omitempty"`
	Limit      *ModelLimit              `json:"limit,omitempty"`
	Modalities *openCodeModalities      `json:"modalities,omitempty"`
	Reasoning  bool                     `json:"reasoning,omitempty"`
	Variants   map[string]PresetVariant `json:"variants,omitempty"`
}

func buildOpenCodeModels(models []PresetModel) (map[string]openCodeModel, string) {
	result := make(map[string]openCodeModel, len(models))
	defaultModel := ""
	for _, model := range models {
		entry := openCodeModel{Name: model.Name}
		if model.Limit != nil && (model.Limit.Context != 0 || model.Limit.Output != 0) {
			limit := *model.Limit
			entry.Limit = &limit
		}
		if len(model.Modalities) > 0 {
			entry.Modalities = &openCodeModalities{
				Input: append([]string(nil), model.Modalities...),
			}
		}
		if model.Reasoning {
			entry.Reasoning = true
		}
		if len(model.Variants) > 0 {
			entry.Variants = make(map[string]PresetVariant, len(model.Variants))
			for name, variant := range model.Variants {
				entry.Variants[name] = variant
			}
		}
		result[model.Name] = entry
		if defaultModel == "" && model.Default {
			defaultModel = model.Name
		}
	}
	return result, defaultModel
}

func openCodeVendor(vendor string) string {
	if vendor == "@ai-sdk/openai" || vendor == "@ai-sdk/openai-compatible" {
		return vendor
	}
	return "@ai-sdk/openai-compatible"
}

func (OpenCodeAdapter) Plan(p PresetPlaintext, homeDir string) (*ChangeSet, error) {
	if err := validatePreset(p); err != nil {
		return nil, err
	}
	providerID := providerKey(p.Preset)
	homeDir = absoluteHomeDir(homeDir)
	configPath := DefaultConfigPath(ToolOpencode, homeDir)
	resolvedPath, before, err := snapshotFile(configPath, homeDir)
	if err != nil {
		return nil, err
	}
	doc, err := parseJSONBytes(before)
	if err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, fmt.Errorf("parse opencode config: %w", err)
	}
	if err := requireUniqueKeys(root, "provider", "model"); err != nil {
		return nil, err
	}

	providers, present, err := requireObjectMember(root, "provider")
	if err != nil {
		return nil, err
	}
	if !present {
		providers = &hujson.Object{}
		if err := setObjectMember(root, "provider", hujson.Value{Value: providers}); err != nil {
			return nil, err
		}
	}
	if err := requireUniqueKeys(providers, providerID); err != nil {
		return nil, err
	}

	entry, entryPresent, err := requireObjectMember(providers, providerID)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", providerID, err)
	}
	if !entryPresent {
		entry = &hujson.Object{}
		if err := setObjectMember(providers, providerID, hujson.Value{Value: entry}); err != nil {
			return nil, err
		}
	}
	if err := requireUniqueKeys(entry, "npm", "name", "options", "models"); err != nil {
		return nil, err
	}
	if modelsValue := objectMemberValue(entry, "models"); modelsValue != nil {
		modelsObject, ok := modelsValue.Value.(*hujson.Object)
		if !ok {
			return nil, fmt.Errorf("managed JSON key %q is not an object: %w", "models", ErrUnsafeShape)
		}
		if err := requireUniqueObjectMembers(modelsObject); err != nil {
			return nil, err
		}
	}

	if value, err := jsonValue(openCodeVendor(p.Vendor)); err != nil {
		return nil, err
	} else if err := setObjectMember(entry, "npm", value); err != nil {
		return nil, err
	}
	if value, err := jsonValue(p.Name); err != nil {
		return nil, err
	} else if err := setObjectMember(entry, "name", value); err != nil {
		return nil, err
	}

	options, optionsPresent, err := requireObjectMember(entry, "options")
	if err != nil {
		return nil, err
	}
	if !optionsPresent {
		options = &hujson.Object{}
		if err := setObjectMember(entry, "options", hujson.Value{Value: options}); err != nil {
			return nil, err
		}
	}
	if err := requireUniqueKeys(options, "baseURL", "apiKey"); err != nil {
		return nil, err
	}
	if value, err := jsonValue(p.BaseURL); err != nil {
		return nil, err
	} else if err := setObjectMember(options, "baseURL", value); err != nil {
		return nil, err
	}
	if p.APIKey == "" {
		if err := removeObjectMember(options, "apiKey"); err != nil {
			return nil, err
		}
	} else if value, err := jsonValue(p.APIKey); err != nil {
		return nil, err
	} else if err := setObjectMember(options, "apiKey", value); err != nil {
		return nil, err
	}

	models, defaultModel := buildOpenCodeModels(p.Models)
	modelsValue, err := jsonValue(models)
	if err != nil {
		return nil, err
	}
	if err := setObjectMember(entry, "models", modelsValue); err != nil {
		return nil, err
	}
	if defaultModel != "" {
		value, err := jsonValue(providerID + "/" + defaultModel)
		if err != nil {
			return nil, err
		}
		if err := setObjectMember(root, "model", value); err != nil {
			return nil, err
		}
	} else if strings.HasPrefix(objectString(root, "model"), providerID+"/") {
		// OpenCode's pointer is provider-scoped. Do not clear a pointer to a
		// different provider when this preset has no default model.
		if err := removeObjectMember(root, "model"); err != nil {
			return nil, err
		}
	}

	change := changeForSnapshot(ResOpencodeConfig, resolvedPath, false, before)
	change.After = doc.Pack()
	return &ChangeSet{Tool: ToolOpencode, Changes: []FileChange{change}}, nil
}

func (OpenCodeAdapter) ReadManaged(homeDir, providerID string) (ManagedSection, error) {
	root, entry, options, models, err := loadOpenCodeManaged(homeDir, providerID)
	if err != nil {
		return ManagedSection{}, err
	}
	if entry == nil {
		return ManagedSection{}, nil
	}

	section := ManagedSection{
		Present:    true,
		ProviderID: providerID,
		Fields:     map[string]string{},
	}
	section.BaseURL = objectString(options, "baseURL")
	section.Model = objectString(root, "model")
	if value := objectString(entry, "npm"); value != "" {
		section.Fields["npm"] = value
	}
	if value := objectString(entry, "name"); value != "" {
		section.Fields["name"] = value
	}
	if value := objectString(options, "apiKey"); value != "" {
		section.Fields["apiKey"] = MaskSecret(value)
	}
	if models != nil {
		section.Fields["models_count"] = strconv.Itoa(len(models.Members))
		modelsValue := objectValue(entry, "models")
		section.Fields["models"] = string(modelsValue.Pack())
	}
	return section, nil
}

// ReadManagedRaw returns plaintext credentials for backend reconciliation.
// NEVER expose this result through Wails bindings or logs.
func (OpenCodeAdapter) ReadManagedRaw(homeDir, providerID string) (RawManagedSection, error) {
	root, entry, options, models, err := loadOpenCodeManaged(homeDir, providerID)
	if err != nil {
		return RawManagedSection{}, err
	}
	if entry == nil {
		return RawManagedSection{}, nil
	}
	section := RawManagedSection{
		Present:    true,
		ProviderID: providerID,
		BaseURL:    objectString(options, "baseURL"),
		APIKey:     objectString(options, "apiKey"),
		Model:      objectString(root, "model"),
	}
	if models != nil {
		section.Models = decodeOpenCodeModels(models)
		defaultModel := objectString(root, "model")
		for i := range section.Models {
			if defaultModel == providerID+"/"+section.Models[i].Name {
				section.Models[i].Default = true
			}
		}
	}
	return section, nil
}

func loadOpenCodeManaged(homeDir, providerID string) (*hujson.Object, *hujson.Object, *hujson.Object, *hujson.Object, error) {
	if providerID == "" {
		return nil, nil, nil, nil, fmt.Errorf("%w: provider ID is required", ErrInvalidPreset)
	}
	homeDir = absoluteHomeDir(homeDir)
	path := DefaultConfigPath(ToolOpencode, homeDir)
	_, data, err := snapshotFile(path, homeDir)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if data == nil {
		return nil, nil, nil, nil, nil
	}
	doc, err := parseJSONBytes(data)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("parse opencode config: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("parse opencode config: %w", err)
	}
	if err := requireUniqueKeys(root, "provider", "model"); err != nil {
		return nil, nil, nil, nil, err
	}
	providers, present, err := requireObjectMember(root, "provider")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !present {
		return root, nil, nil, nil, nil
	}
	if err := requireUniqueKeys(providers, providerID); err != nil {
		return nil, nil, nil, nil, err
	}
	entry, present, err := requireObjectMember(providers, providerID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !present {
		return root, nil, nil, nil, nil
	}
	if err := requireUniqueKeys(entry, "npm", "name", "options", "models"); err != nil {
		return nil, nil, nil, nil, err
	}
	options, optionsPresent, err := requireObjectMember(entry, "options")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if optionsPresent {
		if err := requireUniqueKeys(options, "baseURL", "apiKey"); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	models, modelsPresent, err := requireObjectMember(entry, "models")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if modelsPresent {
		if err := requireUniqueObjectMembers(models); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return root, entry, options, models, nil
}

func decodeOpenCodeModels(models *hujson.Object) []PresetModel {
	result := make([]PresetModel, 0, len(models.Members))
	for _, member := range models.Members {
		var model openCodeModel
		if err := json.Unmarshal(member.Value.Pack(), &model); err != nil {
			continue
		}
		name := model.Name
		if name == "" {
			name = memberName(member)
		}
		preset := PresetModel{
			Name:      name,
			Limit:     model.Limit,
			Reasoning: model.Reasoning,
			Variants:  model.Variants,
		}
		if model.Modalities != nil {
			preset.Modalities = append([]string(nil), model.Modalities.Input...)
		}
		result = append(result, preset)
	}
	return result
}

func (OpenCodeAdapter) ExportSnippet(p PresetPlaintext) (Snippet, error) {
	if err := validatePreset(p); err != nil {
		return Snippet{}, err
	}
	providerID := providerKey(p.Preset)
	models, defaultModel := buildOpenCodeModels(p.Models)
	provider := map[string]any{
		"npm":     openCodeVendor(p.Vendor),
		"name":    p.Name,
		"options": map[string]any{"baseURL": p.BaseURL},
		"models":  models,
	}
	if p.APIKey != "" {
		provider["options"].(map[string]any)["apiKey"] = p.APIKey
	}
	fragment := map[string]any{
		"provider": map[string]any{providerID: provider},
	}
	if defaultModel != "" {
		fragment["model"] = providerID + "/" + defaultModel
	}
	data, err := json.MarshalIndent(fragment, "", "  ")
	if err != nil {
		return Snippet{}, err
	}
	return Snippet{
		TargetPath: "~/.config/opencode/opencode.json",
		Format:     "json",
		Content:    string(data) + "\n",
		Notes:      "Paste this fragment into ~/.config/opencode/opencode.json under the top-level object.",
	}, nil
}

func parseJSONBytes(data []byte) (hujson.Value, error) {
	if len(data) == 0 || len(strings.TrimSpace(string(data))) == 0 {
		data = []byte("{}")
	}
	return hujson.Parse(data)
}

// ListProviderModels returns every "<providerID>/<model>" reference declared in
// the opencode config's provider section, across all providers. It is used for
// OMO cross-file validation (agent model references must exist somewhere in
// opencode.json). A missing config yields an empty list, not an error; the
// adapter's Plan path still fails closed on non-object shapes when applying.
func ListProviderModels(homeDir string) ([]string, error) {
	doc, err := readJSONDocument(DefaultConfigPath(ToolOpencode, homeDir))
	if err != nil {
		return nil, err
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, err
	}
	providers := objectMemberValue(root, "provider")
	if providers == nil {
		return nil, nil
	}
	providersObj, ok := providers.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("managed JSON key %q is not an object: %w", "provider", ErrUnsafeShape)
	}
	result := make([]string, 0, len(providersObj.Members))
	for _, pm := range providersObj.Members {
		entryObj, ok := pm.Value.Value.(*hujson.Object)
		if !ok {
			continue
		}
		modelsValue := objectMemberValue(entryObj, "models")
		if modelsValue == nil {
			continue
		}
		modelsObj, ok := modelsValue.Value.(*hujson.Object)
		if !ok {
			continue
		}
		for _, mm := range modelsObj.Members {
			result = append(result, memberName(pm)+"/"+memberName(mm))
		}
	}
	return result, nil
}

// ReadModelPointer returns the current top-level model pointer
// (providerID/model) from the opencode config, or "" when unset. A missing
// config yields "", nil — the live-state read is tolerant while Plan stays
// fail-closed.
func ReadModelPointer(homeDir string) (string, error) {
	doc, err := readJSONDocument(DefaultConfigPath(ToolOpencode, homeDir))
	if err != nil {
		return "", err
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return "", err
	}
	return objectString(root, "model"), nil
}

// ListProviderVariants returns the sorted union of every variant key declared
// under provider models in the opencode config (provider.*.models.*.variants).
// It feeds the OMO agent variant dropdown. A missing config yields an empty
// list, not an error; non-object shapes are skipped leniently here and still
// fail closed on the adapter's Plan path.
func ListProviderVariants(homeDir string) ([]string, error) {
	doc, err := readJSONDocument(DefaultConfigPath(ToolOpencode, homeDir))
	if err != nil {
		return nil, err
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, err
	}
	providers := objectMemberValue(root, "provider")
	if providers == nil {
		return nil, nil
	}
	providersObj, ok := providers.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("managed JSON key %q is not an object: %w", "provider", ErrUnsafeShape)
	}
	seen := make(map[string]struct{})
	for _, pm := range providersObj.Members {
		entryObj, ok := pm.Value.Value.(*hujson.Object)
		if !ok {
			continue
		}
		modelsValue := objectMemberValue(entryObj, "models")
		if modelsValue == nil {
			continue
		}
		modelsObj, ok := modelsValue.Value.(*hujson.Object)
		if !ok {
			continue
		}
		for _, mm := range modelsObj.Members {
			modelObj, ok := mm.Value.Value.(*hujson.Object)
			if !ok {
				continue
			}
			variantsValue := objectMemberValue(modelObj, "variants")
			if variantsValue == nil {
				continue
			}
			variantsObj, ok := variantsValue.Value.(*hujson.Object)
			if !ok {
				continue
			}
			for _, vm := range variantsObj.Members {
				seen[memberName(vm)] = struct{}{}
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

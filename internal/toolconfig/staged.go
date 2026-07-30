package toolconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// ToolProviderChange is one ordered provider mutation in a complete tool
// configuration plan. Park has the same file effect as remove; the service
// layer keeps its preset in the database for later re-enabling.
type ToolProviderChange struct {
	Action string
	Preset PresetPlaintext
}

const (
	codexModelCatalogFilename          = "autoapi-model-catalog.json"
	codexModelCatalogResource Resource = "codex/model_catalog"
)

// PlanToolConfigChange snapshots a tool's managed files once and renders all
// provider mutations and the optional common config into one multi-file
// ChangeSet. commonConfig is deliberately not persisted by this package.
func PlanToolConfigChange(tool Tool, changes []ToolProviderChange, commonConfig, homeDir string) (*ChangeSet, error) {
	switch tool {
	case ToolCodex:
		return planCodexToolConfigChange(changes, commonConfig, homeDir)
	case ToolClaude:
		return planClaudeToolConfigChange(changes, commonConfig, homeDir)
	case ToolOpencode:
		return nil, fmt.Errorf("toolconfig: opencode uses its dedicated staged planner: %w", ErrInvalidPreset)
	default:
		return nil, fmt.Errorf("toolconfig: unsupported staged tool %q: %w", tool, ErrInvalidPreset)
	}
}

func planCodexToolConfigChange(changes []ToolProviderChange, commonConfig, homeDir string) (*ChangeSet, error) {
	if err := validateCodexCommonConfig(commonConfig); err != nil {
		return nil, err
	}
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
	if strings.TrimSpace(commonConfig) != "" {
		configAfter, err = applyCodexCommonConfig(configAfter, commonConfig)
		if err != nil {
			return nil, err
		}
	}
	catalogBytes, hasCatalog, err := buildCodexModelCatalog(changes)
	if err != nil {
		return nil, err
	}
	if hasCatalog {
		configAfter, err = spliceCodexTopLevelString(configAfter, "model_catalog_json", codexModelCatalogFilename)
		if err != nil {
			return nil, err
		}
	} else {
		configAfter, err = spliceCodexTopLevelString(configAfter, "model_catalog_json", "")
		if err != nil {
			return nil, err
		}
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
	if hasCatalog {
		catalogPath := filepath.Join(homeDir, ".codex", codexModelCatalogFilename)
		resolvedCatalogPath, catalogBefore, err := snapshotFile(catalogPath, homeDir)
		if err != nil {
			return nil, err
		}
		catalogChange := changeForSnapshot(codexModelCatalogResource, resolvedCatalogPath, false, catalogBefore)
		catalogChange.After = catalogBytes
		fileChanges = append(fileChanges, catalogChange)
	}
	return &ChangeSet{Tool: ToolCodex, Changes: fileChanges}, nil
}

func planClaudeToolConfigChange(changes []ToolProviderChange, commonConfig, homeDir string) (*ChangeSet, error) {
	if err := validateClaudeCommonConfig(commonConfig); err != nil {
		return nil, err
	}
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
	if strings.TrimSpace(commonConfig) != "" {
		after, err = applyClaudeCommonConfig(after, commonConfig)
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

type codexCatalogDocument struct {
	Models []codexCatalogModel `json:"models"`
}

type codexCatalogModel struct {
	Slug                       string   `json:"slug"`
	DisplayName                string   `json:"display_name"`
	Description                string   `json:"description"`
	BaseInstructions           string   `json:"base_instructions"`
	SupportsReasoningSummaries bool     `json:"supports_reasoning_summaries"`
	ContextWindow              int      `json:"context_window,omitempty"`
	MaxContextWindow           int      `json:"max_context_window,omitempty"`
	InputModalities            []string `json:"input_modalities"`
}

func buildCodexModelCatalog(changes []ToolProviderChange) ([]byte, bool, error) {
	seen := make(map[string]struct{})
	models := make([]codexCatalogModel, 0)
	for _, change := range changes {
		if change.Action != "upsert" {
			continue
		}
		for _, presetModel := range change.Preset.Models {
			if strings.TrimSpace(presetModel.Name) == "" {
				return nil, false, fmt.Errorf("Codex model catalog model name is empty: %w", ErrInvalidPreset)
			}
			if _, exists := seen[presetModel.Name]; exists {
				continue
			}
			seen[presetModel.Name] = struct{}{}
			catalogModel := codexCatalogModel{
				Slug:                       presetModel.Name,
				DisplayName:                presetModel.Name,
				Description:                presetModel.Name,
				BaseInstructions:           "You are Codex, a coding agent.",
				SupportsReasoningSummaries: presetModel.Reasoning,
				InputModalities:            append([]string(nil), presetModel.Modalities...),
			}
			if len(catalogModel.InputModalities) == 0 {
				catalogModel.InputModalities = []string{"text"}
			}
			if presetModel.Limit != nil && presetModel.Limit.Context > 0 {
				catalogModel.ContextWindow = presetModel.Limit.Context
				catalogModel.MaxContextWindow = presetModel.Limit.Context
			}
			models = append(models, catalogModel)
		}
	}
	if len(models) == 0 {
		return nil, false, nil
	}
	data, err := json.MarshalIndent(codexCatalogDocument{Models: models}, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("marshal Codex model catalog: %w", err)
	}
	return append(data, '\n'), true, nil
}

func spliceCodexTopLevelString(data []byte, key, value string) ([]byte, error) {
	lines := splitCodexLines(data)
	firstTable := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(tomlHeader(line.content), "[") {
			firstTable = i
			break
		}
	}
	seen := false
	filtered := make([]codexLine, 0, len(lines)+1)
	for i, line := range lines {
		if i < firstTable {
			if lineKey, ok := tomlLineKey(line.content); ok && lineKey == key {
				if seen {
					return nil, fmt.Errorf("duplicate top-level TOML key %q: %w", key, ErrUnsafeShape)
				}
				seen = true
				if value == "" {
					if comment := tomlCommentSuffix(line.content); len(comment) > 0 {
						filtered = append(filtered, codexLine{content: bytes.TrimSpace(comment), ending: line.ending})
					}
					continue
				}
				filtered = append(filtered, codexLine{
					content: replaceTomlAssignment(line.content, key, quotedTOMLString(value)),
					ending:  line.ending,
				})
				continue
			}
		}
		filtered = append(filtered, line)
	}
	if value == "" || seen {
		return joinCodexLines(filtered), nil
	}
	ending := codexLineEnding(lines, data)
	missing := codexLine{
		content: []byte(key + " = " + quotedTOMLString(value)),
		ending:  ending,
	}
	return joinCodexLines(append([]codexLine{missing}, lines...)), nil
}

var managedClaudeCommonPaths = map[string]struct{}{
	"env.ANTHROPIC_BASE_URL":             {},
	"env.ANTHROPIC_AUTH_TOKEN":           {},
	"env.ANTHROPIC_MODEL":                {},
	"env.ANTHROPIC_DEFAULT_HAIKU_MODEL":  {},
	"env.ANTHROPIC_DEFAULT_SONNET_MODEL": {},
	"env.ANTHROPIC_DEFAULT_OPUS_MODEL":   {},
	"model":                              {},
}

var managedCodexCommonKeys = map[string]struct{}{
	"model_providers":          {},
	"model_provider":           {},
	"model":                    {},
	"disable_response_storage": {},
	"model_catalog_json":       {},
}

func isSensitiveConfigKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_PAT", "_CREDS"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	lower := strings.ToLower(key)
	return strings.Contains(lower, "apikey") || strings.Contains(lower, "authtoken")
}

func commonConfigSecretError(tool, path string) error {
	return fmt.Errorf("%s common config key %q contains a secret; secrets belong in preset key fields: %w", tool, path, ErrInvalidPreset)
}

func validateClaudeCommonConfig(config string) error {
	if strings.TrimSpace(config) == "" {
		return nil
	}
	doc, err := parseJSONBytes([]byte(config))
	if err != nil {
		return fmt.Errorf("validate Claude common config: invalid JSON (%v): %w", err, ErrInvalidPreset)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return fmt.Errorf("validate Claude common config: root must be a JSON object (%v): %w", err, ErrInvalidPreset)
	}
	if err := validateClaudeCommonValue(hujson.Value{Value: root}, nil); err != nil {
		return fmt.Errorf("validate Claude common config: %w", err)
	}
	return nil
}

func validateClaudeCommonValue(value hujson.Value, path []string) error {
	switch current := value.Value.(type) {
	case *hujson.Object:
		if err := requireUniqueObjectMembers(current); err != nil {
			return err
		}
		for _, member := range current.Members {
			name := memberName(member)
			memberPath := append(append([]string(nil), path...), name)
			pathText := strings.Join(memberPath, ".")
			if _, managed := managedClaudeCommonPaths[pathText]; managed {
				return fmt.Errorf("managed key path %q is not allowed in a common config snippet: %w", pathText, ErrConflict)
			}
			if isSensitiveConfigKey(name) {
				return commonConfigSecretError("Claude", pathText)
			}
			if err := validateClaudeCommonValue(member.Value, memberPath); err != nil {
				return err
			}
		}
	case *hujson.Array:
		for i, element := range current.Elements {
			arrayPath := append(append([]string(nil), path...), fmt.Sprintf("[%d]", i))
			if err := validateClaudeCommonValue(element, arrayPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateClaudeDocumentShape(value hujson.Value) error {
	switch current := value.Value.(type) {
	case *hujson.Object:
		if err := requireUniqueObjectMembers(current); err != nil {
			return err
		}
		for _, member := range current.Members {
			if err := validateClaudeDocumentShape(member.Value); err != nil {
				return err
			}
		}
	case *hujson.Array:
		for _, element := range current.Elements {
			if err := validateClaudeDocumentShape(element); err != nil {
				return err
			}
		}
	}
	return nil
}

type claudeManagedCommonValue struct {
	path  []string
	value hujson.Value
}

func captureClaudeManagedCommonValues(root *hujson.Object) []claudeManagedCommonValue {
	paths := [][]string{
		{"model"},
		{"env", "ANTHROPIC_BASE_URL"},
		{"env", "ANTHROPIC_AUTH_TOKEN"},
		{"env", "ANTHROPIC_MODEL"},
		{"env", "ANTHROPIC_DEFAULT_HAIKU_MODEL"},
		{"env", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
		{"env", "ANTHROPIC_DEFAULT_OPUS_MODEL"},
	}
	values := make([]claudeManagedCommonValue, 0, len(paths))
	for _, path := range paths {
		if value := objectValue(root, path...); value != nil {
			values = append(values, claudeManagedCommonValue{
				path:  append([]string(nil), path...),
				value: value.Clone(),
			})
		}
	}
	return values
}

func mergeClaudeCommonObjects(target, source *hujson.Object) error {
	for _, member := range source.Members {
		name := memberName(member)
		existing := objectMemberValue(target, name)
		incomingObject, incomingIsObject := member.Value.Value.(*hujson.Object)
		if existing != nil {
			if existingObject, existingIsObject := existing.Value.(*hujson.Object); existingIsObject && incomingIsObject {
				if err := mergeClaudeCommonObjects(existingObject, incomingObject); err != nil {
					return err
				}
				continue
			}
		}
		if err := setObjectMember(target, name, member.Value.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func restoreClaudeManagedCommonValues(root *hujson.Object, values []claudeManagedCommonValue) error {
	for _, managed := range values {
		if len(managed.path) == 1 {
			if err := setObjectMember(root, managed.path[0], managed.value.Clone()); err != nil {
				return err
			}
			continue
		}
		envValue := objectMemberValue(root, "env")
		var env *hujson.Object
		if envValue != nil {
			env, _ = envValue.Value.(*hujson.Object)
		}
		if env == nil {
			env = &hujson.Object{}
			if err := setObjectMember(root, "env", hujson.Value{Value: env}); err != nil {
				return err
			}
		}
		if err := setObjectMember(env, managed.path[1], managed.value.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func applyClaudeCommonConfig(configBytes []byte, commonConfig string) ([]byte, error) {
	if err := validateClaudeCommonConfig(commonConfig); err != nil {
		return nil, err
	}
	doc, err := parseJSONBytes(configBytes)
	if err != nil {
		return nil, fmt.Errorf("parse Claude settings before common config merge: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, fmt.Errorf("parse Claude settings before common config merge: %w", err)
	}
	if err := validateClaudeDocumentShape(doc); err != nil {
		return nil, fmt.Errorf("validate Claude settings before common config merge: %w", err)
	}
	snippet, err := parseJSONBytes([]byte(commonConfig))
	if err != nil {
		return nil, fmt.Errorf("parse Claude common config: %w", err)
	}
	snippetRoot, err := jsonRootObject(&snippet)
	if err != nil {
		return nil, fmt.Errorf("parse Claude common config: %w", err)
	}
	managed := captureClaudeManagedCommonValues(root)
	if err := mergeClaudeCommonObjects(root, snippetRoot); err != nil {
		return nil, fmt.Errorf("merge Claude common config: %w", err)
	}
	if err := restoreClaudeManagedCommonValues(root, managed); err != nil {
		return nil, fmt.Errorf("restore Claude managed config: %w", err)
	}
	return packFormatted(doc)
}

type codexCommonEntry struct {
	key   string
	block []codexLine
}

type codexAssignment struct {
	key    string
	rawKey string
	start  int
	end    int
	block  []codexLine
}

type tomlValueState struct {
	arrayDepth  int
	inlineDepth int
	quote       byte
	triple      bool
	escaped     bool
}

func (s *tomlValueState) consume(data []byte) {
	for i := 0; i < len(data); i++ {
		char := data[i]
		if s.quote != 0 {
			if s.triple {
				if i+2 < len(data) && data[i] == s.quote && data[i+1] == s.quote && data[i+2] == s.quote {
					s.quote = 0
					s.triple = false
					i += 2
				}
				continue
			}
			if s.quote == '"' && s.escaped {
				s.escaped = false
				continue
			}
			if s.quote == '"' && char == '\\' {
				s.escaped = true
				continue
			}
			if char == s.quote {
				s.quote = 0
			}
			continue
		}
		switch char {
		case '#':
			return
		case '"', '\'':
			if i+2 < len(data) && data[i] == char && data[i+1] == char && data[i+2] == char {
				s.quote = char
				s.triple = true
				i += 2
			} else {
				s.quote = char
			}
		case '[':
			s.arrayDepth++
		case ']':
			if s.arrayDepth > 0 {
				s.arrayDepth--
			}
		case '{':
			s.inlineDepth++
		case '}':
			if s.inlineDepth > 0 {
				s.inlineDepth--
			}
		}
	}
}

func (s tomlValueState) complete() bool {
	return s.quote == 0 && s.arrayDepth == 0 && s.inlineDepth == 0
}

func tomlAssignmentValue(line []byte) ([]byte, []byte) {
	withoutComment := tomlLineWithoutComment(line)
	equal := bytes.IndexByte(withoutComment, '=')
	if equal < 0 {
		return nil, nil
	}
	value := bytes.TrimSpace(withoutComment[equal+1:])
	return value, tomlCommentSuffix(line)
}

func canonicalTomlKey(rawKey string) (string, error) {
	rawKey = strings.TrimSpace(rawKey)
	if strings.Contains(rawKey, ".") && !strings.HasPrefix(rawKey, "\"") && !strings.HasPrefix(rawKey, "'") {
		return "", fmt.Errorf("Codex common config key %q is not a top-level key: %w", rawKey, ErrInvalidPreset)
	}
	if len(rawKey) >= 2 && rawKey[0] == '"' && rawKey[len(rawKey)-1] == '"' {
		key, err := strconv.Unquote(rawKey)
		if err != nil {
			return "", fmt.Errorf("decode Codex common config key %q: %w", rawKey, err)
		}
		return key, nil
	}
	if len(rawKey) >= 2 && rawKey[0] == '\'' && rawKey[len(rawKey)-1] == '\'' {
		return rawKey[1 : len(rawKey)-1], nil
	}
	return rawKey, nil
}

func scanCodexTopLevelAssignments(data []byte, rejectTables bool) ([]codexAssignment, int, error) {
	lines := splitCodexLines(data)
	assignments := make([]codexAssignment, 0)
	seen := make(map[string]struct{})
	firstTable := len(lines)
	for i := 0; i < len(lines); {
		header := tomlHeader(lines[i].content)
		if strings.HasPrefix(header, "[") {
			firstTable = i
			if rejectTables {
				return nil, firstTable, fmt.Errorf("Codex common config table sections are not supported in v1: %w", ErrInvalidPreset)
			}
			break
		}
		key, ok := tomlLineKey(lines[i].content)
		if !ok {
			i++
			continue
		}
		canonical, err := canonicalTomlKey(key)
		if err != nil {
			return nil, firstTable, err
		}
		if _, exists := seen[canonical]; exists {
			return nil, firstTable, fmt.Errorf("duplicate Codex common config key %q: %w", canonical, ErrInvalidPreset)
		}
		seen[canonical] = struct{}{}
		value, _ := tomlAssignmentValue(lines[i].content)
		state := tomlValueState{}
		state.consume(value)
		end := i
		for !state.complete() && end+1 < len(lines) {
			end++
			continuation := tomlLineWithoutComment(lines[end].content)
			state.consume(continuation)
		}
		if !state.complete() {
			return nil, firstTable, fmt.Errorf("Codex common config key %q has an incomplete value: %w", canonical, ErrInvalidPreset)
		}
		assignments = append(assignments, codexAssignment{
			key:    canonical,
			rawKey: key,
			start:  i,
			end:    end,
			block:  cloneCodexLines(lines[i : end+1]),
		})
		i = end + 1
	}
	return assignments, firstTable, nil
}

func validateCodexCommonValue(value any, path string) error {
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			nestedPath := key
			if path != "" {
				nestedPath = path + "." + key
			}
			if isSensitiveConfigKey(key) {
				return commonConfigSecretError("Codex", nestedPath)
			}
			if err := validateCodexCommonValue(nested, nestedPath); err != nil {
				return err
			}
		}
	case []any:
		for i, nested := range current {
			if err := validateCodexCommonValue(nested, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseCodexCommonEntries(config string) ([]codexCommonEntry, error) {
	doc, err := readTOMLBytes([]byte(config))
	if err != nil {
		return nil, fmt.Errorf("validate Codex common config: invalid TOML (%v): %w", err, ErrInvalidPreset)
	}
	assignments, _, err := scanCodexTopLevelAssignments([]byte(config), true)
	if err != nil {
		return nil, fmt.Errorf("validate Codex common config: %w", err)
	}
	assignmentByKey := make(map[string]codexAssignment, len(assignments))
	for _, assignment := range assignments {
		assignmentByKey[assignment.key] = assignment
	}
	entries := make([]codexCommonEntry, 0, len(doc))
	for key, value := range doc {
		if _, managed := managedCodexCommonKeys[key]; managed {
			return nil, fmt.Errorf("Codex common config key %q is managed and cannot be overridden: %w", key, ErrConflict)
		}
		if isSensitiveConfigKey(key) {
			return nil, commonConfigSecretError("Codex", key)
		}
		if err := validateCodexCommonValue(value, key); err != nil {
			return nil, err
		}
		assignment, ok := assignmentByKey[key]
		if !ok {
			return nil, fmt.Errorf("Codex common config key %q is not a top-level assignment: %w", key, ErrInvalidPreset)
		}
		entries = append(entries, codexCommonEntry{
			key:   key,
			block: cloneCodexLines(assignment.block),
		})
	}
	for _, assignment := range assignments {
		if _, ok := doc[assignment.key]; !ok {
			return nil, fmt.Errorf("Codex common config key %q could not be decoded: %w", assignment.key, ErrInvalidPreset)
		}
	}
	return entries, nil
}

func validateCodexCommonConfig(config string) error {
	if strings.TrimSpace(config) == "" {
		return nil
	}
	_, err := parseCodexCommonEntries(config)
	return err
}

func cloneCodexLines(lines []codexLine) []codexLine {
	if len(lines) == 0 {
		return nil
	}
	cloned := make([]codexLine, len(lines))
	for i, line := range lines {
		cloned[i] = codexLine{
			content: append([]byte(nil), line.content...),
			ending:  append([]byte(nil), line.ending...),
		}
	}
	return cloned
}

func codexBlockWithIndent(block []codexLine, indent, suffix, ending []byte) []codexLine {
	cloned := cloneCodexLines(block)
	for i := range cloned {
		content := make([]byte, 0, len(indent)+len(cloned[i].content)+len(suffix))
		content = append(content, indent...)
		content = append(content, cloned[i].content...)
		if i == 0 && len(suffix) > 0 {
			content = append(content, suffix...)
		}
		cloned[i].content = content
		if len(ending) > 0 {
			cloned[i].ending = append([]byte(nil), ending...)
		}
	}
	return cloned
}

func spliceCodexCommonConfig(data []byte, entries []codexCommonEntry) ([]byte, error) {
	assignments, firstTable, err := scanCodexTopLevelAssignments(data, false)
	if err != nil {
		return nil, err
	}
	lines := splitCodexLines(data)
	existing := make(map[string]codexAssignment, len(assignments))
	for _, assignment := range assignments {
		existing[assignment.key] = assignment
	}
	ending := codexLineEnding(lines, data)
	replacements := make(map[int][]codexLine)
	skipped := make(map[int]struct{})
	insertAt := firstTable
	missing := make([]codexLine, 0)
	for _, entry := range entries {
		assignment, found := existing[entry.key]
		if !found {
			missing = append(missing, codexBlockWithIndent(entry.block, nil, nil, ending)...)
			continue
		}
		indentEnd := 0
		for indentEnd < len(lines[assignment.start].content) && (lines[assignment.start].content[indentEnd] == ' ' || lines[assignment.start].content[indentEnd] == '\t') {
			indentEnd++
		}
		replacements[assignment.start] = codexBlockWithIndent(entry.block, lines[assignment.start].content[:indentEnd], tomlCommentSuffix(lines[assignment.start].content), ending)
		for i := assignment.start + 1; i <= assignment.end; i++ {
			skipped[i] = struct{}{}
		}
	}
	result := make([]codexLine, 0, len(lines)+len(missing))
	for i, line := range lines {
		if i == insertAt {
			result = append(result, missing...)
		}
		if _, skip := skipped[i]; skip {
			continue
		}
		if replacement, ok := replacements[i]; ok {
			result = append(result, replacement...)
			continue
		}
		result = append(result, line)
	}
	if insertAt >= len(lines) {
		result = append(result, missing...)
	}
	return joinCodexLines(result), nil
}

func applyCodexCommonConfig(configBytes []byte, commonConfig string) ([]byte, error) {
	entries, err := parseCodexCommonEntries(commonConfig)
	if err != nil {
		return nil, err
	}
	result, err := spliceCodexCommonConfig(configBytes, entries)
	if err != nil {
		return nil, fmt.Errorf("merge Codex common config: %w", err)
	}
	if _, err := readTOMLBytes(result); err != nil {
		return nil, fmt.Errorf("validate merged Codex common config: %w", err)
	}
	return result, nil
}

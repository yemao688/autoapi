package toolconfig

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// CodexAdapter manages config.toml with a text splice and the neighboring
// OPENAI_API_KEY in auth.json. TOML reads use go-toml/v2, but writes never
// round-trip the document, which keeps unmanaged bytes and comments intact.
type CodexAdapter struct{}

func NewCodexAdapter() Adapter { return CodexAdapter{} }

func (CodexAdapter) Tool() Tool { return ToolCodex }

var reservedCodexProviderIDs = map[string]struct{}{
	"openai":         {},
	"ollama":         {},
	"oss":            {},
	"amazon-bedrock": {},
	"lmstudio":       {},
	"ollama-chat":    {},
}

func validateCodexProviderID(providerID string) error {
	if providerID == "" {
		return fmt.Errorf("codex provider ID is empty: %w", ErrInvalidPreset)
	}
	for _, char := range providerID {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
			return fmt.Errorf("codex provider ID %q is not a bare TOML key: %w", providerID, ErrInvalidPreset)
		}
	}
	if _, reserved := reservedCodexProviderIDs[strings.ToLower(providerID)]; reserved {
		return fmt.Errorf("codex provider ID %q is reserved: %w", providerID, ErrConflict)
	}
	return nil
}

func (CodexAdapter) Detect(homeDir string) ToolStatus {
	return detectCodex(homeDir)
}

func (CodexAdapter) Plan(p PresetPlaintext, homeDir string) (*ChangeSet, error) {
	if err := validatePreset(p); err != nil {
		return nil, err
	}
	providerID := providerKey(p.Preset)
	if err := validateCodexProviderID(providerID); err != nil {
		return nil, err
	}
	homeDir = absoluteHomeDir(homeDir)
	configPath := DefaultConfigPath(ToolCodex, homeDir)
	resolvedConfigPath, before, err := snapshotFile(configPath, homeDir)
	if err != nil {
		return nil, err
	}
	if err := validateCodexConfigShape(before, providerID); err != nil {
		return nil, err
	}
	after, err := spliceCodexConfig(before, providerID, p.Name, p.BaseURL, presetDefaultModel(p.Models))
	if err != nil {
		return nil, err
	}
	configChange := changeForSnapshot(ResCodexConfig, resolvedConfigPath, false, before)
	configChange.After = after
	changes := []FileChange{configChange}

	// A key-less preset must not touch auth.json, including an existing key.
	if p.APIKey == "" {
		return &ChangeSet{Tool: ToolCodex, Changes: changes}, nil
	}
	authPath := filepath.Join(homeDir, ".codex", "auth.json")
	resolvedAuthPath, authBefore, err := snapshotFile(authPath, homeDir)
	if err != nil {
		return nil, err
	}
	authAfter, err := patchCodexAuth(authBefore, p.APIKey)
	if err != nil {
		return nil, err
	}
	authChange := changeForSnapshot(ResCodexAuth, resolvedAuthPath, true, authBefore)
	authChange.After = authAfter
	changes = append(changes, authChange)
	return &ChangeSet{Tool: ToolCodex, Changes: changes}, nil
}

func (CodexAdapter) ReadManaged(homeDir, providerID string) (ManagedSection, error) {
	if providerID == "" {
		return ManagedSection{}, fmt.Errorf("%w: provider ID is required", ErrInvalidPreset)
	}
	if err := validateCodexProviderID(providerID); err != nil {
		return ManagedSection{}, err
	}
	homeDir = absoluteHomeDir(homeDir)
	path := DefaultConfigPath(ToolCodex, homeDir)
	_, data, err := snapshotFile(path, homeDir)
	if err != nil {
		return ManagedSection{}, err
	}
	if data == nil {
		return ManagedSection{}, nil
	}
	doc, err := readTOMLBytes(data)
	if err != nil {
		return ManagedSection{}, err
	}
	providersValue, exists := doc["model_providers"]
	if !exists {
		return ManagedSection{}, nil
	}
	providers, ok := tomlMap(providersValue)
	if !ok {
		return ManagedSection{}, fmt.Errorf("model_providers is not a table: %w", ErrUnsafeShape)
	}
	entryValue, exists := providers[providerID]
	if !exists {
		return ManagedSection{}, nil
	}
	entry, ok := tomlMap(entryValue)
	if !ok {
		return ManagedSection{}, fmt.Errorf("model_providers.%s is not a table: %w", providerID, ErrUnsafeShape)
	}

	section := ManagedSection{
		Present:    true,
		ProviderID: providerID,
		BaseURL:    tomlString(entry["base_url"]),
		Model:      tomlString(doc["model"]),
		Fields:     map[string]string{},
	}
	for _, field := range []string{"name", "wire_api"} {
		if value := tomlString(entry[field]); value != "" {
			section.Fields[field] = value
		}
	}
	if value, ok := entry["requires_openai_auth"].(bool); ok {
		section.Fields["requires_openai_auth"] = strconv.FormatBool(value)
	}

	authPath := filepath.Join(homeDir, ".codex", "auth.json")
	_, authData, err := snapshotFile(authPath, homeDir)
	if err != nil {
		return ManagedSection{}, err
	}
	if authData != nil {
		authDoc, parseErr := parseJSONBytes(authData)
		if parseErr != nil {
			return ManagedSection{}, fmt.Errorf("parse Codex auth: %w", parseErr)
		}
		authRoot, rootErr := jsonRootObject(&authDoc)
		if rootErr != nil {
			return ManagedSection{}, fmt.Errorf("parse Codex auth: %w", rootErr)
		}
		if keyErr := requireUniqueKeys(authRoot, "OPENAI_API_KEY"); keyErr != nil {
			return ManagedSection{}, keyErr
		}
		if value := objectString(authRoot, "OPENAI_API_KEY"); value != "" {
			section.Fields["OPENAI_API_KEY"] = MaskSecret(value)
		}
	}
	return section, nil
}

// ListCodexProviderIDs returns the sorted provider keys declared under
// model_providers in the codex config. A missing config yields an empty
// list; a non-table model_providers fails closed.
func ListCodexProviderIDs(homeDir string) ([]string, error) {
	homeDir = absoluteHomeDir(homeDir)
	_, configData, err := snapshotFile(DefaultConfigPath(ToolCodex, homeDir), homeDir)
	if err != nil {
		return nil, err
	}
	if configData == nil {
		return nil, nil
	}
	doc, err := readTOMLBytes(configData)
	if err != nil {
		return nil, err
	}
	providers, exists := doc["model_providers"]
	if !exists {
		return nil, nil
	}
	providerTable, ok := tomlMap(providers)
	if !ok {
		return nil, fmt.Errorf("model_providers is not a table: %w", ErrUnsafeShape)
	}
	ids := make([]string, 0, len(providerTable))
	for id := range providerTable {
		// Skip keys we could never manage (non-bare TOML keys, reserved
		// names) — the write path rejects them, so do not offer them.
		if err := validateCodexProviderID(id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// ReadManagedRaw returns plaintext credentials for backend reconciliation.
// NEVER expose this result through Wails bindings or logs.
func (CodexAdapter) ReadManagedRaw(homeDir, providerID string) (RawManagedSection, error) {
	if providerID == "" {
		return RawManagedSection{}, fmt.Errorf("%w: provider ID is required", ErrInvalidPreset)
	}
	if err := validateCodexProviderID(providerID); err != nil {
		return RawManagedSection{}, err
	}
	homeDir = absoluteHomeDir(homeDir)
	configPath := DefaultConfigPath(ToolCodex, homeDir)
	_, configData, err := snapshotFile(configPath, homeDir)
	if err != nil {
		return RawManagedSection{}, err
	}
	if configData == nil {
		return RawManagedSection{}, nil
	}
	doc, err := readTOMLBytes(configData)
	if err != nil {
		return RawManagedSection{}, err
	}
	providers, exists := doc["model_providers"]
	if !exists {
		return RawManagedSection{}, nil
	}
	providerTable, ok := tomlMap(providers)
	if !ok {
		return RawManagedSection{}, fmt.Errorf("model_providers is not a table: %w", ErrUnsafeShape)
	}
	entryValue, exists := providerTable[providerID]
	if !exists {
		return RawManagedSection{}, nil
	}
	entry, ok := tomlMap(entryValue)
	if !ok {
		return RawManagedSection{}, fmt.Errorf("model_providers.%s is not a table: %w", providerID, ErrUnsafeShape)
	}
	section := RawManagedSection{
		Present:    true,
		ProviderID: providerID,
		BaseURL:    tomlString(entry["base_url"]),
		Model:      tomlString(doc["model"]),
	}
	if section.Model != "" {
		section.Models = []PresetModel{{Name: section.Model, Default: true}}
	}
	authPath := filepath.Join(homeDir, ".codex", "auth.json")
	_, authData, err := snapshotFile(authPath, homeDir)
	if err != nil {
		return RawManagedSection{}, err
	}
	if authData != nil {
		authDoc, err := parseJSONBytes(authData)
		if err != nil {
			return RawManagedSection{}, fmt.Errorf("parse Codex auth: %w", err)
		}
		authRoot, err := jsonRootObject(&authDoc)
		if err != nil {
			return RawManagedSection{}, fmt.Errorf("parse Codex auth: %w", err)
		}
		if err := requireUniqueKeys(authRoot, "OPENAI_API_KEY"); err != nil {
			return RawManagedSection{}, err
		}
		section.APIKey = objectString(authRoot, "OPENAI_API_KEY")
	}
	return section, nil
}

func (CodexAdapter) ExportSnippet(p PresetPlaintext) (Snippet, error) {
	if err := validatePreset(p); err != nil {
		return Snippet{}, err
	}
	providerID := providerKey(p.Preset)
	if err := validateCodexProviderID(providerID); err != nil {
		return Snippet{}, err
	}
	content, err := spliceCodexConfig(nil, providerID, p.Name, p.BaseURL, presetDefaultModel(p.Models))
	if err != nil {
		return Snippet{}, err
	}
	return Snippet{
		TargetPath: "~/.codex/config.toml",
		Format:     "toml",
		Content:    string(content),
		Notes:      "Place the plaintext API key in ~/.codex/auth.json as {\"OPENAI_API_KEY\": \"...\"}.",
	}, nil
}

func presetDefaultModel(models []PresetModel) string {
	for _, model := range models {
		if model.Default {
			return model.Name
		}
	}
	return ""
}

func readTOMLBytes(data []byte) (map[string]any, error) {
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse TOML file: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func validateCodexConfigShape(data []byte, providerID string) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	doc, err := readTOMLBytes(data)
	if err != nil {
		return err
	}
	providersValue, exists := doc["model_providers"]
	if !exists {
		return nil
	}
	providers, ok := tomlMap(providersValue)
	if !ok {
		return fmt.Errorf("model_providers is not a table: %w", ErrUnsafeShape)
	}
	if entryValue, exists := providers[providerID]; exists {
		if _, ok := tomlMap(entryValue); !ok {
			return fmt.Errorf("model_providers.%s is not a table: %w", providerID, ErrUnsafeShape)
		}
	}
	return nil
}

func tomlMap(value any) (map[string]any, bool) {
	mapValue, ok := value.(map[string]any)
	return mapValue, ok
}

func tomlString(value any) string {
	valueString, _ := value.(string)
	return valueString
}

type codexLine struct {
	content []byte
	ending  []byte
}

func splitCodexLines(data []byte) []codexLine {
	if len(data) == 0 {
		return nil
	}
	lines := make([]codexLine, 0, bytes.Count(data, []byte("\n"))+1)
	for start := 0; start < len(data); {
		relative := bytes.IndexByte(data[start:], '\n')
		if relative < 0 {
			lines = append(lines, codexLine{content: append([]byte(nil), data[start:]...)})
			break
		}
		end := start + relative
		contentEnd := end
		ending := []byte("\n")
		if contentEnd > start && data[contentEnd-1] == '\r' {
			contentEnd--
			ending = []byte("\r\n")
		}
		lines = append(lines, codexLine{
			content: append([]byte(nil), data[start:contentEnd]...),
			ending:  ending,
		})
		start = end + 1
	}
	return lines
}

func joinCodexLines(lines []codexLine) []byte {
	var result bytes.Buffer
	for _, line := range lines {
		result.Write(line.content)
		result.Write(line.ending)
	}
	return result.Bytes()
}

func codexLineEnding(lines []codexLine, data []byte) []byte {
	for _, line := range lines {
		if len(line.ending) > 0 {
			return append([]byte(nil), line.ending...)
		}
	}
	if bytes.Contains(data, []byte("\r\n")) {
		return []byte("\r\n")
	}
	return []byte("\n")
}

func tomlCommentIndex(line []byte) int {
	var quote byte
	escaped := false
	for i, char := range line {
		if quote == '"' {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		if quote == '\'' {
			if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '"', '\'':
			quote = char
		case '#':
			return i
		}
	}
	return -1
}

func tomlLineWithoutComment(line []byte) []byte {
	if index := tomlCommentIndex(line); index >= 0 {
		return line[:index]
	}
	return line
}

func tomlLineKey(line []byte) (string, bool) {
	withoutComment := bytes.TrimSpace(tomlLineWithoutComment(line))
	if len(withoutComment) == 0 || withoutComment[0] == '[' {
		return "", false
	}
	for i, char := range withoutComment {
		if char != '=' {
			continue
		}
		key := strings.TrimSpace(string(withoutComment[:i]))
		if key == "" || strings.ContainsAny(key, " \t\r\n") {
			return "", false
		}
		return key, true
	}
	return "", false
}

func tomlHeader(line []byte) string {
	return strings.TrimSpace(string(tomlLineWithoutComment(line)))
}

func tomlCommentSuffix(line []byte) []byte {
	index := tomlCommentIndex(line)
	if index < 0 {
		return nil
	}
	start := index
	for start > 0 && (line[start-1] == ' ' || line[start-1] == '\t') {
		start--
	}
	return append([]byte(nil), line[start:]...)
}

func replaceTomlAssignment(line []byte, key, value string) []byte {
	indentEnd := 0
	for indentEnd < len(line) && (line[indentEnd] == ' ' || line[indentEnd] == '\t') {
		indentEnd++
	}
	result := make([]byte, 0, indentEnd+len(key)+len(value)+8)
	result = append(result, line[:indentEnd]...)
	result = append(result, key...)
	result = append(result, " = "...)
	result = append(result, value...)
	result = append(result, tomlCommentSuffix(line)...)
	return result
}

func quotedTOMLString(value string) string {
	return strconv.Quote(value)
}

func spliceCodexTopLevel(data []byte, providerID, model string) ([]byte, error) {
	lines := splitCodexLines(data)
	values := map[string]string{
		"model_provider":           quotedTOMLString(providerID),
		"disable_response_storage": "true",
	}
	if model != "" {
		values["model"] = quotedTOMLString(model)
	}
	managedOrder := []string{"model_provider", "model", "disable_response_storage"}
	firstTable := len(lines)
	for i, line := range lines {
		if strings.HasPrefix(tomlHeader(line.content), "[") {
			firstTable = i
			break
		}
	}
	seen := map[string]bool{}
	removeModel := model == ""
	filtered := make([]codexLine, 0, len(lines)+len(managedOrder))
	for i, line := range lines {
		if i < firstTable {
			if key, ok := tomlLineKey(line.content); ok {
				if _, managed := values[key]; managed || key == "model" {
					if seen[key] {
						return nil, fmt.Errorf("duplicate top-level TOML key %q: %w", key, ErrUnsafeShape)
					}
					seen[key] = true
					if key == "model" && removeModel {
						if comment := tomlCommentSuffix(line.content); len(comment) > 0 {
							filtered = append(filtered, codexLine{content: bytes.TrimSpace(comment), ending: line.ending})
						}
						continue
					}
					filtered = append(filtered, codexLine{content: replaceTomlAssignment(line.content, key, values[key]), ending: line.ending})
					continue
				}
			}
		}
		filtered = append(filtered, line)
	}
	lines = filtered
	missing := make([]codexLine, 0, len(managedOrder))
	ending := codexLineEnding(lines, data)
	for _, key := range managedOrder {
		if _, required := values[key]; !required || seen[key] {
			continue
		}
		missing = append(missing, codexLine{content: []byte(key + " = " + values[key]), ending: ending})
	}
	if len(missing) == 0 {
		return joinCodexLines(lines), nil
	}
	return joinCodexLines(append(missing, lines...)), nil
}

func codexTableHeader(providerID string) string {
	return "[model_providers." + providerID + "]"
}

func codexTableBlock(providerID, name, baseURL string, lineEnding []byte, headerComment []byte, comments map[string][]byte, unmanaged []codexLine, finalEnding []byte) []codexLine {
	managed := []struct {
		key     string
		content string
	}{
		{key: "name", content: "name = " + quotedTOMLString(name)},
		{key: "base_url", content: "base_url = " + quotedTOMLString(baseURL)},
		{key: "wire_api", content: `wire_api = "responses"`},
		{key: "requires_openai_auth", content: "requires_openai_auth = true"},
	}
	lines := []codexLine{{content: append([]byte(codexTableHeader(providerID)), headerComment...), ending: append([]byte(nil), lineEnding...)}}
	for _, item := range managed {
		content := []byte(item.content)
		content = append(content, comments[item.key]...)
		lines = append(lines, codexLine{content: content, ending: append([]byte(nil), lineEnding...)})
	}
	for _, line := range unmanaged {
		lines = append(lines, codexLine{
			content: append([]byte(nil), line.content...),
			ending:  append([]byte(nil), line.ending...),
		})
	}
	lines[len(lines)-1].ending = append([]byte(nil), finalEnding...)
	return lines
}

func spliceCodexTable(data []byte, providerID, name, baseURL string) ([]byte, error) {
	lines := splitCodexLines(data)
	header := codexTableHeader(providerID)
	headerIndex := -1
	for i, line := range lines {
		lineHeader := tomlHeader(line.content)
		if lineHeader == "[[model_providers."+providerID+"]]" {
			return nil, fmt.Errorf("model_providers.%s is an array: %w", providerID, ErrUnsafeShape)
		}
		if lineHeader == header {
			headerIndex = i
			break
		}
	}
	lineEnding := codexLineEnding(lines, data)
	if headerIndex < 0 {
		block := codexTableBlock(providerID, name, baseURL, lineEnding, nil, nil, nil, lineEnding)
		result := append([]byte(nil), data...)
		if len(result) > 0 && !bytes.HasSuffix(result, []byte("\n")) {
			result = append(result, lineEnding...)
		}
		return append(result, joinCodexLines(block)...), nil
	}

	end := len(lines)
	for i := headerIndex + 1; i < len(lines); i++ {
		if strings.HasPrefix(tomlHeader(lines[i].content), "[") {
			end = i
			break
		}
	}
	finalEnding := lines[end-1].ending
	if end == headerIndex+1 {
		finalEnding = lines[headerIndex].ending
	}
	headerComment := tomlCommentSuffix(lines[headerIndex].content)
	comments := make(map[string][]byte)
	unmanaged := make([]codexLine, 0, end-headerIndex-1)
	for _, line := range lines[headerIndex+1 : end] {
		key, isAssignment := tomlLineKey(line.content)
		switch key {
		case "name", "base_url", "wire_api", "requires_openai_auth":
			if isAssignment {
				comments[key] = tomlCommentSuffix(line.content)
				continue
			}
		}
		unmanaged = append(unmanaged, line)
	}
	block := codexTableBlock(providerID, name, baseURL, lineEnding, headerComment, comments, unmanaged, finalEnding)
	replaced := make([]codexLine, 0, len(lines)-end+headerIndex+len(block))
	replaced = append(replaced, lines[:headerIndex]...)
	replaced = append(replaced, block...)
	replaced = append(replaced, lines[end:]...)
	return joinCodexLines(replaced), nil
}

// spliceCodexConfig uses a line scanner for byte-preserving edits. The scanner
// can misidentify table headers inside multiline strings or quoted keys; the
// final go-toml reparse below turns those cases into safe errors instead of
// silently writing corrupted configuration.
func spliceCodexConfig(data []byte, providerID, name, baseURL, model string) ([]byte, error) {
	topLevel, err := spliceCodexTopLevel(data, providerID, model)
	if err != nil {
		return nil, err
	}
	result, err := spliceCodexTable(topLevel, providerID, name, baseURL)
	if err != nil {
		return nil, err
	}
	doc, err := readTOMLBytes(result)
	if err != nil {
		return nil, fmt.Errorf("reparse spliced Codex config: %w", err)
	}
	if value, ok := doc["model_provider"].(string); !ok || value != providerID {
		return nil, fmt.Errorf("spliced Codex config has unexpected model_provider: %w", ErrUnsafeShape)
	}
	if model == "" {
		if _, exists := doc["model"]; exists {
			return nil, fmt.Errorf("spliced Codex config retained an empty model: %w", ErrUnsafeShape)
		}
	} else if value, ok := doc["model"].(string); !ok || value != model {
		return nil, fmt.Errorf("spliced Codex config has unexpected model: %w", ErrUnsafeShape)
	}
	if value, ok := doc["disable_response_storage"].(bool); !ok || !value {
		return nil, fmt.Errorf("spliced Codex config has unexpected disable_response_storage: %w", ErrUnsafeShape)
	}
	providers, ok := tomlMap(doc["model_providers"])
	if !ok {
		return nil, fmt.Errorf("spliced Codex config has no model_providers table: %w", ErrUnsafeShape)
	}
	entry, ok := tomlMap(providers[providerID])
	if !ok || tomlString(entry["name"]) != name || tomlString(entry["base_url"]) != baseURL || tomlString(entry["wire_api"]) != "responses" {
		return nil, fmt.Errorf("spliced Codex config has unexpected provider table: %w", ErrUnsafeShape)
	}
	if value, ok := entry["requires_openai_auth"].(bool); !ok || !value {
		return nil, fmt.Errorf("spliced Codex config has unexpected auth setting: %w", ErrUnsafeShape)
	}
	return result, nil
}

func patchCodexAuth(before []byte, apiKey string) ([]byte, error) {
	doc, err := parseJSONBytes(before)
	if err != nil {
		return nil, fmt.Errorf("parse Codex auth: %w", err)
	}
	root, err := jsonRootObject(&doc)
	if err != nil {
		return nil, fmt.Errorf("parse Codex auth: %w", err)
	}
	if err := requireUniqueKeys(root, "OPENAI_API_KEY"); err != nil {
		return nil, err
	}
	value, err := jsonValue(apiKey)
	if err != nil {
		return nil, err
	}
	if err := setObjectMember(root, "OPENAI_API_KEY", value); err != nil {
		return nil, err
	}
	return doc.Pack(), nil
}

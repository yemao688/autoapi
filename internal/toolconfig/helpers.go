package toolconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"
)

// MaskSecret intentionally reveals only whether a secret is present.
func MaskSecret(value string) string {
	if value == "" {
		return ""
	}
	return "********"
}

func providerKey(p Preset) string {
	if p.ProviderID != "" {
		return p.ProviderID
	}

	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(p.Name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	key := strings.Trim(b.String(), "-")
	if key == "" {
		return "provider"
	}
	return key
}

// ProviderKey returns the config-file provider key for a preset.
func ProviderKey(p Preset) string {
	return providerKey(p)
}

func validatePreset(p PresetPlaintext) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: name is empty", ErrInvalidPreset)
	}
	if p.Kind == PresetDirect && strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("%w: direct preset %q has an empty base URL", ErrInvalidPreset, p.Name)
	}
	return nil
}

func normalizedProviderKey(p Preset) (string, error) {
	if err := validatePreset(PresetPlaintext{Preset: p}); err != nil {
		return "", err
	}
	return providerKey(p), nil
}

func absoluteHomeDir(homeDir string) string {
	if filepath.IsAbs(homeDir) {
		return filepath.Clean(homeDir)
	}
	abs, err := filepath.Abs(homeDir)
	if err == nil {
		return abs
	}
	return filepath.Clean(homeDir)
}

// pathWithin reports whether target is base or a descendant of base after
// filepath.Clean. Rel is used instead of a string prefix so /home/app2 does
// not count as a child of /home/app.
func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func pathEscapesHome(path, homeDir string) error {
	return fmt.Errorf("path escapes home %q (home %q): %w", path, homeDir, ErrInvalidPreset)
}

// resolveNearestPath resolves the closest existing ancestor and joins the
// missing remainder back onto it. This is important for a missing file below a
// symlinked config directory: resolving the full missing path is impossible,
// but resolving its nearest ancestor still lets Commit target the real file.
func resolveNearestPath(path string) (string, bool, error) {
	current := filepath.Clean(path)
	var remainder []string
	for {
		info, err := os.Lstat(current)
		if err == nil {
			resolvedAncestor, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				if info.Mode()&os.ModeSymlink != 0 {
					remainder = append([]string{filepath.Base(current)}, remainder...)
					parent := filepath.Dir(current)
					if parent == current {
						return "", false, fmt.Errorf("resolve path %s: %w", path, evalErr)
					}
					current = parent
					continue
				}
				return "", false, fmt.Errorf("resolve path %s: %w", path, evalErr)
			}
			resolvedAncestor = filepath.Clean(resolvedAncestor)
			if len(remainder) > 0 {
				ancestorInfo, statErr := os.Stat(resolvedAncestor)
				if statErr != nil {
					return "", false, fmt.Errorf("stat resolved ancestor %s: %w", resolvedAncestor, statErr)
				}
				if !ancestorInfo.IsDir() {
					return "", false, fmt.Errorf("resolved ancestor %s is not a directory: %w", resolvedAncestor, ErrUnsafeShape)
				}
			}
			return filepath.Join(append([]string{resolvedAncestor}, remainder...)...), len(remainder) == 0, nil
		}
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("lstat %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, fmt.Errorf("no existing ancestor for %s: %w", path, err)
		}
		remainder = append([]string{filepath.Base(current)}, remainder...)
		current = parent
	}
}

func resolveManagedPath(path, homeDir string) (string, bool, error) {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", false, fmt.Errorf("resolve absolute path %s: %w", path, err)
	}
	cleanHome, err := filepath.Abs(filepath.Clean(homeDir))
	if err != nil {
		return "", false, fmt.Errorf("resolve absolute home %s: %w", homeDir, err)
	}
	if !pathWithin(cleanHome, cleanPath) {
		return "", false, pathEscapesHome(cleanPath, cleanHome)
	}
	resolvedHome := cleanHome
	if evaluatedHome, evalErr := filepath.EvalSymlinks(cleanHome); evalErr == nil {
		resolvedHome = filepath.Clean(evaluatedHome)
	}
	resolvedPath, exists, err := resolveNearestPath(cleanPath)
	if err != nil {
		return "", false, err
	}
	resolvedPath = filepath.Clean(resolvedPath)
	if !pathWithin(resolvedHome, resolvedPath) {
		return "", false, pathEscapesHome(resolvedPath, resolvedHome)
	}
	return resolvedPath, exists, nil
}

// snapshotFile resolves an existing symlink before taking the snapshot. A
// missing file is resolved through its nearest existing ancestor so a Commit
// can create it without replacing a symlinked directory. The variadic homeDir
// keeps this helper convenient for package-local callers while all adapters
// pass their validated home explicitly.
func snapshotFile(path string, homes ...string) (string, []byte, error) {
	homeDir := filepath.Dir(filepath.Clean(path))
	if len(homes) > 0 && homes[0] != "" {
		homeDir = homes[0]
	}
	resolved, exists, err := resolveManagedPath(path, homeDir)
	if err != nil {
		return "", nil, err
	}
	if !exists {
		return resolved, nil, nil
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("stat %s: %w", resolved, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s: %w", resolved, ErrUnsafeShape)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", resolved, err)
	}
	return resolved, data, nil
}

func changeForSnapshot(resource Resource, path string, secret bool, data []byte) FileChange {
	return FileChange{
		Resource:   resource,
		Path:       path,
		Secret:     secret,
		Before:     data,
		BeforeHash: hashBytes(data),
		Mode:       modeForChange(secret),
	}
}

func modeForChange(secret bool) uint32 {
	if secret {
		return 0o600
	}
	return 0o644
}

func jsonValue(value any) (hujson.Value, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return hujson.Value{}, err
	}
	return hujson.Parse(b)
}

// packFormatted serializes a hujson document and applies hujson's stable
// two-space formatter while retaining comments and HuJSON syntax.
func packFormatted(doc hujson.Value) ([]byte, error) {
	formatted, err := hujson.Format(doc.Pack())
	if err != nil {
		return nil, fmt.Errorf("format JSON document: %w", err)
	}
	return twoSpaceIndent(formatted), nil
}

func twoSpaceIndent(data []byte) []byte {
	result := make([]byte, 0, len(data))
	lineStart := true
	for _, char := range data {
		if lineStart && char == '\t' {
			result = append(result, ' ', ' ')
			continue
		}
		result = append(result, char)
		lineStart = char == '\n'
	}
	return result
}

func readJSONDocument(path string) (hujson.Value, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return hujson.Parse([]byte("{}"))
		}
		return hujson.Value{}, fmt.Errorf("read JSON file: %w", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}")
	}
	return hujson.Parse(b)
}

func jsonRootObject(doc *hujson.Value) (*hujson.Object, error) {
	object, ok := doc.Value.(*hujson.Object)
	if !ok {
		return nil, fmt.Errorf("root value is not an object: %w", ErrUnsafeShape)
	}
	return object, nil
}

func memberName(member hujson.ObjectMember) string {
	if literal, ok := member.Name.Value.(hujson.Literal); ok {
		return literal.String()
	}
	return ""
}

func objectMemberIndexes(object *hujson.Object, name string) []int {
	if object == nil {
		return nil
	}
	indexes := make([]int, 0, 1)
	for i, member := range object.Members {
		if memberName(member) == name {
			indexes = append(indexes, i)
		}
	}
	return indexes
}

func findUniqueObjectMember(object *hujson.Object, name string) (int, error) {
	indexes := objectMemberIndexes(object, name)
	if len(indexes) > 1 {
		return -1, fmt.Errorf("duplicate JSON key %q: %w", name, ErrUnsafeShape)
	}
	if len(indexes) == 0 {
		return -1, nil
	}
	return indexes[0], nil
}

// deleteObjectMember removes every member with the given name from an object.
// Callers must have established key uniqueness beforehand (requireUniqueKeys),
// so in practice at most one member is removed.
func deleteObjectMember(object *hujson.Object, name string) {
	if object == nil {
		return
	}
	kept := object.Members[:0]
	for _, member := range object.Members {
		if memberName(member) != name {
			kept = append(kept, member)
		}
	}
	object.Members = kept
}

func objectMemberValue(object *hujson.Object, name string) *hujson.Value {
	if object == nil {
		return nil
	}
	index := -1
	for i, member := range object.Members {
		if memberName(member) == name {
			index = i
			break
		}
	}
	if index < 0 {
		return nil
	}
	return &object.Members[index].Value
}

func objectString(object *hujson.Object, path ...string) string {
	current := object
	for i, name := range path {
		value := objectMemberValue(current, name)
		if value == nil {
			return ""
		}
		if i == len(path)-1 {
			if literal, ok := value.Value.(hujson.Literal); ok {
				return literal.String()
			}
			return ""
		}
		next, ok := value.Value.(*hujson.Object)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

func objectValue(object *hujson.Object, path ...string) *hujson.Value {
	current := object
	for i, name := range path {
		value := objectMemberValue(current, name)
		if value == nil {
			return nil
		}
		if i == len(path)-1 {
			return value
		}
		next, ok := value.Value.(*hujson.Object)
		if !ok {
			return nil
		}
		current = next
	}
	return nil
}

func requireUniqueKeys(object *hujson.Object, names ...string) error {
	for _, name := range names {
		if _, err := findUniqueObjectMember(object, name); err != nil {
			return err
		}
	}
	return nil
}

func requireUniqueObjectMembers(object *hujson.Object) error {
	if object == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(object.Members))
	for _, member := range object.Members {
		name := memberName(member)
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate JSON key %q: %w", name, ErrUnsafeShape)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func requireObjectMember(object *hujson.Object, name string) (*hujson.Object, bool, error) {
	index, err := findUniqueObjectMember(object, name)
	if err != nil {
		return nil, false, err
	}
	if index < 0 {
		return nil, false, nil
	}
	child, ok := object.Members[index].Value.Value.(*hujson.Object)
	if !ok {
		return nil, true, fmt.Errorf("managed JSON key %q is not an object: %w", name, ErrUnsafeShape)
	}
	return child, true, nil
}

func isWhitespace(data []byte) bool {
	for _, b := range data {
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return false
		}
	}
	return true
}

func memberIndent(object *hujson.Object) []byte {
	var extra []byte
	if len(object.Members) > 0 {
		extra = object.Members[len(object.Members)-1].Name.BeforeExtra
	} else {
		extra = object.AfterExtra
	}
	if isWhitespace(extra) {
		return append([]byte(nil), extra...)
	}
	if index := bytes.LastIndexByte(extra, '\n'); index >= 0 {
		return append([]byte(nil), extra[index:]...)
	}
	return []byte(" ")
}

func valueBeforeExtra(object *hujson.Object) []byte {
	if len(object.Members) > 0 {
		extra := object.Members[len(object.Members)-1].Value.BeforeExtra
		if isWhitespace(extra) && len(extra) > 0 {
			return append([]byte(nil), extra...)
		}
	}
	return []byte(" ")
}

func setObjectMember(object *hujson.Object, name string, value hujson.Value) error {
	index, err := findUniqueObjectMember(object, name)
	if err != nil {
		return err
	}
	if index >= 0 {
		old := object.Members[index].Value
		value.BeforeExtra = append([]byte(nil), old.BeforeExtra...)
		value.AfterExtra = append([]byte(nil), old.AfterExtra...)
		object.Members[index].Value = value
		return nil
	}
	value.BeforeExtra = valueBeforeExtra(object)
	object.Members = append(object.Members, hujson.ObjectMember{
		Name: hujson.Value{
			BeforeExtra: memberIndent(object),
			Value:       hujson.String(name),
		},
		Value: value,
	})
	return nil
}

func removeObjectMember(object *hujson.Object, name string) error {
	index, err := findUniqueObjectMember(object, name)
	if err != nil {
		return err
	}
	if index >= 0 {
		// A member's Name.BeforeExtra carries comments written immediately
		// before its key. Move that trivia to the next surviving member (or
		// the object's trailing trivia) so removal never drops unmanaged text.
		before := append([]byte(nil), object.Members[index].Name.BeforeExtra...)
		if index+1 < len(object.Members) {
			next := append([]byte(nil), before...)
			next = append(next, object.Members[index+1].Name.BeforeExtra...)
			object.Members[index+1].Name.BeforeExtra = next
		} else {
			object.AfterExtra = append(before, object.AfterExtra...)
		}
		object.Members = append(object.Members[:index], object.Members[index+1:]...)
	}
	return nil
}

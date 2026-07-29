package toolconfig

import (
	"os"
	"path/filepath"
)

// DefaultConfigPath returns the conventional primary configuration path for a
// supported tool under an absolute home directory.
func DefaultConfigPath(tool Tool, homeDir string) string {
	homeDir = absoluteHomeDir(homeDir)
	switch tool {
	case ToolOpencode:
		return filepath.Join(homeDir, ".config", "opencode", "opencode.json")
	case ToolCodex:
		return filepath.Join(homeDir, ".codex", "config.toml")
	case ToolClaude:
		return filepath.Join(homeDir, ".claude", "settings.json")
	default:
		return ""
	}
}

// ResolveConfigPath returns the effective primary config path for tool — the
// file the tool itself would load. opencode supports and prefers JSONC:
// opencode.jsonc wins when both files exist; opencode.json is used when only
// it exists; when neither exists the preferred creation target
// (opencode.jsonc) is returned with found=false. Other tools resolve to their
// conventional path.
func ResolveConfigPath(tool Tool, homeDir string) (path string, found bool) {
	if tool == ToolOpencode {
		dir := filepath.Join(absoluteHomeDir(homeDir), ".config", "opencode")
		jsonc := filepath.Join(dir, "opencode.jsonc")
		if pathExists(jsonc) {
			return jsonc, true
		}
		jsonPath := filepath.Join(dir, "opencode.json")
		if pathExists(jsonPath) {
			return jsonPath, true
		}
		return jsonc, false
	}
	path = DefaultConfigPath(tool, homeDir)
	if path == "" {
		return "", false
	}
	return path, pathExists(path)
}

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if err != nil {
			return false
		}
	}
	return info.Mode().IsRegular()
}

func detectPrimary(tool Tool, homeDir string) ToolStatus {
	primary, exists := ResolveConfigPath(tool, homeDir)
	return ToolStatus{
		Tool:         tool,
		Installed:    exists,
		ConfigPath:   primary,
		ConfigExists: exists,
		ExtraPaths:   map[string]string{},
	}
}

func detectOpencode(homeDir string) ToolStatus {
	status := detectPrimary(ToolOpencode, homeDir)
	dir := filepath.Dir(status.ConfigPath)
	jsonc := filepath.Join(dir, "oh-my-opencode-slim.jsonc")
	json := filepath.Join(dir, "oh-my-opencode-slim.json")
	status.ExtraPaths["omo_slim_config"] = ""
	if pathExists(jsonc) {
		status.ExtraPaths["omo_slim_config"] = jsonc
	} else if pathExists(json) {
		status.ExtraPaths["omo_slim_config"] = json
	}
	return status
}

func detectCodex(homeDir string) ToolStatus {
	status := detectPrimary(ToolCodex, homeDir)
	authPath := filepath.Join(absoluteHomeDir(homeDir), ".codex", "auth.json")
	status.ExtraPaths["auth_json"] = ""
	if pathExists(authPath) {
		status.ExtraPaths["auth_json"] = authPath
	}
	return status
}

func detectClaude(homeDir string) ToolStatus {
	return detectPrimary(ToolClaude, homeDir)
}

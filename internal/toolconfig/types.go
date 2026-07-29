// Package toolconfig manages provider presets for external AI coding tools
// (opencode, Codex CLI, Claude Code) and applies them to each tool's config
// files via managed-section read-modify-write with backup and drift detection.
//
// Safety contract every producer of a ChangeSet must honor:
//   - preserve all unmanaged content (unknown keys, comments, formatting of
//     untouched regions) — adapters patch owned leaves only, never replace
//     whole parent objects;
//   - fail closed with a readable error when an existing managed parent or
//     entry has an unexpected shape (array/scalar where an object is
//     expected, duplicate JSON keys on managed paths);
//   - all writes go through Commit, which snapshots, backs up, re-validates
//     hashes, and rolls back in reverse order on any failure;
//   - secret files (Codex auth.json) always get 0600 on the live file AND its
//     backups, even when a looser mode already exists.
//
// Path policy: adapters derive every path from the validated absolute homeDir
// handed in by the service layer; they never accept caller-supplied file
// paths. Existing symlinks are resolved with filepath.EvalSymlinks at Plan
// time and writes target the RESOLVED path, so an atomic rename never
// replaces the symlink itself. backupRoot is validated by the service layer
// (must live under the app data dir).
package toolconfig

import "errors"

// Tool identifies a supported external coding tool.
type Tool string

const (
	ToolOpencode Tool = "opencode"
	ToolCodex    Tool = "codex"
	ToolClaude   Tool = "claude"
)

// SupportedTools lists every tool with an adapter, in UI display order.
var SupportedTools = []Tool{ToolOpencode, ToolCodex, ToolClaude}

// Resource identifies one managed file of a tool. A tool may manage several
// files (Codex: config + auth; opencode: config + OMO Slim plugin config).
type Resource string

const (
	ResOpencodeConfig Resource = "opencode/config"
	// These string literals are persisted in tool_file_state and backup paths;
	// keep them unchanged when renaming the Go constants.
	ResOpencodeOmoSlim Resource = "opencode/omo"
	ResOmoSlimConfig   Resource = "opencode-omo"
	ResCodexConfig     Resource = "codex/config"
	ResCodexAuth       Resource = "codex/auth"
	ResClaudeSettings  Resource = "claude/settings"
)

// PresetKind distinguishes third-party direct presets from the Autoapi relay preset.
type PresetKind string

const (
	PresetDirect  PresetKind = "direct"  // third-party provider endpoint
	PresetAutoapi PresetKind = "autoapi" // relay through this Autoapi instance
)

// Preset is one named provider configuration for a tool.
//
// APIKeyEnc is stored ENCRYPTED (AES-256-GCM via service.Encrypt) and is only
// decrypted at apply/export time; adapters always receive plaintext via
// PresetPlaintext.
type Preset struct {
	ID         int64
	Tool       Tool
	Kind       PresetKind
	Name       string
	ProviderID string // config-file provider key; empty => derived from slugified Name
	Vendor     string // canonical interface-format key; mapped to an OpenCode npm package when rendered
	BaseURL    string // for PresetAutoapi this is resolved at apply time from current relay settings
	APIKeyEnc  string // encrypted; empty when the preset has no key
	APIKeyID   string // PresetAutoapi only: api_keys.id (UUID string; the id IS the relay token)
	Models     []PresetModel
	Extra      map[string]string // tool-specific extras (e.g. codex "wire_api")
	CreatedAt  int64
	UpdatedAt  int64
}

// PresetPlaintext is the decrypted view handed to adapters at plan/export time.
type PresetPlaintext struct {
	Preset
	APIKey string // decrypted plaintext ("" when none)
}

// PresetModel is one model entry inside a preset.
type PresetModel struct {
	Name       string                   `json:"name"`
	Limit      *ModelLimit              `json:"limit,omitempty"`
	Modalities []string                 `json:"modalities,omitempty"` // input modalities: text/image/video/audio/pdf
	Reasoning  bool                     `json:"reasoning,omitempty"`
	Variants   map[string]PresetVariant `json:"variants,omitempty"` // opencode variant name -> options object
	Default    bool                     `json:"default,omitempty"`  // set as the tool's default model pointer
}

// PresetVariant mirrors opencode's per-variant options object shape.
type PresetVariant struct {
	ReasoningEffort  string   `json:"reasoningEffort,omitempty"`
	ReasoningSummary string   `json:"reasoningSummary,omitempty"`
	Include          []string `json:"include,omitempty"`
}

// ModelLimit carries context/output token limits for a model.
type ModelLimit struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// ToolStatus reports detection of a tool installation and its managed files.
type ToolStatus struct {
	Tool           Tool
	Installed      bool              // primary config file exists
	ConfigPath     string            // conventional absolute path (always set, even when absent)
	ConfigExists   bool              // primary config file currently on disk
	ExtraPaths     map[string]string // secondary managed files, e.g. "auth_json", "omo_slim_config" ("" when absent)
	ActivePresetID int64             // from tool_state; 0 = never applied by us
	Drifted        bool
}

// FileChange is one file's part in an atomic multi-file apply.
type FileChange struct {
	Resource   Resource
	Path       string // resolved absolute path (symlinks evaluated at Plan time)
	Secret     bool   // enforce 0600 on the live file and its backups
	Before     []byte // snapshot at Plan time (nil = file absent)
	BeforeHash string // sha256 hex of Before ("" when absent)
	After      []byte // rendered content to write
	Mode       uint32 // mode for new files (0o600 when Secret else 0o644)
}

// FileCheck is a read-only hash dependency of a ChangeSet: Commit validates
// that the current on-disk hash of Path equals ExpectedHash before writing
// anything (e.g. an OMO Slim apply depending on opencode.json staying unchanged).
type FileCheck struct {
	Resource     Resource
	Path         string
	ExpectedHash string
}

// ChangeSet is the unit of commit: every file rendered from one captured
// snapshot. Produced by adapters (provider presets) and by the OMO Slim
// manager (Phase 2) — Commit is input-agnostic. Checks are validated but
// never written.
type ChangeSet struct {
	Tool    Tool
	Changes []FileChange
	Checks  []FileCheck
}

// CommitOpts controls Commit.
type CommitOpts struct {
	BackupRoot     string              // validated dir under the app data dir
	KeepBackups    int                 // newest N backups retained per resource (0 = keep all)
	ExpectedHashes map[Resource]string // per-resource hash from tool_file_state; missing entry = never applied
	AllowDrift     bool                // false => any mismatch vs ExpectedHashes aborts with ErrDrifted
}

// FileResult records the outcome for one file.
type FileResult struct {
	Resource   Resource
	Path       string
	BackupPath string
	Hash       string // sha256 of the file after commit
}

// CommitResult maps one result per committed change, in ChangeSet order.
type CommitResult struct {
	Files []FileResult
}

// Commit atomically applies a ChangeSet:
//  1. for every change, the current on-disk hash must equal BeforeHash AND
//     (when ExpectedHashes has an entry and !AllowDrift) equal it — any
//     mismatch aborts BEFORE any write (lost-update guard / drift);
//  2. every existing target is backed up (secret backups get 0600);
//  3. each file is written atomically (temp in same dir + fsync + rename),
//     secret files always end up 0600;
//  4. on any failure, already-written files are restored from their Before
//     snapshots in reverse order (files created by this commit are removed);
//  5. backups are pruned to KeepBackups per resource.
//
// Implemented in engine.go. Adapters never write files themselves.
//
// func Commit(cs *ChangeSet, opts CommitOpts) (*CommitResult, error)

// ManagedSection is the adapter-owned projection of a config file: the fields
// an apply would write, extracted from the current file. Used for drift
// import-back (file -> preset) and UI display of current state.
//
// REDACTION: values for sensitive keys (api keys, tokens — apiKey,
// ANTHROPIC_AUTH_TOKEN, OPENAI_API_KEY, …) MUST be replaced by MaskSecret
// output; plaintext secrets never leave the backend through this projection.
type ManagedSection struct {
	Present    bool              // false when the file has no managed section yet
	ProviderID string            // provider key found in the file ("" when none)
	BaseURL    string            // already safe to display
	Model      string            // default model pointer ("" when none)
	Fields     map[string]string // remaining managed fields, secrets masked
}

// MaskSecret renders a presence-only placeholder for a non-empty secret.
// Implemented alongside the adapters.
//
// func MaskSecret(v string) string // "" -> "", else "********"

// RawManagedSection is the backend-internal plaintext counterpart of
// ManagedSection, used for drift import-back (file -> preset).
type RawManagedSection struct {
	Present    bool
	ProviderID string
	Name       string
	Vendor     string
	BaseURL    string
	APIKey     string        // plaintext ("" when none)
	Model      string        // default model pointer
	Models     []PresetModel // best-effort reconstruction for import
}

// Snippet is an exportable config fragment for manual application on another machine.
type Snippet struct {
	TargetPath string // e.g. "~/.config/opencode/opencode.json"
	Format     string // "json" | "toml"
	Content    string
	Notes      string // human guidance (activation, env vars, etc.)
}

// Adapter owns managed-section planning for one tool's provider config.
// Adapters render ChangeSets; they never write files directly.
type Adapter interface {
	Tool() Tool
	// Detect resolves conventional paths under homeDir and reports presence.
	// Must not create files. ConfigPath is always the conventional path.
	Detect(homeDir string) ToolStatus
	// Plan snapshots current state and renders the ChangeSet for applying p.
	// Fails closed on unexpected shapes or unowned provider-ID collisions.
	Plan(p PresetPlaintext, homeDir string) (*ChangeSet, error)
	// PlanRemoval snapshots current state and renders the ChangeSet that removes
	// providerID's managed section. Fails closed on unexpected shapes. Returns
	// an ErrConfigNotFound-classified error when the provider is absent.
	PlanRemoval(homeDir, providerID string) (*ChangeSet, error)
	// ReadManaged extracts the managed section for providerID (required —
	// callers derive it from persisted state, never "any provider").
	// Secrets in the result are masked. Zero value + nil error when absent.
	ReadManaged(homeDir, providerID string) (ManagedSection, error)
	// ReadManagedRaw extracts the managed section WITH plaintext secrets.
	// Backend-internal only (R9 drift import-back); the result MUST NEVER be
	// exposed through Wails bindings or logs.
	ReadManagedRaw(homeDir, providerID string) (RawManagedSection, error)
	// ExportSnippet renders config text + guidance for manual paste elsewhere.
	// No writes, no decryption (input is already plaintext); homeDir is only
	// used to point TargetPath at the config file actually in effect (e.g.
	// opencode.jsonc vs opencode.json).
	ExportSnippet(p PresetPlaintext, homeDir string) (Snippet, error)
}

// ToolState is the persisted per-tool apply bookkeeping (table tool_state).
type ToolState struct {
	Tool           Tool
	ActivePresetID int64
	ConfigPath     string
	AppliedAt      int64
}

// ToolFileState is the persisted per-resource hash state (table tool_file_state),
// the basis of drift detection for every managed file independently.
type ToolFileState struct {
	Tool            Tool
	Resource        Resource
	Path            string
	AppliedFileHash string
	AppliedAt       int64
}

// Sentinel errors for the service layer to classify failures.
var (
	ErrToolNotInstalled = errors.New("toolconfig: tool not installed")
	ErrConfigNotFound   = errors.New("toolconfig: config file not found")
	ErrDrifted          = errors.New("toolconfig: config file changed externally since last apply")
	ErrInvalidPreset    = errors.New("toolconfig: preset failed validation")
	ErrConflict         = errors.New("toolconfig: conflicting existing content")
	ErrUnsafeShape      = errors.New("toolconfig: managed section has unexpected shape")
)

// DefaultConfigPath returns the conventional config path for a tool under homeDir.
// Implemented in detect.go:
//   - opencode: ~/.config/opencode/opencode.jsonc (preferred) or opencode.json
//   - codex:    ~/.codex/config.toml   (+ extra auth_json: ~/.codex/auth.json)
//   - claude:   ~/.claude/settings.json
//
// HashFile (drift.go) computes the sha256 hex of a file; "" when absent.
// BackupFile (backup.go) copies a file next to the source with a localtime
// timestamped sibling name and prunes source-directory backups; secret
// backups are written 0600. CommitOpts.BackupRoot remains for compatibility
// with the legacy central backup tree and validation of existing restores.

package store

import (
	"database/sql"
	"fmt"
	"time"
)

// migration defines a single schema migration.
type migration struct {
	ID  string // unique identifier, e.g. "001_init"
	SQL string // DDL / DML to apply
}

// migrations is the ordered list of all schema changes.
// Apply new entries at the end — never modify existing ones.
var migrations = []migration{
	{
		ID: "001_init",
		SQL: `
CREATE TABLE IF NOT EXISTS providers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'unknown',
    api_key_id TEXT,
    api_key_ref TEXT,
    models_count INTEGER NOT NULL DEFAULT 0,
    monthly_tokens INTEGER NOT NULL DEFAULT 0,
    avg_latency_ms INTEGER NOT NULL DEFAULT 0,
    last_tested_at INTEGER NOT NULL DEFAULT 0,
    error_message TEXT NOT NULL DEFAULT '',
    is_custom INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    context_window INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    UNIQUE(provider_id, name)
);

CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    provider_id TEXT,
    name TEXT NOT NULL,
    key_prefix TEXT NOT NULL DEFAULT '',
    key_suffix TEXT NOT NULL DEFAULT '',
    key_ciphertext BLOB,
    key_nonce BLOB,
    permission TEXT NOT NULL DEFAULT 'read_write',
    environment TEXT NOT NULL DEFAULT 'production',
    monthly_tokens INTEGER NOT NULL DEFAULT 0,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS routes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS route_conditions (
    id TEXT PRIMARY KEY,
    route_id TEXT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
    field TEXT NOT NULL,
    operator TEXT NOT NULL,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS route_targets (
    id TEXT PRIMARY KEY,
    route_id TEXT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL,
    model_name TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL DEFAULT 'forward'
);

CREATE TABLE IF NOT EXISTS request_logs (
    id TEXT PRIMARY KEY,
    timestamp_ms INTEGER NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    provider_id TEXT NOT NULL DEFAULT '',
    provider_name TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    route_id TEXT NOT NULL DEFAULT '',
    route_label TEXT NOT NULL DEFAULT '',
    api_key_id TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_request_logs_ts ON request_logs(timestamp_ms DESC);
CREATE INDEX IF NOT EXISTS idx_request_logs_provider ON request_logs(provider_id);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS master_password (
    id INTEGER PRIMARY KEY CHECK(id=1),
    salt BLOB NOT NULL,
    hash BLOB NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS _migrations (
    id TEXT PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
`,
	},
	// Future migrations append here:
	// {ID: "002_xxx", SQL: `...`},
}

// migrate applies all pending migrations in a single transaction.
func migrate(db *sql.DB) error {
	// Ensure the migrations tracking table exists first.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (id TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: create _migrations table: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin migrate tx: %w", err)
	}
	defer tx.Rollback() // no-op if committed

	now := time.Now().UnixMilli()

	for _, m := range migrations {
		// Check if already applied.
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE id = ?`, m.ID).Scan(&count); err != nil {
			return fmt.Errorf("store: check migration %q: %w", m.ID, err)
		}
		if count > 0 {
			continue
		}

		if _, err := tx.Exec(m.SQL); err != nil {
			return fmt.Errorf("store: apply migration %q: %w", m.ID, err)
		}
		if _, err := tx.Exec(`INSERT INTO _migrations (id, applied_at) VALUES (?, ?)`, m.ID, now); err != nil {
			return fmt.Errorf("store: record migration %q: %w", m.ID, err)
		}
	}

	return tx.Commit()
}

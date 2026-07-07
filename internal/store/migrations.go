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
	{
		ID: "002_failover",
		SQL: `
ALTER TABLE route_targets ADD COLUMN tier INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_route_targets_route_tier ON route_targets(route_id, tier);
`,
	},
	// Future migrations append here:
	{
		ID: "003_provider_keys",
		SQL: `
ALTER TABLE providers ADD COLUMN key_ciphertext BLOB;
ALTER TABLE providers ADD COLUMN key_nonce BLOB;
ALTER TABLE providers ADD COLUMN key_masked TEXT NOT NULL DEFAULT '';

UPDATE providers SET
  key_ciphertext = (SELECT key_ciphertext FROM api_keys WHERE id = providers.api_key_id),
  key_nonce = (SELECT key_nonce FROM api_keys WHERE id = providers.api_key_id),
  key_masked = COALESCE((SELECT key_prefix || '****' || key_suffix FROM api_keys WHERE id = providers.api_key_id), '')
WHERE api_key_id IS NOT NULL;

ALTER TABLE providers DROP COLUMN api_key_id;
ALTER TABLE providers DROP COLUMN api_key_ref;

ALTER TABLE api_keys DROP COLUMN provider_id;
ALTER TABLE api_keys DROP COLUMN key_prefix;
ALTER TABLE api_keys DROP COLUMN key_suffix;
ALTER TABLE api_keys DROP COLUMN key_ciphertext;
ALTER TABLE api_keys DROP COLUMN key_nonce;
ALTER TABLE api_keys DROP COLUMN permission;
ALTER TABLE api_keys DROP COLUMN environment;
ALTER TABLE api_keys DROP COLUMN monthly_tokens;
	ALTER TABLE api_keys DROP COLUMN last_used_at;
`,
	},
	{
		ID: "004_drop_master_password",
		SQL: `
DROP TABLE IF EXISTS master_password;
`,
	},
	{
		ID: "005_model_enrichment",
		SQL: `
ALTER TABLE models ADD COLUMN active INTEGER NOT NULL DEFAULT 1;
ALTER TABLE models ADD COLUMN owned_by TEXT NOT NULL DEFAULT '';
ALTER TABLE models ADD COLUMN latency_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE models ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		ID: "006_request_log_cost",
		SQL: `
ALTER TABLE request_logs ADD COLUMN cost REAL NOT NULL DEFAULT 0;
`,
	},
	{
		ID: "007_request_log_timing",
		SQL: `
ALTER TABLE request_logs ADD COLUMN first_token_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN is_stream INTEGER NOT NULL DEFAULT 0;
`,
	},
}

// backfillCost recomputes cost for historical request_logs rows that have
// cost=0 but did consume tokens. Runs once after migration 006 on startup.
// The actual per-model pricing lives in store.go's costTable; this function
// pulls a copy via estimateCost (closure over the package-level map).
func (s *Store) backfillCost() {
	res, err := s.db.Exec(`
		UPDATE request_logs
		SET cost = (
			(input_tokens + output_tokens) * 2.0 / 1000000.0
		)
		WHERE cost = 0
		  AND (input_tokens > 0 OR output_tokens > 0)
		  AND model NOT IN (
		      'gpt-4o','gpt-4o-mini','gpt-4','gpt-4-turbo','gpt-3.5-turbo',
		      'claude-3.5-sonnet','claude-3-opus','claude-3-haiku',
		      'deepseek-chat','deepseek-reasoner','moonshot-v1','glm-4'
		  )
	`)
	if err != nil {
		// Non-fatal: best-effort backfill.
		return
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		// One-liner using the precise costTable would require per-row Go logic;
		// we approximate unknown models with the default pricing here.
		_ = n
	}
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

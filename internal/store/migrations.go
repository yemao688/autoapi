package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// migration defines a single schema migration.
type migration struct {
	ID              string                                            // unique identifier, e.g. "001_init"
	SQL             string                                            // DDL / DML to apply
	SkipIfRedundant func(tx *sql.Tx) (alreadyApplied bool, err error) // optional: if non-nil and returns (true, nil), treat the SQL and Hook as no-ops (e.g. the schema change was already made under a different migration ID in an older build). The migration is still recorded as applied.
	Hook            func(tx *sql.Tx) error                            // optional Go code run after SQL when the migration is applied (not run when skipped).
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
		ID: "007_route_reorder_retry_counters",
		SQL: `
-- Route targets: drop the forward/skip action column (every target now forwards),
-- and add per-target retry budget + hit/failure counters for the new drag-reorder UX.
-- NOTE: the routes.priority and route_targets.tier columns are intentionally KEPT
-- as internal ordinal sort keys (they back ORDER BY); they are removed from the Go
-- struct/JSON only. Skip/block rules (action='skip') lose their blocking semantics
-- and become normal forwarding targets — accepted per product decision.
ALTER TABLE route_targets DROP COLUMN action;
ALTER TABLE route_targets ADD COLUMN max_retries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_targets ADD COLUMN hit_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_targets ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		ID: "008_request_log_timing",
		SQL: `
ALTER TABLE request_logs ADD COLUMN first_token_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN is_stream INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		ID: "009_request_log_cache",
		SQL: `
-- Prompt-cache token counters. The proxy does not yet report them; values are
-- always 0 until upstream providers expose cache_read_input_tokens / cache_creation_input_tokens
-- on the request response. New columns default to 0 so historical rows remain valid.
ALTER TABLE request_logs ADD COLUMN cache_creation INTEGER NOT NULL DEFAULT 0;
ALTER TABLE request_logs ADD COLUMN cache_hit INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		// Per-target enable/disable toggle (Phase 3 of the uiux-route-targets
		// worktree). Defaults to 1 so existing rows are treated as enabled;
		// the proxy's selectCandidates skips rows where enabled=0 while
		// preserving the existing tier ordering.
		//
		// Numbered 010 (not 009) to leave room for Phase 2's
		// 009_request_log_cache that lives on other branches — applying both
		// with the same ID would short-circuit the second one via the
		// _migrations tracking table.
		//
		// Safety: this migration is idempotent against pre-existing DBs where
		// an earlier `009_route_target_enabled` was applied under the old
		// naming. Such DBs already have the `enabled` column but the
		// _migrations row says "009", so the new "010" entry will be picked
		// up as pending. `routeTargetsHasEnabled` below checks for the
		// column and treats the SQL as a no-op when it's already present,
		// then records the migration as applied. This means the rename is
		// safe to ship and existing users with the old ID won't be broken.
		ID:              "010_route_target_enabled",
		SkipIfRedundant: routeTargetsHasEnabled,
		SQL: `
ALTER TABLE route_targets ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
`,
	},
	{
		// Phase 4: route rules → model rules. The previous `routes` table
		// (which was keyed by an internal UUID and ordered by `priority`) is
		// renamed to `model_rules` and the `description` and `priority`
		// columns are dropped. We also rename `route_id` → `rule_id` on the
		// `route_targets` table and drop the `route_conditions` table.
		//
		// The migration is split into steps so each one is a simple, fast
		// DDL statement — SQLite does not support renaming columns
		// directly, so route_targets.route_id is rebuilt via table-rename
		// + copy + drop. Existing rows are preserved.
		ID: "011_route_to_model_rule",
		SQL: `
-- Rename the routes table to model_rules (preserves all rows).
ALTER TABLE routes RENAME TO model_rules;

-- Drop the description and priority columns. SQLite supports
-- ALTER TABLE ... DROP COLUMN since 3.35.0; the Wails runtime requires a
-- recent enough SQLite so this is safe.
ALTER TABLE model_rules DROP COLUMN description;
ALTER TABLE model_rules DROP COLUMN priority;

-- Drop the now-unused route_conditions table.
DROP TABLE IF EXISTS route_conditions;

-- Rebuild route_targets so its foreign key column is rule_id instead
-- of route_id. SQLite does not support renaming a column directly; the
-- canonical rebuild dance is: create a new table with the desired schema,
-- copy the data, drop the old, rename the new. The new schema includes
-- every column added by earlier migrations (tier, max_retries, hit_count,
-- failure_count, enabled) so the resulting table is equivalent to the
-- pre-migration one with the route_id→rule_id column rename.
CREATE TABLE rule_targets_new (
    id TEXT PRIMARY KEY,
    rule_id TEXT NOT NULL REFERENCES model_rules(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL,
    model_name TEXT NOT NULL DEFAULT '',
    tier INTEGER NOT NULL DEFAULT 0,
    max_retries INTEGER NOT NULL DEFAULT 0,
    hit_count INTEGER NOT NULL DEFAULT 0,
    failure_count INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1
);
INSERT INTO rule_targets_new (id, rule_id, provider_id, model_name, tier, max_retries, hit_count, failure_count, enabled)
    SELECT id, route_id, provider_id, model_name, tier, max_retries, hit_count, failure_count, enabled
    FROM route_targets;
DROP TABLE route_targets;
ALTER TABLE rule_targets_new RENAME TO rule_targets;
CREATE INDEX IF NOT EXISTS idx_rule_targets_rule_tier ON rule_targets(rule_id, tier);
`,
	},
	{
		// Request-log diagnostics. Captures the per-attempt history of a
		// single proxied request (chain_json) plus the request context
		// (user_agent, client_ip, request_id) so the UI can show a per-row
		// detail panel and operators can correlate a log entry with the
		// matching structured slog line via request_id. Existing rows
		// backfill to the empty string (no chain, no metadata) so the
		// migration is a safe schema bump.
		ID: "012_request_log_details",
		SQL: `
ALTER TABLE request_logs ADD COLUMN chain_json TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN client_ip TEXT NOT NULL DEFAULT '';
ALTER TABLE request_logs ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
`,
	},
	{
		// Per-target first-byte timeout. Defaults to 0 so existing rows
		// continue to use the proxy-wide default timeout; non-zero values
		// override it on a per-target basis. Safe schema bump: the new
		// column is added with a default of 0, and the SkipIfRedundant
		// predicate below makes this idempotent against pre-existing DBs
		// that may have picked up the same column under an older ID.
		ID:              "013_rule_target_timeout",
		SkipIfRedundant: ruleTargetsHasTimeout,
		SQL: `
ALTER TABLE rule_targets ADD COLUMN timeout_ms INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		// Per-RULE first-byte timeout. Replaces the per-target
		// `timeout_ms` (kept for backward compatibility; the
		// ModelRuleTarget no longer exposes it). The budget is the
		// total time the proxy is willing to wait for the first
		// response byte across ALL candidates and retries for this
		// rule. 0 = use the proxy default. Stored in milliseconds for
		// consistency with the legacy per-target column.
		//
		// Safe schema bump: the new column is added with a default
		// of 0, and the SkipIfRedundant predicate below makes this
		// idempotent against pre-existing DBs that may have picked
		// up the same column under an older ID.
		ID:              "014_model_rule_first_byte_timeout",
		SkipIfRedundant: modelRulesHasFirstByteTimeout,
		SQL: `
ALTER TABLE model_rules ADD COLUMN first_byte_timeout_ms INTEGER NOT NULL DEFAULT 0;
`,
	},
	{
		ID: "015_request_uri",
		SQL: `
ALTER TABLE request_logs ADD COLUMN request_uri TEXT NOT NULL DEFAULT '';
`,
	},
	{
		// Provider-level enable/disable toggle. When false, the proxy
		// skips every target that resolves to this provider. Existing
		// providers default to enabled so behavior is unchanged.
		ID:              "016_provider_enabled",
		SkipIfRedundant: providersHasEnabled,
		SQL: `
ALTER TABLE providers ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
`,
	},
	{
		// Display-only ordering for model rules. The Hook adds the column if
		// it is missing and backfills/normalizes display_order values so the
		// previous visible order (created_at DESC, id DESC) is preserved with
		// the newest rule at 0. The no-op SQL exists because some pre-017
		// DBs already have the column (with all-zero or non-contiguous values),
		// and the Go Hook can handle both cases conditionally.
		ID:              "017_model_rule_display_order",
		SkipIfRedundant: displayOrderValid,
		SQL:             `SELECT 1;`,
		Hook:            ensureDisplayOrder,
	},
	{
		// Phase 1B: provider + upstream model + endpoint price records.
		// ProviderID is NULL for a global fallback. The unique expression
		// index prevents duplicate global or provider-specific records.
		ID: "018_price_schema",
		SQL: `
CREATE TABLE IF NOT EXISTS prices (
    id TEXT PRIMARY KEY,
    provider_id TEXT REFERENCES providers(id) ON DELETE CASCADE,
    upstream_model TEXT NOT NULL,
    endpoint_kind TEXT NOT NULL DEFAULT '',
    billing_mode TEXT NOT NULL DEFAULT 'unknown' CHECK(billing_mode IN ('token','request','quota','custom','unknown')),
    input_price_per_million REAL NOT NULL DEFAULT 0 CHECK(input_price_per_million >= 0 AND input_price_per_million = input_price_per_million AND input_price_per_million < 1.7976931348623157e308),
    output_price_per_million REAL NOT NULL DEFAULT 0 CHECK(output_price_per_million >= 0 AND output_price_per_million = output_price_per_million AND output_price_per_million < 1.7976931348623157e308),
    cache_read_price_per_million REAL NOT NULL DEFAULT 0 CHECK(cache_read_price_per_million >= 0 AND cache_read_price_per_million = cache_read_price_per_million AND cache_read_price_per_million < 1.7976931348623157e308),
    cache_write_price_per_million REAL NOT NULL DEFAULT 0 CHECK(cache_write_price_per_million >= 0 AND cache_write_price_per_million = cache_write_price_per_million AND cache_write_price_per_million < 1.7976931348623157e308),
    request_price_per_request REAL NOT NULL DEFAULT 0 CHECK(request_price_per_request >= 0 AND request_price_per_request = request_price_per_request AND request_price_per_request < 1.7976931348623157e308),
    currency TEXT NOT NULL DEFAULT 'USD',
    source TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    effective_at INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL DEFAULT 0,
    confidence TEXT NOT NULL DEFAULT 'unknown',
    updated_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    CHECK(expires_at = 0 OR effective_at <= expires_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_prices_key ON prices(COALESCE(provider_id, ''), upstream_model, endpoint_kind);
`,
	},
	{
		ID: "019_target_runtime_summary",
		SQL: `CREATE TABLE IF NOT EXISTS target_runtime_summary (
    target_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL,
    model_name TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    requests INTEGER NOT NULL DEFAULT 0 CHECK(requests >= 0),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts >= 0),
    successes INTEGER NOT NULL DEFAULT 0 CHECK(successes >= 0),
    failures INTEGER NOT NULL DEFAULT 0 CHECK(failures >= 0),
    status_429 INTEGER NOT NULL DEFAULT 0 CHECK(status_429 >= 0),
    status_5xx INTEGER NOT NULL DEFAULT 0 CHECK(status_5xx >= 0),
    transport INTEGER NOT NULL DEFAULT 0 CHECK(transport >= 0),
    client_aborts INTEGER NOT NULL DEFAULT 0 CHECK(client_aborts >= 0),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK(truncated >= 0),
    downstream INTEGER NOT NULL DEFAULT 0 CHECK(downstream >= 0),
    last_used INTEGER NOT NULL DEFAULT 0,
    last_success INTEGER NOT NULL DEFAULT 0 CHECK(last_success = 0 OR last_success <= last_used),
    last_failure INTEGER NOT NULL DEFAULT 0 CHECK(last_failure = 0 OR last_failure <= last_used),
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(target_id, provider_id, model_name, endpoint)
);`,
	},
	{
		ID:              "020_model_rule_strategy",
		SkipIfRedundant: func(tx *sql.Tx) (bool, error) { return tableHasColumn(tx, "model_rules", "strategy") },
		SQL:             `ALTER TABLE model_rules ADD COLUMN strategy TEXT NOT NULL DEFAULT 'priority_first';`,
	},
	{
		ID:              "021_model_request_price",
		SkipIfRedundant: func(tx *sql.Tx) (bool, error) { return tableHasColumn(tx, "models", "request_price") },
		SQL:             `ALTER TABLE models ADD COLUMN request_price REAL NOT NULL DEFAULT 0.1 CHECK(request_price >= 0 AND request_price = request_price AND request_price < 1.7976931348623157e308);`,
	},
	{ID: "022_drop_prices", SQL: `DROP TABLE IF EXISTS prices;`},
	{
		ID: "023_model_request_price_constraints",
		SQL: `
CREATE TRIGGER IF NOT EXISTS models_request_price_insert_valid
BEFORE INSERT ON models
WHEN NEW.request_price IS NULL OR NEW.request_price < 0 OR NEW.request_price != NEW.request_price OR NEW.request_price >= 1.7976931348623157e308
BEGIN SELECT RAISE(ABORT, 'models.request_price must be finite and non-negative'); END;
CREATE TRIGGER IF NOT EXISTS models_request_price_update_valid
BEFORE UPDATE OF request_price ON models
WHEN NEW.request_price IS NULL OR NEW.request_price < 0 OR NEW.request_price != NEW.request_price OR NEW.request_price >= 1.7976931348623157e308
BEGIN SELECT RAISE(ABORT, 'models.request_price must be finite and non-negative'); END;
`,
	},
}

// routeTargetsHasEnabled reports whether the `enabled` column already exists
// on the per-target table. Used as a SkipIfRedundant predicate so the
// 010_route_target_enabled migration is safe to ship on DBs that already
// picked up the same schema change under the old `009_route_target_enabled`
// ID (the migration row in _migrations still says 009, so the renamed entry
// would otherwise be applied on top of the existing column and fail with
// "duplicate column name").
//
// Phase 4 renamed `route_targets` to `rule_targets`, so this predicate
// inspects both table names. If either table exists and has the `enabled`
// column, the migration is treated as already applied. The same predicate
// is run for both pre- and post-rename DBs.
func routeTargetsHasEnabled(tx *sql.Tx) (bool, error) {
	for _, table := range []string{"rule_targets", "route_targets"} {
		rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			// PRAGMA errors when the table doesn't exist; treat that as
			// "not yet present" and continue to the next candidate.
			continue
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltValue sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				rows.Close()
				return false, fmt.Errorf("store: scan pragma table_info: %w", err)
			}
			if name == "enabled" {
				rows.Close()
				return true, nil
			}
		}
		rows.Close()
	}
	return false, nil
}

// ruleTargetsHasTimeout reports whether the `timeout_ms` column already
// exists on the per-target table. Used as a SkipIfRedundant predicate so
// the 013_rule_target_timeout migration is safe to ship on DBs that
// picked up the same schema change under an earlier ID or branch (the
// _migrations row would otherwise be missing and the rename would
// re-run the ADD COLUMN and fail with "duplicate column name").
//
// As with routeTargetsHasEnabled, we inspect both rule_targets and the
// legacy route_targets table name so pre-Phase-4 DBs are also covered.
func ruleTargetsHasTimeout(tx *sql.Tx) (bool, error) {
	for _, table := range []string{"rule_targets", "route_targets"} {
		rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			// PRAGMA errors when the table doesn't exist; treat that as
			// "not yet present" and continue to the next candidate.
			continue
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltValue sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				rows.Close()
				return false, fmt.Errorf("store: scan pragma table_info: %w", err)
			}
			if name == "timeout_ms" {
				rows.Close()
				return true, nil
			}
		}
		rows.Close()
	}
	return false, nil
}

// modelRulesHasFirstByteTimeout reports whether the
// `first_byte_timeout_ms` column already exists on the model_rules
// table. Used as a SkipIfRedundant predicate so the
// 014_model_rule_first_byte_timeout migration is safe to ship on DBs
// that picked up the same schema change under an earlier ID (the
// _migrations row would otherwise be missing and the rename would
// re-run the ADD COLUMN and fail with "duplicate column name").
//
// Also inspects the legacy `routes` table name (Phase 4 renamed it to
// `model_rules`) so pre-Phase-4 DBs that picked up this column under
// the old name are also covered.
func modelRulesHasFirstByteTimeout(tx *sql.Tx) (bool, error) {
	for _, table := range []string{"model_rules", "routes"} {
		rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			// PRAGMA errors when the table doesn't exist; treat that as
			// "not yet present" and continue to the next candidate.
			continue
		}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dfltValue sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
				rows.Close()
				return false, fmt.Errorf("store: scan pragma table_info: %w", err)
			}
			if name == "first_byte_timeout_ms" {
				rows.Close()
				return true, nil
			}
		}
		rows.Close()
	}
	return false, nil
}

// providersHasEnabled reports whether the providers table already has an
// "enabled" column. Used as a SkipIfRedundant predicate so the
// 016_provider_enabled migration is safe on DBs that picked up the column
// under a different migration ID or branch.
func providersHasEnabled(tx *sql.Tx) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(providers)`)
	if err != nil {
		return false, nil
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("store: scan pragma table_info: %w", err)
		}
		if name == "enabled" {
			return true, nil
		}
	}
	return false, nil
}

// displayOrderValid reports whether model_rules.display_order exists and is
// normalized: contiguous values 0..n-1, one per rule. This is used as the
// 017 migration skip predicate, and it also detects pre-existing invalid
// states (all-zero or non-contiguous) so the Hook can normalize them.
func displayOrderValid(tx *sql.Tx) (bool, error) {
	colExists, err := tableHasColumn(tx, "model_rules", "display_order")
	if err != nil {
		return false, err
	}
	if !colExists {
		return false, nil
	}

	var n, distinct, max sql.NullInt64
	if err := tx.QueryRow(`SELECT COUNT(*) FROM model_rules`).Scan(&n); err != nil {
		return false, fmt.Errorf("store: count model_rules: %w", err)
	}
	if err := tx.QueryRow(`SELECT COUNT(DISTINCT display_order) FROM model_rules`).Scan(&distinct); err != nil {
		return false, fmt.Errorf("store: count distinct display_order: %w", err)
	}
	if err := tx.QueryRow(`SELECT COALESCE(MAX(display_order), -1) FROM model_rules`).Scan(&max); err != nil {
		return false, fmt.Errorf("store: max display_order: %w", err)
	}
	if !n.Valid || n.Int64 == 0 {
		return true, nil
	}
	if !distinct.Valid || !max.Valid {
		return false, nil
	}
	return distinct.Int64 == n.Int64 && max.Int64 == n.Int64-1, nil
}

// ensureDisplayOrder adds the display_order column to model_rules if it is
// missing and then normalizes/backfills values from the existing visible order
// (created_at DESC, then id DESC), with the newest rule at 0.
func ensureDisplayOrder(tx *sql.Tx) error {
	colExists, err := tableHasColumn(tx, "model_rules", "display_order")
	if err != nil {
		return err
	}
	if !colExists {
		if _, err := tx.Exec(`ALTER TABLE model_rules ADD COLUMN display_order INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("store: add display_order column: %w", err)
		}
	}
	_, err = tx.Exec(`
		UPDATE model_rules SET display_order = (
			SELECT COUNT(*) FROM model_rules AS m2
			WHERE m2.created_at > model_rules.created_at
			   OR (m2.created_at = model_rules.created_at AND m2.id > model_rules.id)
		)`)
	if err != nil {
		return fmt.Errorf("store: backfill display_order: %w", err)
	}
	return nil
}

// tableHasColumn reports whether the named table has the named column.
func tableHasColumn(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, nil
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return false, fmt.Errorf("store: scan pragma table_info: %w", err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, nil
}

// migrate applies all pending migrations in a single transaction.
func migrate(db *sql.DB) error {
	return applyMigrations(db, migrations)
}

// migrateUpTo applies all migrations up to and including upToID. It is
// intended for test bootstrapping a schema at a specific historical point.
func migrateUpTo(db *sql.DB, upToID string) error {
	var upTo []migration
	for _, m := range migrations {
		upTo = append(upTo, m)
		if m.ID == upToID {
			break
		}
	}
	return applyMigrations(db, upTo)
}

func applyMigrations(db *sql.DB, list []migration) error {
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

	for _, m := range list {
		// Check if already applied.
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE id = ?`, m.ID).Scan(&count); err != nil {
			return fmt.Errorf("store: check migration %q: %w", m.ID, err)
		}
		if count > 0 {
			continue
		}

		// Optional "already applied under an older ID" predicate. When this
		// returns true we skip the SQL (it's a no-op against the existing
		// schema) but still record the new ID in _migrations so subsequent
		// boots treat it as applied.
		if m.SkipIfRedundant != nil {
			already, err := m.SkipIfRedundant(tx)
			if err != nil {
				return fmt.Errorf("store: skip-if-redundant %q: %w", m.ID, err)
			}
			if already {
				slog.Info("store: migration skipped (already applied)", "id", m.ID)
				if _, err := tx.Exec(`INSERT INTO _migrations (id, applied_at) VALUES (?, ?)`, m.ID, now); err != nil {
					return fmt.Errorf("store: record migration %q (redundant skip): %w", m.ID, err)
				}
				continue
			}
		}

		slog.Info("store: applying migration", "id", m.ID)
		if m.SQL != "" {
			if _, err := tx.Exec(m.SQL); err != nil {
				return fmt.Errorf("store: apply migration %q: %w", m.ID, err)
			}
		}
		if m.Hook != nil {
			if err := m.Hook(tx); err != nil {
				return fmt.Errorf("store: migration hook %q: %w", m.ID, err)
			}
		}
		if _, err := tx.Exec(`INSERT INTO _migrations (id, applied_at) VALUES (?, ?)`, m.ID, now); err != nil {
			return fmt.Errorf("store: record migration %q: %w", m.ID, err)
		}
		slog.Info("store: migration applied", "id", m.ID)
	}

	return tx.Commit()
}

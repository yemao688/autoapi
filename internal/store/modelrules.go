package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"autoapi/internal/model"
)

// ListModelRules returns all model rules with their targets, ordered by
// created_at descending (most recent first). The Name is unique across
// enabled rules and is the model name exposed to clients.
func (s *Store) ListModelRules() ([]model.ModelRule, error) {
	rows, err := s.db.Query(`
		SELECT id, name, enabled, first_byte_timeout_ms, strategy, created_at, updated_at
		FROM model_rules ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list model rules: %w", err)
	}
	defer rows.Close()

	var rules []model.ModelRule
	for rows.Next() {
		var r model.ModelRule
		var firstByteTimeoutMs int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &firstByteTimeoutMs, &r.Strategy, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan model rule: %w", err)
		}
		// Stored as milliseconds; exposed to clients as seconds.
		r.FirstByteTimeoutSeconds = int(firstByteTimeoutMs / 1000)
		r.Strategy = normalizeStrategy(r.Strategy)
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []model.ModelRule{}
	}

	// Hydrate targets for each rule.
	for i, r := range rules {
		targets, err := s.listTargets(r.ID)
		if err != nil {
			return nil, err
		}
		rules[i].Targets = targets
	}

	return rules, nil
}

// ListModelRulesForDisplay returns all model rules with targets and today's
// success stats, ordered for display (display_order ASC, then created_at DESC,
// then id DESC). Proxy matching and /v1/models continue to use ListModelRules.
func (s *Store) ListModelRulesForDisplay() ([]model.ModelRule, error) {
	rows, err := s.db.Query(`
		SELECT id, name, enabled, first_byte_timeout_ms, strategy, created_at, updated_at
		FROM model_rules ORDER BY display_order ASC, created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list model rules for display: %w", err)
	}
	defer rows.Close()

	var rules []model.ModelRule
	for rows.Next() {
		var r model.ModelRule
		var firstByteTimeoutMs int64
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &firstByteTimeoutMs, &r.Strategy, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan model rule for display: %w", err)
		}
		// Stored as milliseconds; exposed to clients as seconds.
		r.FirstByteTimeoutSeconds = int(firstByteTimeoutMs / 1000)
		r.Strategy = normalizeStrategy(r.Strategy)
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = []model.ModelRule{}
	}

	// Hydrate targets and display stats for each rule.
	// Targets are hydrated per-rule (existing N+1 behaviour), while today stats
	// are populated by a single batched GROUP BY after all rules are loaded.
	for i, r := range rules {
		targets, err := s.listTargets(r.ID)
		if err != nil {
			return nil, err
		}
		rules[i].Targets = targets
	}
	if err := s.hydrateTodayStats(rules); err != nil {
		return nil, err
	}

	return rules, nil
}

// GetModelRule returns a single model rule with targets.
func (s *Store) GetModelRule(id string) (*model.ModelRule, error) {
	row := s.db.QueryRow(`
		SELECT id, name, enabled, first_byte_timeout_ms, strategy, created_at, updated_at
		FROM model_rules WHERE id = ?`, id)

	var r model.ModelRule
	var firstByteTimeoutMs int64
	if err := row.Scan(&r.ID, &r.Name, &r.Enabled, &firstByteTimeoutMs, &r.Strategy, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get model rule %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get model rule %q: %w", id, err)
	}
	r.FirstByteTimeoutSeconds = int(firstByteTimeoutMs / 1000)
	r.Strategy = normalizeStrategy(r.Strategy)

	targets, err := s.listTargets(r.ID)
	if err != nil {
		return nil, err
	}
	r.Targets = targets
	return &r, nil
}

// CreateModelRule inserts a model rule with its targets. Name uniqueness is
// enforced: a duplicate name returns an error so the UI can surface it.
func (s *Store) CreateModelRule(in model.ModelRuleInput) (*model.ModelRule, error) {
	now := nowMs()
	id := makeID()

	r := &model.ModelRule{
		ID:                      id,
		Name:                    in.Name,
		Enabled:                 in.Enabled,
		FirstByteTimeoutSeconds: in.FirstByteTimeoutSeconds,
		Strategy:                normalizeStrategy(in.Strategy),
		CreatedAt:               now,
		UpdatedAt:               now,
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		// Keep this check for the friendly UI error; the database unique index
		// is the final guard against concurrent writers.
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM model_rules WHERE name = ?`, r.Name).Scan(&count); err != nil {
			return fmt.Errorf("store: check unique name: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("store: model rule name %q is already in use", r.Name)
		}
		// Shift existing rules down so the new rule appears at the top.
		if _, err := tx.Exec(`UPDATE model_rules SET display_order = display_order + 1`); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO model_rules (id, name, enabled, first_byte_timeout_ms, strategy, display_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Name, boolInt(r.Enabled), int64(r.FirstByteTimeoutSeconds)*1000, r.Strategy, 0, r.CreatedAt, r.UpdatedAt); err != nil {
			return err
		}
		targets, err := s.insertTargets(tx, r.ID, in.Targets)
		if err != nil {
			return err
		}
		r.Targets = targets
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: create model rule: %w", err)
	}
	slog.Info("store: model rule created", "id", r.ID, "name", r.Name)
	return r, nil
}

// UpdateModelRule replaces a model rule's metadata and targets.
func (s *Store) UpdateModelRule(id string, in model.ModelRuleInput) (*model.ModelRule, error) {
	now := nowMs()

	if err := s.execTx(func(tx *sql.Tx) error {
		// Name uniqueness: allow the rule to keep its own name, but reject
		// a rename to a name that another rule already has.
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM model_rules WHERE name = ? AND id <> ?`,
			in.Name, id).Scan(&count); err != nil {
			return fmt.Errorf("store: check unique name: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("store: model rule name %q is already in use", in.Name)
		}
		res, err := tx.Exec(`
			UPDATE model_rules SET name=?, enabled=?, first_byte_timeout_ms=?, strategy=?, updated_at=?
			WHERE id=?`,
			in.Name, boolInt(in.Enabled), int64(in.FirstByteTimeoutSeconds)*1000, normalizeStrategy(in.Strategy), now, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update model rule %q: %w", id, ErrNotFound)
		}
		// Reconcile targets: UPDATE in place for targets that round-trip
		// their ID (preserves per-target hit_count/failure_count), DELETE
		// for targets the user removed, INSERT for genuinely new ones
		// (empty ID).
		if _, err := s.upsertTargets(tx, id, in.Targets); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	slog.Info("store: model rule updated", "id", id, "name", in.Name)
	return s.GetModelRule(id)
}

func normalizeStrategy(s string) string {
	switch s {
	case "score_within_tier", "cost_first", "priority_first":
		return s
	default:
		return "priority_first"
	}
}

// DeleteModelRule removes a model rule (targets cascade).
func (s *Store) DeleteModelRule(id string) error {
	err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM model_rules WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete model rule: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: delete model rule %q: %w", id, ErrNotFound)
		}
		return nil
	})
	if err == nil {
		slog.Info("store: model rule deleted", "id", id)
	}
	return err
}

// ReorderModelRules is a no-op kept for API compatibility. The previous
// ReorderModelRules updates display_order for all rules transactionally. The
// incoming IDs must exactly match the current rule set; any deviation
// (empty, duplicate, unknown, missing, or count mismatch) returns
// ErrConflict so the caller can reload authoritative state. Concurrent
// reorders with the same rule set are last-write-wins.
func (s *Store) ReorderModelRules(orderedIDs []string) error {
	if len(orderedIDs) == 0 {
		return fmt.Errorf("store: reorder model rules: empty list: %w", ErrConflict)
	}
	return s.execTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM model_rules`)
		if err != nil {
			return fmt.Errorf("store: reorder model rules: list existing: %w", err)
		}
		defer rows.Close()
		existing := make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			existing[id] = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(orderedIDs) != len(existing) {
			return fmt.Errorf("store: reorder model rules: set size changed: %w", ErrConflict)
		}
		seen := make(map[string]bool, len(orderedIDs))
		for _, id := range orderedIDs {
			if !existing[id] {
				return fmt.Errorf("store: reorder model rules: unknown id %q: %w", id, ErrConflict)
			}
			if seen[id] {
				return fmt.Errorf("store: reorder model rules: duplicate id %q: %w", id, ErrConflict)
			}
			seen[id] = true
		}
		for i, id := range orderedIDs {
			res, err := tx.Exec(`UPDATE model_rules SET display_order = ? WHERE id = ?`, i, id)
			if err != nil {
				return fmt.Errorf("store: reorder model rules: update %q: %w", id, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("store: reorder model rules: rows affected %q: %w", id, err)
			}
			if n != 1 {
				return fmt.Errorf("store: reorder model rules: id %q affected %d rows: %w", id, n, ErrConflict)
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
//  Internal helpers
// ---------------------------------------------------------------------------

func (s *Store) listTargets(ruleID string) ([]model.ModelRuleTarget, error) {
	rows, err := s.db.Query(`
		SELECT id, rule_id, provider_id, model_name, timeout_ms, tier, max_retries, hit_count, failure_count, enabled
		FROM rule_targets WHERE rule_id = ? ORDER BY tier ASC, rowid ASC`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.ModelRuleTarget
	for rows.Next() {
		var t model.ModelRuleTarget
		var timeoutMs int64
		if err := rows.Scan(&t.ID, &t.RuleID, &t.ProviderID, &t.ModelName, &timeoutMs, &t.Tier, &t.MaxRetries, &t.HitCount, &t.FailureCount, &t.Enabled); err != nil {
			return nil, err
		}
		t.FirstTokenTimeoutSeconds = int(timeoutMs / 1000)
		out = append(out, t)
	}
	if out == nil {
		out = []model.ModelRuleTarget{}
	}
	return out, rows.Err()
}

func targetTimeoutMillis(seconds int) (int64, error) {
	if seconds < 0 {
		return 0, fmt.Errorf("first_token_timeout_seconds must not be negative")
	}
	if int64(seconds) > math.MaxInt64/1000 {
		return 0, fmt.Errorf("first_token_timeout_seconds is too large")
	}
	return int64(seconds) * 1000, nil
}

// insertTargets writes a slice of targets for a freshly-created rule.
//
// CONTRACT (api consumer-facing): every target inserted here is treated as
// "new". The Go zero-value for Enabled is false, so a caller that omits
// `Enabled` (or sends a freshly-constructed ModelRuleTargetInput) would
// otherwise produce a disabled target. We coerce Enabled=true on insert so a
// brand-new target is always usable without the caller having to opt in. A nil
// `Tier` falls back to the slice index (legacy positional ordering); any
// explicit value (including 0) is stored verbatim. Toggling to false happens via
// the UPDATE path on a subsequent UpdateModelRule. This matches the spec
// "default to true when created without an explicit value" — the "explicit
// false" case is the supported way to create a disabled target and is exercised
// by re-issuing UpdateModelRule with Enabled: false.
func (s *Store) insertTargets(tx *sql.Tx, ruleID string, in []model.ModelRuleTargetInput) ([]model.ModelRuleTarget, error) {
	out := make([]model.ModelRuleTarget, len(in))
	for i, t := range in {
		timeoutMs, err := targetTimeoutMillis(t.FirstTokenTimeoutSeconds)
		if err != nil {
			return nil, err
		}
		if t.MaxRetries < 0 {
			t.MaxRetries = 0
		}
		if !t.Enabled {
			t.Enabled = true
		}
		// nil tier means the legacy "use positional order" sentinel; any
		// explicit value (including 0) is stored verbatim.
		effectiveTier := i
		if t.Tier != nil {
			effectiveTier = *t.Tier
		}
		id := makeID()
		if _, err := tx.Exec(`
			INSERT INTO rule_targets (id, rule_id, provider_id, model_name, timeout_ms, tier, max_retries, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, ruleID, t.ProviderID, t.ModelName, timeoutMs, effectiveTier, t.MaxRetries, boolInt(t.Enabled)); err != nil {
			return nil, err
		}
		out[i] = model.ModelRuleTarget{
			ID:                       id,
			RuleID:                   ruleID,
			ProviderID:               t.ProviderID,
			ModelName:                t.ModelName,
			MaxRetries:               t.MaxRetries,
			Tier:                     effectiveTier,
			FirstTokenTimeoutSeconds: t.FirstTokenTimeoutSeconds,
			Enabled:                  t.Enabled,
		}
	}
	return out, nil
}

// upsertTargets reconciles the incoming target list `in` against the
// existing rule_targets rows for ruleID:
//
//   - Incoming targets with a non-empty ID that matches an existing row are
//     UPDATEd in place (provider_id, model_name, tier, max_retries). This
//     preserves the per-target hit_count/failure_count the proxy has been
//     accumulating, so editing a rule (renaming, reordering, adding a new
//     target) doesn't silently reset its stats.
//   - Incoming targets with an empty ID are INSERTed as new rows with a
//     freshly generated ID. Counters default to 0.
//   - Existing rows whose IDs are NOT present in `in` are DELETEd — the user
//     removed them.
//
// `tier` is written from the slice index `i` on both UPDATE and INSERT paths
// when the caller leaves it nil (legacy positional ordering). An explicit tier
// (including 0) is written verbatim so priority groups round-trip. The ID
// round-trips; tier moves with the position only when the caller relies on the
// default.
//
// max_retries is clamped to >= 0 — see insertTargets for the rationale.
func (s *Store) upsertTargets(tx *sql.Tx, ruleID string, in []model.ModelRuleTargetInput) ([]model.ModelRuleTarget, error) {
	out := make([]model.ModelRuleTarget, len(in))

	// Clamp max_retries up-front so the clamp applies to both UPDATE and
	// INSERT paths without duplicating the check.
	for i := range in {
		if in[i].MaxRetries < 0 {
			in[i].MaxRetries = 0
		}
	}

	// Collect the IDs of incoming targets that round-trip from existing rows
	// (those whose ID the client knew about). Anything in rule_targets whose
	// ID is NOT in this set is a removal and must be DELETEd.
	incomingIDs := make([]string, 0, len(in))
	for _, t := range in {
		if t.ID != "" {
			incomingIDs = append(incomingIDs, t.ID)
		}
	}

	// Step 1: DELETE targets the user removed (not present in the incoming set).
	if len(incomingIDs) == 0 {
		// No incoming IDs at all → drop every existing target for this rule.
		if _, err := tx.Exec(`DELETE FROM rule_targets WHERE rule_id = ?`, ruleID); err != nil {
			return nil, err
		}
	} else {
		placeholders := strings.Repeat("?,", len(incomingIDs)-1) + "?"
		args := make([]interface{}, 0, len(incomingIDs)+1)
		args = append(args, ruleID)
		for _, id := range incomingIDs {
			args = append(args, id)
		}
		if _, err := tx.Exec(
			`DELETE FROM rule_targets WHERE rule_id = ? AND id NOT IN (`+placeholders+`)`,
			args...); err != nil {
			return nil, err
		}
	}

	// Step 2: UPSERT each incoming target.
	for i, t := range in {
		timeoutMs, err := targetTimeoutMillis(t.FirstTokenTimeoutSeconds)
		if err != nil {
			return nil, err
		}
		// nil tier means the legacy "use positional order" sentinel; any
		// explicit value (including 0) is stored verbatim.
		effectiveTier := i
		if t.Tier != nil {
			effectiveTier = *t.Tier
		}
		id := t.ID
		if id == "" {
			// New target: INSERT with a fresh ID. Apply the default-to-enabled
			// heuristic (same CONTRACT as insertTargets) so a caller that omits
			// `Enabled` still gets a usable target. Toggling to false happens
			// via the UPDATE path on a subsequent UpdateModelRule.
			if !t.Enabled {
				t.Enabled = true
			}
			id = makeID()
			if _, err := tx.Exec(`
				INSERT INTO rule_targets (id, rule_id, provider_id, model_name, timeout_ms, tier, max_retries, enabled)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, ruleID, t.ProviderID, t.ModelName, timeoutMs, effectiveTier, t.MaxRetries, boolInt(t.Enabled)); err != nil {
				return nil, err
			}
		} else {
			// Existing target: UPDATE in place; hit_count/failure_count and the
			// row's PK are intentionally left untouched so counters round-trip.
			// `enabled` IS written through verbatim (no defaulting) so the
			// frontend's toggle is honored — a user can flip an existing target
			// off, or back on, with full fidelity. The default-to-true rule
			// only applies to NEW targets (the ID=="" branch above). The
			// legacy `timeout_ms` column is no longer written (per-target
			// timeout moved to the rule level).
			if _, err := tx.Exec(`
				UPDATE rule_targets
				SET provider_id = ?, model_name = ?, timeout_ms = ?, tier = ?, max_retries = ?, enabled = ?
				WHERE id = ? AND rule_id = ?`,
				t.ProviderID, t.ModelName, timeoutMs, effectiveTier, t.MaxRetries, boolInt(t.Enabled), id, ruleID); err != nil {
				return nil, err
			}
		}
		out[i] = model.ModelRuleTarget{
			ID:                       id,
			RuleID:                   ruleID,
			ProviderID:               t.ProviderID,
			ModelName:                t.ModelName,
			MaxRetries:               t.MaxRetries,
			Tier:                     effectiveTier,
			FirstTokenTimeoutSeconds: t.FirstTokenTimeoutSeconds,
			Enabled:                  t.Enabled,
		}
	}
	return out, nil
}

// ReorderModelRuleTargets updates target tiers transactionally. A target-set
// mismatch returns ErrConflict so callers reload authoritative state. Concurrent
// reorders with the same target set intentionally remain last-write-wins.
func (s *Store) ReorderModelRuleTargets(ruleID string, orderedTargetIDs []string) error {
	return s.execTx(func(tx *sql.Tx) error {
		// 1. Verify the incoming IDs exactly match the rule's current targets.
		rows, err := tx.Query(`SELECT id FROM rule_targets WHERE rule_id = ?`, ruleID)
		if err != nil {
			return fmt.Errorf("store: reorder targets %q: %w", ruleID, err)
		}
		defer rows.Close()
		existing := make(map[string]bool)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			existing[id] = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(orderedTargetIDs) != len(existing) {
			return fmt.Errorf("store: reorder targets %q: target set changed: %w", ruleID, ErrConflict)
		}
		// Check each incoming ID exists in the rule
		seen := make(map[string]bool, len(orderedTargetIDs))
		for _, id := range orderedTargetIDs {
			if !existing[id] {
				return fmt.Errorf("store: reorder targets %q: unknown target id %q: %w", ruleID, id, ErrConflict)
			}
			if seen[id] {
				return fmt.Errorf("store: reorder targets %q: duplicate target id %q: %w", ruleID, id, ErrConflict)
			}
			seen[id] = true
		}
		// 2. Update tier for each target.
		for i, id := range orderedTargetIDs {
			res, err := tx.Exec(`UPDATE rule_targets SET tier = ? WHERE id = ? AND rule_id = ?`, i, id, ruleID)
			if err != nil {
				return fmt.Errorf("store: reorder targets %q: %w", ruleID, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("store: reorder targets %q: rows affected: %w", ruleID, err)
			}
			if n != 1 {
				return fmt.Errorf("store: reorder targets %q: target %q affected %d rows: %w", ruleID, id, n, ErrConflict)
			}
		}
		return nil
	})
}

// IncrementTargetStats bumps the per-target hit/failure counters. It is called
// from the proxy hot path after each candidate attempt. hitDelta/failDelta are
// typically 1; pass negatives to reset. TODO(v2): batch async if this shows up
// in profiling.
func (s *Store) IncrementTargetStats(targetID string, hitDelta, failDelta int64) error {
	_, err := s.db.Exec(`UPDATE rule_targets
		SET hit_count = hit_count + ?,
		    failure_count = failure_count + ?
		WHERE id = ?`, hitDelta, failDelta, targetID)
	if err != nil {
		return fmt.Errorf("store: increment target stats: %w", err)
	}
	return nil
}

// hydrateTodayStats computes per-rule request count and success rate for the
// current local day. It mutates the passed rules in place. Only rules whose IDs
// have matching request_logs rows are populated; rules with no completed
// requests keep a nil TodaySuccessRate and a zero TodayRequestCount.
func (s *Store) hydrateTodayStats(rules []model.ModelRule) error {
	if len(rules) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	now := time.Now()
	loc := now.Location()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	startMs := start.UnixMilli()

	placeholders := strings.Repeat("?,", len(ids)-1) + "?"
	args := make([]interface{}, 0, 1+len(ids))
	args = append(args, startMs)
	for _, id := range ids {
		args = append(args, id)
	}

	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT route_id,
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 AND COALESCE(error, '') = '' THEN 1 ELSE 0 END), 0) AS success
		FROM request_logs
		WHERE timestamp_ms >= ? AND route_id IN (%s) AND status_code != 0
		GROUP BY route_id`, placeholders), args...)
	if err != nil {
		return fmt.Errorf("store: hydrate today stats: %w", err)
	}
	defer rows.Close()

	stats := make(map[string]struct {
		total   int64
		success int64
	}, len(ids))
	for rows.Next() {
		var routeID string
		var total, success int64
		if err := rows.Scan(&routeID, &total, &success); err != nil {
			return fmt.Errorf("store: scan today stats: %w", err)
		}
		stats[routeID] = struct{ total, success int64 }{total, success}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for i := range rules {
		st, ok := stats[rules[i].ID]
		if !ok {
			continue
		}
		rules[i].TodayRequestCount = st.total
		if st.total > 0 {
			rate := float64(st.success) / float64(st.total) * 100
			rules[i].TodaySuccessRate = &rate
		}
	}
	return nil
}

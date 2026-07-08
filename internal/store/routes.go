package store

import (
	"database/sql"
	"fmt"
	"strings"

	"autoapi/internal/model"
)

// ListModelRules returns all model rules with their targets, ordered by
// created_at descending (most recent first). The Name is unique across
// enabled rules and is the model name exposed to clients.
func (s *Store) ListModelRules() ([]model.ModelRule, error) {
	rows, err := s.db.Query(`
		SELECT id, name, enabled, created_at, updated_at
		FROM model_rules ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list model rules: %w", err)
	}
	defer rows.Close()

	var rules []model.ModelRule
	for rows.Next() {
		var r model.ModelRule
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan model rule: %w", err)
		}
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

// GetModelRule returns a single model rule with targets.
func (s *Store) GetModelRule(id string) (*model.ModelRule, error) {
	row := s.db.QueryRow(`
		SELECT id, name, enabled, created_at, updated_at
		FROM model_rules WHERE id = ?`, id)

	var r model.ModelRule
	if err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get model rule %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get model rule %q: %w", id, err)
	}

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
		ID:        id,
		Name:      in.Name,
		Enabled:   in.Enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		// Enforce name uniqueness before insert. The CHECK here is the
		// application-level guarantee; the schema has no UNIQUE constraint
		// on name yet (the column already exists from the old `routes` table
		// and we keep description-less backwards compatibility).
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM model_rules WHERE name = ?`, r.Name).Scan(&count); err != nil {
			return fmt.Errorf("store: check unique name: %w", err)
		}
		if count > 0 {
			return fmt.Errorf("store: model rule name %q is already in use", r.Name)
		}
		if _, err := tx.Exec(`
			INSERT INTO model_rules (id, name, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`,
			r.ID, r.Name, boolInt(r.Enabled), r.CreatedAt, r.UpdatedAt); err != nil {
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
			UPDATE model_rules SET name=?, enabled=?, updated_at=?
			WHERE id=?`,
			in.Name, boolInt(in.Enabled), now, id)
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
	return s.GetModelRule(id)
}

// DeleteModelRule removes a model rule (targets cascade).
func (s *Store) DeleteModelRule(id string) error {
	return s.execTx(func(tx *sql.Tx) error {
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
}

// ReorderModelRules is a no-op kept for API compatibility. The previous
// drag-reorder UX was removed when route rules became model rules: rules
// are now keyed by a unique Name (the client-facing model name) and there
// is no meaningful order to preserve.
func (s *Store) ReorderModelRules(orderedIDs []string) error {
	return nil
}

// ---------------------------------------------------------------------------
//  Internal helpers
// ---------------------------------------------------------------------------

func (s *Store) listTargets(ruleID string) ([]model.ModelRuleTarget, error) {
	rows, err := s.db.Query(`
		SELECT id, rule_id, provider_id, model_name, max_retries, hit_count, failure_count, enabled
		FROM rule_targets WHERE rule_id = ? ORDER BY tier ASC`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.ModelRuleTarget
	for rows.Next() {
		var t model.ModelRuleTarget
		if err := rows.Scan(&t.ID, &t.RuleID, &t.ProviderID, &t.ModelName, &t.MaxRetries, &t.HitCount, &t.FailureCount, &t.Enabled); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if out == nil {
		out = []model.ModelRuleTarget{}
	}
	return out, rows.Err()
}

// insertTargets writes a slice of targets for a freshly-created rule.
//
// CONTRACT (api consumer-facing): every target inserted here is treated as
// "new". The Go zero-value for Enabled is false, so a caller that omits
// `Enabled` (or sends a freshly-constructed ModelRuleTarget) would
// otherwise produce a disabled target. We coerce Enabled=true on insert so
// a brand-new target is always usable without the caller having to opt in.
// Toggling to false happens via the UPDATE path on a subsequent
// UpdateModelRule. This matches the spec "default to true when created
// without an explicit value" — the "explicit false" case is the supported
// way to create a disabled target and is exercised by re-issuing
// UpdateModelRule with Enabled: false.
func (s *Store) insertTargets(tx *sql.Tx, ruleID string, in []model.ModelRuleTarget) ([]model.ModelRuleTarget, error) {
	out := make([]model.ModelRuleTarget, len(in))
	for i, t := range in {
		// Defense in depth: the frontend clamps to >= 0, but a negative value
		// would make the retry loop body execute zero times (target silently
		// skipped). Clamp rather than error — friendlier for API consumers.
		if t.MaxRetries < 0 {
			t.MaxRetries = 0
		}
		// New target path: default-to-true (see CONTRACT above). Without this
		// line, a caller that constructs ModelRuleTarget{ProviderID: "p1"} and
		// submits it would get a disabled row that the proxy silently drops.
		if !t.Enabled {
			t.Enabled = true
		}
		t.ID = makeID()
		t.RuleID = ruleID
		// `tier` is the internal positional sort key (kept from the slice
		// index so the target order round-trips through Create/UpdateModelRule);
		// hit_count/failure_count are managed by IncrementTargetStats and
		// default to 0 on insert.
		if _, err := tx.Exec(`
			INSERT INTO rule_targets (id, rule_id, provider_id, model_name, tier, max_retries, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			t.ID, t.RuleID, t.ProviderID, t.ModelName, i, t.MaxRetries, boolInt(t.Enabled)); err != nil {
			return nil, err
		}
		out[i] = t
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
// `tier` is always written from the slice index `i` on both UPDATE and INSERT
// paths so that a target whose position changed still has its row updated
// (the ID round-trips, but tier moves with it).
//
// max_retries is clamped to >= 0 — see insertTargets for the rationale.
func (s *Store) upsertTargets(tx *sql.Tx, ruleID string, in []model.ModelRuleTarget) ([]model.ModelRuleTarget, error) {
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
		t.RuleID = ruleID
		if t.ID == "" {
			// New target: INSERT with a fresh ID. Apply the default-to-enabled
			// heuristic (same CONTRACT as insertTargets) so a caller that omits
			// `Enabled` still gets a usable target. Toggling to false happens
			// via the UPDATE path on a subsequent UpdateModelRule.
			if !t.Enabled {
				t.Enabled = true
			}
			t.ID = makeID()
			if _, err := tx.Exec(`
				INSERT INTO rule_targets (id, rule_id, provider_id, model_name, tier, max_retries, enabled)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				t.ID, t.RuleID, t.ProviderID, t.ModelName, i, t.MaxRetries, boolInt(t.Enabled)); err != nil {
				return nil, err
			}
		} else {
			// Existing target: UPDATE in place; hit_count/failure_count and the
			// row's PK are intentionally left untouched so counters round-trip.
			// `enabled` IS written through verbatim (no defaulting) so the
			// frontend's toggle is honored — a user can flip an existing target
			// off, or back on, with full fidelity. The default-to-true rule
			// only applies to NEW targets (the ID=="" branch above).
			if _, err := tx.Exec(`
				UPDATE rule_targets
				SET provider_id = ?, model_name = ?, tier = ?, max_retries = ?, enabled = ?
				WHERE id = ? AND rule_id = ?`,
				t.ProviderID, t.ModelName, i, t.MaxRetries, boolInt(t.Enabled), t.ID, t.RuleID); err != nil {
				return nil, err
			}
		}
		out[i] = t
	}
	return out, nil
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

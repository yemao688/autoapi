package store

import (
	"database/sql"
	"fmt"
	"strings"

	"autoapi/internal/model"
)

// ListRoutes returns all routes with their conditions and targets, ordered by
// priority ascending (lower number = higher precedence). Priority is an internal
// sort key (not exposed on the Route struct); the slice order is the source of
// truth for callers.
func (s *Store) ListRoutes() ([]model.Route, error) {
	rows, err := s.db.Query(`
		SELECT id, name, description, enabled, created_at, updated_at
		FROM routes ORDER BY priority ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list routes: %w", err)
	}
	defer rows.Close()

	var routes []model.Route
	for rows.Next() {
		var r model.Route
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan route: %w", err)
		}
		routes = append(routes, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if routes == nil {
		routes = []model.Route{}
	}

	// Hydrate conditions and targets for each route.
	for i, r := range routes {
		conds, err := s.listConditions(r.ID)
		if err != nil {
			return nil, err
		}
		targets, err := s.listTargets(r.ID)
		if err != nil {
			return nil, err
		}
		routes[i].Conditions = conds
		routes[i].Targets = targets
	}

	return routes, nil
}

// GetRoute returns a single route with conditions and targets.
func (s *Store) GetRoute(id string) (*model.Route, error) {
	row := s.db.QueryRow(`
		SELECT id, name, description, enabled, created_at, updated_at
		FROM routes WHERE id = ?`, id)

	var r model.Route
	if err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("store: get route %q: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("store: get route %q: %w", id, err)
	}

	conds, err := s.listConditions(r.ID)
	if err != nil {
		return nil, err
	}
	targets, err := s.listTargets(r.ID)
	if err != nil {
		return nil, err
	}
	r.Conditions = conds
	r.Targets = targets
	return &r, nil
}

// CreateRoute inserts a route with its conditions and targets.
func (s *Store) CreateRoute(in model.RouteInput) (*model.Route, error) {
	now := nowMs()
	id := makeID()

	r := &model.Route{
		ID:          id,
		Name:        in.Name,
		Description: in.Description,
		Enabled:     in.Enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		// New rules land at the bottom of the list: priority = current max + 1
		// (COALESCE handles the empty-table case → 1). Priority is the internal
		// ordinal sort key (see ListRoutes ORDER BY); it is not exposed on the
		// Route struct. Note: two concurrent CreateRoute calls could compute
		// the same value under default deferred locking — acceptable for a
		// single-user desktop app; ReorderRoutes rewrites the sequence anyway.
		var priority int
		if err := tx.QueryRow(`SELECT COALESCE(MAX(priority), 0) + 1 FROM routes`).Scan(&priority); err != nil {
			return fmt.Errorf("store: compute next priority: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT INTO routes (id, name, description, priority, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Name, r.Description, priority, boolInt(r.Enabled), r.CreatedAt, r.UpdatedAt); err != nil {
			return err
		}
		conds, err := s.insertConditions(tx, r.ID, in.Conditions)
		if err != nil {
			return err
		}
		targets, err := s.insertTargets(tx, r.ID, in.Targets)
		if err != nil {
			return err
		}
		r.Conditions = conds
		r.Targets = targets
		return nil
	}); err != nil {
		return nil, fmt.Errorf("store: create route: %w", err)
	}
	return r, nil
}

// UpdateRoute replaces a route's metadata, conditions, and targets. Priority
// is intentionally NOT touched here — it's the internal sort key owned by the
// store and is only rewritten by ReorderRoutes.
func (s *Store) UpdateRoute(id string, in model.RouteInput) (*model.Route, error) {
	now := nowMs()

	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE routes SET name=?, description=?, enabled=?, updated_at=?
			WHERE id=?`,
			in.Name, in.Description, boolInt(in.Enabled), now, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update route %q: %w", id, ErrNotFound)
		}
		// Replace conditions. (DELETE+reinsert is fine — conditions have no
		// counters to preserve.)
		if _, err := tx.Exec(`DELETE FROM route_conditions WHERE route_id = ?`, id); err != nil {
			return err
		}
		if _, err := s.insertConditions(tx, id, in.Conditions); err != nil {
			return err
		}
		// Reconcile targets: UPDATE in place for targets that round-trip their
		// ID (preserves per-target hit_count/failure_count), DELETE for
		// targets the user removed, INSERT for genuinely new ones (empty ID).
		// tier is always rewritten from the slice index on both UPDATE and
		// INSERT paths so target order round-trips through drag-reorder.
		if _, err := s.upsertTargets(tx, id, in.Targets); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.GetRoute(id)
}

// DeleteRoute removes a route (conditions/targets cascade).
func (s *Store) DeleteRoute(id string) error {
	return s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM routes WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete route: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: delete route %q: %w", id, ErrNotFound)
		}
		return nil
	})
}

// ReorderRoutes updates the priority values to match the given ordered ID
// slice. Position in the slice determines priority (0-based).
func (s *Store) ReorderRoutes(orderedIDs []string) error {
	// priority values are ordinal, not semantic — only ORDER BY matters.
	return s.execTx(func(tx *sql.Tx) error {
		for i, id := range orderedIDs {
			priority := i
			if _, err := tx.Exec(`UPDATE routes SET priority = ?, updated_at = ? WHERE id = ?`,
				priority, nowMs(), id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
//  Internal helpers
// ---------------------------------------------------------------------------

func (s *Store) listConditions(routeID string) ([]model.RouteCondition, error) {
	rows, err := s.db.Query(`
		SELECT id, route_id, field, operator, value
		FROM route_conditions WHERE route_id = ?`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.RouteCondition
	for rows.Next() {
		var c model.RouteCondition
		if err := rows.Scan(&c.ID, &c.RouteID, &c.Field, &c.Operator, &c.Value); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if out == nil {
		out = []model.RouteCondition{}
	}
	return out, rows.Err()
}

func (s *Store) listTargets(routeID string) ([]model.RouteTarget, error) {
	rows, err := s.db.Query(`
		SELECT id, route_id, provider_id, model_name, max_retries, hit_count, failure_count
		FROM route_targets WHERE route_id = ? ORDER BY tier ASC`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.RouteTarget
	for rows.Next() {
		var t model.RouteTarget
		if err := rows.Scan(&t.ID, &t.RouteID, &t.ProviderID, &t.ModelName, &t.MaxRetries, &t.HitCount, &t.FailureCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if out == nil {
		out = []model.RouteTarget{}
	}
	return out, rows.Err()
}

func (s *Store) insertConditions(tx *sql.Tx, routeID string, in []model.RouteCondition) ([]model.RouteCondition, error) {
	out := make([]model.RouteCondition, len(in))
	for i, c := range in {
		c.ID = makeID()
		c.RouteID = routeID
		if _, err := tx.Exec(`
			INSERT INTO route_conditions (id, route_id, field, operator, value)
			VALUES (?, ?, ?, ?, ?)`,
			c.ID, c.RouteID, c.Field, c.Operator, c.Value); err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

func (s *Store) insertTargets(tx *sql.Tx, routeID string, in []model.RouteTarget) ([]model.RouteTarget, error) {
	out := make([]model.RouteTarget, len(in))
	for i, t := range in {
		// Defense in depth: the frontend clamps to >= 0, but a negative value
		// would make the retry loop body execute zero times (target silently
		// skipped). Clamp rather than error — friendlier for API consumers.
		if t.MaxRetries < 0 {
			t.MaxRetries = 0
		}
		t.ID = makeID()
		t.RouteID = routeID
		// `tier` is the internal positional sort key (kept from the slice
		// index so the target order round-trips through Create/UpdateRoute);
		// hit_count/failure_count are managed by IncrementTargetStats and
		// default to 0 on insert.
		if _, err := tx.Exec(`
			INSERT INTO route_targets (id, route_id, provider_id, model_name, tier, max_retries)
			VALUES (?, ?, ?, ?, ?, ?)`,
			t.ID, t.RouteID, t.ProviderID, t.ModelName, i, t.MaxRetries); err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}

// upsertTargets reconciles the incoming target list `in` against the existing
// route_targets rows for routeID:
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
// paths so that a target whose position changed via drag-reorder still has
// its row updated (the ID round-trips, but tier moves with it).
//
// max_retries is clamped to >= 0 — see insertTargets for the rationale.
func (s *Store) upsertTargets(tx *sql.Tx, routeID string, in []model.RouteTarget) ([]model.RouteTarget, error) {
	out := make([]model.RouteTarget, len(in))

	// Clamp max_retries up-front so the clamp applies to both UPDATE and
	// INSERT paths without duplicating the check.
	for i := range in {
		if in[i].MaxRetries < 0 {
			in[i].MaxRetries = 0
		}
	}

	// Collect the IDs of incoming targets that round-trip from existing rows
	// (those whose ID the client knew about). Anything in route_targets whose
	// ID is NOT in this set is a removal and must be DELETEd.
	incomingIDs := make([]string, 0, len(in))
	for _, t := range in {
		if t.ID != "" {
			incomingIDs = append(incomingIDs, t.ID)
		}
	}

	// Step 1: DELETE targets the user removed (not present in the incoming set).
	if len(incomingIDs) == 0 {
		// No incoming IDs at all → drop every existing target for this route.
		if _, err := tx.Exec(`DELETE FROM route_targets WHERE route_id = ?`, routeID); err != nil {
			return nil, err
		}
	} else {
		placeholders := strings.Repeat("?,", len(incomingIDs)-1) + "?"
		args := make([]interface{}, 0, len(incomingIDs)+1)
		args = append(args, routeID)
		for _, id := range incomingIDs {
			args = append(args, id)
		}
		if _, err := tx.Exec(
			`DELETE FROM route_targets WHERE route_id = ? AND id NOT IN (`+placeholders+`)`,
			args...); err != nil {
			return nil, err
		}
	}

	// Step 2: UPSERT each incoming target.
	for i, t := range in {
		t.RouteID = routeID
		if t.ID == "" {
			// New target: INSERT with a fresh ID.
			t.ID = makeID()
			if _, err := tx.Exec(`
				INSERT INTO route_targets (id, route_id, provider_id, model_name, tier, max_retries)
				VALUES (?, ?, ?, ?, ?, ?)`,
				t.ID, t.RouteID, t.ProviderID, t.ModelName, i, t.MaxRetries); err != nil {
				return nil, err
			}
		} else {
			// Existing target: UPDATE in place; hit_count/failure_count and the
			// row's PK are intentionally left untouched so counters round-trip.
			if _, err := tx.Exec(`
				UPDATE route_targets
				SET provider_id = ?, model_name = ?, tier = ?, max_retries = ?
				WHERE id = ? AND route_id = ?`,
				t.ProviderID, t.ModelName, i, t.MaxRetries, t.ID, t.RouteID); err != nil {
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
	_, err := s.db.Exec(`UPDATE route_targets
		SET hit_count = hit_count + ?,
		    failure_count = failure_count + ?
		WHERE id = ?`, hitDelta, failDelta, targetID)
	if err != nil {
		return fmt.Errorf("store: increment target stats: %w", err)
	}
	return nil
}

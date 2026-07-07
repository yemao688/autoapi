package store

import (
	"database/sql"
	"fmt"

	"autoapi/internal/model"
)

// ListRoutes returns all routes with their conditions and targets, ordered by
// priority ascending (lower number = higher precedence).
func (s *Store) ListRoutes() ([]model.Route, error) {
	rows, err := s.db.Query(`
		SELECT id, name, description, priority, enabled, created_at, updated_at
		FROM routes ORDER BY priority ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list routes: %w", err)
	}
	defer rows.Close()

	var routes []model.Route
	for rows.Next() {
		var r model.Route
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.Priority, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
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
		SELECT id, name, description, priority, enabled, created_at, updated_at
		FROM routes WHERE id = ?`, id)

	var r model.Route
	if err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Priority, &r.Enabled, &r.CreatedAt, &r.UpdatedAt); err != nil {
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
		Priority:    in.Priority,
		Enabled:     in.Enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO routes (id, name, description, priority, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Name, r.Description, r.Priority, boolInt(r.Enabled), r.CreatedAt, r.UpdatedAt); err != nil {
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

// UpdateRoute replaces a route's metadata, conditions, and targets.
func (s *Store) UpdateRoute(id string, in model.RouteInput) (*model.Route, error) {
	now := nowMs()

	if err := s.execTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`
			UPDATE routes SET name=?, description=?, priority=?, enabled=?, updated_at=?
			WHERE id=?`,
			in.Name, in.Description, in.Priority, boolInt(in.Enabled), now, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return fmt.Errorf("store: update route %q: %w", id, ErrNotFound)
		}
		// Replace conditions.
		if _, err := tx.Exec(`DELETE FROM route_conditions WHERE route_id = ?`, id); err != nil {
			return err
		}
		// Replace targets.
		if _, err := tx.Exec(`DELETE FROM route_targets WHERE route_id = ?`, id); err != nil {
			return err
		}
		if _, err := s.insertConditions(tx, id, in.Conditions); err != nil {
			return err
		}
		if _, err := s.insertTargets(tx, id, in.Targets); err != nil {
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
// slice. Position in the slice determines priority (0-based → 1-based).
func (s *Store) ReorderRoutes(orderedIDs []string) error {
	return s.execTx(func(tx *sql.Tx) error {
		for i, id := range orderedIDs {
			priority := i + 1
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
		SELECT id, route_id, provider_id, model_name, action, tier
		FROM route_targets WHERE route_id = ? ORDER BY tier ASC`, routeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.RouteTarget
	for rows.Next() {
		var t model.RouteTarget
		if err := rows.Scan(&t.ID, &t.RouteID, &t.ProviderID, &t.ModelName, &t.Action, &t.Tier); err != nil {
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
		t.ID = makeID()
		t.RouteID = routeID
		if _, err := tx.Exec(`
			INSERT INTO route_targets (id, route_id, provider_id, model_name, action, tier)
			VALUES (?, ?, ?, ?, ?, ?)`,
			t.ID, t.RouteID, t.ProviderID, t.ModelName, t.Action, t.Tier); err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}

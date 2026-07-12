package store

import (
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"autoapi/internal/model"
)

// UpsertPrice inserts or replaces a price record keyed by ProviderID,
// UpstreamModel and EndpointKind. An empty ProviderID means a global fallback.
// Existing records are updated in place so their ID and created_at are preserved.
func (s *Store) UpsertPrice(in model.PriceInput) (*model.Price, error) {
	in, err := normalizePriceInput(in)
	if err != nil {
		return nil, err
	}

	now := nowMs()
	p := &model.Price{
		ID:                        makeID(),
		ProviderID:                in.ProviderID,
		UpstreamModel:             in.UpstreamModel,
		EndpointKind:              in.EndpointKind,
		BillingMode:               in.BillingMode.Normalized(),
		InputPricePerMillion:      in.InputPricePerMillion,
		OutputPricePerMillion:     in.OutputPricePerMillion,
		CacheReadPricePerMillion:  in.CacheReadPricePerMillion,
		CacheWritePricePerMillion: in.CacheWritePricePerMillion,
		RequestPricePerRequest:    in.RequestPricePerRequest,
		Currency:                  in.Currency,
		Source:                    in.Source,
		Version:                   in.Version,
		EffectiveAt:               in.EffectiveAt,
		ExpiresAt:                 in.ExpiresAt,
		Confidence:                in.Confidence.Normalized(),
		UpdatedAt:                 now,
		CreatedAt:                 now,
	}
	if p.Confidence == "" {
		p.Confidence = model.CostConfidenceUnknown
	}

	if err := s.execTx(func(tx *sql.Tx) error {
		var existingID string
		var existingCreated int64
		err := tx.QueryRow(`
			SELECT id, created_at FROM prices
			WHERE COALESCE(provider_id,'') = COALESCE(?,'') AND upstream_model = ? AND endpoint_kind = ?`,
			p.ProviderID, p.UpstreamModel, p.EndpointKind).Scan(&existingID, &existingCreated)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("store: lookup price: %w", err)
		}
		if err == sql.ErrNoRows {
			_, err = tx.Exec(`
				INSERT INTO prices (id, provider_id, upstream_model, endpoint_kind, billing_mode,
					input_price_per_million, output_price_per_million,
					cache_read_price_per_million, cache_write_price_per_million,
					request_price_per_request, currency, source, version,
					effective_at, expires_at, confidence, updated_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				p.ID, nullString(p.ProviderID), p.UpstreamModel, p.EndpointKind, p.BillingMode,
				p.InputPricePerMillion, p.OutputPricePerMillion,
				p.CacheReadPricePerMillion, p.CacheWritePricePerMillion,
				p.RequestPricePerRequest, p.Currency, p.Source, p.Version,
				p.EffectiveAt, p.ExpiresAt, p.Confidence, p.UpdatedAt, p.CreatedAt)
			return err
		}
		p.ID = existingID
		p.CreatedAt = existingCreated
		_, err = tx.Exec(`
			UPDATE prices SET
				billing_mode = ?,
				input_price_per_million = ?,
				output_price_per_million = ?,
				cache_read_price_per_million = ?,
				cache_write_price_per_million = ?,
				request_price_per_request = ?,
				currency = ?,
				source = ?,
				version = ?,
				effective_at = ?,
				expires_at = ?,
				confidence = ?,
				updated_at = ?
			WHERE id = ?`,
			p.BillingMode,
			p.InputPricePerMillion, p.OutputPricePerMillion,
			p.CacheReadPricePerMillion, p.CacheWritePricePerMillion,
			p.RequestPricePerRequest, p.Currency, p.Source, p.Version,
			p.EffectiveAt, p.ExpiresAt, p.Confidence, p.UpdatedAt, p.ID)
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: upsert price: %w", err)
	}
	return p, nil
}

// ListPrices returns every price record, ordered by upstream_model, provider_id,
// endpoint_kind for stable listing.
func (s *Store) ListPrices() ([]model.Price, error) {
	rows, err := s.db.Query(`
		SELECT id, provider_id, upstream_model, endpoint_kind, billing_mode,
			input_price_per_million, output_price_per_million,
			cache_read_price_per_million, cache_write_price_per_million,
			request_price_per_request, currency, source, version,
			effective_at, expires_at, confidence, updated_at, created_at
		FROM prices
		ORDER BY upstream_model ASC, COALESCE(provider_id,'') ASC, endpoint_kind ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list prices: %w", err)
	}
	defer rows.Close()

	var out []model.Price
	for rows.Next() {
		p, err := scanPrice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if out == nil {
		out = []model.Price{}
	}
	return out, rows.Err()
}

// DeletePrice removes a single price record by ID. It is idempotent:
// deleting a non-existent ID succeeds without error.
func (s *Store) DeletePrice(id string) error {
	return s.execTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("DELETE FROM prices WHERE id = ?", id)
		return err
	})
}

// ResolvePrice returns the best price record for the given provider/model/endpoint,
// following the fallback chain:
//
//  1. provider + model + endpoint
//  2. provider + model default (endpoint_kind = "")
//  3. global model + endpoint
//  4. global model default (provider_id NULL, endpoint_kind = "")
//  5. hardcoded costTable
//  6. nil (unknown)
//
// Invalid, not-yet-effective, and expired records are skipped here; ListPrices
// remains the historical view of all stored records.
func (s *Store) ResolvePrice(providerID, modelName, endpointKind string) (*model.Price, error) {
	return s.ResolvePriceAt(providerID, modelName, endpointKind, time.Now().UnixMilli())
}

// ResolvePriceAt resolves a price using an explicit timestamp for stable tests.
func (s *Store) ResolvePriceAt(providerID, modelName, endpointKind string, nowMs int64) (*model.Price, error) {
	providerID = strings.TrimSpace(providerID)
	modelName = strings.TrimSpace(modelName)
	endpointKind = strings.TrimSpace(endpointKind)
	if modelName == "" {
		return nil, nil
	}
	resolve := func(candidateProvider, candidateEndpoint string) (*model.Price, error) {
		p, err := s.findPrice(candidateProvider, modelName, candidateEndpoint)
		if err != nil {
			return nil, err
		}
		if p == nil || !p.IsValid() || !p.IsEffectiveAt(nowMs) {
			return nil, nil
		}
		return p, nil
	}

	if providerID != "" {
		if p, err := resolve(providerID, endpointKind); err != nil {
			return nil, err
		} else if p != nil {
			return p, nil
		}
	}
	if providerID != "" && endpointKind != "" {
		if p, err := resolve(providerID, ""); err != nil {
			return nil, err
		} else if p != nil {
			return p, nil
		}
	}
	if endpointKind != "" {
		if p, err := resolve("", endpointKind); err != nil {
			return nil, err
		} else if p != nil {
			return p, nil
		}
	}
	if p, err := resolve("", ""); err != nil {
		return nil, err
	} else if p != nil {
		return p, nil
	}

	// 5. hardcoded costTable fallback
	if c, ok := costTable[modelName]; ok {
		return &model.Price{
			ProviderID:                "",
			UpstreamModel:             modelName,
			EndpointKind:              "",
			BillingMode:               model.BillingModeToken,
			InputPricePerMillion:      c.InputPerToken * 1e6,
			OutputPricePerMillion:     c.OutputPerToken * 1e6,
			CacheReadPricePerMillion:  0,
			CacheWritePricePerMillion: 0,
			RequestPricePerRequest:    0,
			Currency:                  "USD",
			Source:                    "hardcoded",
			Version:                   "legacy",
			Confidence:                model.CostConfidenceEstimated,
		}, nil
	}

	// 6. unknown
	return nil, nil
}

func (s *Store) findPrice(providerID, modelName, endpointKind string) (*model.Price, error) {
	row := s.db.QueryRow(`
		SELECT id, provider_id, upstream_model, endpoint_kind, billing_mode,
			input_price_per_million, output_price_per_million,
			cache_read_price_per_million, cache_write_price_per_million,
			request_price_per_request, currency, source, version,
			effective_at, expires_at, confidence, updated_at, created_at
		FROM prices
		WHERE COALESCE(provider_id,'') = COALESCE(?,'') AND upstream_model = ? AND endpoint_kind = ?
		LIMIT 1`,
		providerID, modelName, endpointKind)
	p, err := scanPrice(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: find price: %w", err)
	}
	return p, nil
}

func scanPrice(sc rowScanner) (*model.Price, error) {
	var p model.Price
	var providerID sql.NullString
	if err := sc.Scan(
		&p.ID, &providerID, &p.UpstreamModel, &p.EndpointKind, &p.BillingMode,
		&p.InputPricePerMillion, &p.OutputPricePerMillion,
		&p.CacheReadPricePerMillion, &p.CacheWritePricePerMillion,
		&p.RequestPricePerRequest, &p.Currency, &p.Source, &p.Version,
		&p.EffectiveAt, &p.ExpiresAt, &p.Confidence,
		&p.UpdatedAt, &p.CreatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("store: scan price: %w", err)
	}
	if providerID.Valid {
		p.ProviderID = providerID.String
	}
	return &p, nil
}

func normalizePriceInput(in model.PriceInput) (model.PriceInput, error) {
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.UpstreamModel = strings.TrimSpace(in.UpstreamModel)
	in.EndpointKind = strings.TrimSpace(in.EndpointKind)
	in.Currency = strings.ToUpper(strings.TrimSpace(in.Currency))
	in.Source = strings.TrimSpace(in.Source)
	in.Version = strings.TrimSpace(in.Version)
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if in.UpstreamModel == "" {
		return in, fmt.Errorf("store: price upstream_model is required")
	}
	if in.Currency != "USD" {
		return in, fmt.Errorf("store: only USD prices are supported")
	}
	for _, value := range []float64{
		in.InputPricePerMillion, in.OutputPricePerMillion,
		in.CacheReadPricePerMillion, in.CacheWritePricePerMillion,
		in.RequestPricePerRequest,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return in, fmt.Errorf("store: price fields must be finite and non-negative")
		}
	}
	if in.ExpiresAt != 0 && in.EffectiveAt > in.ExpiresAt {
		return in, fmt.Errorf("store: effective_at must not be after expires_at")
	}
	return in, nil
}

package model

import (
	"math"
	"strings"
)

// Price is a persisted or resolved price record for a provider/model/endpoint.
// Per-token prices are expressed per 1,000,000 tokens (the industry standard
// unit); per-request prices are expressed per request.
type Price struct {
	ID                        string         `json:"id,omitempty"`
	ProviderID                string         `json:"provider_id,omitempty"` // empty = global fallback
	UpstreamModel             string         `json:"upstream_model"`
	EndpointKind              string         `json:"endpoint_kind"`
	BillingMode               BillingMode    `json:"billing_mode"`
	InputPricePerMillion      float64        `json:"input_price_per_million"`
	OutputPricePerMillion     float64        `json:"output_price_per_million"`
	CacheReadPricePerMillion  float64        `json:"cache_read_price_per_million"`
	CacheWritePricePerMillion float64        `json:"cache_write_price_per_million"`
	RequestPricePerRequest    float64        `json:"request_price_per_request"`
	Currency                  string         `json:"currency"`
	Source                    string         `json:"source"`
	Version                   string         `json:"version"`
	EffectiveAt               int64          `json:"effective_at"`
	ExpiresAt                 int64          `json:"expires_at"`
	Confidence                CostConfidence `json:"confidence"`
	UpdatedAt                 int64          `json:"updated_at"`
	CreatedAt                 int64          `json:"created_at,omitempty"`
}

// PriceInput is the payload for creating or replacing a price record. The key
// is ProviderID + UpstreamModel + EndpointKind; an existing record is updated
// in place.
type PriceInput struct {
	ProviderID                string         `json:"provider_id,omitempty"`
	UpstreamModel             string         `json:"upstream_model"`
	EndpointKind              string         `json:"endpoint_kind"`
	BillingMode               BillingMode    `json:"billing_mode"`
	InputPricePerMillion      float64        `json:"input_price_per_million"`
	OutputPricePerMillion     float64        `json:"output_price_per_million"`
	CacheReadPricePerMillion  float64        `json:"cache_read_price_per_million"`
	CacheWritePricePerMillion float64        `json:"cache_write_price_per_million"`
	RequestPricePerRequest    float64        `json:"request_price_per_request"`
	Currency                  string         `json:"currency"`
	Source                    string         `json:"source"`
	Version                   string         `json:"version"`
	EffectiveAt               int64          `json:"effective_at"`
	ExpiresAt                 int64          `json:"expires_at"`
	Confidence                CostConfidence `json:"confidence"`
}

// IsValid reports whether the price has finite, non-negative values and the
// minimum fields required for use.
func (p Price) IsValid() bool {
	if strings.TrimSpace(p.UpstreamModel) == "" || !p.BillingMode.Valid() || p.Currency != "USD" {
		return false
	}
	for _, value := range []float64{
		p.InputPricePerMillion, p.OutputPricePerMillion,
		p.CacheReadPricePerMillion, p.CacheWritePricePerMillion,
		p.RequestPricePerRequest,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return false
		}
	}
	return p.ExpiresAt == 0 || p.EffectiveAt <= p.ExpiresAt
}

// IsEffectiveAt reports whether the price is effective at nowMs.
func (p Price) IsEffectiveAt(nowMs int64) bool {
	if p.EffectiveAt != 0 && nowMs < p.EffectiveAt {
		return false
	}
	if p.ExpiresAt != 0 && nowMs >= p.ExpiresAt {
		return false
	}
	return true
}

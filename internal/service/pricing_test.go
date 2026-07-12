package service

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestEstimateEffectiveCostToken(t *testing.T) {
	price := &model.Price{
		UpstreamModel:         "m",
		BillingMode:           model.BillingModeToken,
		Currency:              "USD",
		InputPricePerMillion:  10,
		OutputPricePerMillion: 30,
		Version:               "v1",
		Confidence:            model.CostConfidenceExact,
	}
	got := EstimateEffectiveCost(price, 1000, 500, 0, 0, 1, 0)
	if !got.IsAvailable() {
		t.Fatalf("expected available cost")
	}
	want := 1000*10.0/1e6 + 500*30.0/1e6
	if math.Abs(got.Cost-want) > 1e-9 {
		t.Fatalf("cost got %v, want %v", got.Cost, want)
	}
	if got.Currency != "USD" || got.PriceVersion != "v1" || got.Confidence != model.CostConfidenceExact {
		t.Fatalf("unexpected metadata: %+v", got)
	}
}

func TestEstimateEffectiveCostAtUsesHistoricalValidity(t *testing.T) {
	price := &model.Price{UpstreamModel: "m", BillingMode: model.BillingModeToken, Currency: "USD", InputPricePerMillion: 1, OutputPricePerMillion: 1, Confidence: model.CostConfidenceExact, EffectiveAt: 1000, ExpiresAt: 2000}
	if got := EstimateEffectiveCostAt(price, 1500, 1000, 0, 0, 0, 1, 0); !got.IsAvailable() {
		t.Fatal("historically valid price unavailable")
	}
	if got := EstimateEffectiveCostAt(price, 2500, 1000, 0, 0, 0, 1, 0); got.IsAvailable() {
		t.Fatal("expired price available")
	}
	if got := EstimateEffectiveCostAt(price, 500, 1000, 0, 0, 0, 1, 0); got.IsAvailable() {
		t.Fatal("future price available")
	}
	if a, b := EstimateEffectiveCostAt(price, 1500, 1000, 0, 0, 0, 1, 2), EstimateEffectiveCostAt(price, 1500, 1000, 0, 0, 0, 1, 2); a != b {
		t.Fatalf("nondeterministic historical estimate")
	}
}

func TestEstimateEffectiveCostRequest(t *testing.T) {
	price := &model.Price{
		UpstreamModel:          "m",
		BillingMode:            model.BillingModeRequest,
		Currency:               "USD",
		RequestPricePerRequest: 2.5,
		Version:                "v1",
		Confidence:             model.CostConfidenceEstimated,
	}
	got := EstimateEffectiveCost(price, 0, 0, 0, 0, 3, 1)
	if !got.IsAvailable() {
		t.Fatalf("expected available cost")
	}
	if got.Cost != 2.5*4 {
		t.Fatalf("cost got %v, want 10", got.Cost)
	}
}

func TestEstimateEffectiveCostRetry(t *testing.T) {
	price := &model.Price{
		UpstreamModel:         "m",
		BillingMode:           model.BillingModeToken,
		Currency:              "USD",
		InputPricePerMillion:  5,
		OutputPricePerMillion: 10,
		Confidence:            model.CostConfidenceEstimated,
	}
	base := EstimateEffectiveCost(price, 1000, 0, 0, 0, 1, 0).Cost
	withRetry := EstimateEffectiveCost(price, 1000, 0, 0, 0, 1, 2).Cost
	if withRetry != base*3 {
		t.Fatalf("retry cost got %v, want %v", withRetry, base*3)
	}
}

func TestEstimateEffectiveCostUnavailable(t *testing.T) {
	// nil price
	got := EstimateEffectiveCost(nil, 100, 50, 0, 0, 1, 0)
	if got.IsAvailable() {
		t.Fatal("nil price should be unavailable")
	}
	if got.Cost != 0 {
		t.Fatalf("unavailable cost should be zero, got %v", got.Cost)
	}

	// unknown billing mode
	got = EstimateEffectiveCost(&model.Price{UpstreamModel: "m", BillingMode: model.BillingModeUnknown, Currency: "USD"}, 100, 50, 0, 0, 1, 0)
	if got.IsAvailable() {
		t.Fatal("unknown billing mode should be unavailable")
	}

	// expired
	got = EstimateEffectiveCost(&model.Price{
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		Currency:             "USD",
		InputPricePerMillion: 1, OutputPricePerMillion: 1,
		ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(),
	}, 100, 50, 0, 0, 1, 0)
	if got.IsAvailable() {
		t.Fatal("expired price should be unavailable")
	}
}

func TestEstimateEffectiveCostUnavailableJSON(t *testing.T) {
	got := EstimateEffectiveCost(nil, 1, 2, 0, 0, 1, 0)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal unavailable cost: %v", err)
	}
	if !bytes.Contains(b, []byte(`"cost":0`)) || bytes.Contains(b, []byte("NaN")) {
		t.Fatalf("unexpected unavailable JSON: %s", b)
	}
}

func TestEstimateEffectiveCostNaNIsUnavailable(t *testing.T) {
	price := &model.Price{
		UpstreamModel:         "m",
		BillingMode:           model.BillingModeToken,
		Currency:              "USD",
		InputPricePerMillion:  math.NaN(),
		OutputPricePerMillion: 1,
	}
	got := EstimateEffectiveCost(price, 100, 50, 0, 0, 1, 0)
	if got.IsAvailable() {
		t.Fatal("NaN price should be unavailable")
	}
}

func TestEstimateEffectiveCostZeroIsAvailable(t *testing.T) {
	price := &model.Price{
		UpstreamModel:        "m",
		BillingMode:          model.BillingModeToken,
		Currency:             "USD",
		InputPricePerMillion: 0, OutputPricePerMillion: 0,
		Confidence: model.CostConfidenceExact,
	}
	got := EstimateEffectiveCost(price, 1000, 500, 0, 0, 1, 0)
	if !got.IsAvailable() {
		t.Fatal("explicit zero price should be available (free model), not unknown")
	}
	if got.Cost != 0 {
		t.Fatalf("expected zero cost, got %v", got.Cost)
	}
}

func TestEstimateEffectiveCostRejectsNegativeInputsAndRetryLowersConfidence(t *testing.T) {
	price := &model.Price{
		UpstreamModel:         "m",
		BillingMode:           model.BillingModeToken,
		Currency:              "USD",
		InputPricePerMillion:  1,
		OutputPricePerMillion: 1,
		Confidence:            model.CostConfidenceExact,
	}
	if got := EstimateEffectiveCost(price, -1, 0, 0, 0, 1, 0); got.IsAvailable() || got.Cost != 0 {
		t.Fatalf("negative tokens should be unavailable: %+v", got)
	}
	got := EstimateEffectiveCost(price, 1000, 0, 0, 0, 1, 1)
	if !got.IsAvailable() || got.Confidence != model.CostConfidenceEstimated {
		t.Fatalf("retry should lower exact confidence: %+v", got)
	}
}

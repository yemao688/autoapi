package model

import (
	"encoding/json"
	"testing"
)

func TestPriceValidityAndEffectiveAt(t *testing.T) {
	p := Price{UpstreamModel: "m", BillingMode: BillingModeToken, Currency: "USD"}
	if !p.IsValid() {
		t.Error("expected valid price")
	}
	if !p.IsEffectiveAt(100) {
		t.Error("unbounded price should be effective")
	}

	p.EffectiveAt = 200
	if p.IsEffectiveAt(100) {
		t.Error("expected not-yet-effective price to be unavailable")
	}
	p.EffectiveAt = 0

	p.ExpiresAt = 100
	if p.IsEffectiveAt(100) {
		t.Error("expected expired price to be unavailable")
	}
	if !p.IsEffectiveAt(99) {
		t.Error("expected price to be effective before expiry")
	}
}

func TestPriceJSONRoundTrip(t *testing.T) {
	p := Price{
		UpstreamModel:         "gpt-4o",
		BillingMode:           BillingModeToken,
		InputPricePerMillion:  10,
		OutputPricePerMillion: 30,
		Currency:              "USD",
		Confidence:            CostConfidenceExact,
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Price
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.UpstreamModel != p.UpstreamModel || got.BillingMode != p.BillingMode || got.InputPricePerMillion != p.InputPricePerMillion {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
}

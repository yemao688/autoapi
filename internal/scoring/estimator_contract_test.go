package scoring_test

import (
	"math"
	"testing"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/scoring"
	"autoapi/internal/service"
)

func TestEstimatorEffectiveCostContractCanFeedScorer(t *testing.T) {
	base := func(mode model.BillingMode) *model.Price {
		return &model.Price{UpstreamModel: "m", BillingMode: mode, Currency: "USD", InputPricePerMillion: 10, OutputPricePerMillion: 20, RequestPricePerRequest: 1, Confidence: model.CostConfidenceExact}
	}
	cases := []struct {
		name      string
		mode      model.BillingMode
		available bool
	}{
		{"token", model.BillingModeToken, true},
		{"request", model.BillingModeRequest, true},
		// Quota/custom have no deterministic unit in the Phase 3A estimator;
		// they still safely produce a shadow score, but never a comparable cost.
		{"quota", model.BillingModeQuota, false},
		{"custom", model.BillingModeCustom, false},
		{"unknown", model.BillingModeUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cost := service.EstimateEffectiveCost(base(tc.mode), 1000, 500, 0, 0, 1, 1)
			if cost.IsAvailable() != tc.available {
				t.Fatalf("cost availability=%v, want %v: %+v", cost.IsAvailable(), tc.available, cost)
			}
			if tc.mode == model.BillingModeToken {
				// (1000*10 + 500*20) / 1e6, multiplied by 1 retry.
				if cost.Cost != 0.04 || cost.BaseRequestCount != 1 || cost.AdditionalRetryCount != 1 {
					t.Fatalf("token estimator semantics mismatch: %+v", cost)
				}
			}
			if tc.mode == model.BillingModeRequest {
				// Request billing counts base requests plus retries: 1 + 1 at $1.
				if cost.Cost != 2 || cost.BaseRequestCount != 1 || cost.AdditionalRetryCount != 1 {
					t.Fatalf("request estimator semantics mismatch: %+v", cost)
				}
			}
			got := scoring.ScoreTarget(scoring.TargetInput{Target: model.ModelRuleTarget{ID: tc.name}, Cost: cost}, scoring.ScoreContext{}, .01)
			if math.IsNaN(got.Overall) || math.IsInf(got.Overall, 0) {
				t.Fatalf("non-finite shadow score: %+v", got)
			}
		})
	}
}

func TestResolverEffectiveWindowNeverEntersComparableScoring(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price *model.Price
	}{
		{"future", &model.Price{UpstreamModel: "m", BillingMode: model.BillingModeToken, Currency: "USD", InputPricePerMillion: 1, EffectiveAt: time.Now().Add(time.Hour).UnixMilli(), Confidence: model.CostConfidenceExact}},
		{"expired", &model.Price{UpstreamModel: "m", BillingMode: model.BillingModeToken, Currency: "USD", InputPricePerMillion: 1, ExpiresAt: time.Now().Add(-time.Hour).UnixMilli(), Confidence: model.CostConfidenceExact}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost := service.EstimateEffectiveCost(tc.price, 100, 0, 0, 0, 1, 0)
			if cost.IsAvailable() {
				t.Fatalf("%s price must be unavailable: %+v", tc.name, cost)
			}
			got := scoring.ScoreTargets([]scoring.TargetInput{{Target: model.ModelRuleTarget{ID: tc.name}, Cost: cost}}, scoring.ScoreContext{})
			if len(got) != 1 || got[0].CostEfficiency == 100 {
				t.Fatalf("unresolved price entered comparison: %+v", got)
			}
		})
	}
}

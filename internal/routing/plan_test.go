package routing

import (
	"math"
	"reflect"
	"testing"

	"autoapi/internal/model"
	"autoapi/internal/scoring"
)

func candidate(id string, tier int, score, cost float64) CandidatePlanInput {
	return CandidatePlanInput{OriginalIndex: tier + len(id), TargetID: id, Tier: tier, Enabled: true, HardAvailable: true, CapabilitySatisfied: true, BudgetSatisfied: true, TargetScore: scoring.TargetScore{Overall: score, Reliability: 100}, EffectiveCost: model.EffectiveCost{Available: true, Currency: "USD", Confidence: model.CostConfidenceExact, Cost: cost}}
}

func ids(p CandidatePlan) []string { return p.PlannedOrder }

func TestPlanStrategiesAndBoundaries(t *testing.T) {
	in := []CandidatePlanInput{candidate("a", 0, 10, 3), candidate("b", 0, 20, 1), candidate("c", 1, 100, 0)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	for _, tc := range []struct {
		s    Strategy
		want []string
	}{{"", []string{"a", "b", "c"}}, {"bogus", []string{"a", "b", "c"}}, {PriorityFirst, []string{"a", "b", "c"}}, {ScoreWithinTier, []string{"b", "a", "c"}}, {CostFirst, []string{"b", "a", "c"}}} {
		if got := ids(BuildCandidatePlan(in, tc.s, Policy{MaxCostUSD: -1})); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %v want %v", tc.s, got, tc.want)
		}
	}
}

func TestPlanFiltersHardGatesAndCosts(t *testing.T) {
	in := []CandidatePlanInput{candidate("disabled", 0, 100, 0), candidate("open", 0, 90, 1), candidate("low", 0, 80, 2), candidate("ok", 0, 70, 3), candidate("unknown", 0, 60, 0), candidate("nan", 0, 50, math.NaN())}
	for i := range in {
		in[i].OriginalIndex = i
	}
	in[0].Enabled, in[1].CircuitOpen, in[2].TargetScore.Reliability = false, true, 10
	in[4].EffectiveCost = model.DefaultEffectiveCost()
	in[5].EffectiveCost = model.EffectiveCost{Available: true, Currency: "USD", Cost: math.NaN()}
	p := BuildCandidatePlan(in, CostFirst, Policy{MinimumReliability: 50, MaxCostUSD: 4})
	if !reflect.DeepEqual(ids(p), []string{"ok", "unknown", "nan"}) {
		t.Fatalf("unexpected plan: %v", ids(p))
	}
	if !p.Changed || len(p.OriginalOrder) != len(in) {
		t.Fatalf("comparison contract broken: %+v", p)
	}
}

func TestPlanTieAndInputImmutability(t *testing.T) {
	in := []CandidatePlanInput{candidate("a", 0, 5, 2), candidate("b", 0, 5, 1)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	orig := append([]CandidatePlanInput(nil), in...)
	p := BuildCandidatePlan(in, ScoreWithinTier, Policy{MaxCostUSD: -1})
	if !reflect.DeepEqual(ids(p), []string{"a", "b"}) || !reflect.DeepEqual(in, orig) {
		t.Fatalf("unstable or mutated input")
	}
}

func TestComparableCostRejectsUnknownAndInvalidValues(t *testing.T) {
	base := model.EffectiveCost{Available: true, Currency: "USD", Confidence: model.CostConfidenceExact, Cost: 1}
	for name, cost := range map[string]model.EffectiveCost{
		"unknown confidence":     func() model.EffectiveCost { c := base; c.Confidence = model.CostConfidenceUnknown; return c }(),
		"unavailable confidence": func() model.EffectiveCost { c := base; c.Confidence = model.CostConfidenceUnavailable; return c }(),
		"lowercase usd":          func() model.EffectiveCost { c := base; c.Currency = "usd"; return c }(),
		"nan":                    func() model.EffectiveCost { c := base; c.Cost = math.NaN(); return c }(),
		"positive infinity":      func() model.EffectiveCost { c := base; c.Cost = math.Inf(1); return c }(),
		"negative infinity":      func() model.EffectiveCost { c := base; c.Cost = math.Inf(-1); return c }(),
		"negative":               func() model.EffectiveCost { c := base; c.Cost = -1; return c }(),
	} {
		if comparableCost(cost) {
			t.Errorf("%s was considered comparable", name)
		}
	}
}

func TestPlanSortsNonContiguousTiersAndInvalidScoresLast(t *testing.T) {
	in := []CandidatePlanInput{candidate("a", 0, 1, 3), candidate("b", 1, 100, 1), candidate("c", 0, math.NaN(), 1), candidate("d", 0, 2, 2)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	p := BuildCandidatePlan(in, ScoreWithinTier, Policy{})
	if want := []string{"d", "a", "c", "b"}; !reflect.DeepEqual(p.PlannedOrder, want) {
		t.Fatalf("got %v want %v", p.PlannedOrder, want)
	}
	if !p.Changed || !reflect.DeepEqual(p.OriginalOrder, []string{"a", "b", "c", "d"}) {
		t.Fatalf("bad comparison: %+v", p)
	}
}

func TestCostFirstUnknownZeroIsNotFree(t *testing.T) {
	in := []CandidatePlanInput{candidate("unknown", 0, 1, 0), candidate("priced", 0, 1, 2)}
	in[0].OriginalIndex, in[1].OriginalIndex = 0, 1
	in[0].EffectiveCost = model.DefaultEffectiveCost()
	p := BuildCandidatePlan(in, CostFirst, Policy{})
	if want := []string{"priced", "unknown"}; !reflect.DeepEqual(p.PlannedOrder, want) {
		t.Fatalf("got %v want %v", p.PlannedOrder, want)
	}
}

func TestDuplicateIDsScoreSwapChangesPlan(t *testing.T) {
	in := []CandidatePlanInput{candidate("same", 0, 1, 1), candidate("same", 0, 2, 2)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	p := BuildCandidatePlan(in, ScoreWithinTier, Policy{})
	if !p.Changed || !p.Candidates[0].Changed || !p.Candidates[1].Changed {
		t.Fatalf("duplicate ID score swap was not reported: %+v", p)
	}
	if p.Candidates[0].Reason != string(ScoreWithinTier) || p.Candidates[1].Reason != string(ScoreWithinTier) {
		t.Fatalf("unexpected reasons: %+v", p.Candidates)
	}
}

func TestDuplicateIDsCostSwapChangesPlan(t *testing.T) {
	in := []CandidatePlanInput{candidate("same", 0, 1, 2), candidate("same", 0, 1, 1)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	p := BuildCandidatePlan(in, CostFirst, Policy{})
	if !p.Changed || !p.Candidates[0].Changed || !p.Candidates[1].Changed {
		t.Fatalf("duplicate ID cost swap was not reported: %+v", p)
	}
}

func TestDuplicateIDsFilteringWithoutReorderStillChangesPlan(t *testing.T) {
	in := []CandidatePlanInput{candidate("same", 0, 1, 1), candidate("same", 0, 2, 2), candidate("same", 0, 3, 3)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	in[0].Enabled = false
	p := BuildCandidatePlan(in, PriorityFirst, Policy{})
	if !p.Changed || p.Candidates[0].Changed || p.Candidates[1].Changed {
		t.Fatalf("duplicate ID filtering was not distinguished from reorder: %+v", p)
	}
	if p.Candidates[0].Reason != "eligible" || p.Candidates[1].Reason != "eligible" {
		t.Fatalf("survivor reasons changed unexpectedly: %+v", p.Candidates)
	}
}

func TestDuplicateIDsFilteringAndReorderChangesSurvivors(t *testing.T) {
	in := []CandidatePlanInput{candidate("same", 0, 1, 1), candidate("same", 0, 2, 2), candidate("same", 0, 3, 3)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	in[0].Enabled = false
	p := BuildCandidatePlan(in, ScoreWithinTier, Policy{})
	if !p.Changed || !p.Candidates[0].Changed || !p.Candidates[1].Changed {
		t.Fatalf("duplicate ID filtering and reorder was not reported: %+v", p)
	}
}

func TestChangedUsesEligibleBaselineAfterDisabledPrefix(t *testing.T) {
	in := []CandidatePlanInput{candidate("disabled", 0, 1, 1), candidate("a", 0, 2, 2), candidate("b", 0, 3, 3)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	in[0].Enabled = false
	p := BuildCandidatePlan(in, PriorityFirst, Policy{})
	if want := []string{"disabled", "a", "b"}; !reflect.DeepEqual(p.OriginalOrder, want) {
		t.Fatalf("original order: %v", p.OriginalOrder)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(p.PlannedOrder, want) {
		t.Fatalf("planned order: %v", p.PlannedOrder)
	}
	if !p.Changed || p.Candidates[0].Changed || p.Candidates[1].Changed || p.Candidates[0].Reason != "eligible" || p.Candidates[1].Reason != "eligible" {
		t.Fatalf("filter incorrectly reported as reorder: %+v", p)
	}
}

func TestChangedUsesEligibleBaselineAfterCircuitPrefix(t *testing.T) {
	in := []CandidatePlanInput{candidate("open", 0, 1, 1), candidate("a", 0, 2, 2), candidate("b", 0, 3, 3)}
	for i := range in {
		in[i].OriginalIndex = i
	}
	in[0].CircuitOpen = true
	p := BuildCandidatePlan(in, PriorityFirst, Policy{})
	if !p.Changed || !reflect.DeepEqual(p.PlannedOrder, []string{"a", "b"}) || p.Candidates[0].Changed || p.Candidates[1].Changed {
		t.Fatalf("circuit filtering incorrectly reported as reorder: %+v", p)
	}
}

func TestChangedFilterReorderAndFilterPlusReorder(t *testing.T) {
	t.Run("only reorder", func(t *testing.T) {
		in := []CandidatePlanInput{candidate("low", 0, 1, 1), candidate("high", 0, 2, 2)}
		for i := range in {
			in[i].OriginalIndex = i
		}
		p := BuildCandidatePlan(in, ScoreWithinTier, Policy{})
		if !p.Changed || !p.Candidates[0].Changed || !p.Candidates[1].Changed || p.Candidates[0].Reason != string(ScoreWithinTier) {
			t.Fatalf("expected reorder: %+v", p)
		}
	})
	t.Run("only cost filter", func(t *testing.T) {
		in := []CandidatePlanInput{candidate("disabled", 0, 3, 0), candidate("a", 0, 2, 1), candidate("b", 0, 1, 2)}
		for i := range in {
			in[i].OriginalIndex = i
		}
		in[0].Enabled = false
		p := BuildCandidatePlan(in, CostFirst, Policy{})
		if !p.Changed || p.Candidates[0].Changed || p.Candidates[1].Changed || !reflect.DeepEqual(p.PlannedOrder, []string{"a", "b"}) {
			t.Fatalf("expected filter only: %+v", p)
		}
	})
	t.Run("filter plus reorder", func(t *testing.T) {
		in := []CandidatePlanInput{candidate("disabled", 0, 3, 0), candidate("low", 0, 1, 1), candidate("high", 0, 2, 2)}
		for i := range in {
			in[i].OriginalIndex = i
		}
		in[0].Enabled = false
		p := BuildCandidatePlan(in, ScoreWithinTier, Policy{})
		if !p.Changed || !p.Candidates[0].Changed || !p.Candidates[1].Changed || !reflect.DeepEqual(p.PlannedOrder, []string{"high", "low"}) {
			t.Fatalf("expected filter and reorder: %+v", p)
		}
	})
}

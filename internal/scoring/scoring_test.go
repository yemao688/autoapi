package scoring

import (
	"math"
	"reflect"
	"testing"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
)

func target(id string, tier int) TargetInput {
	return TargetInput{Target: model.ModelRuleTarget{ID: id, Tier: tier}, Metrics: metrics.Snapshot{Requests: 20, Attempts: 20, Successes: 20}, Cost: model.EffectiveCost{Cost: .01, Available: true, Currency: "USD"}}
}

func TestScoreTargetHealthCapacityAndFailureClasses(t *testing.T) {
	cheapBad := target("cheap-bad", 0)
	cheapBad.Metrics.Status5xx, cheapBad.Metrics.Failures = 12, 12
	stable := target("stable", 0)
	stable.Cost.Cost = .02
	if ScoreTarget(cheapBad, ScoreContext{}, .015).Overall >= ScoreTarget(stable, ScoreContext{}, .015).Overall {
		t.Fatal("low price must not overcome a high provider failure rate")
	}
	noisy := target("429", 0)
	noisy.Metrics.Status429 = 1
	many429 := noisy
	many429.Metrics.Status429 = 15
	low := ScoreTarget(noisy, ScoreContext{}, .01).Capacity
	high := ScoreTarget(many429, ScoreContext{}, .01).Capacity
	if low < 99 || high > 50 || (100-low) >= (100-high)/15 {
		t.Fatal("429 capacity penalty should be forgiving initially and stronger when persistent")
	}
	if ScoreTarget(target("ok", 0), ScoreContext{}, .01).Reliability != 90 {
		t.Fatal("healthy prior reliability should be deterministic")
	}
}

func TestScoreTargetColdStartLatencyPriceAndHardState(t *testing.T) {
	cold := target("cold", 2)
	s := ScoreTarget(cold, ScoreContext{Stream: true}, .01)
	if s.Confidence != 83.33333333333334 || s.Overall != 79.8 {
		t.Fatalf("unexpected 20-sample confidence/baseline: %+v", s)
	}
	zero := cold
	zero.Metrics = metrics.Snapshot{}
	if got := ScoreTarget(zero, ScoreContext{}, .01); got.Confidence != 0 || got.Overall != 79.8 || got.ExplorationBonus != 8 {
		t.Fatalf("zero-sample baseline mismatch: %+v", got)
	}
	previous := 0.0
	for _, n := range []int64{1, 4, 20} {
		v := cold
		v.Metrics.Requests = n
		v.Metrics.Attempts = n
		got := ScoreTarget(v, ScoreContext{}, .01)
		if got.Confidence <= previous {
			t.Fatalf("confidence not monotonic at %d: %+v", n, got)
		}
		previous = got.Confidence
	}
	fast := target("fast", 2)
	fast.Metrics.TTFTMs = []int64{100, 110, 120}
	fast.Metrics.FirstByteMs = []int64{150, 160, 170}
	if ScoreTarget(fast, ScoreContext{Stream: true}, .01).TTFT <= ScoreTarget(cold, ScoreContext{Stream: true}, .01).TTFT {
		t.Fatal("stream TTFT must be scored")
	}
	unknown := target("unknown", 2)
	unknown.Cost = model.DefaultEffectiveCost()
	if ScoreTarget(unknown, ScoreContext{}, .01).CostEfficiency == 100 {
		t.Fatal("unknown price must not be treated as free")
	}
	cold.HardState.CircuitOpen = true
	cold.HardState.Disabled, cold.HardState.Cooldown = true, true
	if got := ScoreTarget(cold, ScoreContext{}, .01); got.Availability != Unavailable || got.Overall != 0 || got.Reason != "disabled" {
		t.Fatalf("hard state must remain unavailable: %+v", got)
	}
	cold.HardState.Disabled = false
	if got := ScoreTarget(cold, ScoreContext{}, .01); got.Reason != "circuit_open" {
		t.Fatalf("circuit priority mismatch: %+v", got)
	}
	cold.HardState.CircuitOpen = false
	if got := ScoreTarget(cold, ScoreContext{}, .01); got.Reason != "cooldown" {
		t.Fatalf("cooldown priority mismatch: %+v", got)
	}
	cold.HardState.Cooldown = false
	cold.HardState.HalfOpen = true
	if got := ScoreTarget(cold, ScoreContext{}, .01); got.Reason != "half_open_recovery" {
		t.Fatalf("half-open reason mismatch: %+v", got)
	}
}

func TestScoreTargetNonComparableCostsAreFiniteAndNeutral(t *testing.T) {
	cases := []struct {
		name          string
		cost          float64
		referenceCost float64
	}{
		{"nan_cost", math.NaN(), .01},
		{"positive_inf_cost", math.Inf(1), .01},
		{"negative_inf_cost", math.Inf(-1), .01},
		{"negative_cost", -.01, .01},
		{"nan_reference", .01, math.NaN()},
		{"positive_inf_reference", .01, math.Inf(1)},
		{"negative_inf_reference", .01, math.Inf(-1)},
		{"negative_reference", .01, -.01},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := target(tc.name, 0)
			in.Cost.Cost = tc.cost
			got := ScoreTarget(in, ScoreContext{}, tc.referenceCost)
			if !finiteScore(got) {
				t.Fatalf("non-finite score: %+v", got)
			}
			if got.CostEfficiency != baselineCost {
				t.Fatalf("non-comparable cost must be neutral: got %v", got.CostEfficiency)
			}
		})
	}
}

func finiteScore(s TargetScore) bool {
	for _, v := range []float64{s.Reliability, s.Latency, s.TTFT, s.Capacity, s.CostEfficiency, s.Confidence, s.Overall, s.EstimatedCost, s.ExplorationBonus} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

func TestScoreTargetsSameTierReferenceAndStableOrder(t *testing.T) {
	a, b, c := target("a", 1), target("b", 1), target("c", 0)
	a.Cost.Cost, b.Cost.Cost, c.Cost.Cost = .01, .03, .01
	a.Metrics.FirstByteMs = []int64{300, 100, 200}
	a.Metrics.TTFTMs = []int64{250, 80, 160}
	b.Metrics.FirstByteMs = []int64{500, 400}
	b.Metrics.TTFTMs = []int64{450, 350}
	inputs := []TargetInput{a, b, c}
	original := make([]TargetInput, len(inputs))
	for i := range inputs {
		original[i] = inputs[i]
		original[i].Metrics.FirstByteMs = append([]int64(nil), inputs[i].Metrics.FirstByteMs...)
		original[i].Metrics.TTFTMs = append([]int64(nil), inputs[i].Metrics.TTFTMs...)
	}
	got := ScoreTargets(inputs, ScoreContext{})
	if len(got) != 3 || got[0].TargetID != "a" || got[1].TargetID != "b" || got[2].TargetID != "c" {
		t.Fatalf("scoring must preserve input order: %+v", got)
	}
	if got[0].CostEfficiency == got[1].CostEfficiency {
		t.Fatal("same-tier median reference should distinguish effective costs")
	}
	if !reflect.DeepEqual(inputs, original) {
		t.Fatalf("ScoreTargets mutated input: before=%+v after=%+v", original, inputs)
	}
	if got[0].CostEfficiency == got[2].CostEfficiency {
		t.Fatal("different tiers must not share cost reference")
	}
}

func TestScoreTargetsUsesDeterministicUpperMedian(t *testing.T) {
	inputs := []TargetInput{
		target("one", 3), target("two", 3), target("hundred", 3), target("two-hundred", 3),
	}
	inputs[0].Cost.Cost, inputs[1].Cost.Cost = 1, 2
	inputs[2].Cost.Cost, inputs[3].Cost.Cost = 100, 200
	got := ScoreTargets(inputs, ScoreContext{})
	// Four sorted costs use values[len/2], i.e. 100, rather than the lower
	// median 2. This makes the even-cardinality reference deterministic.
	if got[2].CostEfficiency != 50 {
		t.Fatalf("expected upper median reference to score cost 100 neutrally: %+v", got)
	}
	if got[1].CostEfficiency <= 50 {
		t.Fatalf("cost 2 should be cheaper than the upper-median reference: %+v", got)
	}
}

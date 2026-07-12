// Package scoring contains the deterministic, side-effect-free Phase 3A
// shadow scorer. It deliberately has no knowledge of the proxy or the store.
package scoring

import (
	"math"
	"sort"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
)

// Constants are intentionally fixed in Phase 3A; they are not user weights.
const (
	baselineReliability = 90.0
	baselineLatency     = 70.0
	baselineCapacity    = 100.0
	baselineCost        = 50.0
	priorSamples        = 4.0
	max429Penalty       = 100.0
	halfOpenCapacity    = 0.65
)

// TargetInput is all configuration/state needed to score one target. Hard
// states are inputs, not decisions: a scorer must never re-enable a target.
type TargetInput struct {
	Target    model.ModelRuleTarget
	Metrics   metrics.Snapshot
	Cost      model.EffectiveCost
	HardState HardState
}

type HardState struct {
	Disabled    bool
	CircuitOpen bool
	Cooldown    bool
	HalfOpen    bool
}

// ScoreContext contains the only request property consumed by the scorer.
// Token/request/quota/custom pricing must be resolved by service's estimator
// first; the scorer consumes only the resulting EffectiveCost. Effective and
// expiry timestamps are likewise validated by the resolver before this point.
type ScoreContext struct{ Stream bool }

// ReplayOneRequest scores recorded attempts only. It performs no I/O, clock
// reads, or mutation; historical breaker state is intentionally unavailable.
func ReplayOneRequest(log model.RequestLog, rule model.ModelRule, endpoint string, snapshots map[model.TargetMetricKey]metrics.Snapshot, costs []model.EffectiveCost) []TargetScore {
	inputs := make([]TargetInput, 0, len(log.Chain))
	for i, attempt := range log.Chain {
		var target model.ModelRuleTarget
		for _, candidate := range rule.Targets {
			if candidate.ID == attempt.TargetID && candidate.ProviderID == attempt.ProviderID && candidate.ModelName == attempt.ModelName {
				target = candidate
				break
			}
		}
		key := model.TargetMetricKey{TargetID: attempt.TargetID, ProviderID: attempt.ProviderID, ModelName: attempt.ModelName, Endpoint: endpoint}
		cost := model.DefaultEffectiveCost()
		if target.ID != "" && i < len(costs) {
			cost = costs[i]
		}
		inputs = append(inputs, TargetInput{Target: target, Metrics: snapshots[key], Cost: cost, HardState: HardState{}})
	}
	refs := make(map[int][]float64)
	for _, in := range inputs {
		if comparableCost(in.Cost) {
			refs[in.Target.Tier] = appendFinite(refs[in.Target.Tier], in.Cost.Cost)
		}
	}
	for tier := range refs {
		sort.Float64s(refs[tier])
	}
	out := make([]TargetScore, len(inputs))
	for i, in := range inputs {
		ref := 0.0
		if v := refs[in.Target.Tier]; len(v) > 0 {
			ref = v[len(v)/2]
		}
		out[i] = ScoreTarget(in, ScoreContext{Stream: log.IsStream}, ref)
	}
	return out
}

// TargetScore is a shadow result. Component scores are percentages in [0,100].
// Overall is also a percentage; EstimatedCost is USD for this request.
type TargetScore struct {
	TargetID         string  `json:"target_id"`
	Tier             int     `json:"tier"`
	Reliability      float64 `json:"reliability"`
	Latency          float64 `json:"latency"`
	TTFT             float64 `json:"ttft"`
	Capacity         float64 `json:"capacity"`
	CostEfficiency   float64 `json:"cost_efficiency"`
	Confidence       float64 `json:"confidence"` // 0..100 confidence in observations
	Overall          float64 `json:"overall"`
	EstimatedCost    float64 `json:"estimated_cost"`
	SampleCount      int64   `json:"sample_count"`
	Availability     string  `json:"availability"`
	Reason           string  `json:"reason"`
	PriceVersion     string  `json:"price_version"`
	ExplorationBonus float64 `json:"exploration_bonus"`
}

const (
	Available   = "available"
	Unavailable = "unavailable"
)

// ScoreTarget computes one score without I/O, clocks, randomness, or mutation.
func ScoreTarget(in TargetInput, context ScoreContext, referenceCost float64) TargetScore {
	s := TargetScore{TargetID: in.Target.ID, Tier: in.Target.Tier, Availability: Available, PriceVersion: in.Cost.PriceVersion}
	if in.HardState.Disabled {
		s.Availability, s.Reason = Unavailable, "disabled"
	} else if in.HardState.CircuitOpen {
		s.Availability, s.Reason = Unavailable, "circuit_open"
	} else if in.HardState.Cooldown {
		s.Availability, s.Reason = Unavailable, "cooldown"
	}
	s.SampleCount = max64(in.Metrics.Requests, in.Metrics.Attempts)
	confidence := clamp(safeDiv(float64(s.SampleCount), float64(s.SampleCount)+priorSamples), 0, 1)
	s.Confidence = confidence * 100

	healthFailures := in.Metrics.Status5xx + in.Metrics.Transport + in.Metrics.Truncated
	healthRate := safeDiv(float64(healthFailures), float64(max64(1, in.Metrics.Attempts)))
	s.Reliability = clamp(100*(0.90-0.80*healthRate), 0, 100)
	// 429 is capacity pressure, not provider health. A convex square curve is
	// deliberately forgiving at low rates (1/20 => 99.75) and accelerates as
	// pressure persists (15/20 => 43.75).
	rate429 := safeDiv(float64(in.Metrics.Status429), float64(max64(1, in.Metrics.Attempts)))
	s.Capacity = clamp(100-max429Penalty*math.Pow(clamp(rate429, 0, 1), 2), 0, 100)
	if in.HardState.HalfOpen && s.Availability == Available {
		s.Capacity *= halfOpenCapacity
		s.Reason = "half_open_recovery"
	}

	latency := percentile(in.Metrics.FirstByteMs, .75)
	ttft := percentile(in.Metrics.TTFTMs, .75)
	s.Latency = latencyScore(latency)
	s.TTFT = latencyScore(ttft)
	if context.Stream {
		s.Latency = 0.35*s.Latency + 0.65*s.TTFT
	}

	s.EstimatedCost = finiteNonnegative(in.Cost.Cost)
	if comparableCost(in.Cost) && comparableReference(referenceCost) {
		if referenceCost == 0 {
			// A zero reference is a legitimate all-free tier, but logarithmic
			// relative cost has no meaningful denominator. Keep it neutral.
			s.CostEfficiency = baselineCost
		} else if s.EstimatedCost == 0 {
			s.CostEfficiency = 100
		} else {
			ratio := s.EstimatedCost / referenceCost
			s.CostEfficiency = clamp(50-30*math.Log(ratio), 0, 100)
		}
	} else {
		s.CostEfficiency = baselineCost
		if s.Reason == "" {
			s.Reason = "price_unavailable_or_not_comparable"
		}
	}

	observed := .36*s.Reliability + .22*s.Latency + .10*s.TTFT + .18*s.Capacity + .14*s.CostEfficiency
	baseline := .36*baselineReliability + .22*baselineLatency + .10*baselineLatency + .18*baselineCapacity + .14*baselineCost
	s.Overall = clamp(confidence*observed+(1-confidence)*baseline, 0, 100)
	if s.Availability != Available {
		s.Overall = 0
	}
	s.ExplorationBonus = clamp(8*(1-confidence), 0, 8)
	return sanitize(s)
}

// ScoreTargets scores in input order and computes a reference independently
// for each tier. It is a snapshot only; it never sorts or changes candidates.
func ScoreTargets(inputs []TargetInput, context ScoreContext) []TargetScore {
	refs := make(map[int][]float64)
	for _, in := range inputs {
		if comparableCost(in.Cost) {
			refs[in.Target.Tier] = appendFinite(refs[in.Target.Tier], in.Cost.Cost)
		}
	}
	medians := make(map[int]float64, len(refs))
	for tier, values := range refs {
		sort.Float64s(values)
		medians[tier] = values[len(values)/2]
	}
	out := make([]TargetScore, len(inputs))
	for i, in := range inputs {
		out[i] = ScoreTarget(in, context, medians[in.Target.Tier])
	}
	return out
}

func appendFinite(v []float64, x float64) []float64 {
	if !finite(x) || x < 0 {
		return v
	}
	return append(v, x)
}
func comparableCost(c model.EffectiveCost) bool {
	return c.IsAvailable() && finite(c.Cost) && c.Cost >= 0 && c.Currency == "USD" &&
		c.Confidence != model.CostConfidenceUnknown && c.Confidence != model.CostConfidenceUnavailable
}
func comparableReference(x float64) bool { return finite(x) && x >= 0 }
func percentile(v []int64, p float64) float64 {
	a := make([]float64, 0, len(v))
	for _, x := range v {
		if x > 0 {
			a = append(a, float64(x))
		}
	}
	if len(a) == 0 {
		return 0
	}
	sort.Float64s(a)
	i := int(math.Ceil(p*float64(len(a)))) - 1
	if i < 0 {
		i = 0
	}
	return a[i]
}
func latencyScore(ms float64) float64 {
	if ms <= 0 {
		return baselineLatency
	}
	return clamp(100/(1+ms/1000), 0, 100)
}
func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func finiteNonnegative(x float64) float64 {
	if !finite(x) || x < 0 {
		return 0
	}
	return x
}
func safeDiv(a, b float64) float64 {
	if b == 0 || !finite(a) || !finite(b) {
		return 0
	}
	return a / b
}
func clamp(x, lo, hi float64) float64 {
	if !finite(x) {
		return lo
	}
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func sanitize(s TargetScore) TargetScore {
	s.Reliability = clamp(s.Reliability, 0, 100)
	s.Latency = clamp(s.Latency, 0, 100)
	s.TTFT = clamp(s.TTFT, 0, 100)
	s.Capacity = clamp(s.Capacity, 0, 100)
	s.CostEfficiency = clamp(s.CostEfficiency, 0, 100)
	s.Confidence = clamp(s.Confidence, 0, 100)
	s.Overall = clamp(s.Overall, 0, 100)
	return s
}

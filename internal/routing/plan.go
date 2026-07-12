// Package routing contains pure, shadow-only routing planning primitives.
//
// Phase 4A deliberately does not participate in production routing. A plan is
// a deterministic description of an order that a later phase may compare with
// the existing matcher; it performs no I/O, clock reads, randomness, or
// mutation.
package routing

import (
	"math"
	"sort"

	"autoapi/internal/model"
	"autoapi/internal/scoring"
)

type Strategy string

const (
	PriorityFirst   Strategy = "priority_first"
	ScoreWithinTier Strategy = "score_within_tier"
	CostFirst       Strategy = "cost_first"
)

func normalizeStrategy(s Strategy) Strategy {
	switch s {
	case PriorityFirst, ScoreWithinTier, CostFirst:
		return s
	default:
		return PriorityFirst
	}
}

// Policy contains hard gates only. It intentionally has no user weights.
type Policy struct {
	MinimumReliability   float64
	MaxCostUSD           float64
	RequiredCapabilities []string
}

// CandidatePlanInput is a snapshot supplied by the caller. Hard availability
// is explicit so a planner can never promote a disabled or unavailable target.
type CandidatePlanInput struct {
	OriginalIndex       int
	TargetID            string
	Tier                int
	Enabled             bool
	HardAvailable       bool
	CircuitOpen         bool
	Cooldown            bool
	CapabilitySatisfied bool
	BudgetSatisfied     bool
	Capabilities        []string
	TargetScore         scoring.TargetScore
	EffectiveCost       model.EffectiveCost
}

type PlanCandidate struct {
	OriginalIndex         int
	OriginalEligibleIndex int
	TargetID              string
	Tier                  int
	Enabled               bool
	Available             bool
	TargetScore           scoring.TargetScore
	EffectiveCost         model.EffectiveCost
	Reason                string
	ShadowOnly            bool
	Changed               bool
}

type CandidatePlan struct {
	Candidates []PlanCandidate
	// These are display sequences; IDs may repeat. Changed is computed from
	// candidate positions and filtering, never by comparing these strings.
	OriginalOrder []string
	PlannedOrder  []string
	Changed       bool
	Strategy      Strategy
}

func comparableCost(c model.EffectiveCost) bool {
	return c.Available && c.Currency == "USD" &&
		c.Confidence != model.CostConfidenceUnknown && c.Confidence != model.CostConfidenceUnavailable &&
		!math.IsNaN(c.Cost) && !math.IsInf(c.Cost, 0) && c.Cost >= 0
}

func hardReason(in CandidatePlanInput, p Policy) string {
	if !in.Enabled {
		return "disabled"
	}
	if !in.HardAvailable || in.TargetScore.Availability == scoring.Unavailable {
		return "unavailable"
	}
	if in.CircuitOpen {
		return "circuit_open"
	}
	if in.Cooldown {
		return "cooldown"
	}
	if !in.CapabilitySatisfied {
		return "capability"
	}
	if !in.BudgetSatisfied {
		return "budget"
	}
	if finite(p.MinimumReliability) && p.MinimumReliability > 0 && in.TargetScore.Reliability < p.MinimumReliability {
		return "reliability"
	}
	// Zero means that no cost ceiling was supplied; unknown costs are never
	// silently accepted as free.
	if finite(p.MaxCostUSD) && p.MaxCostUSD > 0 && comparableCost(in.EffectiveCost) && in.EffectiveCost.Cost > p.MaxCostUSD {
		return "budget"
	}
	if len(p.RequiredCapabilities) > 0 {
		for _, required := range p.RequiredCapabilities {
			found := false
			for _, have := range in.Capabilities {
				if have == required {
					found = true
					break
				}
			}
			if !found {
				return "capability"
			}
		}
	}
	return ""
}

// BuildCandidatePlan filters hard-unavailable inputs and sorts only within
// tier groups. Thus tier boundaries never move. Unknown,
// non-USD, NaN, Inf, and negative prices are retained but are never comparable
// (and therefore never treated as free); they remain after comparable prices
// in cost_first, in original order. The input slice and all input values are
// left untouched. This plan is Phase 4A shadow-only; production routing does
// not call it, and it creates no retry/stream execution chain.
func BuildCandidatePlan(inputs []CandidatePlanInput, strategy Strategy, policy Policy) CandidatePlan {
	strategy = normalizeStrategy(strategy)
	plan := CandidatePlan{Strategy: strategy, Candidates: make([]PlanCandidate, 0, len(inputs))}
	for i, in := range inputs {
		idx := in.OriginalIndex
		if idx < 0 {
			idx = i
		}
		plan.OriginalOrder = append(plan.OriginalOrder, in.TargetID)
		reason := hardReason(in, policy)
		if reason != "" {
			continue
		}
		plan.Candidates = append(plan.Candidates, PlanCandidate{
			OriginalIndex:         idx,
			OriginalEligibleIndex: len(plan.Candidates),
			TargetID:              in.TargetID,
			Tier:                  in.Tier,
			Enabled:               in.Enabled,
			Available:             true,
			TargetScore:           in.TargetScore,
			EffectiveCost:         in.EffectiveCost,
			Reason:                "eligible",
			ShadowOnly:            true,
		})
	}
	sort.SliceStable(plan.Candidates, func(i, j int) bool {
		a, b := plan.Candidates[i], plan.Candidates[j]
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if strategy == PriorityFirst {
			return a.OriginalIndex < b.OriginalIndex
		}
		if strategy == ScoreWithinTier {
			af, bf := finite(a.TargetScore.Overall), finite(b.TargetScore.Overall)
			if af != bf {
				return af
			}
			if af && a.TargetScore.Overall != b.TargetScore.Overall {
				return a.TargetScore.Overall > b.TargetScore.Overall
			}
			return a.OriginalIndex < b.OriginalIndex
		}
		ac, bc := comparableCost(a.EffectiveCost), comparableCost(b.EffectiveCost)
		if ac != bc {
			return ac
		}
		if ac && a.EffectiveCost.Cost != b.EffectiveCost.Cost {
			return a.EffectiveCost.Cost < b.EffectiveCost.Cost
		}
		return a.OriginalIndex < b.OriginalIndex
	})
	for i := range plan.Candidates {
		// Compare against the eligible baseline, not the input index: filtering
		// disabled/open candidates must not make surviving candidates appear
		// reordered merely because their absolute indexes shifted.
		plan.Candidates[i].Changed = plan.Candidates[i].OriginalEligibleIndex != i
		plan.Candidates[i].Reason = reasonFor(plan.Candidates[i], strategy)
		plan.PlannedOrder = append(plan.PlannedOrder, plan.Candidates[i].TargetID)
	}
	hasFiltered := len(plan.Candidates) != len(inputs)
	hasReordered := false
	for _, candidate := range plan.Candidates {
		if candidate.Changed {
			hasReordered = true
			break
		}
	}
	plan.Changed = hasFiltered || hasReordered
	return plan
}

func reasonFor(c PlanCandidate, strategy Strategy) string {
	if c.Changed {
		return string(strategy)
	}
	return "eligible"
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

package proxy

import (
	"log/slog"
	"math"
	"time"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/routing"
	"autoapi/internal/scoring"
)

type metricSnapshotter interface {
	CurrentSnapshot(model.TargetMetricKey) metrics.Snapshot
}

type routeMetricSnapshotter interface {
	CurrentRouteSnapshot(model.RouteModeKey) metrics.RouteSnapshot
}

type planDiagnostics struct {
	Strategy            routing.Strategy
	RuleID              string
	OriginalOrder       []string
	ScoreSortedOrder    []string
	FinalOrder          []string
	Candidates          []candidateDiagnostic
	PlannerReason       string
	ReorderReason       string
	FinalReorderReason  string
	ExplorationSelected string
	ExplorationReason   string
}

type candidateDiagnostic struct {
	OriginalIndex  int
	TargetID       string
	RouteKey       model.RouteModeKey
	Tier           int
	Overall        float64
	Reliability    float64
	Latency        float64
	Capacity       float64
	CostEfficiency float64
	Confidence     float64
	Cost           float64
	CostAvailable  bool
	ScoreReason    string
	PlannerReason  string
	Changed        bool
	Availability   string
}

type explorationKey struct {
	ruleID string
	tier   int
}

type explorationState struct {
	qualified       uint64
	lastExploration time.Time
	lastTouched     time.Time
	hasExplored     bool
}

const maxExplorationScopes = 1024

// planCandidates builds the legacy shadow plan for diagnostics and compatibility
// tooling. Production resolution does not call it; target slice order is the
// only active execution order.
func (p *Proxy) planCandidates(req *InboundRequest, candidates []candidate) []candidate {
	if len(candidates) == 0 || !activeStrategy(candidates[0].strategy) {
		return candidates
	}
	inputs := make([]routing.CandidatePlanInput, len(candidates))
	scoreInputs := make([]scoring.TargetInput, len(candidates))
	routeSnapshotter, hasRouteMetrics := p.metricSink.(routeMetricSnapshotter)
	for i, c := range candidates {
		target := model.ModelRuleTarget{ID: c.targetID, ProviderID: c.provider.ID, ModelName: c.modelName, Tier: c.tier}
		var snapshot metrics.Snapshot
		if hasRouteMetrics {
			upstreamProtocol := string(c.protocol)
			if c.convertTo != "" {
				upstreamProtocol = string(c.convertTo)
			}
			route := routeSnapshotter.CurrentRouteSnapshot(model.RouteModeKey{TargetID: c.targetID, InboundProtocol: string(c.protocol), UpstreamProtocol: upstreamProtocol})
			snapshot = metrics.Snapshot{Attempts: route.Attempts, Successes: route.Successes, Failures: route.Failures, Status429: route.Status429, Status5xx: route.Status5xx, Transport: route.Transport, Truncated: route.Truncated, ConversionLocal: route.ConversionLocal, ClientAborts: route.ClientAborts, Downstream: route.Downstream, FirstByteMs: route.FirstByteMs, TTFTMs: route.TTFTMs, LastUsed: route.LastUsed, LastSuccess: route.LastSuccess, LastFailure: route.LastFailure}
		}
		cost := model.DefaultEffectiveCost()
		if c.requestPriceAvailable && !math.IsNaN(c.requestPrice) && !math.IsInf(c.requestPrice, 0) && c.requestPrice >= 0 {
			// Phase 2 cost_first compares the next upstream attempt. Retry
			// limits remain execution budgets and do not inflate planning cost.
			cost = model.EffectiveCost{Cost: c.requestPrice, Currency: "USD", Available: true}
		}
		// Planning is read-only: do not call breakerFor here because it creates
		// breakers (and emits logs) for providers that have never executed. A
		// missing breaker is the normal closed state. The execution path still
		// claims half-open probes through breakerFor(...).Allow().
		state, _ := p.breakerState(c.provider.ID)
		halfOpen := state == StateHalfOpen
		scoreInputs[i] = scoring.TargetInput{Target: target, Metrics: snapshot, Cost: cost, HardState: scoring.HardState{HalfOpen: halfOpen}}
		// There is no real active capability/budget gate yet. Keep the shadow
		// planner's assumptions explicit so these candidates are not filtered.
		inputs[i] = routing.CandidatePlanInput{OriginalIndex: i, TargetID: c.targetID, Tier: c.tier, Enabled: true, HardAvailable: true, CapabilitySatisfied: true, BudgetSatisfied: true, EffectiveCost: cost}
	}
	scores := scoring.ScoreTargets(scoreInputs, scoring.ScoreContext{Stream: req.Stream})
	for i, score := range scores {
		inputs[i].TargetScore = score
	}
	plan := routing.BuildCandidatePlan(inputs, candidates[0].strategy, routing.Policy{})
	if len(plan.Candidates) == 0 {
		return candidates
	}
	out := make([]candidate, 0, len(plan.Candidates))
	for _, planned := range plan.Candidates {
		if planned.OriginalIndex < 0 || planned.OriginalIndex >= len(candidates) {
			return candidates
		}
		out = append(out, candidates[planned.OriginalIndex])
	}
	if len(out) != len(candidates) {
		return candidates
	}
	scoreSortedOrder := candidateIDs(out)
	plannerReason := plannerReorderReason(candidates[0].strategy, plan)
	explorationSelected, explorationReason := "", "strategy_not_eligible"
	if candidates[0].strategy == routing.ScoreWithinTier {
		out, explorationSelected, explorationReason = p.applyExploration(candidates, out, scoreInputs, scores)
	} else {
		explorationReason = "strategy_not_score_within_tier"
	}
	finalReorderReason := plannerReason
	if explorationSelected != "" {
		finalReorderReason = "exploration_selected_cold_highest_tier"
	}
	p.emitPlanDiagnostics(planDiagnostics{
		Strategy:            candidates[0].strategy,
		RuleID:              candidates[0].ruleID,
		OriginalOrder:       candidateIDs(candidates),
		ScoreSortedOrder:    scoreSortedOrder,
		FinalOrder:          candidateIDs(out),
		Candidates:          diagnosticCandidates(candidates, scores, inputs, plan),
		PlannerReason:       plannerReason,
		ReorderReason:       plannerReason,
		FinalReorderReason:  finalReorderReason,
		ExplorationSelected: explorationSelected,
		ExplorationReason:   explorationReason,
	})
	return out
}

func candidateIDs(candidates []candidate) []string {
	out := make([]string, len(candidates))
	for i := range candidates {
		out[i] = candidates[i].targetID
	}
	return out
}

func routeKeyForCandidate(c candidate) model.RouteModeKey {
	upstream := string(c.protocol)
	if c.convertTo != "" {
		upstream = string(c.convertTo)
	}
	return model.RouteModeKey{TargetID: c.targetID, InboundProtocol: string(c.protocol), UpstreamProtocol: upstream}
}

func plannerReorderReason(strategy routing.Strategy, plan routing.CandidatePlan) string {
	reordered := false
	for i, planned := range plan.Candidates {
		if planned.OriginalIndex != i {
			reordered = true
			break
		}
	}
	suffix := "order_unchanged"
	if reordered {
		suffix = "reordered"
	}
	switch strategy {
	case routing.CostFirst:
		return "cost_" + suffix
	default:
		return "score_" + suffix
	}
}

func diagnosticCandidates(candidates []candidate, scores []scoring.TargetScore, inputs []routing.CandidatePlanInput, plan ...routing.CandidatePlan) []candidateDiagnostic {
	out := make([]candidateDiagnostic, len(candidates))
	planByOriginal := make(map[int]routing.PlanCandidate)
	if len(plan) > 0 {
		for _, candidate := range plan[0].Candidates {
			planByOriginal[candidate.OriginalIndex] = candidate
		}
	}
	for i, c := range candidates {
		s := scores[i]
		plannerReason, changed := "filtered", false
		if planned, ok := planByOriginal[i]; ok {
			plannerReason, changed = planned.Reason, planned.Changed
		}
		out[i] = candidateDiagnostic{OriginalIndex: i, TargetID: c.targetID, RouteKey: routeKeyForCandidate(c), Tier: c.tier, Overall: s.Overall, Reliability: s.Reliability, Latency: s.Latency, Capacity: s.Capacity, CostEfficiency: s.CostEfficiency, Confidence: s.Confidence, Cost: inputs[i].EffectiveCost.Cost, CostAvailable: inputs[i].EffectiveCost.Available, ScoreReason: s.Reason, PlannerReason: plannerReason, Changed: changed, Availability: s.Availability}
	}
	return out
}

func (p *Proxy) applyExploration(candidates, scoreOrder []candidate, scoreInputs []scoring.TargetInput, scores []scoring.TargetScore) ([]candidate, string, string) {
	if len(candidates) < 2 {
		return scoreOrder, "", "insufficient_candidates"
	}
	minTier := candidates[0].tier
	for _, c := range candidates[1:] {
		if c.tier < minTier {
			minTier = c.tier
		}
	}
	eligible := make([]int, 0, len(candidates))
	for i, c := range candidates {
		if c.tier == minTier && scores[i].Availability == scoring.Available {
			eligible = append(eligible, i)
		}
	}
	if len(eligible) < 2 {
		return scoreOrder, "", "insufficient_highest_tier_candidates"
	}
	key := explorationKey{ruleID: candidates[0].ruleID, tier: minTier}
	p.explorationMu.Lock()
	now := p.explorationNowLocked()
	if p.exploration == nil {
		p.exploration = make(map[explorationKey]*explorationState)
	}
	state := p.exploration[key]
	if state == nil {
		if len(p.exploration) >= maxExplorationScopes {
			p.evictExplorationScopeLocked()
		}
		state = &explorationState{}
		p.exploration[key] = state
	}
	state.qualified++
	state.lastTouched = now
	qualified := state.qualified
	selected := -1
	var selectedAt time.Time
	for _, i := range eligible {
		lastUsed := scoreInputs[i].Metrics.LastUsed
		cold := lastUsed.IsZero() || now.Sub(lastUsed) >= 10*time.Minute
		if !cold {
			continue
		}
		if selected == -1 || lastUsed.Before(selectedAt) || (lastUsed.Equal(selectedAt) && i < selected) {
			selected, selectedAt = i, lastUsed
		}
	}
	if qualified%20 != 0 {
		p.explorationMu.Unlock()
		return scoreOrder, "", "not_20th_qualified_request"
	}
	if state.hasExplored && now.Sub(state.lastExploration) < 30*time.Second {
		p.explorationMu.Unlock()
		return scoreOrder, "", "exploration_cooldown"
	}
	if selected == -1 {
		p.explorationMu.Unlock()
		return scoreOrder, "", "no_cold_candidate"
	}
	state.lastExploration = now
	state.hasExplored = true
	p.explorationMu.Unlock()
	out := make([]candidate, 0, len(scoreOrder))
	out = append(out, candidates[selected])
	skipped := false
	for _, c := range scoreOrder {
		if !skipped && c.targetID == candidates[selected].targetID && routeKeyForCandidate(c) == routeKeyForCandidate(candidates[selected]) {
			skipped = true
			continue
		}
		out = append(out, c)
	}
	return out, candidates[selected].targetID, "exploration_selected_cold_highest_tier"
}

func (p *Proxy) explorationNowLocked() time.Time {
	now := time.Now()
	if p.explorationClock != nil {
		now = p.explorationClock()
	}
	if now.Before(p.explorationLastNow) {
		return p.explorationLastNow
	}
	p.explorationLastNow = now
	return now
}

func (p *Proxy) evictExplorationScopeLocked() {
	var oldest explorationKey
	var oldestAt time.Time
	found := false
	for key, state := range p.exploration {
		if !found || state.lastTouched.Before(oldestAt) || (state.lastTouched.Equal(oldestAt) && (key.ruleID < oldest.ruleID || (key.ruleID == oldest.ruleID && key.tier < oldest.tier))) {
			oldest, oldestAt = key, state.lastTouched
			found = true
		}
	}
	if found {
		delete(p.exploration, oldest)
	}
}

func (p *Proxy) emitPlanDiagnostics(d planDiagnostics) {
	slog.Debug("proxy active plan", "strategy", d.Strategy, "rule_id", d.RuleID, "original_order", d.OriginalOrder, "score_sorted_order", d.ScoreSortedOrder, "final_order", d.FinalOrder, "planner_reason", d.PlannerReason, "reorder_reason", d.ReorderReason, "final_reorder_reason", d.FinalReorderReason, "candidates", d.Candidates, "exploration_selected", d.ExplorationSelected, "exploration_reason", d.ExplorationReason)
	if p.planObserver == nil {
		return
	}
	copyOf := d
	copyOf.OriginalOrder = append([]string(nil), d.OriginalOrder...)
	copyOf.ScoreSortedOrder = append([]string(nil), d.ScoreSortedOrder...)
	copyOf.FinalOrder = append([]string(nil), d.FinalOrder...)
	copyOf.Candidates = append([]candidateDiagnostic(nil), d.Candidates...)
	func() {
		defer func() { _ = recover() }()
		p.planObserver(copyOf)
	}()
}

func (p *Proxy) resetPlanningState() {
	p.resetRouteBreakers()
	p.targetBreakersMu.Lock()
	p.targetBreakers = make(map[model.RouteModeKey]*targetBreaker)
	p.targetBreakersMu.Unlock()
	p.explorationMu.Lock()
	p.exploration = make(map[explorationKey]*explorationState)
	p.explorationLastNow = time.Time{}
	p.explorationMu.Unlock()
}

func activeStrategy(strategy routing.Strategy) bool {
	return strategy == routing.ScoreWithinTier || strategy == routing.CostFirst
}

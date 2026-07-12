package proxy

import (
	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/routing"
	"autoapi/internal/scoring"
	"autoapi/internal/service"
)

const (
	planningInputTokens  = 512
	planningOutputTokens = 256
)

type priceResolver interface {
	ResolvePrice(providerID, modelName, endpointKind string) (*model.Price, error)
}
type metricSnapshotter interface {
	CurrentSnapshot(model.TargetMetricKey) metrics.Snapshot
}

// planCandidates creates one immutable execution order after matcher filtering.
// Planning errors deliberately fail open to the already-safe priority order.
func (p *Proxy) planCandidates(req *InboundRequest, candidates []candidate) []candidate {
	if len(candidates) == 0 || !activeStrategy(candidates[0].strategy) {
		return candidates
	}
	inputs := make([]routing.CandidatePlanInput, len(candidates))
	scoreInputs := make([]scoring.TargetInput, len(candidates))
	snapshotter, hasMetrics := p.metricSink.(metricSnapshotter)
	prices, hasPrices := p.store.(priceResolver)
	endpointKind, knownEndpoint := endpointKind(req.Endpoint)
	var priceErr error
	for i, c := range candidates {
		target := model.ModelRuleTarget{ID: c.targetID, ProviderID: c.provider.ID, ModelName: c.modelName, Tier: c.tier}
		key := model.TargetMetricKey{TargetID: c.targetID, ProviderID: c.provider.ID, ModelName: c.modelName, Endpoint: req.Endpoint}
		var snapshot metrics.Snapshot
		if hasMetrics {
			snapshot = snapshotter.CurrentSnapshot(key)
		}
		cost := model.DefaultEffectiveCost()
		if hasPrices && knownEndpoint {
			price, err := prices.ResolvePrice(c.provider.ID, c.modelName, endpointKind)
			if err != nil {
				priceErr = err
			} else {
				cost = service.EstimateEffectiveCost(price, planningInputTokens, planningOutputTokens, 0, 0, 1, c.maxRetries)
			}
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
	if priceErr != nil {
		return candidates
	}
	for i, score := range scoring.ScoreTargets(scoreInputs, scoring.ScoreContext{Stream: req.Stream}) {
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
	return out
}

func activeStrategy(strategy routing.Strategy) bool {
	return strategy == routing.ScoreWithinTier || strategy == routing.CostFirst
}

func endpointKind(path string) (string, bool) {
	switch path {
	case "/v1/embeddings":
		return "embedding", true
	case "/v1/chat/completions", "/v1/completions", "/v1/responses":
		return "chat", true
	default:
		return "", false
	}
}

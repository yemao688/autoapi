// matcher.go implements the model-rule lookup used by the proxy to select a
// target provider/model. A rule matches by exact model name: the rule's
// Name equals the request's Model field. Disabled rules and rules with no
// enabled targets fall through to the default provider (if configured).
package proxy

import (
	"fmt"

	"autoapi/internal/model"
)

// InboundRequest carries the request context used for model-rule lookup.
// Header / EstimatedTokens / Task / TimeHour are preserved on the struct
// for forward-compatibility (e.g. later routing hints) but are no longer
// consulted by the matcher.
type InboundRequest struct {
	Model           string
	Header          map[string]string
	EstimatedTokens int
	Task            string
	TimeHour        int
}

// candidate is one possible provider/model the proxy can forward a request to.
type candidate struct {
	provider   *model.Provider
	modelName  string
	ruleID     string
	ruleLabel  string
	targetID   string
	maxRetries int
}

// selectCandidates picks the enabled model rule whose Name equals req.Model
// (exact match) and collects all of its forwarding targets in slice order,
// filtering out targets that are disabled or whose provider's circuit
// breaker is open. If no rule matches (or all targets are open), the call
// falls back to the default provider/model when one is configured.
//
// `rules` does not need to be pre-sorted; lookup is by exact name equality
// so the input order is irrelevant. The getProvider closure resolves
// provider IDs to full provider records. Disabled targets are skipped
// without disturbing the relative tier ordering of the survivors.
func selectCandidates(req *InboundRequest, rules []model.ModelRule, defaultProviderID, defaultModel string, breakers map[string]*CircuitBreaker, getProvider func(string) (*model.Provider, error)) ([]candidate, error) {
	rule, matched := findModelRule(req, rules)
	if !matched {
		if defaultProviderID == "" {
			return nil, fmt.Errorf("unknown model: %s", req.Model)
		}
		p, err := getProvider(defaultProviderID)
		if err != nil {
			return nil, fmt.Errorf("default provider not found")
		}
		if isOpen(defaultProviderID, breakers) {
			return nil, fmt.Errorf("no available provider: default provider circuit is open")
		}
		outName := req.Model
		if defaultModel != "" {
			outName = defaultModel
		}
		return []candidate{{provider: p, modelName: outName, ruleID: "", ruleLabel: ""}}, nil
	}

	var out []candidate
	for _, t := range rule.Targets {
		// Disabled targets are skipped during candidate selection. The slice
		// order (tier) is the source of truth for failover ordering, so this
		// `continue` preserves the relative order of the remaining targets.
		if !t.Enabled {
			continue
		}
		if isOpen(t.ProviderID, breakers) {
			continue
		}
		p, err := getProvider(t.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("matched provider not found")
		}
		out = append(out, candidate{
			provider:   p,
			modelName:  modelNameForTarget(t.ModelName, req.Model),
			ruleID:     rule.ID,
			ruleLabel:  rule.Name,
			targetID:   t.ID,
			maxRetries: t.MaxRetries,
		})
	}

	if len(out) == 0 && defaultProviderID != "" {
		p, err := getProvider(defaultProviderID)
		if err != nil {
			return nil, fmt.Errorf("no available provider")
		}
		if isOpen(defaultProviderID, breakers) {
			return nil, fmt.Errorf("no available provider: all targets and default are open")
		}
		outName := req.Model
		if defaultModel != "" {
			outName = defaultModel
		}
		out = append(out, candidate{provider: p, modelName: outName, ruleID: "", ruleLabel: ""})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no available provider")
	}
	return out, nil
}

func isOpen(providerID string, breakers map[string]*CircuitBreaker) bool {
	if cb, ok := breakers[providerID]; ok {
		return !cb.WouldAllow()
	}
	return false
}

// findModelRule returns the first enabled rule whose Name equals req.Model.
// Order in `rules` is irrelevant because Name is unique by design (the
// store enforces uniqueness on Create/Update).
func findModelRule(req *InboundRequest, rules []model.ModelRule) (*model.ModelRule, bool) {
	for i := range rules {
		if !rules[i].Enabled {
			continue
		}
		if rules[i].Name == req.Model {
			return &rules[i], true
		}
	}
	return nil, false
}

func modelNameForTarget(targetModel, requestModel string) string {
	if targetModel != "" {
		return targetModel
	}
	return requestModel
}

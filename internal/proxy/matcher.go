// matcher.go implements the model-rule lookup used by the proxy to select a
// target provider/model. A rule matches by exact model name: the rule's
// Name equals the request's Model field. Disabled rules are skipped. If no
// enabled rule matches, the matcher returns errNoMatch so the handler can
// respond with 503 — the "default fallback" feature has been removed: the
// proxy no longer forwards unmatched requests to a synthetic
// provider/model.
package proxy

import (
	"errors"
	"fmt"
	"time"

	"autoapi/internal/model"
)

// errNoMatch is returned by selectCandidates when the inbound request
// model does not correspond to any enabled model rule. The proxy handler
// converts this into a 503 with error type "no_matching_rule".
var errNoMatch = errors.New("no matching model rule")

// defaultFirstByteTimeout is the budget for receiving the first response
// byte from an upstream provider when no per-target override is set. It
// covers both headers arrival and first byte read. LLMs can legitimately
// take 30-60s post-header on large prompts, so 60s is conservative.
const defaultFirstByteTimeout = 60 * time.Second

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
	timeout    time.Duration // 0 = use default first-byte timeout; otherwise per-target budget
}

// selectCandidates picks the enabled model rule whose Name equals req.Model
// (exact match) and collects all of its forwarding targets in slice order,
// filtering out targets that are disabled or whose provider's circuit
// breaker is open. If no rule matches the request, selectCandidates returns
// errNoMatch — the caller must respond with 503; the proxy no longer falls
// back to a synthetic default provider/model.
//
// `rules` does not need to be pre-sorted; lookup is by exact name equality
// so the input order is irrelevant. The getProvider closure resolves
// provider IDs to full provider records. Disabled targets are skipped
// without disturbing the relative tier ordering of the survivors.
func selectCandidates(req *InboundRequest, rules []model.ModelRule, breakers map[string]*CircuitBreaker, getProvider func(string) (*model.Provider, error)) ([]candidate, error) {
	rule, matched := findModelRule(req, rules)
	if !matched {
		// Include the model name in the error so the 503 body is
		// actionable: clients (and operators reading the request log)
		// can see which model was unknown without having to cross-
		// reference the request log. We do this by wrapping errNoMatch
		// with fmt.Errorf("%w: %s", errNoMatch, req.Model) — the
		// handler uses errors.Is to detect errNoMatch and pick the
		// 503 error type, while the wrapped message still bubbles up
		// to the JSON body verbatim.
		return nil, fmt.Errorf("%w: %s", errNoMatch, req.Model)
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
			timeout:    targetTimeout(t.TimeoutSeconds),
		})
	}

	if len(out) == 0 {
		// The rule matched, but every target is disabled or its circuit is
		// open. There is no longer a default fallback to bridge this gap, so
		// the caller must surface the failure rather than silently forward
		// to a wrong target.
		return nil, fmt.Errorf("no available provider: all targets of model %q are disabled or have open circuits", req.Model)
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

// targetTimeout converts a per-target timeout_seconds setting (0 = use default)
// into a time.Duration for the candidate. A zero return value means
// "use the default first-byte timeout"; the actual default is resolved
// at the call site in forwardStream so this constant is only referenced
// once.
func targetTimeout(timeoutSeconds int) time.Duration {
	if timeoutSeconds > 0 {
		return time.Duration(timeoutSeconds) * time.Second
	}
	return 0 // 0 signals "use defaultFirstByteTimeout"; resolved in forwardStream
}

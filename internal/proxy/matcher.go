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
	"autoapi/internal/routing"
)

// errNoMatch is returned by selectCandidates when the inbound request
// model does not correspond to any enabled model rule. The proxy handler
// converts this into a 503 with error type "no_matching_rule".
var errNoMatch = errors.New("no matching model rule")

// defaultFirstByteTimeout is the budget for receiving the first response
// byte from an upstream provider when the rule's first_byte_timeout_seconds
// is 0. It covers both headers arrival and first byte read. LLMs can
// legitimately take 30-60s post-header on large prompts, so 60s is
// conservative. The budget is only counted BEFORE the first byte is
// received — once a response is established, the budget stops.
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
	Endpoint        string
	Stream          bool
	Protocol        Protocol
}

// candidate is one possible provider/model the proxy can forward a request to.
//
// firstByteBudget is the rule-level maximum first-byte timeout: the
// total time the proxy is willing to wait for the first response byte
// from ANY upstream candidate (across all candidates and all per-target
// retries) before declaring "first-byte budget exceeded" and falling
// through. It is identical for every candidate in the same rule (the
// budget is set on the rule, not the target).
type candidate struct {
	provider                   *model.Provider
	modelName                  string
	protocol                   Protocol
	upstreamPath               string
	ruleID                     string
	ruleLabel                  string
	targetID                   string
	maxRetries                 int
	firstByteBudget            time.Duration
	targetFirstBodyByteTimeout time.Duration
	tier                       int
	strategy                   routing.Strategy
	requestPrice               float64
	requestPriceAvailable      bool
	convertTo                  Protocol
}

func selectConversionCandidates(req *InboundRequest, rules []model.ModelRule, breakers map[string]*CircuitBreaker, getProvider func(string) (*model.Provider, error)) ([]candidate, error) {
	rule, matched := findModelRule(req, rules)
	if !matched {
		return nil, fmt.Errorf("%w: %s", errNoMatch, req.Model)
	}

	var to Protocol
	var upstreamPath string
	switch req.Protocol {
	case ProtocolAnthropicMessages:
		to, upstreamPath = ProtocolOpenAIResponses, "/v1/responses"
	case ProtocolOpenAIResponses:
		to, upstreamPath = ProtocolAnthropicMessages, "/v1/messages"
	default:
		return nil, fmt.Errorf("no conversion fallback for protocol %q", req.Protocol)
	}

	firstByteBudget := firstByteTimeout(rule.FirstByteTimeoutSeconds)
	var out []candidate
	for _, t := range rule.Targets {
		if !t.Enabled {
			continue
		}
		p, err := getProvider(t.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("matched provider not found")
		}
		if !p.Enabled || isOpen(t.ProviderID, breakers) {
			continue
		}
		if to == ProtocolOpenAIResponses && !p.ResponsesEnabled {
			continue
		}
		if to == ProtocolAnthropicMessages && !p.MessagesEnabled {
			continue
		}
		out = append(out, candidate{
			provider:                   p,
			modelName:                  modelNameForTarget(t.ModelName, req.Model),
			protocol:                   req.Protocol,
			upstreamPath:               upstreamPath,
			ruleID:                     rule.ID,
			ruleLabel:                  rule.Name,
			targetID:                   t.ID,
			maxRetries:                 t.MaxRetries,
			firstByteBudget:            firstByteBudget,
			targetFirstBodyByteTimeout: targetFirstBodyByteTimeout(t.FirstTokenTimeoutSeconds),
			tier:                       t.Tier,
			strategy:                   routing.Strategy(rule.Strategy),
			convertTo:                  to,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no available conversion provider for model %q", req.Model)
	}
	return out, nil
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
	// Resolve the rule's first-byte budget ONCE; every candidate in
	// the same rule shares the same budget.
	firstByteBudget := firstByteTimeout(rule.FirstByteTimeoutSeconds)
	for _, t := range rule.Targets {
		// Disabled targets are skipped during candidate selection. The slice
		// order (tier) is the source of truth for failover ordering, so this
		// `continue` preserves the relative order of the remaining targets.
		if !t.Enabled {
			continue
		}
		p, err := getProvider(t.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("matched provider not found")
		}
		// Disabled providers cause every target that references them to be
		// skipped, regardless of circuit-breaker state.
		if !p.Enabled {
			continue
		}
		if req.Task == "responses" && !p.ResponsesEnabled {
			continue
		}
		if req.Task == "messages" && !p.MessagesEnabled {
			continue
		}
		if isOpen(t.ProviderID, breakers) {
			continue
		}
		out = append(out, candidate{
			provider:                   p,
			modelName:                  modelNameForTarget(t.ModelName, req.Model),
			protocol:                   req.Protocol,
			upstreamPath:               req.Endpoint,
			ruleID:                     rule.ID,
			ruleLabel:                  rule.Name,
			targetID:                   t.ID,
			maxRetries:                 t.MaxRetries,
			firstByteBudget:            firstByteBudget,
			targetFirstBodyByteTimeout: targetFirstBodyByteTimeout(t.FirstTokenTimeoutSeconds),
			tier:                       t.Tier,
			strategy:                   routing.Strategy(rule.Strategy),
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

func targetFirstBodyByteTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// effectiveAttemptFirstBodyByteDeadline returns the earliest positive deadline
// for an attempt. A zero target timeout means the rule deadline alone applies.
func effectiveAttemptFirstBodyByteDeadline(now, ruleDeadline time.Time, targetTimeout time.Duration) time.Time {
	deadline := ruleDeadline
	if targetTimeout > 0 {
		targetDeadline := now.Add(targetTimeout)
		if deadline.IsZero() || targetDeadline.Before(deadline) {
			deadline = targetDeadline
		}
	}
	return deadline
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

// firstByteTimeout converts the rule-level first_byte_timeout_seconds
// setting (0 = use default) into a time.Duration. The returned duration
// is the total budget the proxy will wait for the first byte across
// ALL candidates and retries for the rule.
func firstByteTimeout(timeoutSeconds int) time.Duration {
	if timeoutSeconds > 0 {
		return time.Duration(timeoutSeconds) * time.Second
	}
	return defaultFirstByteTimeout
}

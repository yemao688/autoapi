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
	"log/slog"
	"strings"
	"time"

	"autoapi/internal/model"
	"autoapi/internal/routing"
)

// errNoMatch is returned by selectCandidates when the inbound request
// model does not correspond to any enabled model rule. The proxy handler
// converts this into a 503 with error type "no_matching_rule".
var errNoMatch = errors.New("no matching model rule")

// errUnsupportedFeature is returned when the request requires a feature that
// no candidate provider/model can satisfy (or that is unsafe to convert).
// Handlers convert this into a 422 with error type "unsupported_feature".
var errUnsupportedFeature = errors.New("unsupported feature")

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
	Requirements    *model.RequestRequirements
	Enforcement     string
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

func selectConversionCandidates(req *InboundRequest, rules []model.ModelRule, breakers map[string]*CircuitBreaker, getProvider func(string) (*model.Provider, error), snapshots ...capabilitySnapshot) ([]candidate, error) {
	capabilities := capabilitySnapshot{}
	if len(snapshots) > 0 {
		capabilities = snapshots[0]
	}
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

	// First determine whether there is at least one "basic" conversion
	// candidate: enabled target, enabled provider, closed breaker, and the
	// target protocol is supported. Without such a candidate the conversion
	// path is genuinely unavailable and must surface as a regular 503, even
	// if the request requires features that cannot be preserved.
	basicAvailable := false
	for _, t := range rule.Targets {
		if !t.Enabled {
			continue
		}
		p, err := getProvider(t.ProviderID)
		if err != nil || p == nil || !p.Enabled {
			continue
		}
		if isOpen(t.ProviderID, breakers) {
			continue
		}
		modelName := modelNameForTarget(t.ModelName, req.Model)
		if capabilities.providers == nil {
			capabilities = newCapabilitySnapshot(nil, map[string]*model.Provider{p.ID: p})
		}
		if _, exists := capabilities.providers[p.ID]; !exists {
			capabilities.providers[p.ID] = newCapabilitySnapshot(nil, map[string]*model.Provider{p.ID: p}).providers[p.ID]
		}
		if capabilities.supportsModel(p.ID, modelName, to) {
			basicAvailable = true
			break
		}
	}
	if !basicAvailable {
		return nil, fmt.Errorf("no available conversion provider for model %q", req.Model)
	}

	// Messages<->Responses conversion is restricted to text/system/tools/tool
	// results and the supported streaming edge. Do this only after confirming a
	// basic conversion path exists: otherwise the request is ordinary routing
	// unavailability (503), not a preservation failure (422).
	if req.Requirements != nil {
		for _, f := range req.Requirements.Features {
			switch f {
			case model.FeatureTools, model.FeatureStreaming:
				continue
			}
			return nil, fmt.Errorf("%w: conversion cannot preserve feature %q", errUnsupportedFeature, f)
		}
		if req.Requirements.NativeOnly || req.Requirements.UnknownSemantic {
			return nil, fmt.Errorf("%w: request contains native-only semantics", errUnsupportedFeature)
		}
	}

	// Static stream-conversion edge is a conversion limitation, not a feature
	// capability mismatch. Treat it as unsupported_feature when a basic path
	// exists.
	if req.Stream && !supportsStreamConversion(req.Protocol, to) {
		return nil, fmt.Errorf("%w: streaming not supported for conversion %s->%s", errUnsupportedFeature, req.Protocol, to)
	}

	firstByteBudget := firstByteTimeout(rule.FirstByteTimeoutSeconds)
	var out []candidate
	var featureFiltered bool
	for _, t := range rule.Targets {
		if !t.Enabled {
			continue
		}
		p, err := getProvider(t.ProviderID)
		if err != nil || p == nil || !p.Enabled {
			continue
		}
		if isOpen(t.ProviderID, breakers) {
			continue
		}
		modelName := modelNameForTarget(t.ModelName, req.Model)
		if capabilities.providers == nil {
			capabilities = newCapabilitySnapshot(nil, map[string]*model.Provider{p.ID: p})
		}
		if _, exists := capabilities.providers[p.ID]; !exists {
			capabilities.providers[p.ID] = newCapabilitySnapshot(nil, map[string]*model.Provider{p.ID: p}).providers[p.ID]
		}
		if !capabilities.supportsModel(p.ID, modelName, to) {
			continue
		}
		if req.Requirements != nil {
			skip := false
			for _, f := range req.Requirements.Features {
				// Preservation is enforced in both observe and enforce mode:
				// conversion must only use explicitly supported capabilities.
				supported, _ := capabilities.featureEnabled(p.ID, modelName, to, string(f), true)
				if !supported {
					skip = true
					featureFiltered = true
					break
				}
			}
			if skip {
				continue
			}
		}
		out = append(out, candidate{
			provider:                   p,
			modelName:                  modelName,
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
		if featureFiltered {
			return nil, fmt.Errorf("%w: no conversion provider satisfies required features %v for model %q", errUnsupportedFeature, req.Requirements.Features, req.Model)
		}
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
func selectCandidates(req *InboundRequest, rules []model.ModelRule, breakers map[string]*CircuitBreaker, getProvider func(string) (*model.Provider, error), snapshots ...capabilitySnapshot) ([]candidate, error) {
	capabilities := capabilitySnapshot{}
	if len(snapshots) > 0 {
		capabilities = snapshots[0]
	}
	protocol := req.Protocol
	if protocol == ProtocolUnknown {
		switch req.Task {
		case "responses":
			protocol = ProtocolOpenAIResponses
		case "messages":
			protocol = ProtocolAnthropicMessages
		case "gemini":
			protocol = ProtocolGemini
		default:
			endpoint := strings.TrimSuffix(req.Endpoint, "/")
			if req.Task != "" || (endpoint != "" && endpoint != "/v1/chat/completions") {
				return nil, fmt.Errorf("unknown request protocol")
			}
			protocol = ProtocolOpenAIChat
		}
	}
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
	var featureFiltered bool
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
		modelName := modelNameForTarget(t.ModelName, req.Model)
		if capabilities.providers == nil {
			capabilities = newCapabilitySnapshot(nil, map[string]*model.Provider{p.ID: p})
		}
		if _, exists := capabilities.providers[p.ID]; !exists {
			capabilities.providers[p.ID] = newCapabilitySnapshot(nil, map[string]*model.Provider{p.ID: p}).providers[p.ID]
		}
		if !capabilities.supportsModel(p.ID, modelName, protocol) {
			continue
		}
		// Circuit breaker must be checked before feature filtering: an open
		// target is not "available" and must not turn a 503 into a 422.
		if isOpen(t.ProviderID, breakers) {
			continue
		}
		// A target that reaches this point is enabled, resolvable, supports
		// the target protocol and has a closed breaker. If it is rejected
		// only because of a requirements feature, we remember that so the
		// caller can return 422 instead of a generic 503.
		if req.Requirements != nil {
			skip := false
			for _, f := range req.Requirements.Features {
				enforce := req.Enforcement == model.FeatureCapabilityEnforcementEnforce
				supported, known := capabilities.featureEnabled(p.ID, modelName, protocol, string(f), enforce)
				// Observe-mode diagnostics: log when an unknown feature is
				// allowed through so operators can audit implicit capability.
				if !enforce && !known && supported {
					slog.Debug("proxy: native feature implicitly allowed",
						"provider", p.ID, "model", modelName, "protocol", protocol, "feature", f, "enforcement", req.Enforcement)
				}
				if !supported {
					skip = true
					featureFiltered = true
					break
				}
			}
			if skip {
				continue
			}
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
		if featureFiltered {
			return nil, fmt.Errorf("%w: no native provider satisfies required features %v for model %q", errUnsupportedFeature, req.Requirements.Features, req.Model)
		}
		return nil, fmt.Errorf("no available provider for model %q", req.Model)
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

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
	"sort"
	"strings"
	"time"

	"autoapi/internal/model"
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
	AllowedRuleIDs  []string
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
	requestPrice               float64
	requestPriceAvailable      bool
	convertTo                  Protocol
}

type conversionEdge struct {
	From           Protocol
	To             Protocol
	UpstreamPath   string
	Priority       int
	SupportsStream bool
	Preserves      func(*InboundRequest) bool
}

var conversionEdges = []conversionEdge{
	{From: ProtocolAnthropicMessages, To: ProtocolOpenAIResponses, UpstreamPath: "/v1/responses", Priority: 10, SupportsStream: true, Preserves: preservesTextTools},
	{From: ProtocolOpenAIResponses, To: ProtocolAnthropicMessages, UpstreamPath: "/v1/messages", Priority: 10, SupportsStream: true, Preserves: preservesTextTools},
	{From: ProtocolOpenAIChat, To: ProtocolOpenAIResponses, UpstreamPath: "/v1/responses", Priority: 10, SupportsStream: true, Preserves: preservesTextTools},
	{From: ProtocolOpenAIResponses, To: ProtocolOpenAIChat, UpstreamPath: "/v1/chat/completions", Priority: 20, SupportsStream: true, Preserves: preservesTextTools},
}

func preservesTextTools(req *InboundRequest) bool {
	if req.Requirements == nil {
		return true
	}
	if req.Requirements.NativeOnly || req.Requirements.UnknownSemantic {
		return false
	}
	for _, feature := range req.Requirements.Features {
		if feature != model.FeatureTools && feature != model.FeatureStreaming {
			return false
		}
	}
	return true
}

func edgeFor(from, to Protocol) (conversionEdge, bool) {
	for _, edge := range conversionEdges {
		if edge.From == from && edge.To == to {
			return edge, true
		}
	}
	return conversionEdge{}, false
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
	protocol, err := normalizedInboundProtocol(req)
	if err != nil {
		return nil, err
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

	normalizedReq := *req
	normalizedReq.Protocol = protocol
	var out []candidate
	var rejectedWireRoute bool
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
			continue
		}
		if p == nil {
			continue
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
		// Circuit breaker must be checked before feature filtering: an open
		// target is not "available" and must not turn a 503 into a 422.
		if isOpen(t.ProviderID, breakers) {
			continue
		}
		if capabilities.supportsModel(p.ID, modelName, protocol) {
			if nativeFeaturesSupported(&normalizedReq, capabilities, p.ID, modelName, protocol) {
				out = append(out, makeCandidate(p, modelName, protocol, req.Endpoint, rule, t, firstByteBudget))
				continue
			}
			rejectedWireRoute = true
		}
		for _, edge := range conversionEdgesFor(protocol) {
			if !capabilities.supportsModel(p.ID, modelName, edge.To) {
				continue
			}
			if !edgeSupportsRequest(&normalizedReq, edge, capabilities, p.ID, modelName) {
				rejectedWireRoute = true
				continue
			}
			candidate := makeCandidate(p, modelName, protocol, edge.UpstreamPath, rule, t, firstByteBudget)
			candidate.convertTo = edge.To
			out = append(out, candidate)
			break
		}
	}

	if len(out) == 0 {
		if rejectedWireRoute {
			return nil, fmt.Errorf("%w: no provider satisfies request requirements %v for model %q", errUnsupportedFeature, requestFeatureNames(req), req.Model)
		}
		return nil, fmt.Errorf("no available provider for model %q", req.Model)
	}
	return out, nil
}

func requestFeatureNames(req *InboundRequest) []model.Feature {
	if req.Requirements == nil {
		return nil
	}
	return req.Requirements.Features
}

func normalizedInboundProtocol(req *InboundRequest) (Protocol, error) {
	if req.Protocol != ProtocolUnknown {
		return req.Protocol, nil
	}
	switch req.Task {
	case "responses":
		return ProtocolOpenAIResponses, nil
	case "messages":
		return ProtocolAnthropicMessages, nil
	case "gemini":
		return ProtocolGemini, nil
	default:
		endpoint := strings.TrimSuffix(req.Endpoint, "/")
		if req.Task != "" || (endpoint != "" && endpoint != "/v1/chat/completions") {
			return ProtocolUnknown, fmt.Errorf("unknown request protocol")
		}
		return ProtocolOpenAIChat, nil
	}
}

func conversionEdgesFor(from Protocol) []conversionEdge {
	edges := make([]conversionEdge, 0, len(conversionEdges))
	for _, edge := range conversionEdges {
		if edge.From == from {
			edges = append(edges, edge)
		}
	}
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].Priority < edges[j].Priority })
	return edges
}

func nativeFeaturesSupported(req *InboundRequest, capabilities capabilitySnapshot, providerID, modelName string, protocol Protocol) bool {
	if req.Requirements == nil {
		return true
	}
	enforce := req.Enforcement == model.FeatureCapabilityEnforcementEnforce
	for _, feature := range req.Requirements.Features {
		supported, known := capabilities.featureEnabled(providerID, modelName, protocol, string(feature), enforce)
		if !enforce && !known && supported {
			slog.Debug("proxy: native feature implicitly allowed", "provider", providerID, "model", modelName, "protocol", protocol, "feature", feature, "enforcement", req.Enforcement)
		}
		if !supported {
			return false
		}
	}
	return true
}

func edgeSupportsRequest(req *InboundRequest, edge conversionEdge, capabilities capabilitySnapshot, providerID, modelName string) bool {
	if (req.Stream && !edge.SupportsStream) || !edge.Preserves(req) {
		return false
	}
	if req.Requirements == nil {
		return true
	}
	enforce := req.Enforcement == model.FeatureCapabilityEnforcementEnforce
	for _, feature := range req.Requirements.Features {
		if feature == model.FeatureStreaming && !edge.SupportsStream {
			return false
		}
		supported, _ := capabilities.featureEnabled(providerID, modelName, edge.To, string(feature), enforce)
		if !supported {
			return false
		}
	}
	return true
}

func makeCandidate(p *model.Provider, modelName string, protocol Protocol, upstreamPath string, rule *model.ModelRule, t model.ModelRuleTarget, firstByteBudget time.Duration) candidate {
	return candidate{provider: p, modelName: modelName, protocol: protocol, upstreamPath: upstreamPath, ruleID: rule.ID, ruleLabel: rule.Name, targetID: t.ID, maxRetries: t.MaxRetries, firstByteBudget: firstByteBudget, targetFirstBodyByteTimeout: targetFirstBodyByteTimeout(t.FirstTokenTimeoutSeconds)}
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

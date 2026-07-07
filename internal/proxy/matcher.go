// matcher.go implements the route rule engine used by the proxy to select a
// target provider/model. Rules are evaluated by priority ascending; only enabled
// rules are considered. All conditions in a rule must match (AND semantics).
// A rule whose only target is "skip" causes a fall-through to the next rule.
package proxy

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"autoapi/internal/model"
)

// InboundRequest carries the request context used for route matching.
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
	routeID    string
	routeLabel string
}

// selectCandidates picks the highest-priority enabled route, collects all of
// its forward targets ordered by tier (lower tier = higher priority), filters
// out providers whose circuit breaker is open, and falls back to the default
// provider when every routed target is open. The getProvider closure resolves
// provider IDs to full provider records.
func selectCandidates(req *InboundRequest, rules []model.Route, defaultProviderID string, breakers map[string]*CircuitBreaker, getProvider func(string) (*model.Provider, error)) ([]candidate, error) {
	route, matched := selectRoute(req, rules)
	if !matched {
		if defaultProviderID == "" {
			return nil, fmt.Errorf("no route matched and no default provider configured")
		}
		p, err := getProvider(defaultProviderID)
		if err != nil {
			return nil, fmt.Errorf("default provider not found")
		}
		if isOpen(defaultProviderID, breakers) {
			return nil, fmt.Errorf("no available provider: default provider circuit is open")
		}
		return []candidate{{provider: p, modelName: "", routeID: "", routeLabel: ""}}, nil
	}

	var targets []model.RouteTarget
	for _, t := range route.Targets {
		if t.Action == model.RouteActionForward {
			targets = append(targets, t)
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Tier < targets[j].Tier })

	var out []candidate
	for _, t := range targets {
		if isOpen(t.ProviderID, breakers) {
			continue
		}
		p, err := getProvider(t.ProviderID)
		if err != nil {
			return nil, fmt.Errorf("matched provider not found")
		}
		out = append(out, candidate{
			provider:   p,
			modelName:  t.ModelName,
			routeID:    route.ID,
			routeLabel: route.Name,
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
		out = append(out, candidate{provider: p, modelName: "", routeID: "", routeLabel: ""})
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no available provider")
	}
	return out, nil
}

func isOpen(providerID string, breakers map[string]*CircuitBreaker) bool {
	if cb, ok := breakers[providerID]; ok {
		return !cb.Allow()
	}
	return false
}

// selectRoute picks the highest-priority enabled route whose conditions all
// match and which contains at least one "forward" target. If the matched rule
// only has "skip" targets, it falls through to the next rule. The returned
// route is the caller's to inspect ( Targets[0] is typically the forward
// target). The bool reports whether a route was selected.
func selectRoute(req *InboundRequest, rules []model.Route) (*model.Route, bool) {
	sorted := make([]model.Route, len(rules))
	copy(sorted, rules)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })

	for _, r := range sorted {
		if !r.Enabled {
			continue
		}
		if !matchAll(req, r.Conditions) {
			continue
		}
		for _, t := range r.Targets {
			if t.Action == model.RouteActionForward {
				return &r, true
			}
		}
		// Skip-only rule matched: continue to lower-priority rules.
	}
	return nil, false
}

func matchAll(req *InboundRequest, conds []model.RouteCondition) bool {
	for _, c := range conds {
		if !matchCondition(req, c) {
			return false
		}
	}
	return true
}

func matchCondition(req *InboundRequest, c model.RouteCondition) bool {
	actual := extractField(req, c.Field)
	if actual == nilField {
		return false
	}

	switch c.Operator {
	case model.OpMatches:
		return matchMatches(actual, c.Value)
	case model.OpEquals:
		return strings.EqualFold(actual, c.Value)
	case model.OpLT:
		return cmpNumeric(actual, c.Value) < 0
	case model.OpGT:
		return cmpNumeric(actual, c.Value) > 0
	case model.OpBetween:
		return betweenNumeric(actual, c.Value, c.Field == "time.hour")
	case model.OpIn:
		return inSet(actual, c.Value)
	default:
		return false
	}
}

const nilField = ""

func extractField(req *InboundRequest, field string) string {
	switch field {
	case "model":
		return req.Model
	case "header.x-priority":
		if v, ok := req.Header["X-Priority"]; ok {
			return v
		}
		return ""
	case "estimated_tokens":
		return strconv.Itoa(req.EstimatedTokens)
	case "task":
		return req.Task
	case "time.hour":
		return strconv.Itoa(req.TimeHour)
	default:
		return nilField
	}
}

// matchMatches supports two conventions:
//   - If the pattern contains a comma, it is treated as a list of sub-patterns;
//     the value matches if any sub-pattern matches.
//   - If a sub-pattern contains '*', it is treated as a glob (prefix/suffix/wildcard
//     only). Otherwise it is case-insensitive equality.
func matchMatches(value, pattern string) bool {
	if value == "" || pattern == "" {
		return false
	}
	parts := strings.Split(pattern, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(p, "*") {
			if matchGlob(value, p) {
				return true
			}
		} else if strings.EqualFold(value, p) {
			return true
		}
	}
	return false
}

func matchGlob(value, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		mid := pattern[1 : len(pattern)-1]
		return strings.Contains(value, mid)
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(value, pattern[:len(pattern)-1])
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(value, pattern[1:])
	}
	if ok, _ := path.Match(pattern, value); ok {
		return true
	}
	return false
}

func cmpNumeric(a, b string) int {
	fa, ok1 := parseFloat(a)
	fb, ok2 := parseFloat(b)
	if !ok1 || !ok2 {
		return 0
	}
	if fa < fb {
		return -1
	}
	if fa > fb {
		return 1
	}
	return 0
}

// betweenNumeric parses lo,hi and checks inclusivity. When wrap is true (used
// for time.hour), a reversed range such as 23,7 means [23,24] or [0,7].
func betweenNumeric(value, bounds string, wrap bool) bool {
	parts := strings.Split(bounds, ",")
	if len(parts) != 2 {
		return false
	}
	v, ok1 := parseFloat(value)
	lo, ok2 := parseFloat(parts[0])
	hi, ok3 := parseFloat(parts[1])
	if !ok1 || !ok2 || !ok3 {
		return false
	}
	if lo <= hi {
		return v >= lo && v <= hi
	}
	if wrap {
		return v >= lo || v <= hi
	}
	return false
}

func inSet(value, set string) bool {
	parts := strings.Split(set, ",")
	for _, p := range parts {
		if strings.EqualFold(strings.TrimSpace(p), value) {
			return true
		}
	}
	return false
}

func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

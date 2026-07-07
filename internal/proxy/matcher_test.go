package proxy

import (
	"testing"

	"autoapi/internal/model"
)

func TestSelectRoute_PriorityAndEnabled(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r2", Priority: 2, Enabled: true,
			Conditions: []model.RouteCondition{{Field: "model", Operator: model.OpMatches, Value: "gpt-*"}},
			Targets:    []model.RouteTarget{{ProviderID: "p2", ModelName: "gpt-4o", Action: model.RouteActionForward}},
		},
		{
			ID: "r1", Priority: 1, Enabled: true,
			Conditions: []model.RouteCondition{{Field: "model", Operator: model.OpEquals, Value: "gpt-4o"}},
			Targets:    []model.RouteTarget{{ProviderID: "p1", ModelName: "gpt-4o", Action: model.RouteActionForward}},
		},
		{
			ID: "r3", Priority: 3, Enabled: false,
			Conditions: []model.RouteCondition{{Field: "model", Operator: model.OpMatches, Value: "*"}},
			Targets:    []model.RouteTarget{{ProviderID: "p3", ModelName: "gpt-4o", Action: model.RouteActionForward}},
		},
	}

	req := &InboundRequest{Model: "gpt-4o"}
	route, ok := selectRoute(req, rules)
	if !ok {
		t.Fatal("expected a match")
	}
	if route.ID != "r1" {
		t.Fatalf("expected r1 to win by priority, got %s", route.ID)
	}
}

func TestSelectRoute_ANDConditions(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Priority: 1, Enabled: true,
			Conditions: []model.RouteCondition{
				{Field: "model", Operator: model.OpMatches, Value: "gpt-4o*"},
				{Field: "estimated_tokens", Operator: model.OpGT, Value: "1000"},
			},
			Targets: []model.RouteTarget{{ProviderID: "p1", ModelName: "gpt-4o", Action: model.RouteActionForward}},
		},
	}

	if _, ok := selectRoute(&InboundRequest{Model: "gpt-4o", EstimatedTokens: 2000}, rules); !ok {
		t.Fatal("expected match when both conditions true")
	}
	if _, ok := selectRoute(&InboundRequest{Model: "gpt-4o", EstimatedTokens: 500}, rules); ok {
		t.Fatal("expected no match when second condition false")
	}
}

func TestSelectRoute_SkipFallthrough(t *testing.T) {
	rules := []model.Route{
		{
			ID: "skip", Priority: 1, Enabled: true,
			Conditions: []model.RouteCondition{{Field: "model", Operator: model.OpMatches, Value: "glm-*"}},
			Targets:    []model.RouteTarget{{ProviderID: "p5", Action: model.RouteActionSkip}},
		},
		{
			ID: "fallback", Priority: 2, Enabled: true,
			Conditions: []model.RouteCondition{{Field: "model", Operator: model.OpMatches, Value: "*"}},
			Targets:    []model.RouteTarget{{ProviderID: "p1", ModelName: "gpt-4o", Action: model.RouteActionForward}},
		},
	}

	route, ok := selectRoute(&InboundRequest{Model: "glm-4"}, rules)
	if !ok {
		t.Fatal("expected skip to fall through to fallback")
	}
	if route.ID != "fallback" {
		t.Fatalf("expected fallback, got %s", route.ID)
	}
}

func TestMatchCondition_MatchesCommaList(t *testing.T) {
	req := &InboundRequest{Task: "code"}
	cond := model.RouteCondition{Field: "task", Operator: model.OpMatches, Value: "reasoning,code,analysis"}
	if !matchCondition(req, cond) {
		t.Fatal("expected task=code to match comma list")
	}
}

func TestMatchCondition_MatchesGlob(t *testing.T) {
	cases := []struct {
		value, pattern string
		want           bool
	}{
		{"gpt-4o-mini", "gpt-4o*", true},
		{"gpt-4", "gpt-4o*", false},
		{"moonshot-v1", "moonshot-*", true},
		{"claude-3-opus", "claude-*", true},
		{"anything", "*", true},
		{"hello-world", "*world", true},
	}
	for _, tc := range cases {
		req := &InboundRequest{Model: tc.value}
		cond := model.RouteCondition{Field: "model", Operator: model.OpMatches, Value: tc.pattern}
		got := matchCondition(req, cond)
		if got != tc.want {
			t.Fatalf("model %q matches %q: got %v want %v", tc.value, tc.pattern, got, tc.want)
		}
	}
}

func TestMatchCondition_NumericOperators(t *testing.T) {
	rules := []model.RouteCondition{
		{Field: "estimated_tokens", Operator: model.OpLT, Value: "500"},
		{Field: "estimated_tokens", Operator: model.OpGT, Value: "2000"},
		{Field: "estimated_tokens", Operator: model.OpBetween, Value: "100,300"},
	}
	if !matchCondition(&InboundRequest{EstimatedTokens: 100}, rules[0]) {
		t.Fatal("lt should match")
	}
	if !matchCondition(&InboundRequest{EstimatedTokens: 3000}, rules[1]) {
		t.Fatal("gt should match")
	}
	if !matchCondition(&InboundRequest{EstimatedTokens: 200}, rules[2]) {
		t.Fatal("between should match")
	}
	if matchCondition(&InboundRequest{EstimatedTokens: 500}, rules[0]) {
		t.Fatal("lt boundary should not match")
	}
}

func TestMatchCondition_BetweenWrapTimeHour(t *testing.T) {
	cond := model.RouteCondition{Field: "time.hour", Operator: model.OpBetween, Value: "23,7"}
	if !matchCondition(&InboundRequest{TimeHour: 23}, cond) {
		t.Fatal("hour 23 should match wrap range 23,7")
	}
	if !matchCondition(&InboundRequest{TimeHour: 3}, cond) {
		t.Fatal("hour 3 should match wrap range 23,7")
	}
	if !matchCondition(&InboundRequest{TimeHour: 7}, cond) {
		t.Fatal("hour 7 should match wrap range 23,7")
	}
	if matchCondition(&InboundRequest{TimeHour: 12}, cond) {
		t.Fatal("hour 12 should not match wrap range 23,7")
	}
}

func TestMatchCondition_In(t *testing.T) {
	cond := model.RouteCondition{Field: "header.x-priority", Operator: model.OpIn, Value: "low,normal"}
	req := &InboundRequest{Header: map[string]string{"X-Priority": "low"}}
	if !matchCondition(req, cond) {
		t.Fatal("expected in set to match")
	}
	req.Header["X-Priority"] = "high"
	if matchCondition(req, cond) {
		t.Fatal("expected in set not to match")
	}
}

func TestMatchCondition_UnknownField(t *testing.T) {
	cond := model.RouteCondition{Field: "unknown", Operator: model.OpEquals, Value: "x"}
	if matchCondition(&InboundRequest{}, cond) {
		t.Fatal("unknown field should fail condition")
	}
}

func TestSelectRoute_NoMatch(t *testing.T) {
	rules := []model.Route{
		{
			ID: "r1", Priority: 1, Enabled: true,
			Conditions: []model.RouteCondition{{Field: "model", Operator: model.OpEquals, Value: "gpt-4o"}},
			Targets:    []model.RouteTarget{{ProviderID: "p1", ModelName: "gpt-4o", Action: model.RouteActionForward}},
		},
	}
	if _, ok := selectRoute(&InboundRequest{Model: "claude-3"}, rules); ok {
		t.Fatal("expected no match")
	}
}

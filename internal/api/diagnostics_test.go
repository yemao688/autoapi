package api

import (
	"errors"
	"testing"

	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/proxy"
)

type diagnosticsStore struct {
	StoreService
	rules       []model.ModelRule
	providers   map[string]*model.Provider
	providerErr map[string]error
	models      map[string]*model.Model
}

func (s *diagnosticsStore) ListModelRules() ([]model.ModelRule, error) { return s.rules, nil }
func (s *diagnosticsStore) GetProvider(id string) (*model.Provider, error) {
	return s.providers[id], s.providerErr[id]
}
func (s *diagnosticsStore) GetModel(providerID, modelName string) (*model.Model, error) {
	m, ok := s.models[providerID+":"+modelName]
	if !ok {
		return nil, errors.New("model not found")
	}
	return m, nil
}

type diagnosticsService struct{ BusinessService }
type diagnosticsMetrics struct {
	snapshots map[model.TargetMetricKey]metrics.Snapshot
	routes    map[model.RouteModeKey]metrics.RouteSnapshot
}

type shadowProxy struct {
	ProxyService
	statuses map[string]proxy.BreakerStatus
}

func (p shadowProxy) BreakerStatuses() map[string]proxy.BreakerStatus { return p.statuses }

func (m diagnosticsMetrics) CurrentSnapshot(k model.TargetMetricKey) metrics.Snapshot {
	return m.snapshots[k]
}

func (m diagnosticsMetrics) CurrentRouteSnapshots(targetID string) []metrics.RouteSnapshot {
	out := make([]metrics.RouteSnapshot, 0)
	for k, v := range m.routes {
		if k.TargetID == targetID {
			out = append(out, v)
		}
	}
	return out
}

func diagnosticApp(s *diagnosticsStore, m diagnosticsMetrics) *App {
	return NewApp(Deps{Store: s, Service: diagnosticsService{}, Metrics: m})
}

func shadowApp(s *diagnosticsStore, statuses map[string]proxy.BreakerStatus) *App {
	return NewApp(Deps{Store: s, Proxy: shadowProxy{statuses: statuses}})
}

func TestGetModelRuleShadowComparisonsUsesDetachedBreakerStates(t *testing.T) {
	s := &diagnosticsStore{
		rules: []model.ModelRule{{ID: "r", Name: "rule", Enabled: true, Targets: []model.ModelRuleTarget{
			{ID: "open", ProviderID: "p-open", Enabled: true, Tier: 1},
			{ID: "half", ProviderID: "p-half", Enabled: true, Tier: 1},
			{ID: "closed", ProviderID: "p-closed", Enabled: true, Tier: 1},
		}}},
		providers: map[string]*model.Provider{"p-open": {ID: "p-open", Enabled: true}, "p-half": {ID: "p-half", Enabled: true}, "p-closed": {ID: "p-closed", Enabled: true}},
	}
	out, err := shadowApp(s, map[string]proxy.BreakerStatus{
		"p-open": {State: proxy.StateOpen}, "p-half": {State: proxy.StateHalfOpen}, "p-closed": {State: proxy.StateClosed},
	}).GetModelRuleShadowComparisons()
	if err != nil || len(out) != 1 {
		t.Fatalf("shadow = %#v, err=%v", out, err)
	}
	if len(out[0].Rejected) != 1 || out[0].Rejected[0].TargetID != "open" || out[0].Rejected[0].Reason != "circuit_open" {
		t.Fatalf("open breaker rejection = %#v", out[0].Rejected)
	}
	if out[0].Candidates[0].TargetID != "half" || out[0].Candidates[0].CircuitState != "half-open" {
		t.Fatalf("half-open candidate = %#v", out[0].Candidates)
	}
	if len(out[0].Candidates) != 2 || out[0].Candidates[1].TargetID != "closed" {
		t.Fatalf("candidate order = %#v", out[0].Candidates)
	}
}

func TestGetModelRuleShadowComparisonsExplicitlyAssumesCircuitClosedWithoutSnapshot(t *testing.T) {
	s := &diagnosticsStore{rules: []model.ModelRule{{ID: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", Enabled: true}}}}, providers: map[string]*model.Provider{"p": {ID: "p", Enabled: true}}}
	out, err := shadowApp(s, nil).GetModelRuleShadowComparisons()
	if err != nil || len(out) != 1 || len(out[0].Assumptions) == 0 {
		t.Fatalf("shadow = %#v, err=%v", out, err)
	}
	found := false
	for _, assumption := range out[0].Assumptions {
		if assumption == "circuit_state_assumed_closed/unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing circuit assumption: %#v", out[0].Assumptions)
	}
}

func TestGetTargetDiagnosticsKeepsDuplicateTargetIDsScopedAndOrdered(t *testing.T) {
	s := &diagnosticsStore{
		rules:     []model.ModelRule{{ID: "r1", Name: "one", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "same", ProviderID: "p1", ModelName: "m1", Tier: 1}}}, {ID: "r2", Name: "two", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "same", ProviderID: "p2", ModelName: "m2", Tier: 2}}}},
		providers: map[string]*model.Provider{"p1": {ID: "p1", Name: "P1"}, "p2": {ID: "p2", Name: "P2"}}, models: map[string]*model.Model{},
	}
	out, err := diagnosticApp(s, diagnosticsMetrics{snapshots: map[model.TargetMetricKey]metrics.Snapshot{}}).GetTargetDiagnostics()
	if err != nil || len(out) != 2 {
		t.Fatalf("diagnostics = %#v, err=%v", out, err)
	}
	if out[0].RuleID != "r1" || out[0].ProviderID != "p1" || out[1].RuleID != "r2" || out[1].ProviderID != "p2" {
		t.Fatalf("metadata crossed or order changed: %#v", out)
	}
	if out[0].Endpoint != "/v1/chat/completions" || !out[0].EndpointAssumed {
		t.Fatalf("endpoint mapping = %#v", out[0])
	}
}

func TestGetTargetDiagnosticsLocalProviderFailureDoesNotAbortBatch(t *testing.T) {
	s := &diagnosticsStore{
		rules:     []model.ModelRule{{ID: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "bad", ProviderID: "missing", ModelName: "m1", Enabled: true}, {ID: "ok", ProviderID: "p", ModelName: "m2", Enabled: true}}}},
		providers: map[string]*model.Provider{"p": {ID: "p", Name: "good"}}, providerErr: map[string]error{"missing": errors.New("not found")}, models: map[string]*model.Model{"p:m2": {Name: "m2", RequestPrice: .1}},
	}
	out, err := diagnosticApp(s, diagnosticsMetrics{snapshots: map[model.TargetMetricKey]metrics.Snapshot{}}).GetTargetDiagnostics()
	if err != nil || len(out) != 2 {
		t.Fatalf("diagnostics = %#v, err=%v", out, err)
	}
	if out[0].Availability != "unavailable" || out[0].Reason != "provider_unavailable" {
		t.Fatalf("bad target = %#v", out[0])
	}
	if out[1].Availability == "unavailable" || out[1].ProviderName != "good" {
		t.Fatalf("good target = %#v", out[1])
	}
}

func TestGetTargetDiagnosticsUsesRecentRouteSample(t *testing.T) {
	target := model.ModelRuleTarget{ID: "t", ProviderID: "p", ModelName: "m", Enabled: true}
	s := &diagnosticsStore{rules: []model.ModelRule{{ID: "r", Name: "rule", Enabled: true, Targets: []model.ModelRuleTarget{target}}}, providers: map[string]*model.Provider{"p": {ID: "p", Name: "provider"}}, models: map[string]*model.Model{"p:m": {Name: "m"}}}
	route := model.RouteModeKey{TargetID: "t", InboundProtocol: "openai_responses", UpstreamProtocol: "openai_chat"}
	out, err := diagnosticApp(s, diagnosticsMetrics{snapshots: map[model.TargetMetricKey]metrics.Snapshot{}, routes: map[model.RouteModeKey]metrics.RouteSnapshot{route: {Key: route, Attempts: 2, Successes: 1}}}).GetTargetDiagnostics()
	if err != nil || len(out) != 1 {
		t.Fatalf("diagnostics = %#v, err=%v", out, err)
	}
	if !out[0].MetricsFresh || out[0].SampleCount != 2 {
		t.Fatalf("route sample was not exposed: %#v", out[0])
	}
	if out[0].SampleBasis != "runtime_route_window" || len(out[0].RouteModes) != 1 || out[0].RouteModes[0] != "openai_responses -> openai_chat" {
		t.Fatalf("route sample explanation = %#v", out[0])
	}
}

func TestGetTargetDiagnosticsWithoutRouteSampleRemainsNoSample(t *testing.T) {
	target := model.ModelRuleTarget{ID: "t", ProviderID: "p", ModelName: "m", Enabled: true}
	s := &diagnosticsStore{rules: []model.ModelRule{{ID: "r", Enabled: true, Targets: []model.ModelRuleTarget{target}}}, providers: map[string]*model.Provider{"p": {ID: "p"}}, models: map[string]*model.Model{"p:m": {Name: "m"}}}
	out, err := diagnosticApp(s, diagnosticsMetrics{snapshots: map[model.TargetMetricKey]metrics.Snapshot{}, routes: map[model.RouteModeKey]metrics.RouteSnapshot{}}).GetTargetDiagnostics()
	if err != nil || len(out) != 1 {
		t.Fatalf("diagnostics = %#v, err=%v", out, err)
	}
	if out[0].MetricsFresh || out[0].SampleCount != 0 {
		t.Fatalf("unexpected sample: %#v", out[0])
	}
}

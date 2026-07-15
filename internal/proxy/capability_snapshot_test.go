package proxy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"autoapi/internal/model"
)

func TestCapabilitySnapshotManualAndFallbackRules(t *testing.T) {
	providers := map[string]*model.Provider{
		"p": {ID: "p", Enabled: true, ResponsesEnabled: true, MessagesEnabled: false, GeminiEnabled: true},
	}
	rows := []model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		{ProviderID: "p", Protocol: string(ProtocolAnthropicMessages), Feature: "native", Enabled: true, Source: "manual"},
		{ProviderID: "p", Protocol: string(ProtocolGemini), Feature: "native", Enabled: false, Source: "legacy"},
		{ProviderID: "p", Protocol: string(ProtocolGemini), Feature: "model", Enabled: false, Source: "manual"},
	}
	s := newCapabilitySnapshot(rows, providers)
	if s.supports("p", ProtocolOpenAIResponses) {
		t.Fatal("manual false must override legacy true")
	}
	if !s.supports("p", ProtocolAnthropicMessages) {
		t.Fatal("manual true must override legacy false")
	}
	if !s.supports("p", ProtocolGemini) {
		t.Fatal("legacy row must not override legacy bool")
	}
	if !s.supports("p", ProtocolOpenAIChat) || !s.supports("p", ProtocolOpenAI) {
		t.Fatal("Chat/OpenAI defaults must be enabled")
	}
	if s.supports("missing", ProtocolOpenAIResponses) || s.supports("p", ProtocolUnknown) {
		t.Fatal("unknown provider/protocol must be false")
	}
}

func TestFeatureEnabledPrecedenceAndIsolation(t *testing.T) {
	p := &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true}
	rows := []model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureTools), Enabled: true, Source: "manual"},
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureVision), Enabled: true, Source: "manual"},
	}
	s := newCapabilitySnapshot(rows, map[string]*model.Provider{"p": p})

	if ok, _ := s.featureEnabled("p", "m", ProtocolOpenAIResponses, string(model.FeatureTools), false); !ok {
		t.Fatal("provider explicit true should enable")
	}
	if ok, _ := s.featureEnabled("missing", "m", ProtocolOpenAIResponses, string(model.FeatureTools), false); ok {
		t.Fatal("unknown provider should be false")
	}
	if ok, _ := s.featureEnabled("p", "m", ProtocolOpenAIResponses, string(model.FeatureStreaming), false); !ok {
		t.Fatal("streaming unknown should default true")
	}
	if ok, _ := s.featureEnabled("p", "m", ProtocolOpenAIResponses, string(model.FeatureReasoning), true); ok {
		t.Fatal("non-streaming unknown should default false in enforce")
	}
	if ok, known := s.featureEnabled("p", "m", ProtocolOpenAIResponses, string(model.FeatureReasoning), false); !ok || known {
		t.Fatal("non-streaming unknown should default true in observe")
	}

	// Model override disables vision for this model.
	s = s.withModels([]model.ModelCapability{
		{ProviderID: "p", ModelName: "m", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureVision), Enabled: false, Source: "manual"},
	})
	if ok, _ := s.featureEnabled("p", "m", ProtocolOpenAIResponses, string(model.FeatureVision), false); ok {
		t.Fatal("model explicit false should override provider true")
	}
	if ok, _ := s.featureEnabled("p", "other", ProtocolOpenAIResponses, string(model.FeatureVision), false); !ok {
		t.Fatal("model override should not leak to other models")
	}

	// Explicit streaming false is respected.
	s2 := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureStreaming), Enabled: false, Source: "manual"},
	}, map[string]*model.Provider{"p": p})
	if ok, _ := s2.featureEnabled("p", "m", ProtocolOpenAIResponses, string(model.FeatureStreaming), false); ok {
		t.Fatal("explicit streaming false should reject")
	}

	// Protocol isolation: same feature on a different protocol must not leak.
	if ok, _ := s.featureEnabled("p", "m", ProtocolAnthropicMessages, string(model.FeatureTools), true); ok {
		t.Fatal("feature must be protocol-isolated")
	}

	// Legacy rows must not act as explicit feature overrides.
	s3 := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureTools), Enabled: true, Source: "legacy"},
	}, map[string]*model.Provider{"p": p})
	if ok, known := s3.featureEnabled("p", "m", ProtocolOpenAIResponses, string(model.FeatureTools), false); !ok || known {
		t.Fatalf("legacy row must not be explicit override: ok=%v known=%v", ok, known)
	}
}

func TestNativeSelectorPrefersNativeBeforeConversionFallback(t *testing.T) {
	// Native target does NOT support tools; conversion target supports tools.
	rule := []model.ModelRule{{
		Name: "r", Enabled: true,
		Targets: []model.ModelRuleTarget{
			{ProviderID: "native", Enabled: true},
			{ProviderID: "convert", Enabled: true},
		},
	}}
	providers := map[string]*model.Provider{
		"native":  {ID: "native", Enabled: true, MessagesEnabled: true},
		"convert": {ID: "convert", Enabled: true, ResponsesEnabled: true},
	}
	caps := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "convert", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureTools), Enabled: true, Source: "manual"},
	}, providers)

	req := &InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages, Enforcement: model.FeatureCapabilityEnforcementEnforce, Requirements: &model.RequestRequirements{Features: []model.Feature{model.FeatureTools}}}
	cands, err := selectCandidates(req, rule, nil, func(id string) (*model.Provider, error) { return providers[id], nil }, caps)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].provider.ID != "convert" {
		t.Fatalf("unexpected hybrid candidates: %+v", cands)
	}
	if cands[0].convertTo != ProtocolOpenAIResponses {
		t.Fatalf("unexpected hybrid modes: %+v", cands)
	}
}

func TestConversionRejectsUnpreservableFeatures(t *testing.T) {
	rule := []model.ModelRule{{
		Name: "r", Enabled: true,
		Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}},
	}}
	providers := map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true}}
	caps := newCapabilitySnapshot(nil, providers)
	for _, f := range []model.Feature{model.FeatureVision, model.FeatureReasoning, model.FeatureStructuredOutput, model.FeatureStateful, model.FeatureCacheControl, model.FeatureAudio, model.FeatureDocument} {
		req := &InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages, Requirements: &model.RequestRequirements{Features: []model.Feature{f}}}
		if _, err := selectCandidates(req, rule, nil, func(id string) (*model.Provider, error) { return providers[id], nil }, caps); err == nil || !errors.Is(err, errUnsupportedFeature) {
			t.Fatalf("feature %q should be rejected, got %v", f, err)
		}
	}
	// NativeOnly / UnknownSemantic also rejected.
	req := &InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages, Requirements: &model.RequestRequirements{NativeOnly: true}}
	if _, err := selectCandidates(req, rule, nil, func(id string) (*model.Provider, error) { return providers[id], nil }, caps); err == nil || !errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("NativeOnly should be rejected, got %v", err)
	}
}

func TestConversionToolsRequireExplicitCapability(t *testing.T) {
	rule := []model.ModelRule{{
		Name: "r", Enabled: true,
		Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}},
	}}
	providers := map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true}}
	caps := newCapabilitySnapshot(nil, providers)
	req := &InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages, Requirements: &model.RequestRequirements{Features: []model.Feature{model.FeatureTools}}}
	if _, err := selectCandidates(req, rule, nil, func(id string) (*model.Provider, error) { return providers[id], nil }, caps); err == nil || !errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("tools without explicit capability should be rejected, got %v", err)
	}

	caps = newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureTools), Enabled: true, Source: "manual"},
	}, providers)
	conv, err := selectCandidates(req, rule, nil, func(id string) (*model.Provider, error) { return providers[id], nil }, caps)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 1 {
		t.Fatalf("expected one conversion candidate, got %d", len(conv))
	}
}

func TestFeatureFilteredNotAffectedByOpenCircuit(t *testing.T) {
	// Single target: breaker open + feature false must still be 503 (unavailable),
	// not 422, because the open target is not an available candidate.
	rule := []model.ModelRule{{
		Name: "r", Enabled: true,
		Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}},
	}}
	providers := map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true}}
	caps := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureTools), Enabled: false, Source: "manual"},
	}, providers)
	cb := NewCircuitBreaker()
	for i := 0; i < failureThreshold+1; i++ {
		cb.Record(false) // force open
	}
	breakers := map[string]*CircuitBreaker{"p": cb}
	req := &InboundRequest{Model: "r", Protocol: ProtocolOpenAIResponses, Enforcement: model.FeatureCapabilityEnforcementEnforce, Requirements: &model.RequestRequirements{Features: []model.Feature{model.FeatureTools}}}
	_, err := selectCandidates(req, rule, breakers, func(id string) (*model.Provider, error) { return providers[id], nil }, caps)
	if err == nil || errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("expected regular unavailable error, got %v", err)
	}

	// One open target + one feature-failed available target => 422.
	rule = []model.ModelRule{{
		Name: "r", Enabled: true,
		Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}, {ProviderID: "p2", Enabled: true}},
	}}
	providers["p2"] = &model.Provider{ID: "p2", Enabled: true, ResponsesEnabled: true}
	req = &InboundRequest{Model: "r", Protocol: ProtocolOpenAIResponses, Enforcement: model.FeatureCapabilityEnforcementEnforce, Requirements: &model.RequestRequirements{Features: []model.Feature{model.FeatureTools}}}
	_, err = selectCandidates(req, rule, breakers, func(id string) (*model.Provider, error) { return providers[id], nil }, caps)
	if err == nil || !errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("expected unsupported feature, got %v", err)
	}
}

func TestConversionFeatureFilteringRespectsAvailability(t *testing.T) {
	// Helper to build a rule with a single target.
	makeRule := func(pid string) []model.ModelRule {
		return []model.ModelRule{{
			Name: "r", Enabled: true,
			Targets: []model.ModelRuleTarget{{ProviderID: pid, Enabled: true}},
		}}
	}
	reqTools := &InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages, Requirements: &model.RequestRequirements{Features: []model.Feature{model.FeatureTools}}}

	// 1) Tools request, only conversion target breaker open -> regular unavailable, not 422.
	providersOpen := map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true}}
	capsTools := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureTools), Enabled: true, Source: "manual"},
	}, providersOpen)
	cb := NewCircuitBreaker()
	for i := 0; i < failureThreshold+1; i++ {
		cb.Record(false)
	}
	breakers := map[string]*CircuitBreaker{"p": cb}
	_, err := selectCandidates(reqTools, makeRule("p"), breakers, func(id string) (*model.Provider, error) { return providersOpen[id], nil }, capsTools)
	if err == nil || errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("expected regular conversion unavailable, got %v", err)
	}

	// 2) Tools request, target protocol not supported -> regular unavailable.
	providersNoProto := map[string]*model.Provider{"p": {ID: "p", Enabled: true, MessagesEnabled: false}}
	_, err = selectCandidates(reqTools, makeRule("p"), nil, func(id string) (*model.Provider, error) { return providersNoProto[id], nil }, newCapabilitySnapshot(nil, providersNoProto))
	if err == nil || errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("expected regular conversion unavailable, got %v", err)
	}

	// 3) Tools request, available target with explicit tools=false -> unsupported feature 422.
	providersFalse := map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true}}
	capsFalse := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureTools), Enabled: false, Source: "manual"},
	}, providersFalse)
	_, err = selectCandidates(reqTools, makeRule("p"), nil, func(id string) (*model.Provider, error) { return providersFalse[id], nil }, capsFalse)
	if err == nil || !errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("expected unsupported feature, got %v", err)
	}

	// 4) Preservation: vision request -> unsupported feature 422 regardless of capability rows.
	providersVision := map[string]*model.Provider{"p": {ID: "p", Enabled: true, ResponsesEnabled: true}}
	capsVision := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: string(model.FeatureVision), Enabled: true, Source: "manual"},
	}, providersVision)
	reqVision := &InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages, Requirements: &model.RequestRequirements{Features: []model.Feature{model.FeatureVision}}}
	_, err = selectCandidates(reqVision, makeRule("p"), nil, func(id string) (*model.Provider, error) { return providersVision[id], nil }, capsVision)
	if err == nil || !errors.Is(err, errUnsupportedFeature) {
		t.Fatalf("expected unsupported feature for vision preservation, got %v", err)
	}
}

func TestCapabilitySnapshotModelOverridesAndIsolation(t *testing.T) {
	p := &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true}
	s := newCapabilitySnapshot(nil, map[string]*model.Provider{"p": p}).withModels([]model.ModelCapability{
		{ProviderID: "p", ModelName: "m", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		{ProviderID: "p", ModelName: "m", Protocol: string(ProtocolAnthropicMessages), Feature: "native", Enabled: true, Source: "manual"},
		{ProviderID: "p", ModelName: "m2", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: true, Source: "manual"},
		{ProviderID: "p2", ModelName: "m", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: true, Source: "manual"},
		{ProviderID: "p", ModelName: "other", Protocol: string(ProtocolOpenAIResponses), Feature: "tools", Enabled: false, Source: "manual"},
	})
	if s.supportsModel("p", "m", ProtocolOpenAIResponses) {
		t.Fatal("manual model false must tighten provider true")
	}
	if !s.supportsModel("p", "m", ProtocolAnthropicMessages) {
		t.Fatal("manual model true must extend provider false")
	}
	if !s.supportsModel("p", "other", ProtocolOpenAIResponses) {
		t.Fatal("non-native feature must not gate protocol")
	}
	if !s.supportsModel("p", "m2", ProtocolOpenAIResponses) {
		t.Fatal("different model isolation failed")
	}
	if s.supportsModel("p2", "m", ProtocolOpenAIResponses) {
		t.Fatal("model override for unknown provider must be false")
	}
}

func TestSelectConversionCandidatesUsesUpstreamModelCapability(t *testing.T) {
	p := &model.Provider{ID: "p", Enabled: true}
	rule := []model.ModelRule{{Name: "client", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}}
	lookup := func(string) (*model.Provider, error) { return p, nil }
	s := newCapabilitySnapshot(nil, map[string]*model.Provider{"p": p}).withModels([]model.ModelCapability{{ProviderID: "p", ModelName: "upstream", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"}})
	if _, err := selectCandidates(&InboundRequest{Model: "client", Protocol: ProtocolAnthropicMessages}, rule, nil, lookup, s); err == nil {
		t.Fatal("Messages→Responses ignored upstream model override")
	}
	s = newCapabilitySnapshot(nil, map[string]*model.Provider{"p": p}).withModels([]model.ModelCapability{{ProviderID: "p", ModelName: "upstream", Protocol: string(ProtocolAnthropicMessages), Feature: "native", Enabled: true, Source: "manual"}})
	if _, err := selectCandidates(&InboundRequest{Model: "client", Protocol: ProtocolOpenAIResponses}, rule, nil, lookup, s); err != nil {
		t.Fatalf("Responses→Messages: %v", err)
	}
}

func TestSelectCandidatesUsesUpstreamModelNameForModelCapability(t *testing.T) {
	p := &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true}
	rule := []model.ModelRule{{Name: "client", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "upstream", Enabled: true}}}}
	s := newCapabilitySnapshot(nil, map[string]*model.Provider{"p": p}).withModels([]model.ModelCapability{{ProviderID: "p", ModelName: "upstream", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"}})
	candidates, err := selectCandidates(&InboundRequest{Model: "client", Protocol: ProtocolOpenAIResponses}, rule, nil, func(string) (*model.Provider, error) { return p, nil }, s)
	if err != nil || len(candidates) != 1 || candidates[0].convertTo != ProtocolOpenAIChat {
		t.Fatalf("expected conversion after native model override, candidates=%+v err=%v", candidates, err)
	}
}

func TestSelectCandidatesNativeCapabilityOverrides(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		protocol             Protocol
		legacy, manual, want bool
	}{
		{"responses-off", ProtocolOpenAIResponses, true, false, false},
		{"responses-on", ProtocolOpenAIResponses, false, true, true},
		{"messages-on", ProtocolAnthropicMessages, false, true, true},
		{"gemini-off", ProtocolGemini, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: tc.legacy, GeminiEnabled: tc.legacy}
			rows := []model.ProviderCapability{{ProviderID: "p", Protocol: string(tc.protocol), Feature: "native", Enabled: tc.manual, Source: "manual"}}
			r, err := selectCandidates(&InboundRequest{Model: "r", Protocol: tc.protocol}, []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}}}}, nil, func(string) (*model.Provider, error) { return p, nil }, newCapabilitySnapshot(rows, map[string]*model.Provider{"p": p}))
			if tc.name == "responses-off" {
				if err != nil || len(r) != 1 || r[0].convertTo != ProtocolOpenAIChat {
					t.Fatalf("expected Responses to Chat conversion, candidates=%+v err=%v", r, err)
				}
				return
			}
			if (err == nil) != tc.want || (tc.want && len(r) != 1) {
				t.Fatalf("want=%v candidates=%d err=%v", tc.want, len(r), err)
			}
		})
	}
}

func TestSelectConversionCandidatesCapabilityGate(t *testing.T) {
	p := &model.Provider{ID: "p", Enabled: true}
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}}}}
	lookup := func(string) (*model.Provider, error) { return p, nil }
	if _, err := selectCandidates(&InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages}, rule, nil, lookup, newCapabilitySnapshot([]model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"}}, map[string]*model.Provider{"p": p})); err == nil {
		t.Fatal("Messages to Responses should be gated")
	}
	if _, err := selectCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIResponses}, rule, nil, lookup, newCapabilitySnapshot([]model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolAnthropicMessages), Feature: "native", Enabled: true, Source: "manual"}}, map[string]*model.Provider{"p": p})); err != nil {
		t.Fatalf("Responses to Messages: %v", err)
	}
}

func TestSelectConversionCandidatesPreservationClassificationAfterBasicAvailability(t *testing.T) {
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}}}}
	nativeOnly := &model.RequestRequirements{NativeOnly: true, UnknownSemantic: true}

	tests := []struct {
		name        string
		provider    *model.Provider
		breakerOpen bool
		snap        capabilitySnapshot
		reqs        *model.RequestRequirements
		want422     bool
	}{
		{
			name:     "native-only with disabled provider is unavailable",
			provider: &model.Provider{ID: "p", Enabled: false, ResponsesEnabled: true},
			reqs:     nativeOnly,
		},
		{
			name:        "vision with breaker open is unavailable",
			provider:    &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true},
			breakerOpen: true,
			reqs:        &model.RequestRequirements{Features: []model.Feature{model.FeatureVision}},
		},
		{
			name:     "native-only with no target protocol is unavailable",
			provider: &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: false},
			reqs:     nativeOnly,
		},
		{
			name:     "native-only with basic target is unsupported",
			provider: &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true},
			reqs:     nativeOnly,
			want422:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(string) (*model.Provider, error) { return tc.provider, nil }
			providers := map[string]*model.Provider{"p": tc.provider}
			snap := tc.snap
			if snap.providers == nil {
				snap = newCapabilitySnapshot(nil, providers)
			}
			breakers := map[string]*CircuitBreaker(nil)
			if tc.breakerOpen {
				cb := NewCircuitBreaker()
				cb.state = StateOpen
				cb.openedAt = time.Now()
				breakers = map[string]*CircuitBreaker{"p": cb}
			}
			_, err := selectCandidates(&InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages, Requirements: tc.reqs}, rule, breakers, lookup, snap)
			got422 := errors.Is(err, errUnsupportedFeature)
			if got422 != tc.want422 {
				t.Fatalf("want422=%v err=%v", tc.want422, err)
			}
			if err == nil {
				t.Fatal("expected candidate selection to fail")
			}
		})
	}
}

func TestConversionEdgeRegistryPrioritiesAndStreams(t *testing.T) {
	if got, ok := edgeFor(ProtocolAnthropicMessages, ProtocolOpenAIResponses); !ok || got.Priority != 10 || !got.SupportsStream {
		t.Fatalf("Messages edge = %+v, ok=%v", got, ok)
	}
	if got, ok := edgeFor(ProtocolOpenAIResponses, ProtocolAnthropicMessages); !ok || got.Priority != 10 || !got.SupportsStream {
		t.Fatalf("Responses→Messages edge = %+v, ok=%v", got, ok)
	}
	if got, ok := edgeFor(ProtocolOpenAIChat, ProtocolOpenAIResponses); !ok || got.Priority != 10 || !got.SupportsStream {
		t.Fatalf("Chat edge = %+v, ok=%v", got, ok)
	}
	if got, ok := edgeFor(ProtocolOpenAIResponses, ProtocolOpenAIChat); !ok || got.Priority != 20 || !got.SupportsStream {
		t.Fatalf("Responses→Chat edge = %+v, ok=%v", got, ok)
	}
}

func TestSelectConversionCandidatesMultiEdgeFallbackAndOnePerTarget(t *testing.T) {
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{
		{ID: "t0", ProviderID: "p0", Enabled: true},
		{ID: "t1", ProviderID: "p1", Enabled: true},
	}}}
	providers := map[string]*model.Provider{
		"p0": {ID: "p0", Enabled: true},
		"p1": {ID: "p1", Enabled: true},
	}
	lookup := func(id string) (*model.Provider, error) { return providers[id], nil }
	snapshot := newCapabilitySnapshot([]model.ProviderCapability{
		{ProviderID: "p0", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		{ProviderID: "p0", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: true, Source: "manual"},
		{ProviderID: "p1", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"},
		{ProviderID: "p1", Protocol: string(ProtocolAnthropicMessages), Feature: "native", Enabled: true, Source: "manual"},
	}, providers)
	candidates, err := selectCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIResponses}, rule, nil, lookup, snapshot)
	if err != nil {
		t.Fatalf("select conversion candidates: %v", err)
	}
	if len(candidates) != 2 || candidates[0].targetID != "t0" || candidates[0].convertTo != ProtocolOpenAIChat || candidates[1].targetID != "t1" || candidates[1].convertTo != ProtocolAnthropicMessages {
		t.Fatalf("unexpected target/edge selection: %+v", candidates)
	}
}

func TestSelectConversionCandidatesResponsesStreamFallsBackToChatEdge(t *testing.T) {
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t0", ProviderID: "p", Enabled: true}}}}
	p := &model.Provider{ID: "p", Enabled: true}
	lookup := func(string) (*model.Provider, error) { return p, nil }
	snapshot := newCapabilitySnapshot([]model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: true, Source: "manual"}}, map[string]*model.Provider{"p": p})
	candidates, err := selectCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIResponses, Stream: true}, rule, nil, lookup, snapshot)
	if err != nil || len(candidates) != 1 || candidates[0].convertTo != ProtocolOpenAIChat {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
}

func TestSelectConversionCandidatesChatStreamResponsesIsSupported(t *testing.T) {
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ID: "t", ProviderID: "p", Enabled: true}}}}
	p := &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: true}
	snapshot := newCapabilitySnapshot([]model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIChat), Feature: "native", Enabled: false, Source: "manual"}}, map[string]*model.Provider{"p": p})
	candidates, err := selectCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIChat, Stream: true}, rule, nil, func(string) (*model.Provider, error) { return p, nil }, snapshot)
	if err != nil || len(candidates) != 1 || candidates[0].convertTo != ProtocolOpenAIResponses {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
}

func TestSelectConversionCandidatesResponsesStreamIncludesChatAndMessages(t *testing.T) {
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{
		{ID: "chat", ProviderID: "p0", Enabled: true},
		{ID: "messages", ProviderID: "p1", Enabled: true},
	}}}
	p0 := &model.Provider{ID: "p0", Enabled: true}
	p1 := &model.Provider{ID: "p1", Enabled: true, MessagesEnabled: true}
	providers := map[string]*model.Provider{"p0": p0, "p1": p1}
	snapshot := newCapabilitySnapshot(nil, providers)
	candidates, err := selectCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIResponses, Stream: true}, rule, nil, func(id string) (*model.Provider, error) { return providers[id], nil }, snapshot)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	if candidates[0].targetID != "chat" || candidates[0].convertTo != ProtocolOpenAIChat {
		t.Fatalf("unexpected first candidate: %+v", candidates[0])
	}
	if candidates[1].targetID != "messages" || candidates[1].convertTo != ProtocolAnthropicMessages {
		t.Fatalf("unexpected second candidate: %+v", candidates[1])
	}
}
func TestSelectCandidatesProtocolUnknownValidation(t *testing.T) {
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}}}}
	lookup := func(string) (*model.Provider, error) { return &model.Provider{ID: "p", Enabled: true}, nil }
	if _, err := selectCandidates(&InboundRequest{Model: "r", Endpoint: "/v1/chat/completions"}, rule, nil, lookup); err != nil {
		t.Fatalf("chat compatibility: %v", err)
	}
	for _, req := range []*InboundRequest{{Model: "r", Endpoint: "/v1/images/generations"}, {Model: "r", Task: "wat"}} {
		if _, err := selectCandidates(req, rule, nil, lookup); err == nil || !strings.Contains(err.Error(), "unknown request protocol") {
			t.Fatalf("expected unknown protocol error, got %v", err)
		}
	}
}

func TestResolveCandidatesUsesBulkSnapshotsWithoutProviderFallback(t *testing.T) {
	bulkErr := errors.New("bulk providers")
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Enabled: true}}, rules: []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}, {ProviderID: "p", Enabled: true}}}}, bulkProviderErr: bulkErr}
	p := New(st, &mockService{}, 0, nil)
	if _, err := p.resolveCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIChat}); !errors.Is(err, bulkErr) {
		t.Fatalf("bulk provider error: %v", err)
	}
	capErr := errors.New("bulk capabilities")
	st.bulkCapabilityErr = capErr
	if _, err := p.resolveCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIChat}); !errors.Is(err, capErr) {
		t.Fatalf("bulk capability error: %v", err)
	}
	st.bulkProviderErr = nil
	st.bulkCapabilityErr = nil
	st.providers = map[string]*model.Provider{}
	if _, err := p.resolveCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIChat}); err == nil || st.getProviderCalls != 0 || !strings.Contains(err.Error(), "matched provider not found") {
		t.Fatalf("missing provider fallback/error: err=%v get=%d", err, st.getProviderCalls)
	}
	if len(st.bulkProviderIDs) != 1 || st.bulkProviderIDs[0] != "p" {
		t.Fatalf("provider IDs were not deduplicated: %v", st.bulkProviderIDs)
	}
}

func TestResolveCandidatesModelCapabilityBulkErrorsAndRefs(t *testing.T) {
	rule := []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}, {ProviderID: "p", ModelName: "m", Enabled: true}}}}
	st := &mockStore{providers: map[string]*model.Provider{"p": {ID: "p", Enabled: true}}, rules: rule, bulkModelCapabilityErr: errors.New("model cap")}
	p := New(st, &mockService{}, 0, nil)
	if _, err := p.resolveCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIChat}); err == nil || !strings.Contains(err.Error(), "model cap") {
		t.Fatalf("model capability error: %v", err)
	}
	st.bulkModelCapabilityErr = nil
	if _, err := p.resolveCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIChat}); err != nil {
		t.Fatal(err)
	}
	if len(st.bulkModelRefs) != 1 || st.bulkModelRefs[0].ModelName != "m" {
		t.Fatalf("model refs: %+v", st.bulkModelRefs)
	}
}

package proxy

import (
	"errors"
	"strings"
	"testing"

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
			p := &model.Provider{ID: "p", Enabled: true, ResponsesEnabled: tc.legacy, MessagesEnabled: tc.legacy, GeminiEnabled: tc.legacy}
			rows := []model.ProviderCapability{{ProviderID: "p", Protocol: string(tc.protocol), Feature: "native", Enabled: tc.manual, Source: "manual"}}
			r, err := selectCandidates(&InboundRequest{Model: "r", Protocol: tc.protocol}, []model.ModelRule{{Name: "r", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", Enabled: true}}}}, nil, func(string) (*model.Provider, error) { return p, nil }, newCapabilitySnapshot(rows, map[string]*model.Provider{"p": p}))
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
	if _, err := selectConversionCandidates(&InboundRequest{Model: "r", Protocol: ProtocolAnthropicMessages}, rule, nil, lookup, newCapabilitySnapshot([]model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolOpenAIResponses), Feature: "native", Enabled: false, Source: "manual"}}, map[string]*model.Provider{"p": p})); err == nil {
		t.Fatal("Messages to Responses should be gated")
	}
	if _, err := selectConversionCandidates(&InboundRequest{Model: "r", Protocol: ProtocolOpenAIResponses}, rule, nil, lookup, newCapabilitySnapshot([]model.ProviderCapability{{ProviderID: "p", Protocol: string(ProtocolAnthropicMessages), Feature: "native", Enabled: true, Source: "manual"}}, map[string]*model.Provider{"p": p})); err != nil {
		t.Fatalf("Responses to Messages: %v", err)
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

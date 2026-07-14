package store

import (
	"testing"

	"autoapi/internal/model"
)

func TestProviderCapabilitiesCRUDAndSupportsProtocol(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Anthropic", BaseURL: "https://api.anthropic.com", MessagesEnabled: true})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	supported, err := s.ProviderSupportsProtocol(p.ID, "anthropic_messages")
	if err != nil {
		t.Fatalf("ProviderSupportsProtocol empty: %v", err)
	}
	if supported {
		t.Fatal("unknown capability should not be supported")
	}

	if err := s.SetProviderCapability(p.ID, "anthropic_messages", "native", true); err != nil {
		t.Fatalf("SetProviderCapability: %v", err)
	}
	caps, err := s.GetProviderCapabilities(p.ID)
	if err != nil {
		t.Fatalf("GetProviderCapabilities: %v", err)
	}
	if len(caps) != 1 || caps[0].ProviderID != p.ID || caps[0].Protocol != "anthropic_messages" || caps[0].Feature != "native" || !caps[0].Enabled || caps[0].Source != "manual" || caps[0].UpdatedAt == 0 {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
	supported, err = s.ProviderSupportsProtocol(p.ID, "anthropic_messages")
	if err != nil || !supported {
		t.Fatalf("ProviderSupportsProtocol enabled=%v err=%v", supported, err)
	}

	if err := s.SetProviderCapability(p.ID, "anthropic_messages", "native", false); err != nil {
		t.Fatalf("SetProviderCapability false: %v", err)
	}
	supported, err = s.ProviderSupportsProtocol(p.ID, "anthropic_messages")
	if err != nil || supported {
		t.Fatalf("ProviderSupportsProtocol disabled=%v err=%v", supported, err)
	}
}

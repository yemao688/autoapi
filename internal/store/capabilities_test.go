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
	if !supported {
		t.Fatal("legacy provider bool should be the effective fallback")
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

func TestSetProviderCapabilityPromotesLegacyProjection(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Responses", BaseURL: "https://example.com", ResponsesEnabled: true})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := s.SetProviderCapability(p.ID, "openai_responses", "native", false); err != nil {
		t.Fatalf("SetProviderCapability: %v", err)
	}
	var source string
	if err := s.db.QueryRow(`SELECT source FROM provider_capabilities WHERE provider_id=? AND protocol=?`, p.ID, "openai_responses").Scan(&source); err != nil {
		t.Fatalf("read source: %v", err)
	}
	if source != "manual" {
		t.Fatalf("source = %q, want manual", source)
	}
	supported, err := s.ProviderSupportsProtocol(p.ID, "openai_responses")
	if err != nil {
		t.Fatalf("ProviderSupportsProtocol: %v", err)
	}
	if supported {
		t.Fatal("manual disabled capability should not be supported")
	}
}

func TestUpdateProviderOverridesManualCapability(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Manual", BaseURL: "https://example.com"})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := s.SetProviderCapability(p.ID, "gemini", "native", true); err != nil {
		t.Fatalf("SetProviderCapability: %v", err)
	}
	if _, err := s.UpdateProvider(p.ID, model.ProviderInput{Name: "Manual", BaseURL: "https://example.com", GeminiEnabled: false}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	var enabled bool
	var source string
	if err := s.db.QueryRow(`SELECT enabled, source FROM provider_capabilities WHERE provider_id=? AND protocol=?`, p.ID, "gemini").Scan(&enabled, &source); err != nil {
		t.Fatalf("read capability: %v", err)
	}
	if enabled || source != "manual" {
		t.Fatalf("capability = enabled:%v source:%q, want false/manual", enabled, source)
	}
}

func TestProviderCapabilityFallbackUsesLegacyBoolWithoutRow(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Legacy", BaseURL: "https://example.com", MessagesEnabled: true})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM provider_capabilities WHERE provider_id=?`, p.ID); err != nil {
		t.Fatalf("delete capability rows: %v", err)
	}
	got, err := s.GetProvider(p.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if !got.MessagesEnabled {
		t.Fatal("legacy bool fallback was not preserved")
	}
}

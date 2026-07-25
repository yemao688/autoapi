package store

import (
	"database/sql"
	"strings"
	"testing"

	"autoapi/internal/model"
)

func TestProviderCapabilitiesCRUDAndSupportsProtocol(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Anthropic", BaseURL: "https://api.anthropic.com"})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	supported, err := s.ProviderSupportsProtocol(p.ID, "anthropic_messages")
	if err != nil {
		t.Fatalf("ProviderSupportsProtocol empty: %v", err)
	}
	if supported {
		t.Fatal("new provider save should not enable legacy provider protocol flags")
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
	p, err := s.CreateProvider(model.ProviderInput{Name: "Responses", BaseURL: "https://example.com"})
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

func TestUpdateProviderIgnoresLegacyProtocolInput(t *testing.T) {
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
	if !enabled || source != "manual" {
		t.Fatalf("capability = enabled:%v source:%q, want true/manual", enabled, source)
	}
	var legacy bool
	if err := s.db.QueryRow(`SELECT gemini_enabled FROM providers WHERE id=?`, p.ID).Scan(&legacy); err != nil {
		t.Fatalf("read provider legacy bool: %v", err)
	}
	if !legacy {
		t.Fatal("provider update should preserve existing legacy protocol columns instead of applying input")
	}
}

func TestProviderCapabilityFallbackUsesLegacyBoolWithoutRow(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Legacy", BaseURL: "https://example.com"})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE providers SET messages_enabled = 1 WHERE id=?`, p.ID); err != nil {
		t.Fatalf("seed legacy capability: %v", err)
	}
	got, err := s.GetProvider(p.ID)
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if !got.MessagesEnabled {
		t.Fatal("legacy bool fallback was not preserved")
	}
	supported, err := s.ProviderSupportsProtocol(p.ID, "anthropic_messages")
	if err != nil {
		t.Fatalf("ProviderSupportsProtocol legacy fallback: %v", err)
	}
	if !supported {
		t.Fatal("legacy provider bool should still be honored when no capability row exists")
	}
}

func TestSetProviderCapabilityValidatesInputsAndProviderExists(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Validate", BaseURL: "https://example.com"})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	if err := s.SetProviderCapability("", "anthropic_messages", "native", true); err == nil {
		t.Fatal("empty provider accepted")
	}
	if err := s.SetProviderCapability(p.ID, "unknown_protocol", "native", true); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	if err := s.SetProviderCapability(p.ID, "anthropic_messages", "unknown_feature", true); err == nil {
		t.Fatal("unknown feature accepted")
	}
	if err := s.SetProviderCapability("missing-id", "anthropic_messages", "native", true); err == nil {
		t.Fatal("missing provider accepted")
	}

	if err := s.SetProviderCapability(p.ID, "anthropic_messages", "tools", true); err != nil {
		t.Fatalf("set canonical feature: %v", err)
	}
	caps, err := s.GetProviderCapabilities(p.ID)
	if err != nil {
		t.Fatalf("GetProviderCapabilities: %v", err)
	}
	if len(caps) != 1 || caps[0].Feature != "tools" {
		t.Fatalf("unexpected caps: %+v", caps)
	}
}

func TestDeleteProviderFeatureCapability(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProvider(model.ProviderInput{Name: "Delete", BaseURL: "https://example.com"})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if err := s.SetProviderCapability(p.ID, "anthropic_messages", "tools", true); err != nil {
		t.Fatalf("SetProviderCapability: %v", err)
	}

	if err := s.DeleteProviderFeatureCapability("", "anthropic_messages", "tools"); err == nil {
		t.Fatal("empty provider accepted")
	}
	if err := s.DeleteProviderFeatureCapability(p.ID, "unknown_protocol", "tools"); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	if err := s.DeleteProviderFeatureCapability(p.ID, "anthropic_messages", "native"); err == nil {
		t.Fatal("native delete accepted")
	}
	if err := s.DeleteProviderFeatureCapability("missing", "anthropic_messages", "tools"); err == nil {
		t.Fatal("missing provider accepted")
	}
	if err := s.DeleteProviderFeatureCapability(p.ID, "anthropic_messages", "tools"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteProviderFeatureCapability(p.ID, "anthropic_messages", "tools"); err != nil {
		t.Fatalf("second delete should be idempotent, got %v", err)
	}
}

func TestMigration029RebuildsProviderCapabilitiesWithFK(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := applyMigrations(db, migrationsThrough("028_model_capabilities_model_fk")); err != nil {
		t.Fatalf("apply migrations through 028: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO providers(id,name,base_url,created_at,updated_at) VALUES('p','p','http://p',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO provider_capabilities(provider_id,protocol,feature,enabled,source,updated_at) VALUES
		('p','anthropic_messages','tools',1,'manual',1),
		('orphan','anthropic_messages','tools',1,'manual',2)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='provider_capabilities'`).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sqlText, "REFERENCES providers") {
		t.Fatalf("schema missing FK: %s", sqlText)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_capabilities`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expected 1 row after orphan cleanup, got %d", n)
	}
	if _, err := db.Exec(`INSERT INTO provider_capabilities(provider_id,protocol,feature,enabled,source,updated_at) VALUES('missing-provider','anthropic_messages','tools',1,'manual',1)`); err == nil {
		t.Fatal("missing provider accepted")
	}
	if _, err := db.Exec(`DELETE FROM providers WHERE id='p'`); err != nil {
		t.Fatalf("provider delete failed: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM provider_capabilities`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("provider delete cascade failed: %d", n)
	}
}

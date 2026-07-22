package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"autoapi/internal/model"
)

func TestAPIKeyAllowlistRoundTripAndCascade(t *testing.T) {
	s := newTestStore(t)
	rule, err := s.CreateModelRule(model.ModelRuleInput{Name: "restricted", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.CreateAPIKey(model.ApiKeyInput{Name: "key", AllowedRuleIDs: []string{rule.ID}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAPIKey(key.ID)
	if err != nil || len(got.AllowedRuleIDs) != 1 || got.AllowedRuleIDs[0] != rule.ID {
		t.Fatalf("created allowlist = %#v, err=%v", got.AllowedRuleIDs, err)
	}

	updated, err := s.UpdateAPIKey(key.ID, model.ApiKeyInput{Name: "key2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.AllowedRuleIDs) != 0 {
		t.Fatalf("empty update allowlist = %#v", updated.AllowedRuleIDs)
	}
	if err := s.DeleteModelRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.GetAPIKey(key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining.AllowedRuleIDs) != 0 {
		t.Fatalf("cascade left allowlist rows: %#v", remaining.AllowedRuleIDs)
	}
}

func TestAPIKeyAllowlistEmptySerializesAsArray(t *testing.T) {
	s := newTestStore(t)
	key, err := s.CreateAPIKey(model.ApiKeyInput{Name: "key"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"allowed_rule_ids":[]`)) {
		t.Fatalf("created key JSON = %s, want empty array allowlist", encoded)
	}
	listed, err := s.ListAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].AllowedRuleIDs == nil {
		t.Fatalf("listed allowlist = %#v, want non-nil empty slice", listed[0].AllowedRuleIDs)
	}
}

func TestAPIKeyAllowlistRejectsInvalidAndDuplicateIDsTransactionally(t *testing.T) {
	s := newTestStore(t)
	rule, err := s.CreateModelRule(model.ModelRuleInput{Name: "restricted", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.CreateAPIKey(model.ApiKeyInput{Name: "invalid", AllowedRuleIDs: []string{rule.ID, "missing"}})
	if err == nil {
		t.Fatal("CreateAPIKey with invalid rule ID succeeded")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE name = 'invalid'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid create left %d api key rows", count)
	}

	_, err = s.CreateAPIKey(model.ApiKeyInput{Name: "duplicate", AllowedRuleIDs: []string{rule.ID, rule.ID}})
	if err == nil {
		t.Fatal("CreateAPIKey with duplicate rule IDs succeeded")
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE name = 'duplicate'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("duplicate create left %d api key rows", count)
	}

	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys = %d, want 1", fk)
	}
	if _, err := s.db.Exec(`INSERT INTO api_key_model_rules (api_key_id, model_rule_id) VALUES ('missing-key', ?)`, rule.ID); err == nil {
		t.Fatal("invalid api key foreign key insert succeeded")
	}
	if _, err := s.db.Exec(`INSERT INTO api_keys (id, name, created_at, updated_at) VALUES ('cascade-key', 'cascade', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO api_key_model_rules (api_key_id, model_rule_id) VALUES ('cascade-key', ?)`, rule.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAPIKey("cascade-key"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM api_key_model_rules WHERE api_key_id = 'cascade-key'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("api key delete left %d allowlist rows", count)
	}
}

package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"autoapi/internal/model"
)

func TestResolveCandidatesFiltersAPIKeyRuleAllowlist(t *testing.T) {
	st := &mockStore{
		providers: map[string]*model.Provider{"p": {ID: "p", Enabled: true}},
		rules: []model.ModelRule{
			{ID: "allowed", Name: "allowed-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}}},
			{ID: "blocked", Name: "blocked-model", Enabled: true, Targets: []model.ModelRuleTarget{{ProviderID: "p", ModelName: "m", Enabled: true}}},
		},
	}
	p := &Proxy{store: st}
	if _, err := p.resolveCandidates(&InboundRequest{Model: "blocked-model", Protocol: ProtocolOpenAIChat, AllowedRuleIDs: []string{"allowed"}}); !errors.Is(err, errNoMatch) {
		t.Fatalf("blocked rule error = %v, want errNoMatch", err)
	}
	if _, err := p.resolveCandidates(&InboundRequest{Model: "allowed-model", Protocol: ProtocolOpenAIChat, AllowedRuleIDs: []string{"allowed"}}); err != nil {
		t.Fatalf("allowed rule failed: %v", err)
	}
}

func TestHandleModelsFiltersAPIKeyRuleAllowlist(t *testing.T) {
	st := &mockStore{
		rules: []model.ModelRule{
			{ID: "allowed", Name: "allowed-model", Enabled: true},
			{ID: "blocked", Name: "blocked-model", Enabled: true},
		},
		apiKeys: []model.ApiKey{{ID: "key", Enabled: true, AllowedRuleIDs: []string{"allowed"}}},
	}
	p := &Proxy{store: st}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer key")
	rec := httptest.NewRecorder()
	p.handleModels(rec, req)
	var body struct {
		Data []modelItem `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != "allowed-model" {
		t.Fatalf("models = %#v", body.Data)
	}
}

func TestHandleModelsEmptyAllowlistIsUnrestricted(t *testing.T) {
	st := &mockStore{
		rules: []model.ModelRule{
			{ID: "one", Name: "one", Enabled: true},
			{ID: "two", Name: "two", Enabled: true},
		},
		apiKeys: []model.ApiKey{{ID: "key", Enabled: true, AllowedRuleIDs: []string{}}},
	}
	p := &Proxy{store: st}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer key")
	rec := httptest.NewRecorder()
	p.handleModels(rec, req)
	var body struct {
		Data []modelItem `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("models = %#v, want both models", body.Data)
	}
}

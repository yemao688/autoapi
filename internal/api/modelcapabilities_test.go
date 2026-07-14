package api

import (
	"autoapi/internal/model"
	"errors"
	"testing"
)

type modelCapStore struct {
	StoreService
	got []string
}

func (s *modelCapStore) ListModelCapabilities(providerID, modelName string) ([]model.ModelCapability, error) {
	s.got = []string{providerID, modelName}
	return []model.ModelCapability{}, nil
}
func (s *modelCapStore) SetModelCapability(providerID, modelName, protocol, feature string, enabled bool) error {
	s.got = []string{providerID, modelName, protocol, feature}
	return nil
}
func (s *modelCapStore) DeleteModelCapability(providerID, modelName, protocol, feature string) error {
	s.got = []string{providerID, modelName, protocol, feature}
	return nil
}

func (s *modelCapStore) SetProviderCapability(providerID, protocol, feature string, enabled bool) error {
	s.got = []string{providerID, protocol, feature}
	return nil
}

func (s *modelCapStore) DeleteProviderFeatureCapability(providerID, protocol, feature string) error {
	s.got = []string{providerID, protocol, feature}
	return nil
}

func TestAppModelCapabilityMethodsGuardAndTrim(t *testing.T) {
	app := &App{}
	if _, err := app.ListModelCapabilities("p", "m"); !errors.Is(err, errNotImpl) {
		t.Fatalf("nil list err=%v", err)
	}
	if err := app.SetModelCapability("p", "m", "openai", "native", true); !errors.Is(err, errNotImpl) {
		t.Fatalf("nil set err=%v", err)
	}
	if err := app.DeleteModelCapability("p", "m", "openai", "native"); !errors.Is(err, errNotImpl) {
		t.Fatalf("nil delete err=%v", err)
	}
	st := &modelCapStore{}
	app = &App{deps: Deps{Store: st}}
	if _, err := app.ListModelCapabilities(" p ", " m "); err != nil {
		t.Fatal(err)
	}
	if got := st.got; len(got) != 2 || got[0] != "p" || got[1] != "m" {
		t.Fatalf("list args: %v", got)
	}
	if err := app.SetModelCapability(" p ", " m ", " openai ", " native ", true); err != nil {
		t.Fatal(err)
	}
	if got := st.got; len(got) != 4 || got[0] != "p" || got[1] != "m" || got[2] != "openai" || got[3] != "native" {
		t.Fatalf("set args: %v", got)
	}
	if err := app.DeleteModelCapability(" p ", " m ", " openai ", " native "); err != nil {
		t.Fatal(err)
	}
	if got := st.got; len(got) != 4 || got[0] != "p" || got[1] != "m" || got[2] != "openai" || got[3] != "native" {
		t.Fatalf("delete args: %v", got)
	}
	if err := app.SetModelCapability("", "m", "openai", "native", true); err == nil {
		t.Fatal("empty provider accepted")
	}
}

func TestAppSetProviderFeatureCapability(t *testing.T) {
	app := &App{}
	if err := app.SetProviderFeatureCapability("p", "openai", "tools", true); !errors.Is(err, errNotImpl) {
		t.Fatalf("nil store err=%v", err)
	}
	st := &modelCapStore{}
	app = &App{deps: Deps{Store: st}}
	if err := app.SetProviderFeatureCapability(" p ", " openai ", " tools ", true); err != nil {
		t.Fatal(err)
	}
	if got := st.got; len(got) != 3 || got[0] != "p" || got[1] != "openai" || got[2] != "tools" {
		t.Fatalf("delegate args: %v", got)
	}
	for _, in := range []struct{ p, proto, feat string }{
		{"", "openai", "tools"},
		{"p", "", "tools"},
		{"p", "openai", ""},
		{" ", "openai", "tools"},
	} {
		if err := app.SetProviderFeatureCapability(in.p, in.proto, in.feat, true); err == nil {
			t.Fatalf("missing fields accepted: %+v", in)
		}
	}
	if err := app.SetProviderFeatureCapability("p", "openai", "native", true); err == nil {
		t.Fatal("native feature accepted")
	}

	// Delete delegates with trimmed args and rejects empty/native.
	app = &App{}
	if err := app.DeleteProviderFeatureCapability("p", "openai", "tools"); !errors.Is(err, errNotImpl) {
		t.Fatalf("nil store delete err=%v", err)
	}
	app = &App{deps: Deps{Store: st}}
	if err := app.DeleteProviderFeatureCapability(" p ", " openai ", " tools "); err != nil {
		t.Fatal(err)
	}
	if got := st.got; len(got) != 3 || got[0] != "p" || got[1] != "openai" || got[2] != "tools" {
		t.Fatalf("delete delegate args: %v", got)
	}
	for _, in := range []struct{ p, proto, feat string }{
		{"", "openai", "tools"},
		{"p", "", "tools"},
		{"p", "openai", ""},
		{"p", "openai", "native"},
	} {
		if err := app.DeleteProviderFeatureCapability(in.p, in.proto, in.feat); err == nil {
			t.Fatalf("delete missing fields accepted: %+v", in)
		}
	}
}

package api

import (
	"errors"
	"testing"

	"autoapi/internal/store"
)

type reorderResultStore struct {
	StoreService
	err error
}

func (s *reorderResultStore) ReorderModelRuleTargets(string, []string) error { return s.err }

func TestReorderModelRuleTargetsReturnsExpectedConflictResult(t *testing.T) {
	app := NewApp(Deps{Store: &reorderResultStore{err: store.ErrConflict}})
	result, err := app.ReorderModelRuleTargets("rule", []string{"target"})
	if err != nil || !result.Conflict {
		t.Fatalf("expected conflict result without error, got result=%+v err=%v", result, err)
	}
}

func TestReorderModelRuleTargetsReturnsOperationalError(t *testing.T) {
	opErr := errors.New("database unavailable")
	app := NewApp(Deps{Store: &reorderResultStore{err: opErr}})
	result, err := app.ReorderModelRuleTargets("rule", []string{"target"})
	if !errors.Is(err, opErr) || result.Conflict {
		t.Fatalf("expected operational error, got result=%+v err=%v", result, err)
	}
}

func TestAppVisibilityStartsForegroundAndAcceptsHiddenStartup(t *testing.T) {
	app := NewApp(Deps{})
	if got := app.GetAppVisibilityState(); got != "foreground" {
		t.Fatalf("initial visibility = %q, want foreground", got)
	}
	app.SetInitialVisibility(true)
	if !app.initiallyBackground {
		t.Fatal("expected hidden startup preference to be retained")
	}
}

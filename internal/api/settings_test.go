package api

import (
	"errors"
	"strings"
	"testing"

	"autoapi/internal/model"
)

type rollbackStore struct {
	StoreService
	settings model.Settings
	defaults model.Settings
	saves    []model.Settings
	saveErr  error
}

func (s *rollbackStore) GetSettings() (*model.Settings, error) {
	copy := s.settings
	return &copy, nil
}

func (s *rollbackStore) SaveSettings(settings model.Settings) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.settings = settings
	s.saves = append(s.saves, settings)
	return nil
}

func (s *rollbackStore) StorageDir() string { return "/tmp/autoapi-test" }

func (s *rollbackStore) ResetSettings() (*model.Settings, error) {
	if err := s.SaveSettings(s.defaults); err != nil {
		return nil, err
	}
	copy := s.defaults
	return &copy, nil
}

type rollbackProxy struct {
	ProxyService
	errors []error
	calls  int
}

func (p *rollbackProxy) Restart() error {
	p.calls++
	if len(p.errors) == 0 {
		return nil
	}
	err := p.errors[0]
	p.errors = p.errors[1:]
	return err
}

func TestSaveSettingsRollsBackAfterRestartFailure(t *testing.T) {
	old := model.Settings{Server: model.ServerSettings{Port: 8344}}
	store := &rollbackStore{settings: old}
	restartErr := errors.New("bind failed")
	proxy := &rollbackProxy{errors: []error{restartErr, nil}}
	app := NewApp(Deps{Store: store, Proxy: proxy})

	err := app.SaveSettings(model.Settings{Server: model.ServerSettings{Port: 19090}})
	if !errors.Is(err, restartErr) || !strings.Contains(err.Error(), "previous settings restored") {
		t.Fatalf("SaveSettings error = %v", err)
	}
	if store.settings.Server.Port != old.Server.Port {
		t.Fatalf("persisted port = %d, want rollback to %d", store.settings.Server.Port, old.Server.Port)
	}
	if proxy.calls != 2 {
		t.Fatalf("Restart calls = %d, want 2", proxy.calls)
	}
}

func TestSaveSettingsReportsRollbackFailure(t *testing.T) {
	store := &rollbackStore{settings: model.Settings{Server: model.ServerSettings{Port: 8344}}}
	restartErr := errors.New("bind failed")
	proxy := &rollbackProxy{errors: []error{restartErr}}
	app := NewApp(Deps{Store: store, Proxy: proxy})

	store.saveErr = nil
	err := app.persistSettingsWithRollback(func() (*model.Settings, error) {
		updated := model.Settings{Server: model.ServerSettings{Port: 19090}}
		if err := store.SaveSettings(updated); err != nil {
			return nil, err
		}
		store.saveErr = errors.New("disk full")
		return &updated, nil
	})
	if !errors.Is(err, restartErr) || !strings.Contains(err.Error(), "rollback settings failed: disk full") {
		t.Fatalf("rollback error = %v", err)
	}
	if proxy.calls != 1 {
		t.Fatalf("Restart calls = %d, want 1 when persistence rollback fails", proxy.calls)
	}
}

func TestResetSettingsRollsBackAfterRestartFailure(t *testing.T) {
	old := model.Settings{Server: model.ServerSettings{Port: 19090}}
	defaults := model.Settings{Server: model.ServerSettings{Port: 18344}}
	store := &rollbackStore{settings: old, defaults: defaults}
	restartErr := errors.New("bind failed")
	proxy := &rollbackProxy{errors: []error{restartErr, nil}}
	app := NewApp(Deps{Store: store, Proxy: proxy})

	settings, err := app.ResetSettings()
	if settings == nil || settings.Server.Port != defaults.Server.Port {
		t.Fatalf("returned defaults = %+v", settings)
	}
	if !errors.Is(err, restartErr) || !strings.Contains(err.Error(), "previous settings restored") {
		t.Fatalf("ResetSettings error = %v", err)
	}
	if store.settings.Server.Port != old.Server.Port {
		t.Fatalf("persisted port = %d, want rollback to %d", store.settings.Server.Port, old.Server.Port)
	}
	if proxy.calls != 2 {
		t.Fatalf("Restart calls = %d, want 2", proxy.calls)
	}
}

func TestSaveSettingsReportsListenerRestoreFailure(t *testing.T) {
	store := &rollbackStore{settings: model.Settings{Server: model.ServerSettings{Port: 8344}}}
	restartErr := errors.New("new bind failed")
	restoreErr := errors.New("old bind failed")
	proxy := &rollbackProxy{errors: []error{restartErr, restoreErr}}
	app := NewApp(Deps{Store: store, Proxy: proxy})

	err := app.SaveSettings(model.Settings{Server: model.ServerSettings{Port: 19090}})
	if !errors.Is(err, restartErr) || !strings.Contains(err.Error(), "restore previous listener failed: old bind failed") {
		t.Fatalf("restore listener error = %v", err)
	}
}

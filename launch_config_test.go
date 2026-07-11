package main

import (
	"testing"

	"autoapi/internal/config"
	"autoapi/internal/model"
)

func TestResolveLaunchConfig(t *testing.T) {
	tests := []struct {
		name            string
		settings        *model.Settings
		wantStartHidden bool
		wantHideOnClose bool
		wantEnableTray  bool
	}{
		{
			name:            "nil settings — safe defaults",
			settings:        nil,
			wantStartHidden: false,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
		{
			name: "all defaults",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: model.StartupActionShowWindow,
				MenuBarItem:   true,
				CloseAction:   model.CloseActionBackground,
			}},
			wantStartHidden: false,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
		{
			name: "start hidden + background close + tray",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: model.StartupActionStartHidden,
				MenuBarItem:   true,
				CloseAction:   model.CloseActionBackground,
			}},
			wantStartHidden: true,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
		{
			name: "quit on close",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: model.StartupActionShowWindow,
				MenuBarItem:   true,
				CloseAction:   model.CloseActionQuit,
			}},
			wantStartHidden: false,
			wantHideOnClose: false,
			wantEnableTray:  true,
		},
		{
			name: "tray disabled + background — safety override",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: model.StartupActionShowWindow,
				MenuBarItem:   false,
				CloseAction:   model.CloseActionBackground,
			}},
			wantStartHidden: false,
			wantHideOnClose: false,
			wantEnableTray:  false,
		},
		{
			name: "tray disabled + start hidden — safety override",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: model.StartupActionStartHidden,
				MenuBarItem:   false,
				CloseAction:   model.CloseActionQuit,
			}},
			wantStartHidden: false,
			wantHideOnClose: false,
			wantEnableTray:  false,
		},
		{
			name: "tray disabled + show window + quit — no override needed",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: model.StartupActionShowWindow,
				MenuBarItem:   false,
				CloseAction:   model.CloseActionQuit,
			}},
			wantStartHidden: false,
			wantHideOnClose: false,
			wantEnableTray:  false,
		},
		{
			name: "legacy minimize_menubar normalizes to hidden",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: "minimize_menubar",
				MenuBarItem:   true,
				CloseAction:   model.CloseActionBackground,
			}},
			wantStartHidden: true,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
		{
			name: "legacy no_window normalizes to hidden",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: "no_window",
				MenuBarItem:   true,
				CloseAction:   model.CloseActionBackground,
			}},
			wantStartHidden: true,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
		{
			name: "legacy show normalizes to visible",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: "show",
				MenuBarItem:   true,
				CloseAction:   model.CloseActionBackground,
			}},
			wantStartHidden: false,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
		{
			name: "unknown startup action defaults to show_window",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: "garbage",
				MenuBarItem:   true,
				CloseAction:   model.CloseActionBackground,
			}},
			wantStartHidden: false,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
		{
			name: "unknown close action defaults to background",
			settings: &model.Settings{General: model.GeneralSettings{
				StartupAction: model.StartupActionShowWindow,
				MenuBarItem:   true,
				CloseAction:   "garbage",
			}},
			wantStartHidden: false,
			wantHideOnClose: true,
			wantEnableTray:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := resolveLaunchConfig(tt.settings)
			if cfg.startHidden != tt.wantStartHidden {
				t.Errorf("startHidden = %v, want %v", cfg.startHidden, tt.wantStartHidden)
			}
			if cfg.hideOnClose != tt.wantHideOnClose {
				t.Errorf("hideOnClose = %v, want %v", cfg.hideOnClose, tt.wantHideOnClose)
			}
			if cfg.enableTray != tt.wantEnableTray {
				t.Errorf("enableTray = %v, want %v", cfg.enableTray, tt.wantEnableTray)
			}
		})
	}
}

func TestSingleInstanceLockID(t *testing.T) {
	tests := []struct {
		name    string
		profile config.Profile
		want    string
	}{
		{
			name:    "production retains existing ID for upgrade compatibility",
			profile: config.Profile{Name: "production"},
			want:    "dev.local.autoapi",
		},
		{
			name:    "development gets isolated ID",
			profile: config.Profile{Name: "development"},
			want:    "dev.local.autoapi.development",
		},
		{
			name:    "custom profile gets isolated ID",
			profile: config.Profile{Name: "preview"},
			want:    "dev.local.autoapi.preview",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := singleInstanceLockID(tt.profile); got != tt.want {
				t.Errorf("singleInstanceLockID() = %q, want %q", got, tt.want)
			}
		})
	}
}

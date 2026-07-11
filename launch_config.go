package main

import (
	"log/slog"

	"autoapi/internal/config"
	"autoapi/internal/model"
)

// launchConfig holds the effective runtime lifecycle options resolved from
// persisted settings. All three fields map directly to Wails/tray behaviour
// consumed once at process start and cannot be changed at runtime.
type launchConfig struct {
	startHidden bool
	hideOnClose bool
	enableTray  bool
}

// resolveLaunchConfig computes effective launch options from settings. It
// enforces cross-setting invariants that prevent unrecoverable states
// (e.g. hidden window with no tray icon to restore it). Invalid persisted
// combinations are normalized defensively with a warning log; the caller
// should NOT persist these corrections — the frontend enforces the same
// invariants in the UI.
func resolveLaunchConfig(s *model.Settings) launchConfig {
	cfg := launchConfig{
		startHidden: false,
		hideOnClose: true, // default: background mode
		enableTray:  true,
	}

	if s == nil {
		return cfg
	}

	// StartupAction: show_window → visible. Legacy hidden-start values
	// retain their old behavior. Unknown values fall back to visible.
	switch s.General.StartupAction {
	case model.StartupActionShowWindow, "show":
		cfg.startHidden = false
	case model.StartupActionStartHidden, "minimize_menubar", "no_window":
		cfg.startHidden = true
	default:
		slog.Warn("launch: unknown startup_action, defaulting to show_window",
			"startup_action", s.General.StartupAction)
	}

	// MenuBarItem: default true. Only an explicit false disables the tray.
	cfg.enableTray = s.General.MenuBarItem

	// CloseAction: only explicit "quit" opts out of hide-on-close.
	cfg.hideOnClose = s.General.CloseAction != model.CloseActionQuit
	if cfg.hideOnClose && s.General.CloseAction != model.CloseActionBackground && s.General.CloseAction != "" {
		slog.Warn("launch: unknown close_action, defaulting to background",
			"close_action", s.General.CloseAction)
	}

	// SAFETY INVARIANT: without a tray icon, the user has no way to
	// restore a hidden window. Force visible + quit-on-close.
	if !cfg.enableTray && (cfg.hideOnClose || cfg.startHidden) {
		slog.Warn("launch: menu_bar_item disabled — forcing visible window and quit-on-close for safety",
			"startup_action", s.General.StartupAction,
			"close_action", s.General.CloseAction)
		cfg.hideOnClose = false
		cfg.startHidden = false
	}

	return cfg
}

// singleInstanceLockID returns a profile-specific Wails lock identifier.
// Production retains the original ID so an upgraded production app still
// detects an already-running older build. Development uses its own ID so
// `wails dev` can run alongside the packaged application.
func singleInstanceLockID(profile config.Profile) string {
	if profile.Name == "production" {
		return "dev.local.autoapi"
	}
	return "dev.local.autoapi." + profile.Name
}

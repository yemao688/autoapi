package main

import (
	"context"
	"embed"
	"log/slog"

	"autoapi/internal/tray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	// Resolve static lifecycle options from persisted settings before
	// wails.Run. These options are evaluated once by Wails at startup and
	// cannot be changed at runtime, so setting changes only take effect
	// after an app restart.
	settings, sErr := app.GetSettings()
	cfg := resolveLaunchConfig(settings)
	if sErr != nil {
		slog.Warn("launch: failed to read settings, using safe defaults", "error", sErr)
	}

	// Build the application menu (top-of-screen on macOS, window menu on
	// Windows/Linux). The Wails v2.12.0 release used here does not expose a
	// public system-tray API — the internal menumanager has an AddTrayMenu
	// entry point but it is unexported, and options.App has no TrayIcon
	// field. We therefore register the same menu structure as the
	// application menu, which gives the user a discoverable "Autoapi"
	// entry on every supported platform.
	trayMenu := menu.NewMenu()
	fileMenu := trayMenu.AddSubmenu("Autoapi")
	fileMenu.AddText("显示主窗口", keys.CmdOrCtrl("O"), func(_ *menu.CallbackData) {
		_ = app.ShowApp()
	})
	fileMenu.AddText("设置", keys.CmdOrCtrl(","), func(_ *menu.CallbackData) {
		_ = app.ShowApp()
		app.NavigateTo("/settings")
	})
	fileMenu.AddText("重启服务", nil, func(_ *menu.CallbackData) {
		_ = app.RestartProxy()
	})
	fileMenu.AddSeparator()
	fileMenu.AddText("退出", keys.CmdOrCtrl("Q"), func(_ *menu.CallbackData) {
		app.Quit()
	})

	// Edit menu: Wails v2 on macOS requires a native, role-based Edit
	// submenu for Cmd+C/Cmd+V/Cmd+X/Cmd+A/Cmd+Z to reach the webview's
	// text fields. menu.EditMenu() produces items routed through the
	// standard Cocoa selectors (copy:/paste:/cut:/undo:/redo:/
	// selectAll:) which propagate via the NSApp responder chain to the
	// WKWebView first responder, where they are handled natively.
	//
	// IMPORTANT: do NOT build this menu via AddText("Copy",
	// keys.CmdOrCtrl("c"), emptyCallback). AddText creates custom
	// NSMenuItems with @selector(handleClick) that INTERCEPT the
	// keystroke at the NSMenu level and route it to an empty Go
	// callback, preventing it from ever reaching the webview. The
	// role-based EditMenu() is the only correct way. Do not change.
	trayMenu.Append(menu.EditMenu())

	// System tray — conditionally initialized based on menu_bar_item setting.
	// When disabled, the user has no tray icon to restore a hidden window,
	// so resolveLaunchConfig also forces visible window + quit-on-close.
	var stopTray func()
	if cfg.enableTray {
		start, stop := tray.Run(tray.Handlers{
			ShowWindow: func() {
				_ = app.ShowApp()
			},
			OpenSettings: func() {
				_ = app.ShowApp()
				app.NavigateTo("/settings")
			},
			RestartProxy: func() {
				_ = app.RestartProxy()
			},
			Quit: func() {
				app.Quit()
			},
		})
		stopTray = stop
		start()
	}

	err := wails.Run(&options.App{
		Title:             "Autoapi",
		Width:             1280,
		Height:            800,
		MinWidth:          480,
		MinHeight:         400,
		StartHidden:       cfg.startHidden,
		HideWindowOnClose: cfg.hideOnClose,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "dev.local.autoapi",
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				if err := app.ShowApp(); err != nil {
					slog.Warn("launch: failed to show window for second instance", "error", err)
				}
			},
		},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 245, G: 245, B: 247, A: 1},
		OnStartup:        app.Startup,
		OnShutdown: func(ctx context.Context) {
			if stopTray != nil {
				stopTray()
			}
			app.Shutdown(ctx)
		},
		Bind: []interface{}{
			app,
		},
		Menu: trayMenu,
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

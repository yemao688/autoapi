package main

import (
	"context"
	"embed"

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
		_ = app.ShowWindow()
	})
	fileMenu.AddText("设置", keys.CmdOrCtrl(","), func(_ *menu.CallbackData) {
		_ = app.ShowWindow()
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

	// System tray (macOS menu bar). RunWithExternalLoop does not replace
	// the NSApp delegate, so it coexists safely with Wails. start() must
	// run BEFORE wails.Run (both need the locked main thread; start is
	// non-blocking). stop() runs in OnShutdown BEFORE app.Shutdown so
	// the tray teardown completes before the Wails event loop exits.
	startTray, stopTray := tray.Run(tray.Handlers{
		ShowWindow: func() {
			_ = app.ShowWindow()
		},
		OpenSettings: func() {
			_ = app.ShowWindow()
			app.NavigateTo("/settings")
		},
		RestartProxy: func() {
			_ = app.RestartProxy()
		},
		Quit: func() {
			app.Quit()
		},
	})
	startTray()

	err := wails.Run(&options.App{
		Title:     "Autoapi",
		Width:     1280,
		Height:    800,
		MinWidth:  760,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 245, G: 245, B: 247, A: 1},
		OnStartup:        app.Startup,
		OnShutdown: func(ctx context.Context) {
			stopTray()
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

package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var trayIcon []byte

func main() {
	app := NewApp()

	// Build the application menu (top-of-screen on macOS, window menu on
	// Windows/Linux). The Wails v2.12.0 release used here does not expose a
	// public system-tray API — the internal menumanager has an AddTrayMenu
	// entry point but it is unexported, and options.App has no TrayIcon
	// field. We therefore register the same menu structure as the
	// application menu, which gives the user a discoverable "Autoapi"
	// entry on every supported platform. The build/appicon.png is still
	// embedded above so that an upgrade to a Wails version with a public
	// tray API (or a future trayicons/ build hook) can pick it up without
	// a code change.
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

	// Edit menu: Wails v2 on macOS requires a native Edit submenu bound to
	// the standard edit accelerators for Cmd+C/Cmd+V/Cmd+X/Cmd+A/Cmd+Z to
	// reach the webview's text fields. The webview handles the actual edit
	// operations itself; the menu items only need to exist so the
	// accelerators route through the native menu chain. Do not remove.
	editMenu := trayMenu.AddSubmenu("Edit")
	editMenu.AddText("Undo", keys.CmdOrCtrl("z"), func(_ *menu.CallbackData) {})
	editMenu.AddText("Redo", keys.CmdOrCtrl("shift+z"), func(_ *menu.CallbackData) {})
	editMenu.AddSeparator()
	editMenu.AddText("Cut", keys.CmdOrCtrl("x"), func(_ *menu.CallbackData) {})
	editMenu.AddText("Copy", keys.CmdOrCtrl("c"), func(_ *menu.CallbackData) {})
	editMenu.AddText("Paste", keys.CmdOrCtrl("v"), func(_ *menu.CallbackData) {})
	editMenu.AddSeparator()
	editMenu.AddText("Select All", keys.CmdOrCtrl("a"), func(_ *menu.CallbackData) {})

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
		OnShutdown:       app.Shutdown,
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

	// Reference trayIcon so the embed directive is not flagged as unused.
	// See the comment above for why we are not yet wiring it into a tray
	// slot: the current Wails version does not expose a public one.
	_ = trayIcon
}

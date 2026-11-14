package main

import (
	"embed"
	"log/slog"
	"runtime"

	"autoapi/internal/api"
	"autoapi/internal/config"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

// trayIcon is a 44×44 monochrome template image: pure black (#000000) with
// anti-aliased alpha and a fully transparent background. macOS renders
// template icons as a single-colour silhouette and applies the appropriate
// foreground (dark on light menu bar, light on dark), so a colour PNG would
// appear as an opaque blob. Three connected nodes (equilateral triangle)
// represent routing requests across providers.
//
//go:embed build/trayicon.png
var trayIcon []byte

func main() {
	rt := api.NewRuntime()
	app := NewApp(rt)
	profile := config.Current()

	// Resolve static lifecycle options from persisted settings before the
	// application starts. These options are evaluated once by Wails at startup
	// and cannot be changed at runtime, so setting changes only take effect
	// after an app restart.
	settings, sErr := app.GetSettings()
	cfg := resolveLaunchConfig(settings)
	app.SetInitialVisibility(cfg.startHidden)
	if sErr != nil {
		slog.Warn("launch: failed to read settings, using safe defaults", "error", sErr)
	}

	// The application menu (top-of-screen on macOS, window menu on
	// Windows/Linux). The "Autoapi" submenu carries our lifecycle actions; the
	// standard Edit role submenu is required on macOS so Cmd+C/Cmd+V/Cmd+X/
	// Cmd+A/Cmd+Z reach the webview's text fields through the Cocoa responder
	// chain. Wails v3 emits the standard selectors (copy:, paste:, ...) for
	// role-based items, exactly like v2's menu.EditMenu().
	wailsApp := application.New(application.Options{
		Name:        "Autoapi",
		Description: "AI 模型路由桌面应用",
		Services: []application.Service{
			application.NewService(app),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			// With no tray icon there is no way to restore a hidden window, so
			// closing the last window must terminate. With the tray enabled the
			// window close is intercepted below and the app stays in the
			// background.
			ApplicationShouldTerminateAfterLastWindowClosed: !cfg.enableTray,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: singleInstanceLockID(profile),
			OnSecondInstanceLaunch: func(_ application.SecondInstanceData) {
				if err := app.ShowApp(); err != nil {
					slog.Warn("launch: failed to show window for second instance", "error", err)
				}
			},
		},
	})

	menuManager := wailsApp.Menu
	mainMenu := application.NewMenu()
	autoapiMenu := mainMenu.AddSubmenu("Autoapi")
	autoapiMenu.Add("显示主窗口").
		SetAccelerator("CmdOrCtrl+O").
		OnClick(func(_ *application.Context) { _ = app.ShowApp() })
	autoapiMenu.Add("设置").
		SetAccelerator("CmdOrCtrl+,").
		OnClick(func(_ *application.Context) {
			_ = app.ShowApp()
			app.NavigateTo("/settings")
		})
	autoapiMenu.Add("重启服务").
		OnClick(func(_ *application.Context) { _ = app.RestartProxy() })
	autoapiMenu.Add("重载界面").
		OnClick(func(_ *application.Context) { app.ReloadWebView() })
	autoapiMenu.AddSeparator()
	autoapiMenu.Add("退出").
		SetAccelerator("CmdOrCtrl+Q").
		OnClick(func(_ *application.Context) { app.Quit() })
	mainMenu.AddRole(application.EditMenu)
	menuManager.SetApplicationMenu(mainMenu)

	// Main window. Hidden is resolved from the persisted startup action; the
	// frontend restores its own route and state when it loads.
	mainWindow := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             "main",
		Title:            "Autoapi",
		Width:            1280,
		Height:           800,
		MinWidth:         480,
		MinHeight:        400,
		Hidden:           cfg.startHidden,
		BackgroundColour: application.NewRGB(245, 245, 247),
		URL:              "/",
		// Windows/Linux render the application menu inside the window; macOS
		// always uses the global menu bar.
		UseApplicationMenu: true,
		Mac: application.MacWindow{
			TitleBar: application.MacTitleBarHiddenInset,
		},
	})

	rt.Attach(wailsApp, mainWindow)

	// Close interception is the sole hide-on-close mechanism when tray mode is
	// enabled. An explicit Quit (menu / tray) sets the quitting flag first and
	// lets the window close for real.
	mainWindow.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		if app.IsQuitting() {
			return
		}
		if !cfg.enableTray {
			return
		}
		if err := app.EnterBackground(); err != nil {
			slog.Warn("lifecycle: failed to enter background", "error", err)
		}
		e.Cancel()
	})

	// System tray — conditionally created based on the menu_bar_item setting.
	// When disabled, the user has no tray icon to restore a hidden window, so
	// resolveLaunchConfig also forces a visible window + quit-on-close.
	if cfg.enableTray {
		trayMenu := application.NewMenu()
		trayMenu.Add("显示主窗口").OnClick(func(_ *application.Context) { _ = app.ShowApp() })
		trayMenu.Add("设置").OnClick(func(_ *application.Context) {
			_ = app.ShowApp()
			app.NavigateTo("/settings")
		})
		trayMenu.AddSeparator()
		trayMenu.Add("重启服务").OnClick(func(_ *application.Context) { _ = app.RestartProxy() })
		trayMenu.Add("重载界面").OnClick(func(_ *application.Context) { app.ReloadWebView() })
		trayMenu.AddSeparator()
		trayMenu.Add("退出").OnClick(func(_ *application.Context) { app.Quit() })

		tray := wailsApp.SystemTray.New()
		tray.SetTooltip("Autoapi")
		if runtime.GOOS == "darwin" {
			tray.SetTemplateIcon(trayIcon)
		} else {
			tray.SetIcon(trayIcon)
		}
		tray.SetMenu(trayMenu)
	}

	if err := wailsApp.Run(); err != nil {
		slog.Error("application exited with error", "error", err)
	}
}

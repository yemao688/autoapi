// Package tray provides a thin wrapper around fyne.io/systray for the
// macOS menu-bar tray icon. It owns the icon embed and constructs the
// menu items; main.go wires the click handlers to App methods via the
// Handlers struct so this package has no dependency on internal/api.
package tray

import (
	_ "embed"
	"log/slog"

	"fyne.io/systray"
)

//go:embed build/trayicon.png
// icon is a 44×44 monochrome template image: pure black (#000000) with
// anti-aliased alpha and a fully transparent background. macOS renders
// SetTemplateIcon images as a single-colour silhouette and applies the
// appropriate foreground (dark on light menu bar, light on dark), so a
// colour PNG would appear as an opaque blob. Three connected nodes
// (equilateral triangle) represent routing requests across providers.
var icon []byte

// Handlers groups the menu actions. main.go wires these to App methods
// so this package has no dependency on internal/api. nil fields are
// silently ignored if the corresponding menu item is clicked.
type Handlers struct {
	ShowWindow   func()
	OpenSettings func()
	RestartProxy func()
	Quit         func()
}

// Run builds the tray menu and returns start/stop functions. Call
// start() before wails.Run, stop() in OnShutdown.
//
// RunWithExternalLoop is the only safe integration mode with Wails:
// the regular Run() calls setDelegate: on NSApp, which would conflict
// with Wails' own delegate. The External variant initialises the
// NSStatusItem and fires onReady synchronously inside nativeStart,
// then returns. It does NOT replace the NSApp delegate.
//
// Threading: nativeStart runs on the main goroutine (which the systray
// init() has locked via runtime.LockOSThread). wails.Run also runs on
// the locked main thread; start() must therefore be called BEFORE
// wails.Run, but is non-blocking and yields immediately after firing
// onReady.
func Run(handlers Handlers) (start func(), stop func()) {
	return systray.RunWithExternalLoop(
		func() {
			// Empty title: the icon IS the visual. macOS menu bar
			// items are conventionally icon-only.
			systray.SetTitle("")
			systray.SetTooltip("Autoapi")
			// Pass the same bytes for the @1x and @2x variants. macOS
			// will use the @2x variant on Retina displays. The icon
			// is a monochrome template (see the embed above) so the
			// system applies the correct foreground colour.
			systray.SetTemplateIcon(icon, icon)

			showItem := systray.AddMenuItem("显示主窗口", "Bring the main window to the front")
			settingsItem := systray.AddMenuItem("设置", "Open the settings page")
			systray.AddSeparator()
			restartItem := systray.AddMenuItem("重启服务", "Rebind the local HTTP proxy")
			systray.AddSeparator()
			quitItem := systray.AddMenuItem("退出", "Quit Autoapi")

			slog.Info("tray: started")

			// Each MenuItem has a ClickedCh that is an unbuffered
			// channel: every item MUST have a reader goroutine,
			// otherwise the CGo callback thread blocks on the
			// unbuffered send. We block on the channel for the entire
			// lifetime of the tray; if any goroutine exits the CGo
			// thread will eventually wedge.
			go func() {
				for range showItem.ClickedCh {
					slog.Info("tray: Show Window clicked")
					if handlers.ShowWindow != nil {
						handlers.ShowWindow()
					}
				}
			}()
			go func() {
				for range settingsItem.ClickedCh {
					slog.Info("tray: Settings clicked")
					if handlers.OpenSettings != nil {
						handlers.OpenSettings()
					}
				}
			}()
			go func() {
				for range restartItem.ClickedCh {
					slog.Info("tray: Restart Proxy clicked")
					if handlers.RestartProxy != nil {
						handlers.RestartProxy()
					}
				}
			}()
			go func() {
				for range quitItem.ClickedCh {
					slog.Info("tray: Quit clicked")
					if handlers.Quit != nil {
						handlers.Quit()
					}
				}
			}()
		},
		func() {
			slog.Info("tray: systray onExit")
		},
	)
}


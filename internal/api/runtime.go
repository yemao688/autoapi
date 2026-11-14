package api

import "github.com/wailsapp/wails/v3/pkg/application"

// Runtime adapts App to the Wails v3 desktop runtime. The composition root
// creates it before application.New, injects it through Deps, and fills it in
// with Attach once the application and main window exist (before app.Run).
//
// Runtime is deliberately NOT registered as a Wails service: only App is
// bound, so none of these handles leak into the generated frontend bindings.
type Runtime struct {
	app    *application.App
	window *application.WebviewWindow
}

// NewRuntime returns an empty runtime handle.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// Attach binds the Wails application and its main window.
func (r *Runtime) Attach(app *application.App, window *application.WebviewWindow) {
	if r == nil {
		return
	}
	r.app = app
	r.window = window
}

// ready reports whether both handles have been attached. Calls from unit
// tests (no Wails runtime) are no-ops rather than panics.
func (r *Runtime) ready() bool {
	return r != nil && r.app != nil && r.window != nil
}

func (r *Runtime) emit(name string, data ...any) {
	if r == nil || r.app == nil {
		return
	}
	r.app.Event.Emit(name, data...)
}

func (r *Runtime) hideWindow() {
	if r.ready() {
		r.window.Hide()
	}
}

func (r *Runtime) showWindow() {
	if r.ready() {
		r.window.Show()
	}
}

func (r *Runtime) unminimiseWindow() {
	if r.ready() {
		r.window.UnMinimise()
	}
}

func (r *Runtime) hideApp() {
	if r != nil && r.app != nil {
		r.app.Hide()
	}
}

func (r *Runtime) showApp() {
	if r != nil && r.app != nil {
		r.app.Show()
	}
}

func (r *Runtime) quit() {
	if r != nil && r.app != nil {
		r.app.Quit()
	}
}

// Package main wires the application dependencies together and runs the Wails
// runtime. The actual business logic lives in internal/{api,model,store,
// service,proxy}; this file is just composition root.
package main

import (
	"autoapi/internal/api"
)

// NewApp constructs the bound App with its dependencies. During Phase 1a the
// store/service/proxy are nil so every method returns ErrNotImplemented; the
// frontend can still compile against the generated typed bindings. Phases 1b–d
// will inject real implementations here.
func NewApp() *api.App {
	return api.NewApp(api.Deps{
		// Store:   store.New(...),    // Phase 1b
		// Service: service.New(...),  // Phase 1c
		// Proxy:   proxy.New(...),    // Phase 4
	})
}

// Package main wires the application dependencies together and runs the Wails
// runtime. The actual business logic lives in internal/{api,model,store,
// service,proxy}; this file is just composition root.
package main

import (
	"context"
	"log"

	"autoapi/internal/api"
	"autoapi/internal/service"
	"autoapi/internal/store"
)

// NewApp constructs the bound App with real store and service dependencies.
// The proxy is left nil for Phase 4 (api.App handles nil gracefully).
func NewApp() *api.App {
	ctx := context.Background()

	st, err := store.New(ctx, store.StoreDeps{})
	if err != nil {
		log.Fatalf("FATAL: store initialization failed: %v", err)
	}
	sv := service.New(st)

	return api.NewApp(api.Deps{
		Store:   st,
		Service: sv,
		Proxy:   nil, // Phase 4
	})
}

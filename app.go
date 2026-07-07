// Package main wires the application dependencies together and runs the Wails
// runtime. The actual business logic lives in internal/{api,model,store,
// service,proxy}; this file is just composition root.
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"autoapi/internal/api"
	"autoapi/internal/model"
	"autoapi/internal/proxy"
	"autoapi/internal/service"
	"autoapi/internal/store"
)

// NewApp constructs the bound App with real store, service, and proxy dependencies.
func NewApp() *api.App {
	ctx := context.Background()

	st, err := store.New(ctx, store.StoreDeps{})
	if err != nil {
		log.Fatalf("FATAL: store initialization failed: %v", err)
	}

	prx := proxy.New(st, nil, func() *model.Settings {
		s, _ := st.GetSettings()
		return s
	})

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("FATAL: cannot determine home directory: %v", err)
	}
	keyDir := filepath.Join(home, ".autoapi")
	sv := service.New(st, prx, keyDir)

	// proxy.New needs the service for resolving provider keys; re-create with service now available.
	prx = proxy.New(st, sv, func() *model.Settings {
		s, _ := st.GetSettings()
		return s
	})
	sv.SetProxy(prx)

	return api.NewApp(api.Deps{
		Store:   st,
		Service: sv,
		Proxy:   prx,
	})
}

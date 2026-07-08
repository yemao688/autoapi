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
	"autoapi/internal/logger"
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

	// Initialise the persistent application logger as early as possible
	// so that any startup error from the proxy / service / API layer is
	// captured to disk for post-mortem. The log file lives next to the
	// SQLite database so a single "show in Finder" reveals both. The
	// log section is read from the stored settings (or the defaults
	// baked into the store) so user changes survive a restart.
	storageDir := st.StorageDir()
	logPath := filepath.Join(storageDir, "logs", "autoapi.log")
	{
		loggingCfg := model.LoggingSettings{
			Enabled:    true,
			Level:      "info",
			MaxSizeMB:  10,
			MaxAgeDays: 7,
			MaxBackups: 3,
		}
		if s, sErr := st.GetSettings(); sErr == nil && s != nil {
			loggingCfg = s.Logging
		}
		// Stderr logs the failure; the user still sees it in the Wails
		// console even when the rotating file cannot be created.
		if err := logger.Init(logger.Config{
			Enabled:    loggingCfg.Enabled,
			Level:      loggingCfg.Level,
			MaxSizeMB:  loggingCfg.MaxSizeMB,
			MaxAgeDays: loggingCfg.MaxAgeDays,
			MaxBackups: loggingCfg.MaxBackups,
			Path:       logPath,
		}); err != nil {
			log.Printf("warning: logger.Init failed (%v); continuing with stderr-only", err)
		}
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

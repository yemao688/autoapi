// Package main wires the application dependencies together and runs the Wails
// runtime. The actual business logic lives in internal/{api,model,store,
// service,proxy}; this file is just composition root.
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"autoapi/internal/api"
	"autoapi/internal/config"
	"autoapi/internal/logger"
	"autoapi/internal/metrics"
	"autoapi/internal/model"
	"autoapi/internal/proxy"
	"autoapi/internal/service"
	"autoapi/internal/store"
)

// NewApp constructs the bound App with real store, service, and proxy dependencies.
func NewApp() *api.App {
	ctx := context.Background()
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("FATAL: cannot determine home directory: %v", err)
	}
	profile := config.Current()
	metricRegistry := metrics.New(metrics.DefaultCapacity, metrics.DefaultTTL)
	storageDir := filepath.Join(home, profile.StorageDirName)
	if err := os.MkdirAll(storageDir, 0700); err != nil {
		log.Fatalf("FATAL: cannot create storage directory: %v", err)
	}

	st, err := store.New(ctx, store.StoreDeps{
		DSN:          filepath.Join(storageDir, "autoapi.db"),
		DefaultPort:  profile.DefaultPort,
		SeedFixtures: profile.SeedFixtures,
	})
	if err != nil {
		log.Fatalf("FATAL: store initialization failed: %v", err)
	}
	cutoff := time.Now().UTC()
	if cleanupErr := st.CleanupTargetRuntimeSummaries(cutoff, metrics.DefaultTTL); cleanupErr != nil {
		log.Printf("warning: metrics cleanup failed: %v", cleanupErr)
	}
	if saved, loadErr := st.LoadActiveTargetRuntimeSummaries(cutoff, metrics.DefaultTTL); loadErr == nil {
		metricRegistry.Restore(saved, cutoff)
	} else {
		log.Printf("warning: metrics restore failed: %v", loadErr)
	}
	checkpoint := metrics.NewCheckpoint(metricRegistry, st, time.Minute)
	checkpoint.Start()

	// Initialise the persistent application logger as early as possible
	// so that any startup error from the proxy / service / API layer is
	// captured to disk for post-mortem. The log file lives next to the
	// SQLite database so a single "show in Finder" reveals both. The
	// log section is read from the stored settings (or the defaults
	// baked into the store) so user changes survive a restart.
	storageDir = st.StorageDir()
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

	prx := proxy.New(st, nil, profile.DefaultPort, func() (*model.Settings, error) {
		return st.GetSettings()
	}, metricRegistry)

	sv := service.New(st, prx, st.StorageDir())

	// proxy.New needs the service for resolving provider keys; re-create with service now available.
	prx = proxy.New(st, sv, profile.DefaultPort, func() (*model.Settings, error) {
		return st.GetSettings()
	}, metricRegistry)
	sv.SetProxy(prx)

	app := api.NewApp(api.Deps{
		Store:      st,
		Service:    sv,
		Proxy:      prx,
		Checkpoint: checkpoint,
		Metrics:    metricRegistry,
	})
	app.SetAppInfo(getAppInfo())
	return app
}

package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"autoapi/internal/model"
)

// GetSettings reads all setting sections from the DB, merging defaults for
// any missing keys.
func (s *Store) GetSettings() (*model.Settings, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("store: get settings: %w", err)
	}
	defer rows.Close()

	raw := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("store: scan settings: %w", err)
		}
		raw[k] = json.RawMessage(v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	settings := defaultSettings()
	settings.Data.StoragePath = s.dsnPath
	for section, data := range raw {
		switch section {
		case "general":
			var s model.GeneralSettings
			if json.Unmarshal(data, &s) == nil {
				settings.General = s
			}
		case "appearance":
			var s model.AppearanceSettings
			if json.Unmarshal(data, &s) == nil {
				settings.Appearance = s
			}
		case "routing":
			var s model.RoutingSettings
			if json.Unmarshal(data, &s) == nil {
				settings.Routing = s
			}
		case "server":
			var s model.ServerSettings
			if json.Unmarshal(data, &s) == nil {
				settings.Server = s
			}
		case "data":
			var s model.DataSettings
			if json.Unmarshal(data, &s) == nil {
				settings.Data = s
			}
		case "advanced":
			var s model.AdvancedSettings
			if json.Unmarshal(data, &s) == nil {
				settings.Advanced = s
			}
		case "logging":
			var s model.LoggingSettings
			if json.Unmarshal(data, &s) == nil {
				settings.Logging = s
			}
		}
	}

	return &settings, nil
}

// SaveSettings writes each non-zero section as a JSON blob in the settings
// table, using UPSERT (INSERT OR REPLACE).
func (s *Store) SaveSettings(settings model.Settings) error {
	sections := map[string]interface{}{
		"general":    settings.General,
		"appearance": settings.Appearance,
		"routing":    settings.Routing,
		"server":     settings.Server,
		"data":       settings.Data,
		"advanced":   settings.Advanced,
		"logging":    settings.Logging,
	}

	err := s.execTx(func(tx *sql.Tx) error {
		for key, val := range sections {
			data, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("store: marshal settings %q: %w", key, err)
			}
			if _, err := tx.Exec(`
				INSERT INTO settings (key, value) VALUES (?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
				key, string(data)); err != nil {
				return fmt.Errorf("store: write settings %q: %w", key, err)
			}
		}
		return nil
	})
	if err == nil {
		slog.Info("store: settings saved")
	}
	return err
}

// ListEndpoints returns the static list of endpoints served by the proxy.
func (s *Store) ListEndpoints() ([]model.Endpoint, error) {
	return []model.Endpoint{
		{Method: "POST", Path: "/v1/chat/completions", Desc: "Chat completions (OpenAI-compatible)"},
		{Method: "POST", Path: "/v1/embeddings", Desc: "Text embeddings"},
		{Method: "POST", Path: "/v1/images/generations", Desc: "Image generations"},
		{Method: "POST", Path: "/v1/audio/transcriptions", Desc: "Audio transcriptions"},
		{Method: "POST", Path: "/v1/files", Desc: "File uploads"},
		{Method: "GET", Path: "/v1/models", Desc: "List models from providers"},
		{Method: "GET", Path: "/v1/stats/tokens", Desc: "Token usage statistics"},
	}, nil
}

// ---------------------------------------------------------------------------
//  Defaults
// ---------------------------------------------------------------------------

func defaultSettings() model.Settings {
	return model.Settings{
		General: model.GeneralSettings{
			LaunchAtLogin: false,
			StartupAction: "show",
			MenuBarItem:   true,
			CloseAction:   "background",
		},
		Appearance: model.AppearanceSettings{
			Theme:       "system",
			Density:     "standard",
			AccentColor: "#0071e3",
		},
		Routing: model.RoutingSettings{
			DefaultProviderID: "",
			DefaultModel:      "",
			AutoRetry:         false,
			StreamingSSE:      true,
		},
		Server: model.ServerSettings{
			Port:        8344,
			BindAddress: "0.0.0.0",
		},
		Data: model.DataSettings{
			LogRetentionDays: 90,
			StoragePath:      "", // filled at runtime
		},
		Advanced: model.AdvancedSettings{
			DebugMode:    false,
			Experimental: false,
			HTTPProxy:    "system",
		},
		Logging: model.LoggingSettings{
			Enabled:    true,
			Level:      "info",
			MaxSizeMB:  10,
			MaxAgeDays: 7,
			MaxBackups: 3,
		},
	}
}

package store

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"autoapi/internal/model"
)

// Export returns serialized data in the requested format and a suggested
// filename. The caller (api.App) passes the bytes to the UI for download.
func (s *Store) Export(format model.ExportFormat) ([]byte, string, error) {
	switch format {
	case model.ExportAllJSON:
		return s.exportAllJSON()
	case model.ExportSettingsJSON:
		return s.exportSettingsJSON()
	case model.ExportTokensCSV:
		return s.exportTokensCSV()
	case model.ExportLogsCSV:
		return s.exportLogsCSV()
	default:
		return nil, "", fmt.Errorf("store: unknown export format %q", format)
	}
}

func (s *Store) exportAllJSON() ([]byte, string, error) {
	providers, _ := s.ListProviders()
	models, _ := s.ListModels("")
	keys, _ := s.ListAPIKeys()
	routes, _ := s.ListRoutes()
	settings, _ := s.GetSettings()

	// Request logs (last 10000)
	logs, _, _ := s.QueryLogs(model.LogQuery{Page: 1, PageSize: 10000})

	payload := map[string]interface{}{
		"providers":   providers,
		"models":      models,
		"api_keys":    keys,
		"routes":      routes,
		"settings":    settings,
		"request_logs": logs,
		"exported_at": time.Now().UnixMilli(),
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("store: export all json: %w", err)
	}
	return data, "autoapi-export.json", nil
}

func (s *Store) exportSettingsJSON() ([]byte, string, error) {
	settings, err := s.GetSettings()
	if err != nil {
		return nil, "", err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, "", fmt.Errorf("store: export settings: %w", err)
	}
	return data, "autoapi-settings.json", nil
}

func (s *Store) exportTokensCSV() ([]byte, string, error) {
	cutoff := time.Now().AddDate(0, 0, -30).UnixMilli()
	rows, err := s.db.Query(`
		SELECT date(timestamp_ms / 1000, 'unixepoch') AS day,
		       provider_name, model,
		       SUM(input_tokens), SUM(output_tokens),
		       COUNT(*)
		FROM request_logs
		WHERE timestamp_ms >= ?
		GROUP BY day, provider_name, model
		ORDER BY day ASC`, cutoff)
	if err != nil {
		return nil, "", fmt.Errorf("store: export tokens csv: %w", err)
	}
	defer rows.Close()

	var buf strings.Builder
	w := csv.NewWriter(&buf)
	w.Write([]string{"date", "provider", "model", "input_tokens", "output_tokens", "requests"})

	for rows.Next() {
		var date, provider, modelName string
		var in, out, reqs int64
		if err := rows.Scan(&date, &provider, &modelName, &in, &out, &reqs); err != nil {
			continue
		}
		w.Write([]string{date, provider, modelName,
			fmt.Sprintf("%d", in), fmt.Sprintf("%d", out), fmt.Sprintf("%d", reqs)})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, "", err
	}
	return []byte(buf.String()), "autoapi-tokens.csv", nil
}

func (s *Store) exportLogsCSV() ([]byte, string, error) {
	cutoff := time.Now().AddDate(0, 0, -30).UnixMilli()
	rows, err := s.db.Query(`
		SELECT timestamp_ms, status_code, provider_name, model,
		       input_tokens, output_tokens, latency_ms, route_label,
		       COALESCE(error, '')
		FROM request_logs
		WHERE timestamp_ms >= ?
		ORDER BY timestamp_ms DESC
		LIMIT 10000`, cutoff)
	if err != nil {
		return nil, "", fmt.Errorf("store: export logs csv: %w", err)
	}
	defer rows.Close()

	var buf strings.Builder
	w := csv.NewWriter(&buf)
	w.Write([]string{"timestamp", "status", "provider", "model",
		"input_tokens", "output_tokens", "latency_ms", "route", "error"})

	for rows.Next() {
		var ts int64
		var status int
		var provider, modelName, route, errStr string
		var in, out, lat int
		if err := rows.Scan(&ts, &status, &provider, &modelName, &in, &out, &lat, &route, &errStr); err != nil {
			continue
		}
		t := time.UnixMilli(ts).Format(time.RFC3339)
		w.Write([]string{t, fmt.Sprintf("%d", status), provider, modelName,
			fmt.Sprintf("%d", in), fmt.Sprintf("%d", out), fmt.Sprintf("%d", lat), route, errStr})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, "", err
	}
	return []byte(buf.String()), "autoapi-logs.csv", nil
}

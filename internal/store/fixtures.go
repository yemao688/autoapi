//go:build !production

package store

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

func init() {
	initDev = func(s *Store) {
		seedIfEmpty(s.db)
	}
}

// seedIfEmpty seeds the canonical dataset from the prototype when all core
// tables are empty. Only compiled in dev builds (go:build !production).
func seedIfEmpty(db *sql.DB) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&count); err != nil || count > 0 {
		return
	}
	log.Println("[store] seeding dev fixtures...")

	now := time.Now().UnixMilli()

	// -----------------------------------------------------------------------
	// Providers (5)
	// -----------------------------------------------------------------------
	type providerSeed struct {
		id, name, baseURL, status string
		keyCiphertext, keyNonce   []byte
		keyMasked                 string
		modelsCount               int
		monthlyTokens             int64
		avgLatencyMs              int
		isCustom                  bool
	}
	providers := []providerSeed{
		{"p01", "OpenAI", "https://api.openai.com", "connected", []byte("enc-openai-p01"), []byte("nonce-p01"), "sk-proj-****3fA9", 4, 3420000, 320, false},
		{"p02", "Anthropic", "https://api.anthropic.com", "connected", []byte("enc-anthropic-p02"), []byte("nonce-p02"), "sk-ant-****Bc12", 3, 890000, 480, false},
		{"p03", "DeepSeek", "https://api.deepseek.com", "connected", []byte("enc-deepseek-p03"), []byte("nonce-p03"), "sk-deep-****9xY7", 2, 2100000, 210, false},
		{"p04", "Moonshot", "https://api.moonshot.cn", "error", []byte("enc-moonshot-p04"), []byte("nonce-p04"), "ms-****KpL2", 1, 450000, 680, false},
		{"p05", "GLM", "https://open.bigmodel.cn/api/paas/v4", "unknown", []byte{}, []byte{}, "", 0, 0, 0, false},
	}
	for _, p := range providers {
		status := p.status
		if status == "" {
			status = "unknown"
		}
		db.Exec(`INSERT INTO providers (id, name, base_url, status,
			key_ciphertext, key_nonce, key_masked,
			models_count, monthly_tokens, avg_latency_ms, last_tested_at, error_message, is_custom, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,0,'',?,?,?)`,
			p.id, p.name, p.baseURL, status,
			p.keyCiphertext, p.keyNonce, p.keyMasked,
			p.modelsCount, p.monthlyTokens, p.avgLatencyMs, boolInt(p.isCustom), now, now)
	}

	// -----------------------------------------------------------------------
	// Models per provider
	// -----------------------------------------------------------------------
	type modelSeed struct {
		id, providerID, name string
		ctxWin               int
	}
	models := []modelSeed{
		{"m01", "p01", "gpt-4o", 128000},
		{"m02", "p01", "gpt-4o-mini", 128000},
		{"m03", "p01", "gpt-4-turbo", 128000},
		{"m04", "p01", "gpt-3.5-turbo", 16384},
		{"m05", "p02", "claude-3.5-sonnet", 200000},
		{"m06", "p02", "claude-3-opus", 200000},
		{"m07", "p02", "claude-3-haiku", 200000},
		{"m08", "p03", "deepseek-chat", 65536},
		{"m09", "p03", "deepseek-reasoner", 65536},
		{"m10", "p04", "moonshot-v1", 65536},
	}
	for _, m := range models {
		db.Exec(`INSERT INTO models (id, provider_id, name, context_window, created_at) VALUES (?,?,?,?,?)`,
			m.id, m.providerID, m.name, m.ctxWin, now)
	}

	// -----------------------------------------------------------------------
	// API keys (access tokens) (7)
	// -----------------------------------------------------------------------
	type keySeed struct {
		id, name string
	}
	keys := []keySeed{
		{"k01", "OpenAI Production Token"},
		{"k02", "Anthropic Main Token"},
		{"k03", "DeepSeek API Token"},
		{"k04", "Moonshot Token"},
		{"k05", "Custom Gateway Token"},
		{"k06", "OpenAI Read-only Token"},
		{"k07", "Backup Admin Token"},
	}
	for _, k := range keys {
		db.Exec(`INSERT INTO api_keys (id, name, expires_at, created_at, updated_at)
			VALUES (?,?,0,?,?)`,
			k.id, k.name, now, now)
	}

	// -----------------------------------------------------------------------
	// Routes (4) with conditions and targets
	// -----------------------------------------------------------------------
	type condSeed struct {
		field, operator, value string
	}
	type targSeed struct {
		providerID, modelName string
	}
	type routeSeed struct {
		id, name, desc string
		priority       int
		enabled        bool
		conds          []condSeed
		targets        []targSeed
	}
	routes := []routeSeed{
		{
			id: "r01", name: "High-priority tasks → GPT-4o", desc: "Route complex reasoning tasks to OpenAI GPT-4o for highest quality",
			priority: 1, enabled: true,
			conds: []condSeed{
				{field: "task", operator: "matches", value: "reasoning,code,analysis"},
				{field: "estimated_tokens", operator: "gt", value: "2000"},
			},
			targets: []targSeed{
				{providerID: "p01", modelName: "gpt-4o"},
			},
		},
		{
			id: "r02", name: "Creative writing → Claude", desc: "Route creative and writing tasks to Anthropic Claude",
			priority: 2, enabled: true,
			conds: []condSeed{
				{field: "task", operator: "matches", value: "writing,creative,translation"},
			},
			targets: []targSeed{
				{providerID: "p02", modelName: "claude-3.5-sonnet"},
			},
		},
		{
			id: "r03", name: "Cost-sensitive → DeepSeek", desc: "Route high-volume/cost-sensitive queries to DeepSeek",
			priority: 3, enabled: true,
			conds: []condSeed{
				{field: "header.x-priority", operator: "equals", value: "low"},
				{field: "estimated_tokens", operator: "lt", value: "500"},
			},
			targets: []targSeed{
				{providerID: "p03", modelName: "deepseek-chat"},
			},
		},
		{
			id: "r04", name: "Chinese content → Moonshot", desc: "Route Chinese-language queries to Moonshot for better accuracy",
			priority: 4, enabled: true,
			conds: []condSeed{
				{field: "model", operator: "matches", value: "moonshot-*"},
			},
			targets: []targSeed{
				{providerID: "p04", modelName: "moonshot-v1"},
			},
		},
	}
	for _, r := range routes {
		db.Exec(`INSERT INTO routes (id, name, description, priority, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
			r.id, r.name, r.desc, r.priority, boolInt(r.enabled), now, now)
		for i, c := range r.conds {
			cid := fmt.Sprintf("%s-c%d", r.id, i+1)
			db.Exec(`INSERT INTO route_conditions (id, route_id, field, operator, value) VALUES (?,?,?,?,?)`,
				cid, r.id, c.field, c.operator, c.value)
		}
		for i, t := range r.targets {
			tid := fmt.Sprintf("%s-t%d", r.id, i+1)
			db.Exec(`INSERT INTO route_targets (id, route_id, provider_id, model_name, tier) VALUES (?,?,?,?,?)`,
				tid, r.id, t.providerID, t.modelName, i)
		}
	}

	// -----------------------------------------------------------------------
	// Synthetic request_logs — ~30 days of realistic activity
	// -----------------------------------------------------------------------
	providerNames := map[string]string{
		"p01": "OpenAI", "p02": "Anthropic", "p03": "DeepSeek",
		"p04": "Moonshot", "p05": "GLM",
	}
	modelsByProvider := map[string][]string{
		"p01": {"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
		"p02": {"claude-3.5-sonnet", "claude-3-haiku"},
		"p03": {"deepseek-chat", "deepseek-reasoner"},
		"p04": {"moonshot-v1"},
	}
	routeLabels := map[string]string{
		"r01": "High-priority → GPT-4o",
		"r02": "Creative → Claude",
		"r03": "Cost-sensitive → DeepSeek",
		"r04": "Chinese → Moonshot",
	}

	// Daily patterns: more activity on weekdays, less on weekends
	logID := 1
	for day := 29; day >= 0; day-- {
		date := time.Now().AddDate(0, 0, -day)
		isWeekend := date.Weekday() == time.Saturday || date.Weekday() == time.Sunday
		baseCount := 60
		if isWeekend {
			baseCount = 20
		}

		for i := 0; i < baseCount; i++ {
			// Pick provider weighted towards OpenAI (40%), DeepSeek (30%), Anthropic (20%)
			var pid string
			r := float64(i%100) / 100.0
			switch {
			case r < 0.40:
				pid = "p01"
			case r < 0.70:
				pid = "p03"
			case r < 0.90:
				pid = "p02"
			default:
				pid = "p04"
			}

			models := modelsByProvider[pid]
			modelName := models[i%len(models)]

			hour := (i * 2) % 24
			ts := time.Date(date.Year(), date.Month(), date.Day(), hour, i%60, 0, 0, time.UTC).UnixMilli()

			inputTokens := int64(200 + (i*37)%1800)
			outputTokens := int64(50 + (i*13)%600)
			latencyMs := 200 + (i*7)%800

			status := 200
			errStr := ""
			if i%20 == 0 {
				status = 429
				errStr = "rate limit exceeded"
				latencyMs = 0
			} else if i%25 == 0 {
				status = 500
				errStr = "upstream error"
			}

			rid := ""
			rlabel := ""
			if i%5 != 0 {
				rnames := []string{"r01", "r02", "r03", "r04"}
				rid = rnames[i%4]
				rlabel = routeLabels[rid]
			}

			idStr := fmt.Sprintf("log-%05d", logID)
			db.Exec(`INSERT INTO request_logs (id, timestamp_ms, status_code, provider_id, provider_name, model,
				input_tokens, output_tokens, latency_ms, route_id, route_label, api_key_id, error)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				idStr, ts, status, pid, providerNames[pid], modelName,
				inputTokens, outputTokens, latencyMs, rid, rlabel, "", errStr)
			logID++
		}
	}

	log.Printf("[store] seeded %d request logs across 30 days\n", logID-1)
}

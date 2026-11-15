# AGENTS.md — Autoapi

Compact ramp-up guide for OpenCode sessions working in this repo.

## Project type & entrypoints

- **Wails v3 desktop app** (`v3.1.0`, Go 1.27.4): Go backend + Vue 3 + TypeScript frontend.
- Product name: `Autoapi`, version in `build/config.yml` (currently 0.7.0).
- Backend entry: `main.go` (`application.New` / `app.Run`, native menu + tray wiring) and `app.go` (dependency wiring).
- Frontend entry: `frontend/src/main.ts` → `App.vue` → `router.ts`.
- Local OpenAI-compatible proxy: `internal/proxy/proxy.go`, listens on `0.0.0.0:8344` by default. The UI never fetches 8344 directly; metrics/logs flow through bound `App` methods in `internal/api/app.go`.

## Development workflow

```bash
# Install Wails CLI (one-time)
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.1.0

# Install frontend deps
cd frontend && npm install

# Dev mode with hot reload (wails3 dev drives build/config.yml dev_mode)
wails3 dev
#   Vite server: http://localhost:9245 (WAILS_VITE_PORT)

# Production build (current platform -> build/bin/autoapi)
wails3 build

# macOS .app bundle -> build/bin/autoapi.app
wails3 package

# Cross-compilation (Windows is CGO-free; Linux needs CGO/WebKitGTK)
GOOS=windows wails3 task build
GOOS=linux wails3 task build
```

All build steps live in the root `Taskfile.yml` (go-task is embedded in the
`wails3` CLI). `make build` wraps `wails3 task build` and injects
`main.appVersion` / `main.appBuild` from `build/config.yml` + git.

## Verification commands

```bash
# Backend
go build ./...
go test ./internal/...
go vet ./...

# Frontend typecheck + build
cd frontend && npm run build

# Full app build (also regenerates bindings and frontend dist)
wails3 build

# Verify both tag paths: production and dev
go build -tags production ./...
go build -tags dev ./...
```

## Codegen & generated files

- **`frontend/bindings/`** contains the TypeScript bindings generated from the bound Go service (`api.App`). After changing `internal/model/model.go` or `internal/api/app.go` exported methods, run:
  ```bash
  wails3 generate bindings -ts -clean
  ```
  `wails3 build` runs this automatically (see `generate:bindings` in `Taskfile.yml`). Commit the regenerated files.
- Do **not** pass `-i` to the generator: the class-based models are required by the Vue code's `new model.Foo({...})` style.
- **`frontend/dist/`** is the built frontend. Wails embeds it (`//go:embed all:frontend/dist`); do not commit. It must exist before `go build`, because of the embed.
- Frontend uses path alias `@/` → `frontend/src/`. The binding barrel `frontend/src/api/models.ts` re-exports the generated per-package models under the old `model` / `api` / `proxy` / `service` / `toolconfig` namespace names.
- Service functions are imported directly from `frontend/bindings/autoapi/internal/api/app`; the runtime (`Events`, `Browser`, `Call`) comes from the `@wailsio/runtime` npm package. There is no `window.go` / `window.runtime` anymore.

## Architecture notes

- **Composition root**: `app.go` wires `store` → `proxy` → `service` → `api.App`.
- **Proxy** is created twice: once without `service` to break the dependency cycle, then with `service` for key resolution.
- **Store** (`internal/store`) is SQLite via `modernc.org/sqlite`. All writes go through a single `Writer` goroutine; reads hit `*sql.DB` directly under WAL mode.
- **Migrations**: append new entries to `internal/store/migrations.go`; never modify existing ones. Use `SkipIfRedundant` predicates when a schema change may have been applied under an older ID.
- **Model rules**: the app recently switched from "route rules" to "model rules". Client-facing model IDs come from `model_rules.Name`. `/v1/models` returns only enabled rules, not upstream provider catalogs.
- **Request logs**: stored in `request_logs`, with a `chain_json` column capturing retry/failover attempts.
- **Logging**: `internal/logger` writes to `~/.autoapi/logs/autoapi.log` using `lumberjack` rotation; settings are persisted in the SQLite `settings` table.
- **Application menu + tray**: both are Wails v3 native objects built in `main.go`. `wailsApp.Menu.SetApplicationMenu` installs the top-of-screen menu on macOS (and the in-window menu on Windows/Linux via `UseApplicationMenu: true`); `wailsApp.SystemTray.New()` creates the menu-bar tray (template icon, native `*application.Menu`). They are separate menus; changing one does not change the other. The old `fyne.io/systray` integration (`internal/tray`) is gone.
- **Go ↔ Wails runtime**: `internal/api/runtime.go` holds the injected `*application.App` + main `*application.WebviewWindow`. `api.App` implements `ServiceStartup(ctx, application.ServiceOptions) error` / `ServiceShutdown() error`; `main.go` attaches the runtime before `app.Run`. Do not expose the runtime handles as bound methods (it would leak `*application.App` into the bindings).
- **Multi-protocol gateway**: the proxy supports four inbound protocols — OpenAI Chat (`/v1/chat/completions`), OpenAI Responses (`/v1/responses`), Anthropic Messages (`/v1/messages`), and Gemini (`/v1beta/models/{model}:{action}`). Each can route natively or via single-hop conversion to a different upstream protocol.
- **Conversion edge registry**: `internal/proxy/matcher.go` `conversionEdges` is the static table of allowed single-hop conversions. Each edge has `From/To/UpstreamPath/Priority/SupportsStream/Preserves`. Candidate selection follows rule-target order first, then edge priority within the same target. Currently: Messages↔Responses (prio 10, stream), Chat→Responses (prio 10, stream), Responses→Chat (prio 20, stream). Gemini cross-protocol conversion is out of scope.
- **Capability resolution**: three layers intersect — Provider (`provider_capabilities`), Model (`model_capabilities`), and Converter preservation. Precedence is model explicit > provider explicit > default. Feature keys are scoped by `(provider, protocol, feature)` for providers and `(provider, model, protocol, feature)` for models.
- **Feature inspector**: `internal/proxy/inspect.go` parses request body once, extracts `RequestRequirements` (canonical features + NativeOnly + UnknownSemantic). Mode: `observe` (default, unknown features allowed with diagnostic) or `enforce` (unknown rejected). Stored in `AdvancedSettings.feature_capability_enforcement`, cached as `atomic.Value` on `Proxy`, refreshed on `New`/`Restart`.
- **Error classification**: `errNoMatch` → 503 `no_matching_rule`; `errUnsupportedFeature` → 422 `unsupported_feature`; plain unavailable → 503 `service_unavailable`.
- **Response converters are fail-closed**: unknown output items, content blocks, delta types, and unsupported semantics (reasoning, refusal, annotations, audio, legacy function_call) always return conversion errors, never silently drop.
- **Protocol adapters**: `internal/proxy/adapter.go` dispatches per `(from, to)` pair. Native adapter rewrites model name only. Conversion adapter rewrites request body, sets upstream path, and provides `ConvertResponse` (non-streaming) or `NewStreamConverter` (streaming).
- **Request body**: handlers read into `[]byte` once, inspect it, then pass to `resolveCandidates` → `forwardWithFailover` → `adapter.PrepareAttempt(body)`. Native passthrough never modifies the original body beyond model name rewrite.
- **Active routing strategies**: `priority_first` preserves configured order. `score_within_tier` and `cost_first` reorder only within a tier. Cost means one next upstream attempt; missing/invalid/non-USD prices sort after known prices and never disable health scoring.
- **Route-mode runtime identity**: every real upstream attempt carries `(target_id, inbound_protocol, upstream_protocol)`. Active scoring uses only that exact route mode's in-memory recent window (10 minutes, 64 attempts); persisted target summaries are observability-only and do not seed active scores after restart.
- **Breaker isolation**: provider breakers handle network/timeout/5xx failures. Response conversion failures use an in-memory route-mode breaker (3 consecutive failures, 30-second cooldown, one generation-token half-open probe) and never mark the provider unhealthy. Successful Restart clears route breakers and exploration state; failed Restart retains them.
- **Retry scope**: retry backoff and `Retry-After` are candidate-local. Cross-target failover is immediate, while the global attempt cap and rule first-byte deadline remain request-wide.
- **Deterministic exploration**: only `score_within_tier` explores, only in the highest executable tier, at most every 20th qualified request and no more often than every 30 seconds per `(rule_id, tier)`. It never bypasses capabilities or breakers.

## Data & state paths

- **SQLite DB**: `~/.autoapi/autoapi.db` (code uses `os.UserHomeDir`, not `~/Library/Application Support` despite the README).
- **App logs**: `~/.autoapi/logs/autoapi.log`.
- **AES key dir**: `~/.autoapi/`.
- Tests use `:memory:` or temp dirs; never touch the production DB.
- **Migrations are append-only**: latest is `039_provider_fix_empty_assistant`. Always append new entries with `0NN_` IDs; use `SkipIfRedundant` predicates. Never modify existing migrations.
- **`provider_capabilities`** (`025+029`): `(provider_id, protocol, feature, enabled, source, updated_at)`. `source='manual'` is authoritative; `source='legacy'` is ignored by capability snapshot. Provider FK cascade via 029.
- **`model_capabilities`** (`027+028`): `(provider_id, model_name, protocol, feature, enabled, source, updated_at)`. Composite FK to `models(provider_id, name)` with `ON UPDATE CASCADE ON DELETE CASCADE`. No row = inherit provider.

## i18n

- All UI strings live in `frontend/src/locales/zh-CN.json` and `frontend/src/locales/en-US.json`.
- Default and fallback locale is `zh-CN`.
- Use flat dot-namespaced keys, e.g., `settings.logging.level`, `usage.logTable.chain`.
- In components: `const { t } = useI18n()` in `<script setup>` or `$t(...)` in templates (global injection enabled).

## Testing

- Go tests are in `internal/{proxy,store,api,service}`; `internal/model` has no tests currently.
- Run focused tests: `go test ./internal/proxy` or `go test ./internal/store -run TestQueryLogs`.
- Frontend has no test suite; rely on `npm run build` (vue-tsc + Vite) for type safety.
- Proxy test suite takes ~55-65s due to streaming/failover timing tests.
- Conversion tests follow a pattern: unit tests in `convert_test.go`/`convert_chat_test.go`/`stream_convert_*_test.go`; E2E with `httptest.NewServer` in `proxy_test.go` (`TestChar_*`, `TestChat*`, `TestResponses*` prefixes); matcher/edge tests in `capability_snapshot_test.go`.

## Style & conventions

- Go formatting: run `gofmt -w` on changed files. No custom linter is configured.
- Frontend: no ESLint/Prettier config; rely on `vue-tsc --noEmit` and manual consistency.
- The app UI is Apple-style; dark mode is driven by CSS variables in `frontend/src/styles.css` and `html[data-theme]`.

## Common gotchas

- After any Go model change, verify `frontend/bindings/autoapi/internal/**/models.ts` is regenerated and `npm run build` still passes.
- `wails3 dev` opens the macOS window automatically; it is the fastest way to verify UI changes. It builds with the `dev` tag (development data profile) and Vite serves the frontend.
- Release builds **must** use `wails3 build` / `-tags production`; a bare `go build` compiles the development code path (dev logger/assets/devtools).
- The proxy uses a chi router. Request IDs are available via `middleware.GetReqID(r.context())`.
- **Edit menu must stay role-based.** macOS copy/paste/cut/selectAll/undo/redo in the webview rely on `mainMenu.AddRole(application.EditMenu)` in `main.go`. v3 emits NSMenuItems with standard Cocoa selectors (`copy:`, `paste:`, …) that propagate via the responder chain to the WKWebView first responder. Do NOT replace it with hand-rolled `OnClick` items: they use `@selector(handleClick)`, which INTERCEPTS the keystroke at the NSMenu level and routes it to the Go callback, swallowing the event so the webview never sees it. This was a real regression in v2; see the comment in `main.go`.
- **Close-to-hide** is a window hook (`mainWindow.RegisterHook(events.Common.WindowClosing, …)` + `e.Cancel()`), not `OnBeforeClose`. `app.Quit()` bypasses the hook. `Mac.ApplicationShouldTerminateAfterLastWindowClosed` is `false` whenever the tray is enabled.
- **Window/tray handles**: `app.Window.Current()` is nil before `Run`; capture the window returned by `Window.NewWithOptions` instead (as `main.go` does). Tray items are configured before `app.Run` and started automatically.
- **Conversion code must be fail-closed.** Any unmappable field, item type, content block, delta type, or semantic must return a conversion error — never silently drop it. Applies to both request and response converters, streaming and non-streaming.
- **Tool call `id` vs `call_id`**: In Responses, `call_id` is the cross-message association ID; `id` is the output item resource ID. They must never be equal. Chat→Responses converter generates `fc_<index>` for item `id`; Responses→Chat requires `call_id` and rejects responses with only `id`.
- **OpenCode Go session header.** Providers whose base URL is `https://opencode.ai/inference/go` (Console Go) or `https://opencode.ai/zen/go` reject inference requests missing `x-opencode-session` with `400 MissingSessionID`. The proxy synthesizes a stable per-conversation id from `(api_key_id, system/instructions, first user message)` when the client did not supply a session header; a client-supplied `x-opencode-session` or another recognized native session header always wins. The in-app "test this model" probe does the same. See `internal/proxy/opencode_session.go` and `store.RequiresOpenCodeSession`.
- **Feature capability enforcement mode** is `observe` by default. Switching to `enforce` rejects requests with unknown features on models without explicit capability rows — a breaking change for unconfigured users.

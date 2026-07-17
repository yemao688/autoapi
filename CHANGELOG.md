# Changelog

All notable changes to this project will be documented in this file.

## [0.6.0] - 2026-07-18

### Added

- **Multi-protocol gateway**: native pass-through for OpenAI Responses (`/v1/responses`), Anthropic Messages (`/v1/messages`), and Gemini (`/v1beta/models/{model}:{action}`) endpoints, alongside Chat Completions.
- **Single-hop protocol conversion**: Messages↔Responses and Chat↔Responses request/response conversion, streaming and non-streaming, with fail-closed converters that reject unmappable semantics instead of silently dropping them.
- **Feature-based routing**: request feature inspection gates candidate selection; provider-level and per-model capability overrides resolve in three layers (model > provider > default), with observe/enforce modes.
- **Scored routing strategies**: `score_within_tier` and `cost_first` reorder candidates within a tier using per-route-mode recent performance windows, with deterministic exploration for `score_within_tier`.
- **Per-model protocol routing**: rule targets can declare inbound→upstream protocol conversion per model.
- **Routing diagnostics**: replay score panel, shadow strategy comparisons, and per-route-mode runtime sample windows (10 minutes, 64 attempts) exposed through the diagnostics API.

### Changed

- **Model pricing** moved from rules to provider models; missing or invalid prices sort after known prices without disabling health scoring.
- **Routing strategy copy** now states that strategies affect the production routing order; the Shadow panel is labeled as read-only diagnostics.

### Fixed

- **Streaming failure routing**: typed failure causes; first-byte budget expiry performs zero dials, metrics, or stats; converter byte+error outputs are discarded; failures after commit abort instead of failing over.
- Truncated downstream streams abort the connection instead of presenting a clean EOF.
- Model rule names are now unique at the database level; migration detects existing duplicates and fails loudly.
- Targets whose provider was deleted are skipped so remaining targets still fail over; observe mode now allows unconfigured features on conversion paths, matching native behavior.
- Client disconnects before the first upstream byte now log the chosen provider, model, and route instead of empty fields.
- Model test handles array-form content, higher `max_tokens`, and `finish_reason: length` diagnostics.
- Replay score details and shadow comparisons are localized and easier to read.

## [0.5.1] - 2026-07-12

### Added

- **Per-mapping first-byte timeout**: each model mapping can define its own first response-body-byte timeout in seconds, while the enclosing model rule retains its cumulative timeout budget.
- **Clearer request outcomes**: streaming completions now distinguish successful delivery, upstream truncation, client abort, and downstream delivery failures.

### Changed

- **Model mapping reorder**: rapid consecutive drags are coalesced and persisted safely; the UI confirms the authoritative saved order and distinguishes mapping-set conflicts from ordinary save failures.
- **Background resource use**: Usage charts unmount when inactive or hidden, and relative-time refresh timers pause while the application is in background mode.

### Fixed

- A streaming upstream response that ends before `[DONE]` is no longer recorded as a successful completion merely because HTTP 200 was already committed.
- Close-to-tray lifecycle now uses a centralized visibility state so hidden-window polling and realtime refresh work do not restart from stale async completions.

## [0.5.0] - 2026-07-11

### Added

- **Close-to-tray / background API**: closing the window now hides the app and keeps the local OpenAI-compatible API (`0.0.0.0:8344`) running. Configurable in Settings > General (`close_action`).
- **Startup action**: Settings > General lets you choose `show_window` or `start_hidden` on launch. The app can start minimized to the menu bar.
- **Menu bar toggle**: The system tray icon can now be enabled or disabled from Settings > General. Disabling it forces safe fallbacks (`close_action = quit`, `startup_action = show_window`) so the window is never hidden irrecoverably.
- **Single-instance lock**: running a second instance now signals the first instance to show its window and exits, instead of opening two concurrent apps (`UniqueId: dev.local.autoapi`).
- **UI**: master-password gate, full provider management, model rules, API keys, dashboard, usage stats, dark mode, responsive narrow-screen/portrait layout, model donut chart, trend chart, K/M/B token formatting, request-chain log details.
- **Proxy**: OpenAI-compatible chat completions, embeddings, and models endpoints; SSE streaming pass-through; retry/failover with budget caps and Retry-After handling; two-phase request logging showing pending requests in real time.
- **Security**: AES-256-GCM key encryption, master password with Argon2id derivation, SQLite WAL mode, per-request upstream key decryption.
- **Data**: request-log export (JSON/CSV), log purge/clear, dashboard metrics, latency histograms, provider share/model ranking.
- **i18n**: Chinese (zh-CN) and English (en-US) locale support.

### Changed

- **Terminology**: "route rules" renamed to "model rules" throughout the UI and backend. `internal/store/routes.go` renamed to `internal/store/modelrules.go`.
- **Frontend API bridge**: renamed `frontend/src/api/client.ts` to `frontend/src/api/bridge.ts` to reflect its actual role as a Wails binding wrapper.
- **Go UUID helper**: consolidated duplicate `newUUID()` implementations into `store.NewUUID()`.
- **License**: changed from "internal prototype" to MIT.
- **Version**: bumped from `0.4.2` to `0.5.0`.

### Fixed

- Removed stale `var _ model.ProviderStatus` no-op assertion from `internal/store/store.go`.
- Removed commented-out local `replace` directive from `go.mod`.
- Fixed README data-path drift: the actual DB path is `~/.autoapi/autoapi.db` on all platforms, not `~/Library/Application Support/autoapi/autoapi.db`.
- Fixed README tech-stack drift: removed `adrg/xdg` which is not a dependency.

### Documentation

- Updated `README.md` to reflect current model-rule terminology, storage paths, and shipped features.
- Added `LICENSE` (MIT).
- Added this `CHANGELOG.md`.

## [0.4.2] - 2026-07-07

- Initial tagged release. Local OpenAI proxy, provider management, API keys, model rules, usage stats, and system tray.

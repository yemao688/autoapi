# Changelog

All notable changes to this project will be documented in this file.

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

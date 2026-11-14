# Build Directory

Assets and configuration for the Wails v3 build.

## Project config

`config.yml` is the Wails v3 project configuration (product name, identifier,
version, copyright, `dev_mode`). It replaced `wails.json`. The root
`Taskfile.yml` drives `wails3 build` / `wails3 package` / `wails3 dev`.

After changing `info`, regenerate the platform assets:

```bash
wails3 update build-assets -name autoapi -binaryname autoapi -config build/config.yml -dir build
wails3 generate icons -input build/appicon.png \
  -macfilename build/darwin/icons.icns -windowsfilename build/windows/icon.ico
```

`update build-assets` rewrites the generated plists/manifests. It also deletes
`build/darwin/entitlements.plist`; re-add it if you start signing with a
Developer ID. Re-apply the dev bundle identity if it is overwritten:
`Info.dev.plist` should use `com.wails.autoapi.dev` / `Autoapi Dev` so the
development build can run alongside the production app.

## Contents

- `bin/` — build output (`autoapi`, `autoapi.app`, `autoapi.dev.app`); ignored.
- `appicon.png` — 1024×1024 source icon.
- `trayicon.png` — 44×44 monochrome macOS template icon for the menu bar.
- `darwin/`
  - `Info.plist` — production bundle metadata (`LSMinimumSystemVersion` 12.0).
  - `Info.dev.plist` — development bundle metadata (`com.wails.autoapi.dev`).
  - `icons.icns` — macOS app icon generated from `appicon.png`.
- `windows/`
  - `icon.ico`, `info.json`, `wails.exe.manifest` — Windows resources used by
    the build/packaging tasks.
- `linux/` — Linux packaging assets.

The `.app` bundles are assembled by `package:darwin` / `run:darwin` in the root
`Taskfile.yml`.

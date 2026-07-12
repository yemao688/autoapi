package main

import (
	"runtime"
	"runtime/debug"

	"autoapi/internal/model"
)

// appVersion is set at build time via:
//
//	go build -ldflags "-X main.appVersion=0.5.1"
//
// Defaults to "dev" when unset (e.g. during wails dev).
var appVersion = "dev"

// appBuild is set at build time via:
//
//	go build -ldflags "-X main.appBuild=20260712"
//
// Defaults to "" which triggers a VCS hash fallback.
var appBuild = ""

// getAppInfo returns the application metadata for the About section.
func getAppInfo() model.AppInfo {
	build := appBuild
	if build == "" {
		build = buildHash()
	}
	return model.AppInfo{
		Version:   appVersion,
		Build:     build,
		Platform:  runtime.GOOS,
		Arch:      runtime.GOARCH,
		GoVersion: runtime.Version(),
	}
}

// buildHash extracts the VCS revision from Go's build info. This works in
// `wails dev` and `go run` without any -ldflags. Returns "unknown" if the
// revision can't be determined.
func buildHash() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			if len(s.Value) > 7 {
				return s.Value[:7]
			}
			return s.Value
		}
	}
	return "unknown"
}

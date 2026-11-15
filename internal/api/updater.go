package api

import (
	"context"
	"errors"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

// errUpdaterUnavailable is returned by the update methods when the process
// has no configured updater (development builds, unsupported platforms, or a
// runtime that was never attached).
var errUpdaterUnavailable = errors.New("update checker is not available in this build")

// UpdateInfo is the About section's view of one update check.
type UpdateInfo struct {
	CurrentVersion string `json:"currentVersion"`
	Available      bool   `json:"available"`
	Version        string `json:"version,omitempty"`
	Notes          string `json:"notes,omitempty"`
	PublishedAt    string `json:"publishedAt,omitempty"`
	SizeBytes      int64  `json:"sizeBytes,omitempty"`
	Artifact       string `json:"artifact,omitempty"`
}

// updaterOrErr returns the updater only when the runtime attached one and
// Init ran; development builds and unsupported platforms report a stable
// error the UI can translate.
func (a *App) updaterOrErr() (*updater.Updater, error) {
	u := a.runtime.updater()
	if u == nil || u.State() == updater.StateUnconfigured {
		return nil, errUpdaterUnavailable
	}
	return u, nil
}

// CheckForUpdate asks the configured update provider for a newer release.
// It only performs the check: downloading and installing stay explicit user
// actions (DownloadUpdate, then RestartToUpdate).
func (a *App) CheckForUpdate() (*UpdateInfo, error) {
	u, errU := a.updaterOrErr()
	if errU != nil {
		return nil, errU
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	info := &UpdateInfo{CurrentVersion: u.CurrentVersion()}
	release, err := u.Check(ctx)
	if err != nil {
		return info, err
	}
	if release == nil {
		return info, nil
	}
	info.Available = true
	info.Version = release.Version
	info.Notes = release.Notes
	info.SizeBytes = release.Artifact.Size
	info.Artifact = release.Artifact.Filename
	if !release.PublishedAt.IsZero() {
		info.PublishedAt = release.PublishedAt.UTC().Format(time.RFC3339)
	}
	return info, nil
}

// DownloadUpdate downloads, verifies and stages the release found by the last
// CheckForUpdate. Progress is reported on the updater's Wails events
// (wails:updater:download-progress); errors surface here and as
// wails:updater:error.
func (a *App) DownloadUpdate() error {
	u, errU := a.updaterOrErr()
	if errU != nil {
		return errU
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return u.DownloadAndInstall(ctx)
}

// RestartToUpdate hands the staged update to the updater's helper and quits;
// the helper swaps the application bundle and relaunches the new build.
func (a *App) RestartToUpdate() error {
	u, errU := a.updaterOrErr()
	if errU != nil {
		return errU
	}
	return u.Restart(context.Background())
}

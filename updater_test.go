package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

const (
	testZipName = "autoapi-0.8.0-darwin-universal.zip"
	testSum     = "9f2c1d3e4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

// newUpdateServer serves a GitHub-style release payload plus the checksum
// sidecar so the provider's matching, semver and digest logic can be
// exercised without touching the network.
func newUpdateServer(t *testing.T, withZip bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/yemao688/autoapi/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		assets := []map[string]any{
			{"id": 1, "name": "autoapi-0.8.0-universal.dmg", "content_type": "application/octet-stream", "size": 99, "url": srv.URL + "/dl/dmg"},
			{"id": 2, "name": "SHA256SUMS", "content_type": "text/plain", "size": 96, "url": srv.URL + "/dl/sums"},
		}
		if withZip {
			assets = append(assets, map[string]any{
				"id": 3, "name": testZipName, "content_type": "application/zip", "size": 42, "url": srv.URL + "/dl/zip",
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":     "v0.8.0",
			"name":         "Autoapi v0.8.0",
			"body":         "notes",
			"published_at": "2026-11-20T00:00:00Z",
			"html_url":     "https://github.com/yemao688/autoapi/releases/tag/v0.8.0",
			"assets":       assets,
		})
	})
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", testSum, testZipName)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newUpdateProvider(t *testing.T, srv *httptest.Server) *github.Provider {
	t.Helper()
	p, err := github.New(github.Config{
		Repository:    "yemao688/autoapi",
		BaseURL:       srv.URL,
		ChecksumAsset: "SHA256SUMS",
		AssetMatcher:  updateAssetMatcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestUpdateAssetMatcherPrefersDarwinZip(t *testing.T) {
	assets := []github.ReleaseAsset{
		{Name: "autoapi-0.8.0-universal.dmg"},
		{Name: "SHA256SUMS"},
		{Name: testZipName},
	}
	if got := updateAssetMatcher(updater.CheckRequest{Platform: "darwin", Arch: "arm64"}, assets); got != 2 {
		t.Fatalf("matcher picked %d, want the darwin zip at index 2", got)
	}
	if got := updateAssetMatcher(updater.CheckRequest{Platform: "windows"}, assets); got != -1 {
		t.Fatalf("matcher picked %d for a windows request, want -1", got)
	}
}

func TestUpdateProviderFindsZipAndDigest(t *testing.T) {
	srv := newUpdateServer(t, true)
	p := newUpdateProvider(t, srv)

	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.7.0", Platform: "darwin", Arch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil {
		t.Fatal("no release found, want v0.8.0")
	}
	if rel.Version != "0.8.0" {
		t.Fatalf("version = %q", rel.Version)
	}
	if rel.Artifact.Filename != testZipName {
		t.Fatalf("artifact = %q, want %q", rel.Artifact.Filename, testZipName)
	}
	if rel.Verification == nil || rel.Verification.DigestAlgo != "sha256" {
		t.Fatalf("verification = %+v", rel.Verification)
	}
	if got := hex.EncodeToString(rel.Verification.Digest); got != testSum {
		t.Fatalf("digest = %s, want %s", got, testSum)
	}
}

func TestUpdateProviderReportsUpToDate(t *testing.T) {
	srv := newUpdateServer(t, true)
	p := newUpdateProvider(t, srv)

	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.8.0", Platform: "darwin", Arch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	if rel != nil {
		t.Fatalf("got release %+v, want nil", rel)
	}
}

// A release that only ships the DMG must not be offered to the updater:
// the updater installs .zip archives, never disk images.
func TestUpdateProviderIgnoresDMGOnlyRelease(t *testing.T) {
	srv := newUpdateServer(t, false)
	p := newUpdateProvider(t, srv)

	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.7.0", Platform: "darwin", Arch: "arm64"})
	if err == nil {
		t.Fatalf("got release %+v, want an asset-matching error", rel)
	}
}

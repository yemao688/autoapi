//go:build dev && !production

package store

import (
	"context"
	"testing"
)

func TestFixturesRequireExplicitOptIn(t *testing.T) {
	withoutFixtures, err := New(context.Background(), StoreDeps{DSN: t.TempDir() + "/without.db"})
	if err != nil {
		t.Fatalf("New without fixtures: %v", err)
	}
	t.Cleanup(func() { _ = withoutFixtures.Close() })
	var count int
	if err := withoutFixtures.db.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&count); err != nil {
		t.Fatalf("count providers without fixtures: %v", err)
	}
	if count != 0 {
		t.Fatalf("providers without opt-in = %d, want 0", count)
	}

	withFixtures, err := New(context.Background(), StoreDeps{
		DSN:          t.TempDir() + "/with.db",
		SeedFixtures: true,
	})
	if err != nil {
		t.Fatalf("New with fixtures: %v", err)
	}
	t.Cleanup(func() { _ = withFixtures.Close() })
	if err := withFixtures.db.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&count); err != nil {
		t.Fatalf("count providers with fixtures: %v", err)
	}
	if count == 0 {
		t.Fatal("expected fixtures after explicit opt-in")
	}
}

//go:build !dev || production

package store

import (
	"context"
	"testing"
)

func TestFixturesUnavailableOutsideDevelopment(t *testing.T) {
	s, err := New(context.Background(), StoreDeps{
		DSN:          t.TempDir() + "/store.db",
		SeedFixtures: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&count); err != nil {
		t.Fatalf("count providers: %v", err)
	}
	if count != 0 {
		t.Fatalf("providers = %d, want 0 outside a development build", count)
	}
}

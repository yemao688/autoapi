//go:build !dev || production

package config

import "testing"

func TestCurrentProductionProfile(t *testing.T) {
	p := Current()
	if p.Name != "production" || p.StorageDirName != ".autoapi" || p.DefaultPort != 8344 || p.SeedFixtures {
		t.Fatalf("unexpected production profile: %+v", p)
	}
}

//go:build dev && !production

package config

import "testing"

func TestCurrentDevelopmentProfile(t *testing.T) {
	p := Current()
	if p.Name != "development" || p.StorageDirName != ".autoapi-dev" || p.DefaultPort != 18344 || !p.SeedFixtures {
		t.Fatalf("unexpected development profile: %+v", p)
	}
}

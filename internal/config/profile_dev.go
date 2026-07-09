//go:build dev && !production

package config

var selectedProfile = Profile{
	Name:           "development",
	StorageDirName: ".autoapi-dev",
	DefaultPort:    18344,
	SeedFixtures:   true,
}

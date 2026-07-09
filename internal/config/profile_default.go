//go:build !dev || production

package config

var selectedProfile = Profile{
	Name:           "production",
	StorageDirName: ".autoapi",
	DefaultPort:    8344,
	SeedFixtures:   false,
}

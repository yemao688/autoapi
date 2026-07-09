package config

// Profile describes the runtime defaults selected at build time.
type Profile struct {
	Name           string
	StorageDirName string
	DefaultPort    int
	SeedFixtures   bool
}

// Current returns the build-selected application profile.
func Current() Profile {
	return selectedProfile
}

//go:build !darwin

// Package dock provides no-op stubs on non-macOS platforms.
package dock

// HideDockIcon is a no-op on non-macOS platforms.
func HideDockIcon() {}

// ShowDockIcon is a no-op on non-macOS platforms.
func ShowDockIcon() {}

// IsAccessory always returns false on non-macOS platforms.
func IsAccessory() bool { return false }

//go:build !linux && !darwin

package flowcontrol

// Unsupported platforms report unknown, not a fabricated zero.
func filesystemFree() *uint64 { return nil }

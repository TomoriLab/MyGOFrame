//go:build !linux

package nativeruntime

// Prepare leaves platforms without the Linux private runtime unchanged.
func Prepare() error { return nil }

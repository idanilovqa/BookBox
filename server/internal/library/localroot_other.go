//go:build !windows

package library

func validatePlatformLocalRoot(string) error { return nil }

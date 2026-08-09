//go:build !windows

package workerlog

func rejectReparsePoint(string) error { return nil }

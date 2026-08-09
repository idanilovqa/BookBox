//go:build !windows

package main

import (
	"context"
	"errors"
)

func runWindowsService(string, func(context.Context) error) error {
	return errors.New("Windows service mode is unavailable on this platform")
}

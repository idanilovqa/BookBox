//go:build !windows

package main

import (
	"context"
	"strings"
	"testing"
)

func TestWindowsServiceModeIsUnavailableOffWindows(t *testing.T) {
	err := runWindowsService("BookBoxCatalogWorker", func(context.Context) error {
		t.Fatal("service program ran")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("runWindowsService() error = %v", err)
	}
}

func TestDefaultWorkerConfigPathOffWindows(t *testing.T) {
	if path := defaultWorkerConfigPath(); path != "/etc/bookbox/config.yml" {
		t.Fatalf("default config path = %q", path)
	}
}

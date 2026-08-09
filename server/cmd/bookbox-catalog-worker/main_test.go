package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bookbox.local/server/internal/config"
)

type runnerFunc func(context.Context) error

func (run runnerFunc) Run(ctx context.Context) error { return run(ctx) }

func TestMonitorFailureWarnsWhileWorkerContinues(t *testing.T) {
	monitorErr := errors.New("watcher failed")
	monitorCalled := make(chan struct{})
	warned := make(chan error, 1)
	workerRan := false
	err := runWorkerWithMonitor(t.Context(), runnerFunc(func(context.Context) error {
		<-monitorCalled
		workerRan = true
		return nil
	}), runnerFunc(func(context.Context) error {
		close(monitorCalled)
		return monitorErr
	}), func(err error) { warned <- err })
	if err != nil || !workerRan {
		t.Fatalf("err=%v workerRan=%v", err, workerRan)
	}
	select {
	case got := <-warned:
		if !errors.Is(got, monitorErr) {
			t.Fatalf("warning=%v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor warning missing")
	}
}

func TestWorkerShutdownCancelsAndJoinsMonitor(t *testing.T) {
	monitorStopped := false
	err := runWorkerWithMonitor(t.Context(), runnerFunc(func(context.Context) error { return nil }),
		runnerFunc(func(ctx context.Context) error { <-ctx.Done(); monitorStopped = true; return nil }), nil)
	if err != nil || !monitorStopped {
		t.Fatalf("err=%v monitorStopped=%v", err, monitorStopped)
	}
}

func TestMetadataProvidersAllowEmptyOptionalGoogleKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "google_books_key.txt")
	if err := os.WriteFile(keyPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE", keyPath)

	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := metadataProviders(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("providers = %d, want Open Library only", len(providers))
	}
}

func TestWorkerEnvironmentOverridesStayBoundedAndUseFixtureProviders(t *testing.T) {
	t.Setenv("BOOKBOX_SCAN_INTERVAL", "5s")
	t.Setenv("BOOKBOX_OPENLIBRARY_BASE_URL", "http://provider-fixtures:8080/openlibrary")
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_BASE_URL", "http://provider-fixtures:8080/googlebooks")

	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if options.scanInterval != 5*time.Second {
		t.Fatalf("scan interval = %s, want 5s", options.scanInterval)
	}
	if options.openLibraryBaseURL != "http://provider-fixtures:8080/openlibrary" {
		t.Fatalf("Open Library base URL = %q", options.openLibraryBaseURL)
	}
	if options.googleBooksBaseURL != "http://provider-fixtures:8080/googlebooks" {
		t.Fatalf("Google Books base URL = %q", options.googleBooksBaseURL)
	}

	keyPath := filepath.Join(t.TempDir(), "google_books_key.txt")
	if err := os.WriteFile(keyPath, []byte("fixture-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE", keyPath)
	providers, err := metadataProviders(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %d, want deterministic Open Library and Google Books", len(providers))
	}
}

func TestWorkerConfigurationOverridesEnvironmentPathFallbacks(t *testing.T) {
	t.Setenv("BOOKBOX_COVER_BACKUP_ROOT", "/environment/backups")
	t.Setenv("BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE", "/environment/google-books-key")

	options, err := workerOptionsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	options = applyWorkerSettings(options, config.WorkerSettings{
		CoverBackupRoot:       "/configured/backups",
		GoogleBooksAPIKeyFile: "/configured/google-books-key",
	})

	if options.coverBackupRoot != "/configured/backups" {
		t.Fatalf("cover backup root = %q", options.coverBackupRoot)
	}
	if options.googleBooksAPIKeyFile != "/configured/google-books-key" {
		t.Fatalf("Google Books API key file = %q", options.googleBooksAPIKeyFile)
	}
}

func TestWorkerEnvironmentRejectsInvalidOverrides(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "zero interval", variable: "BOOKBOX_SCAN_INTERVAL", value: "0s"},
		{name: "negative interval", variable: "BOOKBOX_SCAN_INTERVAL", value: "-1s"},
		{name: "excessive interval", variable: "BOOKBOX_SCAN_INTERVAL", value: "25h"},
		{name: "Open Library credentials", variable: "BOOKBOX_OPENLIBRARY_BASE_URL", value: "http://owner:secret@fixtures/openlibrary"},
		{name: "Google Books file URL", variable: "BOOKBOX_GOOGLE_BOOKS_BASE_URL", value: "file:///fixtures/googlebooks"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.variable, test.value)
			if _, err := workerOptionsFromEnvironment(); err == nil {
				t.Fatalf("%s=%q was accepted", test.variable, test.value)
			}
		})
	}
}

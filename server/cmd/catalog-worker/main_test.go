package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetadataProvidersAllowEmptyOptionalGoogleKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "google_books_key.txt")
	if err := os.WriteFile(keyPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETSTORAGE_GOOGLE_BOOKS_API_KEY_FILE", keyPath)

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
	t.Setenv("NETSTORAGE_SCAN_INTERVAL", "5s")
	t.Setenv("NETSTORAGE_OPENLIBRARY_BASE_URL", "http://provider-fixtures:8080/openlibrary")
	t.Setenv("NETSTORAGE_GOOGLE_BOOKS_BASE_URL", "http://provider-fixtures:8080/googlebooks")

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
	t.Setenv("NETSTORAGE_GOOGLE_BOOKS_API_KEY_FILE", keyPath)
	providers, err := metadataProviders(options)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %d, want deterministic Open Library and Google Books", len(providers))
	}
}

func TestWorkerEnvironmentRejectsInvalidOverrides(t *testing.T) {
	tests := []struct {
		name     string
		variable string
		value    string
	}{
		{name: "zero interval", variable: "NETSTORAGE_SCAN_INTERVAL", value: "0s"},
		{name: "negative interval", variable: "NETSTORAGE_SCAN_INTERVAL", value: "-1s"},
		{name: "excessive interval", variable: "NETSTORAGE_SCAN_INTERVAL", value: "25h"},
		{name: "Open Library credentials", variable: "NETSTORAGE_OPENLIBRARY_BASE_URL", value: "http://owner:secret@fixtures/openlibrary"},
		{name: "Google Books file URL", variable: "NETSTORAGE_GOOGLE_BOOKS_BASE_URL", value: "file:///fixtures/googlebooks"},
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

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMetadataProvidersAllowEmptyOptionalGoogleKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "google_books_key.txt")
	if err := os.WriteFile(keyPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETSTORAGE_GOOGLE_BOOKS_API_KEY_FILE", keyPath)

	providers, err := metadataProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("providers = %d, want Open Library only", len(providers))
	}
}

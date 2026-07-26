package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestServeRejectsPlaintextByDefault(t *testing.T) {
	err := serve(&http.Server{}, false)
	if !errors.Is(err, errTLSNotConfigured) {
		t.Fatalf("serve() error = %v, want errTLSNotConfigured", err)
	}
}

func TestRunLoadsConfigBeforeRejectingMissingTLS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	contents := []byte(`
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"-config", path}, false)
	if !errors.Is(err, errTLSNotConfigured) {
		t.Fatalf("run() error = %v, want errTLSNotConfigured", err)
	}
}

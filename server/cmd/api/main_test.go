package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"netstorage.local/server/internal/config"
)

func TestServeRejectsPlaintextByDefault(t *testing.T) {
	err := serve(&http.Server{}, false)
	if !errors.Is(err, errTLSNotConfigured) {
		t.Fatalf("serve() error = %v, want errTLSNotConfigured", err)
	}
}

func TestRunLoadsConfigBeforeRejectingMissingTLS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	contents := []byte(fmt.Sprintf(`
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: %s}
`, filepath.ToSlash(t.TempDir())))
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	err := run([]string{"-config", path}, false)
	if !errors.Is(err, errTLSNotConfigured) {
		t.Fatalf("run() error = %v, want errTLSNotConfigured", err)
	}
}

func TestBuildHandlerWiresConfiguredRootsAndBrowser(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Book"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler, err := buildHandler(config.Config{
		Roots: []config.Root{{
			ID:            "audiobooks",
			Name:          "Audiobooks",
			ContainerPath: root,
		}},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		target string
		want   string
	}{
		{
			target: "/v1/roots",
			want:   "{\"roots\":[{\"id\":\"audiobooks\",\"name\":\"Audiobooks\"}]}\n",
		},
		{
			target: "/v1/browse?root=audiobooks",
			want:   "\"relativePath\":\"Book\"",
		},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.target, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", test.target, response.Code, response.Body.String())
		}
		if body := response.Body.String(); test.target == "/v1/roots" && body != test.want {
			t.Fatalf("%s body = %q, want %q", test.target, body, test.want)
		} else if test.target != "/v1/roots" && !strings.Contains(body, test.want) {
			t.Fatalf("%s body = %q, want substring %q", test.target, body, test.want)
		}
	}
}

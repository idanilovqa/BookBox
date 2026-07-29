package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bookbox.local/server/internal/auth"
	"bookbox.local/server/internal/config"
	"bookbox.local/server/internal/library"
)

func TestRootsReturnsPublicNamesWithoutFilesystemPaths(t *testing.T) {
	root := t.TempDir()
	api := newBrowseAPI(t, []config.Root{
		{ID: "audiobooks", Name: "Audiobooks", ContainerPath: root},
	})

	response := serveRequest(api, "/v1/roots")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	want := "{\"roots\":[{\"id\":\"audiobooks\",\"name\":\"Audiobooks\"}]}\n"
	if response.Body.String() != want {
		t.Fatalf("body = %q, want %q", response.Body.String(), want)
	}
	if bytes.Contains(response.Body.Bytes(), []byte(root)) {
		t.Fatalf("response leaked absolute root %q: %s", root, response.Body.String())
	}
}

func TestBrowseReturnsDirectoryJSON(t *testing.T) {
	root := t.TempDir()
	book := filepath.Join(root, "Author One", "Book One")
	if err := os.MkdirAll(book, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(book, "chapter.mp3"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := newBrowseAPI(t, []config.Root{
		{ID: "audiobooks", Name: "Audiobooks", ContainerPath: root},
	})

	response := serveRequest(api, "/v1/browse?root=audiobooks&path=Author%20One")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var directory library.Directory
	if err := json.Unmarshal(response.Body.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	if directory.RootID != "audiobooks" ||
		directory.Path != "Author One" ||
		len(directory.Entries) != 1 ||
		directory.Entries[0].RelativePath != "Author One/Book One" {
		t.Fatalf("directory = %#v", directory)
	}
	if bytes.Contains(response.Body.Bytes(), []byte(root)) {
		t.Fatalf("response leaked absolute root %q: %s", root, response.Body.String())
	}
}

func TestBrowseDecodesQueryPathExactlyOnce(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"100% Complete", "%20"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	api := newBrowseAPI(t, []config.Root{
		{ID: "audiobooks", Name: "Audiobooks", ContainerPath: root},
	})

	for _, test := range []struct {
		name   string
		target string
		path   string
	}{
		{
			name:   "literal percent",
			target: "/v1/browse?root=audiobooks&path=100%25%20Complete",
			path:   "100% Complete",
		},
		{
			name:   "percent escape text",
			target: "/v1/browse?root=audiobooks&path=%2520",
			path:   "%20",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := serveRequest(api, test.target)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var directory library.Directory
			if err := json.Unmarshal(response.Body.Bytes(), &directory); err != nil {
				t.Fatal(err)
			}
			if directory.Path != test.path {
				t.Fatalf("path = %q, want %q", directory.Path, test.path)
			}
		})
	}
}

func TestFolderSearchReturnsNormalizedPathMatches(t *testing.T) {
	root := t.TempDir()
	for _, relative := range []string{
		"Author/Black.Box/chapter.mp3",
		"Blacksmith/chapter.mp3",
	} {
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	api := newBrowseAPI(t, []config.Root{
		{ID: "audiobooks", Name: "Audiobooks", ContainerPath: root},
	})

	response := serveRequest(
		api,
		"/v1/browse/search?root=audiobooks&q=black%20box",
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result library.SearchResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RootID != "audiobooks" ||
		result.Query != "black box" ||
		len(result.Entries) != 1 ||
		result.Entries[0].RelativePath != "Author/Black.Box" {
		t.Fatalf("search result = %#v", result)
	}
	if bytes.Contains(response.Body.Bytes(), []byte(root)) {
		t.Fatalf("response leaked absolute root %q: %s", root, response.Body.String())
	}
}

func TestFolderSearchMapsUnknownRootToSafeError(t *testing.T) {
	api := newBrowseAPI(t, []config.Root{{
		ID:            "audiobooks",
		Name:          "Audiobooks",
		ContainerPath: t.TempDir(),
	}})

	response := serveRequest(api, "/v1/browse/search?root=missing&q=book")

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":"root_not_found"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestFolderSearchMapsMalformedQueryAndFilesystemFailure(t *testing.T) {
	root := t.TempDir()
	api := newBrowseAPI(t, []config.Root{{
		ID:            "audiobooks",
		Name:          "Audiobooks",
		ContainerPath: root,
	}})

	malformed := serveRequest(
		api,
		"/v1/browse/search?root=audiobooks&q=%zz",
	)
	if malformed.Code != http.StatusBadRequest ||
		!strings.Contains(malformed.Body.String(), `"code":"invalid_path"`) {
		t.Fatalf(
			"malformed response = %d %s",
			malformed.Code,
			malformed.Body.String(),
		)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	missing := serveRequest(
		api,
		"/v1/browse/search?root=audiobooks&q=book",
	)
	if missing.Code != http.StatusNotFound ||
		!strings.Contains(missing.Body.String(), `"code":"path_not_found"`) {
		t.Fatalf("missing response = %d %s", missing.Code, missing.Body.String())
	}
}

func TestBrowseMapsExpectedFailuresToSafeJSONErrors(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "chapter.mp3")
	if err := os.WriteFile(filePath, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := newBrowseAPI(t, []config.Root{
		{ID: "audiobooks", Name: "Audiobooks", ContainerPath: root},
	})

	tests := []struct {
		name    string
		target  string
		status  int
		code    string
		message string
	}{
		{
			name:    "unknown root",
			target:  "/v1/browse?root=missing",
			status:  http.StatusNotFound,
			code:    "root_not_found",
			message: "root not found",
		},
		{
			name:    "encoded traversal",
			target:  "/v1/browse?root=audiobooks&path=%2e%2e%2Fsecret",
			status:  http.StatusBadRequest,
			code:    "invalid_path",
			message: "path must stay within the selected root",
		},
		{
			name:    "malformed encoding",
			target:  "/v1/browse?root=audiobooks&path=%zz",
			status:  http.StatusBadRequest,
			code:    "invalid_path",
			message: "path must stay within the selected root",
		},
		{
			name:    "missing path",
			target:  "/v1/browse?root=audiobooks&path=missing",
			status:  http.StatusNotFound,
			code:    "path_not_found",
			message: "path not found",
		},
		{
			name:    "unexpected filesystem error",
			target:  "/v1/browse?root=audiobooks&path=chapter.mp3",
			status:  http.StatusInternalServerError,
			code:    "internal_error",
			message: "internal server error",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := serveRequest(api, test.target)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != test.code || body.Error.Message != test.message {
				t.Fatalf("error = %#v", body.Error)
			}
			if bytes.Contains(response.Body.Bytes(), []byte(root)) ||
				bytes.Contains(response.Body.Bytes(), []byte(filePath)) {
				t.Fatalf("error leaked an absolute path: %s", response.Body.String())
			}
		})
	}
}

func TestRegisteredRoutesAreReadOnlyForSourceFiles(t *testing.T) {
	banned := []string{"upload", "delete", "rename", "move", "tags"}
	for _, registered := range routes(Dependencies{}) {
		descriptor := strings.ToLower(registered.method + " " + registered.path)
		for _, word := range banned {
			if strings.Contains(descriptor, word) {
				t.Errorf("registered route %q contains banned source mutation %q", descriptor, word)
			}
		}
	}
}

func newBrowseAPI(t *testing.T, roots []config.Root) http.Handler {
	t.Helper()
	resolver, err := library.NewResolver(roots)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewTokenManager([]byte(strings.Repeat("b", 32)), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	api := New(Dependencies{
		Version: "test",
		Roots:   roots,
		Browser: library.NewBrowser(resolver),
		Tokens:  manager,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		request.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		api.ServeHTTP(w, request)
	})
}

func serveRequest(handler http.Handler, target string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

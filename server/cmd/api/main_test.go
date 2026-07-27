package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"netstorage.local/server/internal/auth"
	"netstorage.local/server/internal/catalog"
	"netstorage.local/server/internal/config"
	"netstorage.local/server/internal/database"
	"netstorage.local/server/internal/testpostgres"
)

func TestRunLoadsSecretsCreatesIdentityAndServesMandatoryTLS(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "library")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("secret", auth.ArgonParams{
		MemoryKiB: 64, Iterations: 1, Parallelism: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	hashPath := filepath.Join(directory, "password_hash")
	keyPath := filepath.Join(directory, "session_key")
	if err := os.WriteFile(hashPath, []byte(hash+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("s", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(directory, "tls", "server.crt")
	tlsKeyPath := filepath.Join(directory, "tls", "server.key")
	configPath := writeServerConfig(t, root, certPath, tlsKeyPath, hashPath, keyPath)

	errServed := errors.New("serve callback reached")
	var served *http.Server
	var servedCert, servedKey string
	var output bytes.Buffer
	err = runWith(
		[]string{"-config", configPath},
		func(server *http.Server, cert, key string) error {
			served, servedCert, servedKey = server, cert, key
			return errServed
		},
		&output,
		slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	)
	if !errors.Is(err, errServed) {
		t.Fatalf("runWith() error = %v, want serve callback", err)
	}
	if served == nil || servedCert != certPath || servedKey != tlsKeyPath {
		t.Fatalf("serve args = %#v, %q, %q", served, servedCert, servedKey)
	}
	if !regexp.MustCompile(`netStorage certificate SHA-256: ([0-9A-F]{2}:){31}[0-9A-F]{2}\n`).
		MatchString(output.String()) {
		t.Fatalf("fingerprint output = %q", output.String())
	}
	for _, path := range []string{certPath, tlsKeyPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("TLS path %q: %v", path, err)
		}
	}

	health := httptest.NewRecorder()
	served.Handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}
	protected := httptest.NewRecorder()
	served.Handler.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/v1/roots", nil))
	if protected.Code != http.StatusUnauthorized {
		t.Fatalf("protected status = %d, want 401", protected.Code)
	}
}

func TestRunRejectsMalformedPasswordHashBeforeServing(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "library")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	hashPath := filepath.Join(directory, "password_hash")
	sessionKeyPath := filepath.Join(directory, "session_key")
	if err := os.WriteFile(hashPath, []byte("not-an-argon-hash"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionKeyPath, []byte(strings.Repeat("s", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := writeServerConfig(
		t,
		root,
		filepath.Join(directory, "tls", "server.crt"),
		filepath.Join(directory, "tls", "server.key"),
		hashPath,
		sessionKeyPath,
	)
	called := false
	err := runWith(
		[]string{"-config", configPath},
		func(*http.Server, string, string) error {
			called = true
			return nil
		},
		&bytes.Buffer{},
		slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	)
	if !errors.Is(err, auth.ErrInvalidPasswordHash) || called {
		t.Fatalf("runWith() = %v, called = %v; want invalid hash before serving", err, called)
	}
}

func TestBuildHandlerWiresAuthenticatedConfiguredRootsAndBrowser(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Book"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "Book", "chapter.mp3"),
		[]byte("chapter"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("secret", auth.ArgonParams{
		MemoryKiB: 64, Iterations: 1, Parallelism: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets := config.Secrets{
		PasswordHash: hash,
		SessionKey:   []byte(strings.Repeat("k", 32)),
	}
	handler, manager, err := buildHandler(config.Config{
		Roots: []config.Root{{
			ID:            "audiobooks",
			Name:          "Audiobooks",
			ContainerPath: root,
		}},
		Auth: config.Auth{Username: "owner"},
	}, secrets, "test", slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	pair, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		target string
		want   string
	}{
		{target: "/v1/roots", want: "{\"roots\":[{\"id\":\"audiobooks\",\"name\":\"Audiobooks\"}]}\n"},
		{target: "/v1/browse?root=audiobooks", want: "\"relativePath\":\"Book\""},
	} {
		request := httptest.NewRequest(http.MethodGet, test.target, nil)
		request.Header.Set("Authorization", "Bearer "+pair.AccessToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", test.target, response.Code, response.Body.String())
		}
		if body := response.Body.String(); test.target == "/v1/roots" && body != test.want {
			t.Fatalf("%s body = %q, want %q", test.target, body, test.want)
		} else if test.target != "/v1/roots" && !strings.Contains(body, test.want) {
			t.Fatalf("%s body = %q, want substring %q", test.target, body, test.want)
		}
	}

	manifestRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/transfers/manifest",
		strings.NewReader(
			`{"selections":[{"rootId":"audiobooks","relativePath":"Book","kind":"folder"}]}`,
		),
	)
	manifestRequest.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	manifestRequest.Header.Set("Content-Type", "application/json")
	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, manifestRequest)
	if manifestResponse.Code != http.StatusOK ||
		!strings.Contains(manifestResponse.Body.String(), `"fileId"`) ||
		!strings.Contains(manifestResponse.Body.String(), `"destinationPath":"Book/chapter.mp3"`) {
		t.Fatalf(
			"manifest response = %d %s",
			manifestResponse.Code,
			manifestResponse.Body.String(),
		)
	}
}

func TestOpenCatalogDatabaseMigratesAndSyncsConfiguredRoots(t *testing.T) {
	adminPool := testpostgres.Open(t)
	connection := adminPool.Config().ConnConfig
	passwordPath := filepath.Join(t.TempDir(), "database_password")
	if err := os.WriteFile(passwordPath, []byte(connection.Password), 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := openCatalogDatabase(context.Background(), config.Config{
		Roots: []config.Root{{
			ID:   "audiobooks",
			Name: "Audiobooks",
		}},
		Database: config.Database{
			Host:         connection.Host,
			Port:         connection.Port,
			Name:         connection.Database,
			Username:     connection.User,
			PasswordFile: passwordPath,
			SSLMode:      "disable",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pool == nil {
		t.Fatal("openCatalogDatabase() returned a nil pool for configured PostgreSQL")
	}
	defer pool.Close()

	var tableExists, rootExists bool
	if err := pool.QueryRow(context.Background(), `
		select to_regclass('public.books') is not null,
		       exists(select 1 from library_roots where id='audiobooks')
	`).Scan(&tableExists, &rootExists); err != nil {
		t.Fatal(err)
	}
	if !tableExists || !rootExists {
		t.Fatalf("startup migration table=%v root=%v", tableExists, rootExists)
	}
}

func TestBuildHandlerWiresPostgresCatalogIntoAuthenticatedBooksRoute(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := catalog.NewPostgresRepository(pool)
	if err := repository.SyncRoots(ctx, []catalog.LibraryRoot{{
		ID: "audiobooks", DisplayName: "Audiobooks",
	}}); err != nil {
		t.Fatal(err)
	}
	bookID := uuid.New()
	if _, err := pool.Exec(ctx, `
		insert into books(
			id,root_id,source_folder,grouping_key,title,authors,
			metadata_source,confidence,review_state
		)
		values($1,'audiobooks','Book','folder-key','Dune',
		       array['Frank Herbert'],'local',0.8,'unreviewed')
	`, bookID); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	hash, err := auth.HashPassword("secret", auth.ArgonParams{
		MemoryKiB: 64, Iterations: 1, Parallelism: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, manager, err := buildHandler(config.Config{
		Roots: []config.Root{{
			ID: "audiobooks", Name: "Audiobooks", ContainerPath: root,
		}},
		Auth: config.Auth{Username: "owner"},
	}, config.Secrets{
		PasswordHash: hash, SessionKey: []byte(strings.Repeat("k", 32)),
	}, "test", slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), pool)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := manager.Issue("owner")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/books", nil)
	request.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), bookID.String()) ||
		!strings.Contains(response.Body.String(), `"filters"`) {
		t.Fatalf("books = %d %s", response.Code, response.Body)
	}
}

func writeServerConfig(t *testing.T, root, cert, tlsKey, hash, sessionKey string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	contents := fmt.Sprintf(`
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: %q}
tls:
  cert_path: %q
  key_path: %q
  hosts: [localhost, 127.0.0.1]
auth:
  username: owner
  password_hash_file: %q
  session_key_file: %q
`, filepath.ToSlash(root), filepath.ToSlash(cert), filepath.ToSlash(tlsKey),
		filepath.ToSlash(hash), filepath.ToSlash(sessionKey))
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

# BookBox Server Transfer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the authenticated HTTPS server that browses configured roots and streams verified, resumable transfer manifests without changing source files.

**Architecture:** A Go 1.26.5 executable uses `net/http`, a small domain layer, and read-only filesystem adapters. Docker mounts the audiobook root read-only for this process. PostgreSQL starts in Compose for the next plan but the transfer server does not depend on catalog tables yet.

**Tech Stack:** Go 1.26.5, Go standard library, `pgx/v5`, `golang-jwt/jwt/v5`, `x/crypto/argon2`, YAML v3, Docker Compose, PostgreSQL 18.

## Global Constraints

- Repository path: `C:\Repositories\BookBox`.
- Primary device: Samsung Galaxy S23 Ultra running Android 16 (API 36).
- The Android-facing service can read source files; it cannot upload, rename, move, delete, edit tags, or change audio content.
- `H:\Audiobooks` maps to `/library/audiobooks` with read-only access in the API container.
- PostgreSQL is the only application database; do not add SQLite or Room.
- Version one uses manual server address entry and HTTPS port `8443`.
- Tokens last 15 minutes; refresh tokens last 30 days.
- Clients send root IDs and relative paths, never absolute Windows paths.
- Raw folder copying preserves the selected folder name and recursive contents.

---

### Task 1: Repository and API Skeleton

**Files:**
- Create: `.gitignore`
- Create: `server/go.mod`
- Create: `server/internal/config/config.go`
- Create: `server/internal/config/config_test.go`
- Create: `server/internal/httpapi/api.go`
- Create: `server/internal/httpapi/api_test.go`
- Create: `server/cmd/api/main.go`
- Create: `deploy/config.example.yml`
- Create: `testdata/library/Author One/Book One/chapter01.mp3`

**Interfaces:**
- Produces: `config.Load(path string) (config.Config, error)`
- Produces: `httpapi.New(httpapi.Dependencies) http.Handler`
- Produces: `GET /v1/health -> {"status":"ok","version":"dev"}`

- [ ] **Step 1: Write failing configuration tests**

```go
func TestLoadRequiresUniqueSafeRootIDs(t *testing.T) {
    path := writeConfig(t, `
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/a}
  - {id: audiobooks, name: Duplicate, container_path: /library/b}
`)
    _, err := Load(path)
    if !errors.Is(err, ErrDuplicateRootID) {
        t.Fatalf("Load() error = %v, want ErrDuplicateRootID", err)
    }
}

func TestLoadAcceptsAudiobookRoot(t *testing.T) {
    path := writeConfig(t, `
listen: ":8443"
roots:
  - {id: audiobooks, name: Audiobooks, container_path: /library/audiobooks}
`)
    cfg, err := Load(path)
    if err != nil || cfg.Roots[0].ID != "audiobooks" {
        t.Fatalf("Load() = %#v, %v", cfg, err)
    }
}
```

- [ ] **Step 2: Run the configuration test and verify RED**

Run: `cd server; go test ./internal/config -run TestLoad -v`

Expected: FAIL because package `internal/config` has no implementation.

- [ ] **Step 3: Implement strict configuration loading**

```go
type Root struct {
    ID            string `yaml:"id"`
    Name          string `yaml:"name"`
    ContainerPath string `yaml:"container_path"`
}

type Config struct {
    Listen string `yaml:"listen"`
    Roots  []Root `yaml:"roots"`
}

var ErrDuplicateRootID = errors.New("duplicate root id")

func Load(path string) (Config, error) {
    raw, err := os.ReadFile(path)
    if err != nil { return Config{}, err }
    var cfg Config
    dec := yaml.NewDecoder(bytes.NewReader(raw))
    dec.KnownFields(true)
    if err := dec.Decode(&cfg); err != nil { return Config{}, err }
    if cfg.Listen == "" || len(cfg.Roots) == 0 { return Config{}, errors.New("listen and roots are required") }
    seen := map[string]struct{}{}
    for _, root := range cfg.Roots {
        if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`).MatchString(root.ID) {
            return Config{}, fmt.Errorf("unsafe root id %q", root.ID)
        }
        if _, ok := seen[root.ID]; ok { return Config{}, ErrDuplicateRootID }
        seen[root.ID] = struct{}{}
        if !filepath.IsAbs(root.ContainerPath) { return Config{}, fmt.Errorf("root %q path must be absolute", root.ID) }
    }
    return cfg, nil
}
```

- [ ] **Step 4: Write and run a failing health-handler test**

```go
func TestHealth(t *testing.T) {
    rr := httptest.NewRecorder()
    New(Dependencies{Version: "test"}).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
    if rr.Code != http.StatusOK || rr.Body.String() != "{\"status\":\"ok\",\"version\":\"test\"}\n" {
        t.Fatalf("response = %d %q", rr.Code, rr.Body.String())
    }
}
```

Run: `cd server; go test ./internal/httpapi -run TestHealth -v`

Expected: FAIL because `New` is undefined.

- [ ] **Step 5: Implement the API constructor and executable**

```go
type Dependencies struct{ Version string }

func New(deps Dependencies) http.Handler {
    mux := http.NewServeMux()
    mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": deps.Version})
    })
    return mux
}
```

`main.go` must parse `-config`, load YAML, construct dependencies, and call `http.Server.ListenAndServeTLS` with paths supplied by the later certificate task. Until Task 3, use `http.Server.ListenAndServe` only under `BOOKBOX_INSECURE_TEST_HTTP=1`; otherwise return a startup error stating that TLS is not configured.

- [ ] **Step 6: Add pinned module versions and sample configuration**

```go
module bookbox.local/server

go 1.26.5

require (
    github.com/golang-jwt/jwt/v5 v5.3.0
    github.com/jackc/pgx/v5 v5.7.6
    golang.org/x/crypto v0.41.0
    gopkg.in/yaml.v3 v3.0.1
)
```

```yaml
listen: ":8443"
roots:
  - id: audiobooks
    name: Audiobooks
    container_path: /library/audiobooks
```

- [ ] **Step 7: Verify and commit**

Run: `cd server; go mod tidy; go test ./...`

Expected: PASS with no warnings.

```bash
git add .gitignore server deploy/config.example.yml testdata
git commit -m "feat(server): scaffold configured API"
```

### Task 2: Root Confinement and Raw Browsing

**Files:**
- Create: `server/internal/library/path.go`
- Create: `server/internal/library/path_test.go`
- Create: `server/internal/library/browser.go`
- Create: `server/internal/library/browser_test.go`
- Create: `server/internal/httpapi/browse.go`
- Modify: `server/internal/httpapi/api.go`
- Test: `server/internal/httpapi/browse_test.go`

**Interfaces:**
- Consumes: `config.Root`
- Produces: `library.NewResolver([]config.Root) (*library.Resolver, error)`
- Produces: `(*library.Resolver).Resolve(rootID, relativePath string) (library.ResolvedPath, error)`
- Produces: `(*library.Browser).List(context.Context, rootID, relativePath string) (library.Directory, error)`
- Produces: `GET /v1/roots` and `GET /v1/browse?root=audiobooks&path=Author%20One`

- [ ] **Step 1: Write confinement tests**

```go
func TestResolveRejectsEscapes(t *testing.T) {
    resolver := newTestResolver(t)
    for _, path := range []string{"../secret", "/absolute", `C:\Windows`, "%2e%2e/secret"} {
        t.Run(path, func(t *testing.T) {
            if _, err := resolver.Resolve("audiobooks", path); !errors.Is(err, ErrPathEscape) {
                t.Fatalf("Resolve(%q) error = %v", path, err)
            }
        })
    }
}

func TestResolveRejectsEscapingSymlink(t *testing.T) {
    root, outside := makeRootAndOutside(t)
    requireSymlink(t, outside, filepath.Join(root, "escape"))
    resolver := resolverFor(t, root)
    if _, err := resolver.Resolve("audiobooks", "escape/file.mp3"); !errors.Is(err, ErrPathEscape) {
        t.Fatalf("error = %v", err)
    }
}
```

- [ ] **Step 2: Run the tests and verify RED**

Run: `cd server; go test ./internal/library -run TestResolve -v`

Expected: FAIL because `Resolver` is undefined.

- [ ] **Step 3: Implement canonical root confinement**

```go
type ResolvedPath struct {
    RootID   string
    Relative string
    Absolute string
}

func (r *Resolver) Resolve(rootID, relative string) (ResolvedPath, error) {
    root, ok := r.roots[rootID]
    if !ok { return ResolvedPath{}, ErrUnknownRoot }
    decoded, err := url.PathUnescape(relative)
    if err != nil || filepath.IsAbs(decoded) || filepath.VolumeName(decoded) != "" {
        return ResolvedPath{}, ErrPathEscape
    }
    clean := filepath.Clean(filepath.FromSlash(decoded))
    if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
        return ResolvedPath{}, ErrPathEscape
    }
    candidate := filepath.Join(root.canonical, clean)
    canonical, err := filepath.EvalSymlinks(candidate)
    if err != nil { return ResolvedPath{}, err }
    rel, err := filepath.Rel(root.canonical, canonical)
    if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
        return ResolvedPath{}, ErrPathEscape
    }
    return ResolvedPath{RootID: rootID, Relative: filepath.ToSlash(rel), Absolute: canonical}, nil
}
```

- [ ] **Step 4: Write browser sorting and privacy tests**

```go
func TestListSortsFoldersFirstWithoutAbsolutePaths(t *testing.T) {
    dir := createTree(t, "z.mp3", "A Book/chapter.mp3", "b Book/chapter.mp3")
    got, err := newBrowser(t, dir).List(context.Background(), "audiobooks", "")
    if err != nil { t.Fatal(err) }
    want := []string{"A Book", "b Book", "z.mp3"}
    if actual := entryNames(got.Entries); !slices.Equal(want, actual) { t.Fatalf("names = %v, want %v", actual, want) }
    encoded, _ := json.Marshal(got)
    if bytes.Contains(encoded, []byte(dir)) { t.Fatal("response leaked absolute path") }
}
```

- [ ] **Step 5: Implement browser DTOs and HTTP handlers**

```go
type Entry struct {
    Name         string    `json:"name"`
    RelativePath string    `json:"relativePath"`
    Kind         string    `json:"kind"`
    Size         int64     `json:"size,omitempty"`
    ModifiedAt   time.Time `json:"modifiedAt"`
}

type Directory struct {
    RootID string  `json:"rootId"`
    Path   string  `json:"path"`
    Entries []Entry `json:"entries"`
}
```

Use `os.ReadDir`, `DirEntry.Info`, and `sort.SliceStable`. Sort directories before files, then compare `strings.ToLower(Name)`, then the original name. Map `ErrUnknownRoot`, `ErrPathEscape`, and `os.ErrNotExist` to JSON errors with HTTP 404, 400, and 404.

- [ ] **Step 6: Verify API immutability and commit**

Run: `cd server; go test ./internal/library ./internal/httpapi -v`

Expected: PASS. A route-table test must assert that no registered method/path contains `upload`, `delete`, `rename`, `move`, or `tags`.

```bash
git add server/internal/library server/internal/httpapi
git commit -m "feat(server): add confined read-only browsing"
```

### Task 3: TLS, Password Login, and Remembered Sessions

**Files:**
- Create: `server/internal/auth/password.go`
- Create: `server/internal/auth/password_test.go`
- Create: `server/internal/auth/tokens.go`
- Create: `server/internal/auth/tokens_test.go`
- Create: `server/internal/auth/limiter.go`
- Create: `server/internal/tlsidentity/certificate.go`
- Create: `server/internal/tlsidentity/certificate_test.go`
- Create: `server/internal/httpapi/session.go`
- Create: `server/internal/httpapi/session_test.go`
- Create: `server/cmd/bookbox-admin/main.go`
- Modify: `server/internal/config/config.go`
- Modify: `server/cmd/api/main.go`

**Interfaces:**
- Produces: `auth.HashPassword(password string, params auth.ArgonParams) (string, error)`
- Produces: `auth.VerifyPassword(encoded, candidate string) (bool, error)`
- Produces: `(*auth.TokenManager).Issue(subject string) (auth.Pair, error)`
- Produces: `(*auth.TokenManager).Refresh(refreshToken string) (auth.Pair, error)`
- Produces: `tlsidentity.Ensure(certPath, keyPath string, hosts []string) (tlsidentity.Identity, error)`
- Produces: `POST /v1/session` and `POST /v1/session/refresh`

- [ ] **Step 1: Write failing Argon2id tests**

```go
func TestPasswordHashRoundTrip(t *testing.T) {
    encoded, err := HashPassword("correct horse", TestArgonParams)
    if err != nil { t.Fatal(err) }
    ok, err := VerifyPassword(encoded, "correct horse")
    if err != nil || !ok { t.Fatalf("VerifyPassword() = %v, %v", ok, err) }
    wrong, _ := VerifyPassword(encoded, "wrong")
    if wrong { t.Fatal("wrong password accepted") }
}
```

- [ ] **Step 2: Verify RED, then implement the encoded Argon2id format**

Run: `cd server; go test ./internal/auth -run TestPassword -v`

Expected: FAIL because `HashPassword` is undefined.

Use encoded hashes in this exact form:

```text
$argon2id$v=19$m=65536,t=3,p=2$<base64-salt>$<base64-hash>
```

Use a 16-byte random salt, 32-byte key, `subtle.ConstantTimeCompare`, and production parameters of 64 MiB, 3 iterations, and parallelism 2. The admin command `bookbox-admin hash-password` reads the password twice from the console without echo and prints only the encoded hash.

- [ ] **Step 3: Write failing token tests**

```go
func TestRefreshRejectsAccessTokenAndRotationInvalidatesSessions(t *testing.T) {
    clock := newFakeClock()
    tm := NewTokenManager([]byte("01234567890123456789012345678901"), clock.Now)
    pair, _ := tm.Issue("owner")
    if _, err := tm.Refresh(pair.AccessToken); !errors.Is(err, ErrWrongTokenKind) { t.Fatal(err) }
    tm.Rotate([]byte("abcdefghijklmnopqrstuvwxyz123456"))
    if _, err := tm.Refresh(pair.RefreshToken); !errors.Is(err, ErrInvalidToken) { t.Fatal(err) }
}
```

- [ ] **Step 4: Implement signed access and refresh tokens**

```go
type Claims struct {
    Kind string `json:"kind"`
    jwt.RegisteredClaims
}

type Pair struct {
    AccessToken  string    `json:"accessToken"`
    RefreshToken string    `json:"refreshToken"`
    AccessExpiry time.Time `json:"accessExpiry"`
}
```

Sign with HS256, set issuer `bookbox`, audience `bookbox-android`, random `jti`, access expiry `now+15m`, and refresh expiry `now+30*24h`. Accept only HS256 and the expected token kind.

- [ ] **Step 5: Test login throttling and HTTP responses**

```go
func TestLoginRateLimit(t *testing.T) {
    api := newSessionAPI(t, "owner", hashForTest(t, "secret"))
    for i := 0; i < 5; i++ { postLogin(t, api, "owner", "wrong", http.StatusUnauthorized) }
    rr := postLogin(t, api, "owner", "wrong", http.StatusTooManyRequests)
    if rr.Header().Get("Retry-After") == "" { t.Fatal("missing Retry-After") }
}
```

Implement a bounded in-memory limiter keyed by normalized username plus `RemoteAddr` IP. Allow five failures in ten minutes, reset after a successful login, and cap the map at 10,000 keys.

- [ ] **Step 6: Test and implement persistent self-signed identity**

```go
func TestEnsureReusesCertificateAndReturnsFingerprint(t *testing.T) {
    cert, key := tempIdentityPaths(t)
    first, err := Ensure(cert, key, []string{"localhost"})
    if err != nil { t.Fatal(err) }
    second, err := Ensure(cert, key, []string{"localhost"})
    if err != nil || first.SHA256Fingerprint != second.SHA256Fingerprint {
        t.Fatalf("identities differ: %#v %#v", first, second)
    }
}
```

Generate an ECDSA P-256 key, a random 128-bit serial, SANs for supplied hostnames/IPs, and a five-year certificate. Write key mode `0600` and certificate mode `0644` with create-exclusive temporary files followed by rename. Format the fingerprint as uppercase colon-separated hexadecimal.

- [ ] **Step 7: Wire mandatory TLS and authenticated middleware**

`main.go` must call `tlsidentity.Ensure`, print `BookBox certificate SHA-256: <fingerprint>`, and serve TLS. Remove the temporary insecure HTTP branch. Add middleware that accepts `Authorization: Bearer <access token>` for all routes except health and session creation/refresh.

Extend `config.Config` with required `tls.cert_path`, `tls.key_path`, `tls.hosts`, `auth.username`, `auth.password_hash_file`, and `auth.session_key_file` fields. Read the hash and 32-byte session key from their files at startup; reject missing, empty, or group/world-readable secret files inside the Linux container.

- [ ] **Step 8: Verify and commit**

Run: `cd server; go test ./internal/auth ./internal/tlsidentity ./internal/httpapi -v`

Expected: PASS, including tests that JSON logs redact authorization headers and passwords.

```bash
git add server
git commit -m "feat(server): secure API sessions with pinned TLS"
```

### Task 4: Transfer Manifests, Stable File IDs, and Range Streaming

**Files:**
- Create: `server/internal/transfer/types.go`
- Create: `server/internal/transfer/manifest.go`
- Create: `server/internal/transfer/manifest_test.go`
- Create: `server/internal/transfer/fileid.go`
- Create: `server/internal/transfer/fileid_test.go`
- Create: `server/internal/httpapi/transfers.go`
- Create: `server/internal/httpapi/transfers_test.go`
- Modify: `server/internal/httpapi/api.go`

**Interfaces:**
- Produces: `transfer.Builder.Build(ctx, transfer.Request) (transfer.Manifest, error)`
- Produces: `transfer.FileSigner.Sign(transfer.FileIdentity) (string, error)`
- Produces: `transfer.FileSigner.Verify(string) (transfer.FileIdentity, error)`
- Produces: `POST /v1/transfers/manifest`
- Produces: `GET /v1/files/{fileId}` with `Range`, `ETag`, `Accept-Ranges`, and `Digest`

- [ ] **Step 1: Write manifest path and digest tests**

```go
func TestManifestPreservesSelectedFolderAndHashesFiles(t *testing.T) {
    root := createTree(t, "Author/Book/chapter1.mp3", "Author/Book/cover.jpg")
    b := newBuilder(t, root)
    got, err := b.Build(context.Background(), Request{
        Selections: []Selection{{RootID: "audiobooks", RelativePath: "Author/Book", Kind: "folder"}},
    })
    if err != nil { t.Fatal(err) }
    if got.Files[0].DestinationPath != "Book/chapter1.mp3" { t.Fatalf("%#v", got.Files) }
    if got.Files[0].SHA256 == "" || got.TotalBytes == 0 { t.Fatalf("%#v", got) }
}
```

- [ ] **Step 2: Verify RED and define transfer DTOs**

Run: `cd server; go test ./internal/transfer -run TestManifest -v`

Expected: FAIL because `Builder` is undefined.

```go
type Selection struct {
    RootID       string `json:"rootId"`
    RelativePath string `json:"relativePath"`
    Kind         string `json:"kind"`
}

type Request struct{ Selections []Selection `json:"selections"` }

type ManifestFile struct {
    FileID          string    `json:"fileId"`
    SourcePath      string    `json:"sourcePath"`
    DestinationPath string    `json:"destinationPath"`
    Size            int64     `json:"size"`
    ModifiedAt      time.Time `json:"modifiedAt"`
    ETag            string    `json:"etag"`
    SHA256          string    `json:"sha256"`
}

type Manifest struct {
    ID         string         `json:"id"`
    Files      []ManifestFile `json:"files"`
    TotalBytes int64          `json:"totalBytes"`
}
```

- [ ] **Step 3: Implement recursive manifest building**

Use `filepath.WalkDir` after resolving each selection. Reject non-folder raw selections in version one. Place the selected folder's base name at the destination root. Sort by destination path. Stream SHA-256 with a 1 MiB buffer and cancel on `ctx.Done()`. De-duplicate source files selected through overlapping folders.

- [ ] **Step 4: Write signed file-ID tampering tests**

```go
func TestFileIDRejectsTampering(t *testing.T) {
    signer := NewFileSigner(bytes.Repeat([]byte{7}, 32))
    id, _ := signer.Sign(FileIdentity{RootID: "audiobooks", Path: "Book/a.mp3", Size: 4, ModUnixNano: 10})
    raw := []byte(id)
    raw[len(raw)-2] ^= 1
    if _, err := signer.Verify(string(raw)); !errors.Is(err, ErrInvalidFileID) { t.Fatal(err) }
}
```

Encode a versioned JSON identity with base64url and append an HMAC-SHA256 signature. Do not place an absolute path in the identity.

- [ ] **Step 5: Write range and changed-source HTTP tests**

```go
func TestFileEndpointSupportsRangeAndRejectsChangedSource(t *testing.T) {
    api, fileID := transferAPI(t, []byte("0123456789"))
    rr := serve(t, api, "GET", "/v1/files/"+url.PathEscape(fileID), map[string]string{"Range": "bytes=3-5"})
    if rr.Code != http.StatusPartialContent || rr.Body.String() != "345" { t.Fatalf("%d %q", rr.Code, rr.Body) }
    mutateSource(t)
    rr = serve(t, api, "GET", "/v1/files/"+url.PathEscape(fileID), nil)
    if rr.Code != http.StatusConflict { t.Fatalf("status = %d", rr.Code) }
}
```

- [ ] **Step 6: Implement HTTP manifest and file streaming**

Limit manifest requests to 100 selections and one concurrent hash build per authenticated user. Set request body limit to 1 MiB. Re-resolve and re-stat the file before serving. Compare size and nanosecond mtime with the signed identity, set the quoted ETag, `Accept-Ranges: bytes`, and RFC 3230 `Digest: sha-256=<base64>`, then call `http.ServeContent`.

- [ ] **Step 7: Verify read-only behavior and commit**

Run: `cd server; go test ./internal/transfer ./internal/httpapi -v`

Expected: PASS. A before/after tree digest test must prove that manifest and file endpoints changed no source byte, name, or timestamp.

```bash
git add server/internal/transfer server/internal/httpapi
git commit -m "feat(server): add verified resumable downloads"
```

### Task 5: Containerized Server Deliverable

**Files:**
- Create: `deploy/Dockerfile.api`
- Create: `deploy/compose.yml`
- Create: `deploy/.env.example`
- Create: `deploy/secrets/.gitkeep`
- Create: `deploy/scripts/smoke-server.ps1`
- Create: `docs/setup-server.md`
- Modify: `.gitignore`
- Modify: `server/cmd/api/main.go`

**Interfaces:**
- Consumes: `BOOKBOX_LIBRARY_PATH`, `POSTGRES_PASSWORD_FILE`, `BOOKBOX_PASSWORD_HASH_FILE`, `BOOKBOX_SESSION_KEY_FILE`
- Produces: `docker compose -f deploy/compose.yml up --build`
- Produces: a healthy API at `https://localhost:8443/v1/health`

- [ ] **Step 1: Write the Docker smoke script before the image**

```powershell
$ErrorActionPreference = 'Stop'
$compose = Join-Path $PSScriptRoot '..\compose.yml'
docker compose -f $compose up -d --build postgres bookbox-api
try {
    $status = docker inspect --format '{{.State.Health.Status}}' bookbox-api
    if ($status -ne 'healthy') { throw "API health is $status" }
    $mounts = docker inspect --format '{{json .Mounts}}' bookbox-api
    if ($mounts -notmatch '"RW":false') { throw 'Audiobook mount is not read-only' }
} finally {
    docker compose -f $compose down
}
```

- [ ] **Step 2: Run the smoke script and verify RED**

Run: `powershell -ExecutionPolicy Bypass -File deploy/scripts/smoke-server.ps1`

Expected: FAIL because `deploy/compose.yml` does not exist.

- [ ] **Step 3: Add the multi-stage API image**

```dockerfile
FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bookbox-api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bookbox-admin ./cmd/bookbox-admin

FROM alpine:3.22
RUN addgroup -S bookbox && adduser -S -G bookbox bookbox
COPY --from=build /out/bookbox-api /usr/local/bin/bookbox-api
COPY --from=build /out/bookbox-admin /usr/local/bin/bookbox-admin
USER bookbox:bookbox
ENTRYPOINT ["bookbox-api", "-config", "/etc/bookbox/config.yml"]
```

- [ ] **Step 4: Add Compose with a read-only API mount**

```yaml
services:
  postgres:
    image: postgres:18-alpine
    environment:
      POSTGRES_DB: bookbox
      POSTGRES_USER: bookbox
      POSTGRES_PASSWORD_FILE: /run/secrets/postgres_password
    secrets: [postgres_password]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U bookbox -d bookbox"]
      interval: 5s
      timeout: 3s
      retries: 20
  bookbox-api:
    build:
      context: ..
      dockerfile: deploy/Dockerfile.api
    ports: ["8443:8443"]
    volumes:
      - type: bind
        source: ${BOOKBOX_LIBRARY_PATH}
        target: /library/audiobooks
        read_only: true
      - tls_data:/var/lib/bookbox/tls
      - ./config.yml:/etc/bookbox/config.yml:ro
    secrets: [app_password_hash, session_key]
    read_only: true
    tmpfs: ["/tmp"]
    cap_drop: ["ALL"]
    security_opt: ["no-new-privileges:true"]
    depends_on:
      postgres: {condition: service_healthy}
    healthcheck:
      test: ["CMD", "wget", "--no-check-certificate", "-qO-", "https://localhost:8443/v1/health"]
      interval: 10s
      timeout: 3s
      retries: 12
secrets:
  postgres_password: {file: ./secrets/postgres_password.txt}
  app_password_hash: {file: ./secrets/app_password_hash.txt}
  session_key: {file: ./secrets/session_key.bin}
volumes:
  tls_data:
```

Extend the final file with app password-hash and session-key secrets. Keep all live secret files ignored while retaining `deploy/secrets/.gitkeep`.

- [ ] **Step 5: Document exact Windows setup**

`docs/setup-server.md` must include these commands with their expected outputs:

```powershell
Copy-Item deploy\.env.example deploy\.env
docker compose -f deploy\compose.yml build
docker compose -f deploy\compose.yml run --rm --entrypoint bookbox-admin bookbox-api hash-password
docker compose -f deploy\compose.yml up -d
docker compose -f deploy\compose.yml ps
```

Document Docker Desktop drive access, `BOOKBOX_LIBRARY_PATH=H:/Audiobooks`, private-profile firewall port `8443`, certificate fingerprint comparison, stable DHCP reservation, stop/start, and log inspection.

- [ ] **Step 6: Run all server checks**

Run: `cd server; go test -race ./...`

Expected: PASS.

Run: `docker compose -f deploy/compose.yml config`

Expected: valid resolved configuration with the API audiobook mount marked read-only.

Run: `powershell -ExecutionPolicy Bypass -File deploy/scripts/smoke-server.ps1`

Expected: PASS and all owned containers removed by the script.

- [ ] **Step 7: Commit the server deliverable**

```bash
git add .gitignore deploy docs/setup-server.md server
git commit -m "feat(server): ship read-only Docker transfer service"
```

## Plan Completion Check

Run:

```powershell
Set-Location server
go test -race ./...
Set-Location ..
docker compose -f deploy/compose.yml config
powershell -ExecutionPolicy Bypass -File deploy/scripts/smoke-server.ps1
git status --short
```

Expected: tests and smoke test pass, Compose validates, no owned containers remain, and the working tree is clean.

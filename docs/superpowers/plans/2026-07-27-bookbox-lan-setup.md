# BookBox LAN Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the complete product to BookBox, replace certificate-pinned HTTPS with private-LAN HTTP, and make the server reachable as `bookbox.local` through a headless Windows mDNS process.

**Architecture:** The Go API serves authenticated HTTP on container port `8787`. Android stores a clean HTTP origin and username, then uses the existing access/refresh-token flow without certificate state. A separate Windows Go executable proxy-registers `bookbox.local` and `_bookbox._tcp.local` for the selected LAN address and port. PowerShell deployment scripts own that process and restrict the inbound firewall rule to the private profile and local subnet.

**Tech Stack:** Go 1.26.5, `github.com/enbility/zeroconf/v3`, Kotlin, Jetpack Compose, OkHttp 5.3.0, Android API 36, Docker Desktop and Compose, PostgreSQL 18, PowerShell.

## Global Constraints

- Rename product and technical identifiers from netStorage/netstorage/NETSTORAGE to BookBox/bookbox/BOOKBOX.
- Keep the repository directory unchanged during implementation.
- Treat this release as a clean Android and Docker reinstall boundary; do not silently migrate old tokens, preferences, containers, volumes, or database state.
- Preserve the Windows audiobook source and the API's read-only library mount.
- Serve HTTP only on default port `8787`; reject HTTPS addresses in Android for this release.
- Default Android input to `bookbox.local`, normalized to `http://bookbox.local:8787/`.
- Bind production access to the selected private-LAN interface and scope the Windows Firewall rule to the private profile and local subnet.
- Do not add router port forwarding, WAN access, QR pairing, device approval, or tray UI.
- Keep QR-based HTTPS pairing and the Windows tray approval companion in the roadmap only.
- Preserve Argon2id password verification, rate limiting, access tokens, refresh tokens, and Android Keystore token storage.
- Keep PostgreSQL as the only application database; do not add Room or SQLite.
- Run at most one Gradle, Go full-suite, Compose E2E, or release-gate process at a time.

---

### Task 1: Establish the BookBox Identity Boundary

**Files:**
- Create: `e2e/tests/bookbox-branding.tests.ps1`
- Modify: `server/go.mod`
- Move: `server/cmd/netstorage-admin` to `server/cmd/bookbox-admin`
- Move: `server/cmd/api` to `server/cmd/bookbox-api`
- Move: `server/cmd/catalog-worker` to `server/cmd/bookbox-catalog-worker`
- Move: `android/app/src/main/java/com/rendref/netstorage` to `android/app/src/main/java/com/rendref/bookbox`
- Move: `android/app/src/test/java/com/rendref/netstorage` to `android/app/src/test/java/com/rendref/bookbox`
- Move: `android/app/src/androidTest/java/com/rendref/netstorage` to `android/app/src/androidTest/java/com/rendref/bookbox`
- Modify: all runtime, deployment, E2E, test, and documentation files returned by `rg -l -i "netstorage"`
- Modify: `android/settings.gradle.kts`
- Modify: `android/app/build.gradle.kts`
- Modify: `android/app/src/main/AndroidManifest.xml`
- Modify: `android/app/src/androidTest/AndroidManifest.xml`
- Modify: `android/app/src/main/res/values/strings.xml`

**Interfaces:**
- Produces: Go module `bookbox.local/server`
- Produces: Android application ID and namespace `com.rendref.bookbox`
- Produces: `bookbox-api`, `bookbox-catalog-worker`, `bookbox-admin`, and `bookbox-discovery` command/resource naming
- Produces: `BOOKBOX_*` environment and signing variable prefix

- [ ] **Step 1: Write a failing branding gate**

Create `e2e/tests/bookbox-branding.tests.ps1`:

```powershell
$ErrorActionPreference = 'Stop'
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$runtimeRoots = @('android', 'server', 'deploy', 'e2e', 'README.md', 'RELEASE_NOTES.md')
$allowed = @(
    'docs\migration-from-netstorage.md',
    'docs\superpowers\specs\2026-07-27-bookbox-lan-setup-design.md',
    'docs\superpowers\plans\2026-07-27-bookbox-lan-setup.md'
)
$matches = & rg -n -i 'netstorage' @runtimeRoots
$unexpected = @($matches | Where-Object {
    $line = $_
    -not ($allowed | Where-Object { $line.StartsWith($_ + ':') })
})
if ($unexpected.Count -gt 0) {
    throw "Legacy product identifiers remain:`n$($unexpected -join "`n")"
}
$gradle = Get-Content -Raw (Join-Path $repo 'android\app\build.gradle.kts')
if ($gradle -notmatch 'applicationId = "com\.rendref\.bookbox"') {
    throw 'Android application ID is not com.rendref.bookbox.'
}
Write-Output 'PASS: BookBox product and technical identity is consistent.'
```

- [ ] **Step 2: Run the branding gate and verify RED**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\bookbox-branding.tests.ps1
```

Expected: FAIL with legacy identifiers in Android, Go, Compose, scripts, and docs.

- [ ] **Step 3: Apply the mechanical identity mapping**

Use this exact mapping across non-historical runtime content:

```text
netStorage                 -> BookBox
NetStorage                 -> BookBox
netstorage                 -> bookbox
NETSTORAGE                 -> BOOKBOX
com.rendref.netstorage     -> com.rendref.bookbox
netstorage.local/server    -> bookbox.local/server
```

Rename the three Android source trees and all three Go command directories after
replacement. Update manifest authorities/actions, DataStore file names,
Keystore aliases, notification channels, temporary-file stems, Docker resource
names, database/user names, secrets, artifact names, and script function names.
Do not alter user audiobook filenames containing the old text.

Rename all three Go command directories in the Files list. Change clean-break
identity domains together: JWT issuer/audience, signed file-ID domains, scanner
UUID namespace, provider user agent, cover temporary/backup stems, test database
prefix, worker environment variables, and default BookBox data paths.

- [ ] **Step 4: Verify the renamed trees compile**

Run serially:

```powershell
Set-Location server
go test -mod=readonly -p=1 ./...
Set-Location ..\android
.\gradlew.bat testDebugUnitTest --no-parallel --max-workers=1
```

Expected: both commands PASS after import/package/path correction.

- [ ] **Step 5: Run the branding gate and commit**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\bookbox-branding.tests.ps1
git diff --check
```

Expected: PASS and no whitespace errors.

Commit:

```powershell
git add android server deploy e2e docs README.md RELEASE_NOTES.md
git commit -m "refactor: rename product to BookBox"
```

### Task 2: Replace Mandatory TLS with Authenticated HTTP

**Files:**
- Modify: `server/internal/config/config.go`
- Modify: `server/internal/config/config_test.go`
- Modify: `server/cmd/bookbox-api/main.go`
- Modify: `server/cmd/bookbox-api/main_test.go`
- Delete: `server/internal/tlsidentity/certificate.go`
- Delete: `server/internal/tlsidentity/certificate_test.go`
- Modify: `deploy/config.example.yml`

**Interfaces:**
- Produces: `Config.Listen` plus roots, auth, and database configuration with no TLS section
- Produces: `serveHTTPFunc func(*http.Server) error`
- Produces: API startup through `http.Server.ListenAndServe()`

- [ ] **Step 1: Write failing configuration tests**

Add tests equivalent to:

```go
func TestLoadAcceptsHTTPConfigurationWithoutTLS(t *testing.T) {
    path := writeConfig(t, `
listen: ":8787"
roots:
  - id: audiobooks
    name: Audiobooks
    container_path: /library/audiobooks
auth:
  username: owner
  password_hash_file: /run/bookbox-secrets/app_password_hash
  session_key_file: /run/bookbox-secrets/session_key
`)
    cfg, err := Load(path)
    if err != nil {
        t.Fatal(err)
    }
    if cfg.Listen != ":8787" {
        t.Fatalf("listen = %q", cfg.Listen)
    }
}

func TestLoadRejectsRemovedTLSSection(t *testing.T) {
    _, err := Load(writeConfig(t, validConfig+`
tls:
  cert_path: /tmp/server.crt
  key_path: /tmp/server.key
  hosts: [bookbox.local]
`))
    if err == nil || !strings.Contains(err.Error(), "field tls not found") {
        t.Fatalf("err = %v", err)
    }
}
```

- [ ] **Step 2: Run the configuration tests and verify RED**

Run:

```powershell
Set-Location server
go test ./internal/config -run 'TestLoadAcceptsHTTPConfigurationWithoutTLS|TestLoadRejectsRemovedTLSSection' -v
```

Expected: FAIL because `tls` remains mandatory.

- [ ] **Step 3: Remove TLS from configuration**

Delete the `TLS` type and `Config.TLS`. Remove certificate/key/host validation. Keep absolute-path validation for password, session, and database secret files.

- [ ] **Step 4: Write a failing API startup test**

Replace the TLS startup assertion with:

```go
func TestRunLoadsSecretsAndServesHTTP(t *testing.T) {
    var served *http.Server
    serve := func(server *http.Server) error {
        served = server
        return nil
    }
    var output bytes.Buffer
    if err := runWith([]string{"-config", writeServerConfig(t)}, serve, &output, testLogger()); err != nil {
        t.Fatal(err)
    }
    if served == nil || served.Addr != ":8787" || served.TLSConfig != nil {
        t.Fatalf("server = %#v", served)
    }
    if strings.Contains(strings.ToLower(output.String()), "certificate") {
        t.Fatalf("unexpected TLS output %q", output.String())
    }
}
```

- [ ] **Step 5: Run the API test and verify RED**

Run:

```powershell
go test ./cmd/api -run TestRunLoadsSecretsAndServesHTTP -v
```

Expected: FAIL because `runWith` still requires certificate identity and `serveTLS`.

- [ ] **Step 6: Serve HTTP and delete TLS identity code**

Use:

```go
type serveHTTPFunc func(*http.Server) error

func run(args []string) error {
    logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
    return runWith(args, serveHTTP, os.Stdout, logger)
}

func serveHTTP(server *http.Server) error {
    return server.ListenAndServe()
}
```

Build `http.Server` without `TLSConfig`, delete `tlsidentity.Ensure`, remove fingerprint output, and remove the TLS identity package.

- [ ] **Step 7: Verify and commit**

Run:

```powershell
go test ./internal/config ./cmd/api -v
go test -mod=readonly -p=1 ./...
go vet -mod=readonly ./...
```

Expected: PASS.

Commit:

```powershell
git add server deploy/config.example.yml
git commit -m "feat(server): serve BookBox over private LAN HTTP"
```

### Task 3: Add the Headless Windows Discovery Process

**Files:**
- Modify: `server/go.mod`
- Modify: `server/go.sum`
- Create: `server/internal/discovery/config.go`
- Create: `server/internal/discovery/config_test.go`
- Create: `server/internal/discovery/service.go`
- Create: `server/internal/discovery/service_test.go`
- Create: `server/cmd/bookbox-discovery/main.go`
- Create: `server/cmd/bookbox-discovery/main_test.go`

**Interfaces:**
- Produces: `discovery.Config{Hostname string, Service string, Domain string, Address net.IP, Port int, PollInterval time.Duration}`
- Produces: `discovery.Run(ctx context.Context, cfg Config, register Registrar, current AddressSource) error`
- Consumes: `zeroconf.RegisterProxy("BookBox", "_bookbox._tcp", "local.", port, "bookbox.local.", []string{address}, text, interfaces)`

- [ ] **Step 1: Write failing validation tests**

```go
func TestConfigValidateAcceptsPrivateIPv4(t *testing.T) {
    cfg := Config{
        Hostname: "bookbox.local.",
        Service: "_bookbox._tcp",
        Domain: "local.",
        Address: net.ParseIP("192.168.1.25"),
        Port: 8787,
        PollInterval: 5 * time.Second,
    }
    if err := cfg.Validate(); err != nil {
        t.Fatal(err)
    }
}

func TestConfigValidateRejectsPublicAndLoopbackAddresses(t *testing.T) {
    for _, raw := range []string{"8.8.8.8", "127.0.0.1"} {
        cfg := validConfig()
        cfg.Address = net.ParseIP(raw)
        if err := cfg.Validate(); err == nil {
            t.Fatalf("%s accepted", raw)
        }
    }
}
```

- [ ] **Step 2: Run validation tests and verify RED**

Run:

```powershell
Set-Location server
go test ./internal/discovery -run TestConfigValidate -v
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement configuration validation**

Require exact hostname `bookbox.local.`, service `_bookbox._tcp`, domain `local.`, port `1..65535`, private non-loopback IPv4, and poll interval between one second and five minutes.

- [ ] **Step 4: Write failing lifecycle tests**

Use fakes to prove first registration, re-registration after an address change, no restart for an unchanged address, and shutdown on context cancellation:

```go
func TestRunReregistersWhenAddressChanges(t *testing.T) {
    addresses := newFakeAddressSource("192.168.1.25", "192.168.1.26")
    registrar := &fakeRegistrar{}
    ctx, cancel := context.WithCancel(context.Background())
    registrar.afterSecondRegistration = cancel
    if err := Run(ctx, validConfig(), registrar, addresses); err != nil {
        t.Fatal(err)
    }
    if diff := cmp.Diff(
        []string{"192.168.1.25", "192.168.1.26"},
        registrar.addresses,
    ); diff != "" {
        t.Fatal(diff)
    }
    if registrar.shutdowns != 2 {
        t.Fatalf("shutdowns = %d", registrar.shutdowns)
    }
}
```

Avoid a new comparison dependency if a direct slice assertion is sufficient.

- [ ] **Step 5: Run lifecycle tests and verify RED**

Run:

```powershell
go test ./internal/discovery -run TestRun -v
```

Expected: FAIL because `Run` and its interfaces do not exist.

- [ ] **Step 6: Implement the registrar adapter**

Define:

```go
type Registration interface {
    Shutdown()
}

type Registrar interface {
    Register(Config) (Registration, error)
}

type AddressSource interface {
    Current(context.Context) (net.IP, error)
}
```

Add the dependency with:

```powershell
go get github.com/enbility/zeroconf/v3@latest
```

Commit the version selected in `go.mod` and `go.sum`. The production adapter
calls `zeroconf.RegisterProxy` with `bookbox.local.`, the exact interface that
owns the configured private address, and TXT records `version=1` and
`scheme=http`. `Run` owns the active registration, polls the selected interface
address, re-registers on change, and shuts down on every return path. Before
registering, perform a bounded lookup and reject an existing `bookbox.local`
record that points to another address.

- [ ] **Step 7: Add the command and signal handling**

The command accepts:

```text
-address <private IPv4>
-port 8787
-hostname bookbox.local
-poll-interval 5s
```

Normalize the hostname to a trailing-dot FQDN, reject positional arguments, use `signal.NotifyContext` for Ctrl+C and service termination, and log only hostname, address, port, and lifecycle events.

- [ ] **Step 8: Verify Windows build and commit**

Run:

```powershell
go test ./internal/discovery ./cmd/bookbox-discovery -v
$env:GOOS='windows'
$env:GOARCH='amd64'
go build -o ..\artifacts\bookbox-discovery.exe ./cmd/bookbox-discovery
Remove-Item Env:GOOS, Env:GOARCH
```

Expected: tests PASS and the Windows executable builds.

Commit:

```powershell
git add server
git commit -m "feat(discovery): advertise bookbox.local on Windows"
```

### Task 4: Rename and Simplify the Windows Deployment

**Files:**
- Modify: `deploy/compose.yml`
- Modify: `deploy/compose.test.yml`
- Modify: `deploy/compose.recovery.test.yml`
- Modify: `deploy/config.example.yml`
- Modify: `deploy/Dockerfile.api`
- Modify: `deploy/Dockerfile.worker`
- Modify: `deploy/entrypoint-api.sh`
- Modify: `deploy/entrypoint-worker.sh`
- Create: `deploy/scripts/discovery-process.ps1`
- Create: `deploy/scripts/discovery-process.tests.ps1`
- Create: `deploy/scripts/install-bookbox-discovery.ps1`
- Create: `deploy/scripts/start-bookbox.ps1`
- Create: `deploy/scripts/stop-bookbox.ps1`
- Modify: every existing script under `deploy/scripts`
- Modify: all static deployment tests under `deploy/scripts` and `e2e/tests`

**Interfaces:**
- Produces: Compose project `bookbox-prod`
- Produces: API service `bookbox-api` on HTTP container/default host port `8787`
- Produces: `Start-BookBoxDiscovery`, `Stop-BookBoxDiscovery`, and `Test-BookBoxDiscovery`
- Consumes: `BOOKBOX_BIND_ADDRESS`, `BOOKBOX_HOST_PORT`, `BOOKBOX_CONFIG_HOST_FILE`, and renamed secret/library variables

- [ ] **Step 1: Write failing Compose/static assertions**

Add assertions equivalent to:

```powershell
$resolved = docker compose -f deploy\compose.yml config --format json |
    ConvertFrom-Json
$api = $resolved.services.'bookbox-api'
if ([string]$api.ports[0].target -cne '8787') { throw 'API target port must be 8787.' }
if ([string]$api.ports[0].published -cne '8787') { throw 'Default host port must be 8787.' }
if ($api.ports[0].name -cne 'http') { throw 'Published port must be named http.' }
if ($api.healthcheck.test -join ' ' -match 'https|certificate|--no-check-certificate') {
    throw 'HTTP health check still contains TLS behavior.'
}
if ($resolved.volumes.PSObject.Properties.Name -contains 'tls_data') {
    throw 'TLS volume remains.'
}
```

- [ ] **Step 2: Run static deployment tests and verify RED**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\compose-test-static.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\test-recovery-static.tests.ps1
```

Expected: FAIL on old services, port, HTTPS health check, and TLS volume.

- [ ] **Step 3: Update Compose and container paths**

Use `/etc/bookbox/config.yml`, `/run/bookbox-secrets`, and `/var/lib/bookbox/{covers,backups}`. Remove `/var/lib/bookbox/tls` and its volume. Publish:

```yaml
ports:
  - name: http
    target: 8787
    published: "${BOOKBOX_HOST_PORT:-8787}"
    host_ip: "${BOOKBOX_BIND_ADDRESS:?Set a private LAN address}"
    protocol: tcp
healthcheck:
  test: ["CMD", "wget", "-qO-", "http://localhost:8787/v1/ready"]
```

Keep the API library mount read-only, worker without an inbound port, dropped capabilities, read-only container roots, secret-file staging, and resource bounds.

- [ ] **Step 4: Write failing discovery process tests**

The tests use a harmless child PowerShell process in place of the executable and verify:

```powershell
$record = Start-BookBoxDiscovery `
    -Executable $fakeExecutable `
    -Address '192.168.1.25' `
    -Port 8787 `
    -StateDirectory $taskState
if (-not (Get-Process -Id $record.ProcessId -ErrorAction SilentlyContinue)) {
    throw 'Discovery process did not start.'
}
Stop-BookBoxDiscovery -StateDirectory $taskState
if (Get-Process -Id $record.ProcessId -ErrorAction SilentlyContinue) {
    throw 'Owned discovery process did not stop.'
}
```

Tests must prove PID-reuse protection, exact executable-path validation, refusal to stop an unrelated process, and cleanup of owned stdout/stderr/PID files.

- [ ] **Step 5: Run process tests and verify RED**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\discovery-process.tests.ps1
```

Expected: FAIL because the lifecycle module does not exist.

- [ ] **Step 6: Implement deployment lifecycle**

`start-bookbox.ps1` will:

1. Validate `BOOKBOX_BIND_ADDRESS` as a private address assigned to Windows.
2. Start Compose.
3. Wait for `http://<address>:<port>/v1/ready`.
4. Start `bookbox-discovery.exe` hidden with exact address and port.
5. Verify the process and mDNS record.
6. Create or validate a Windows Firewall rule scoped to the private profile, local subnet, TCP, and the configured port.

`stop-bookbox.ps1` will stop only the recorded discovery process, run `docker compose down` without `-v`, and verify the process and port state. `install-bookbox-discovery.ps1` will install the release executable and register a least-privilege startup action that calls the normal start script; it will not add a tray UI.

- [ ] **Step 7: Convert recovery, smoke, E2E, and release scripts**

Remove certificate extraction, TLS backup/restore, fingerprint comparisons, insecure curl flags, `https://` URLs, and TLS test arguments. Rename the task-scoped project prefixes and keep their exact-scope deletion guards. Use HTTP task port `18787` to avoid production `8787`.

- [ ] **Step 8: Verify and commit**

Run serially:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\discovery-process.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\compose-test-static.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\run-static.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\stop-scope.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\test-recovery-static.tests.ps1
docker compose -f deploy\compose.yml config
```

Expected: PASS.

Commit:

```powershell
git add deploy e2e
git commit -m "feat(deploy): add BookBox HTTP and discovery lifecycle"
```

### Task 5: Replace Android Certificate Pairing with Direct Sign-In

**Files:**
- Delete: `android/app/src/main/java/com/rendref/bookbox/core/network/CertificateProbe.kt`
- Delete: `android/app/src/test/java/com/rendref/bookbox/core/network/CertificateProbeTest.kt`
- Rename: `android/app/src/main/java/com/rendref/bookbox/core/network/PinnedClientFactory.kt` to `BookBoxClientFactory.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/storage/ConnectionStore.kt`
- Modify: `android/app/src/main/AndroidManifest.xml`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/BrowseApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/CatalogApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/ReviewApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/SessionApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/SettingsApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/TransferManifestApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/connect/ConnectViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/connect/ConnectScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/settings/ConnectionSettingsSource.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/settings/SettingsViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/settings/SettingsScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/ProductionBookBoxApp.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/BookBoxRuntime.kt`
- Modify: related unit and instrumentation tests under `android/app/src/test` and `android/app/src/androidTest`
- Modify: `android/app/build.gradle.kts`

**Interfaces:**
- Produces: `SavedConnection(baseUrl: String, username: String, destinationUri: String? = null)`
- Produces: `normalizeServerOrigin(input: String): HttpUrl`
- Produces: `BookBoxClientFactory.create(connection: SavedConnection): OkHttpClient`
- Produces: direct `EnterCredentials` to `Ready` connection state

- [ ] **Step 1: Write failing URL and persistence tests**

Add:

```kotlin
@Test
fun `bare default hostname becomes BookBox HTTP origin`() {
    assertEquals(
        "http://bookbox.local:8787/",
        normalizeServerOrigin("bookbox.local").toString(),
    )
}

@Test
fun `private IP without scheme receives HTTP and default port`() {
    assertEquals(
        "http://192.168.1.25:8787/",
        normalizeServerOrigin("192.168.1.25").toString(),
    )
}

@Test
fun `HTTPS credentials paths queries and fragments are rejected`() {
    listOf(
        "https://bookbox.local",
        "http://owner:secret@bookbox.local",
        "http://bookbox.local/path",
        "http://bookbox.local/?query=1",
        "http://bookbox.local/#fragment",
    ).forEach { assertFailsWith<IllegalArgumentException> { normalizeServerOrigin(it) } }
}
```

Update `ConnectionStoreTest` to prove no certificate key is required or written.

- [ ] **Step 2: Run focused tests and verify RED**

Run:

```powershell
Set-Location android
.\gradlew.bat testDebugUnitTest `
  --tests 'com.rendref.bookbox.feature.connect.ConnectViewModelTest' `
  --tests 'com.rendref.bookbox.core.storage.ConnectionStoreTest' `
  --no-parallel --max-workers=1
```

Expected: FAIL because normalization requires HTTPS and `SavedConnection` requires a fingerprint.

- [ ] **Step 3: Simplify saved connection and HTTP client**

Use:

```kotlin
data class SavedConnection(
    val baseUrl: String,
    val username: String,
    val destinationUri: String? = null,
) {
    init {
        val url = baseUrl.toHttpUrl()
        require(url.scheme == "http")
        require(url.encodedUsername.isEmpty() && url.encodedPassword.isEmpty())
        require(url.query == null && url.fragment == null && url.encodedPath == "/")
        require(username.isNotBlank())
    }
}
```

Remove the certificate preference and validation. Keep same-origin redirect enforcement, reject credential-bearing redirects, and change error text from “approved HTTPS origin” to “saved BookBox origin.” Use a standard `OkHttpClient` with no custom trust manager or hostname verifier. Remove `okhttp-tls` from test dependencies after the remaining tests stop using it.

Enable Android cleartext traffic in the application manifest. Change only the
BookBox server-origin invariants in the six API clients to require `http`.
Retain HTTPS validation for Open Library, Google Books, and cover-provider URLs.

- [ ] **Step 4: Write failing direct sign-in state tests**

```kotlin
@Test
fun `first screen contains default server and signs in without certificate state`() = runTest {
    val viewModel = newViewModel()
    assertEquals(
        ConnectState.EnterCredentials(server = "bookbox.local"),
        viewModel.state.value,
    )
    viewModel.connect("bookbox.local", "owner", "correct horse")
    advanceUntilIdle()
    assertEquals(ConnectState.Ready, viewModel.state.value)
    assertEquals(
        SavedConnection("http://bookbox.local:8787/", "owner"),
        connections.saved,
    )
}
```

Add focused cases for blank fields, hostname resolution, unreachable server, HTTP 401, incompatible API, refresh 401 returning to sign-in, and generic refresh retry.

- [ ] **Step 5: Run state tests and verify RED**

Run:

```powershell
.\gradlew.bat testDebugUnitTest `
  --tests 'com.rendref.bookbox.feature.connect.ConnectViewModelTest' `
  --no-parallel --max-workers=1
```

Expected: FAIL because certificate review remains in the state machine.

- [ ] **Step 6: Implement direct connection UI and state**

Replace `ReviewCertificate`, `CertificateInspector`, `ObservedCertificate`, `approveFingerprint`, and `inspectServer` with:

```kotlin
sealed interface ConnectState {
    data class EnterCredentials(
        val server: String = "bookbox.local",
        val username: String = "",
    ) : ConnectState
    data object Connecting : ConnectState
    data object Ready : ConnectState
    data class Error(
        val message: String,
        val recovery: ConnectRecovery,
    ) : ConnectState
}
```

The first screen contains server, username, password, and **Connect**. Clear the password immediately after submission. Map `UnknownHostException`, `ConnectException`/timeouts, `SessionException(401)`, and incompatible capability responses to separate messages and recovery actions.

- [ ] **Step 7: Remove certificate settings and add recovery actions**

Remove fingerprint diagnostics and clipboard behavior. Keep **Sign in again**, **Change server**, and **Reset saved connection** as explicit actions. Confirmation copy must state that tokens and the saved server are cleared while Android downloads and transfer history remain.

- [ ] **Step 8: Verify and commit**

Run:

```powershell
.\gradlew.bat testDebugUnitTest --no-parallel --max-workers=1
```

Expected: PASS.

Commit:

```powershell
git add android
git commit -m "feat(android): connect to BookBox with hostname and password"
```

### Task 6: Update End-to-End, Release, Operations, and Roadmap

**Files:**
- Modify: `e2e/run.ps1`
- Modify: `e2e/stop.ps1`
- Modify: `deploy/scripts/build-release.ps1`
- Modify: `deploy/scripts/verify-release.ps1`
- Modify: `README.md`
- Modify: `RELEASE_NOTES.md`
- Modify: `docs/android.md`
- Modify: `docs/installation.md`
- Modify: `docs/operations.md`
- Modify: `docs/setup-server.md`
- Modify: `docs/backup-and-recovery.md`
- Modify: `docs/galaxy-s23-checklist.md`
- Create: `docs/migration-from-netstorage.md`
- Create: `docs/roadmap.md`

**Interfaces:**
- Produces: `artifacts/BookBox-v<version>-debug.apk` and optional release APK
- Produces: E2E argument `e2eBaseUrl=http://10.0.2.2:18787/`
- Produces: roadmap entries for QR HTTPS pairing and Windows tray approval

- [ ] **Step 1: Write failing release and roadmap assertions**

Extend static tests to require:

```powershell
if ($releaseText -notmatch 'BookBox-v\$Version-debug\.apk') {
    throw 'BookBox APK artifact naming is missing.'
}
if ($verifyText -notmatch "application ID is com\.rendref\.bookbox") {
    throw 'BookBox application-ID verification is missing.'
}
$roadmap = Get-Content -Raw docs\roadmap.md
foreach ($required in @(
    'QR-based HTTPS pairing',
    'short-lived single-use pairing token',
    'Windows tray companion',
    'approve or deny',
    'still enter the BookBox username and password'
)) {
    if ($roadmap -notmatch [regex]::Escape($required)) {
        throw "Roadmap requirement missing: $required"
    }
}
```

- [ ] **Step 2: Run static checks and verify RED**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release-process.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\run-static.tests.ps1
```

Expected: FAIL on old APK, package, E2E, and roadmap content.

- [ ] **Step 3: Convert the real-server Android journey**

Remove fingerprint instrumentation arguments and certificate approval. Start the journey with:

```kotlin
compose.onNodeWithText("Server").performTextReplacement(baseUrl)
compose.onNodeWithText("Username").performTextInput(username)
compose.onNodeWithText("Password").performTextInput(password)
compose.onNodeWithText("Connect").performClick()
compose.onNodeWithText("Audiobooks").assertExists()
```

Run the Compose proxy and API over HTTP on task port `18787`. Preserve transfer interruption, resumption, destination conflict, byte comparison, source mutation checks, and exact cleanup.

- [ ] **Step 4: Update release artifacts and verification**

Build BookBox-named APKs, use optional `local-secrets/bookbox-release.jks`, and read `BOOKBOX_KEY_ALIAS`, `BOOKBOX_KEYSTORE_PASSWORD`, and `BOOKBOX_KEY_PASSWORD`. Verify application ID `com.rendref.bookbox`, API 36 levels, permissions, signatures, and checksums.

- [ ] **Step 5: Write clean-install and private-LAN documentation**

Document:

- Remove the former Android app after preserving downloaded audiobooks.
- Stop the former Docker project without deleting the audiobook source.
- Generate BookBox secrets and rebuild the catalog.
- Start BookBox through `start-bookbox.ps1`.
- Connect with `bookbox.local`, username, and password.
- Fall back to the selected private IP when mDNS is blocked.
- Keep BookBox off guest Wi-Fi, public networks, router forwarding, and WAN exposure.
- Treat HTTP traffic as visible to devices capable of monitoring the LAN.

- [ ] **Step 6: Write the future roadmap**

`docs/roadmap.md` must keep both items out of current behavior:

```markdown
## QR-based HTTPS pairing

Restore HTTPS through a QR payload containing the server URL, server trust
information, server identity, and a short-lived single-use pairing token.
Android verifies and stores trust without asking the user to type a certificate
fingerprint.

## Windows tray companion

Show local new-device requests with a matching code and Approve or Deny.
Approval opens the sign-in step; the Android user must still enter the BookBox
username and password. The tray app lists and revokes devices and may absorb
the existing mDNS discovery process.
```

- [ ] **Step 7: Verify and commit**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\bookbox-branding.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release-process.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\run-static.tests.ps1
git diff --check
```

Expected: PASS.

Commit:

```powershell
git add e2e deploy android README.md RELEASE_NOTES.md docs
git commit -m "docs: publish BookBox setup and security roadmap"
```

### Task 7: Run the BookBox Release Gate

**Files:**
- Modify only files needed to correct failures caused by Tasks 1–6
- Do not modify unrelated user file: `storagecloud.zip`

**Interfaces:**
- Consumes: the complete renamed HTTP deployment, discovery binary, Android app, and documentation
- Produces: verified BookBox APK and release metadata

- [ ] **Step 1: Run focused static and unit gates**

Run serially:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\bookbox-branding.tests.ps1
Set-Location server
go test -mod=readonly -p=1 -race ./...
go vet -mod=readonly ./...
Set-Location ..\android
.\gradlew.bat testDebugUnitTest --no-parallel --max-workers=1
```

Expected: PASS with no warnings attributable to BookBox changes.

- [ ] **Step 2: Run deployment static and process gates**

Run each PowerShell test script serially:

```powershell
Set-Location ..
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\compose-test-static.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\run-static.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\stop-scope.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\process-runner.runtime.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\discovery-process.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\synthetic-recovery-scope.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\test-recovery-static.tests.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release-process.tests.ps1
```

Expected: PASS.

- [ ] **Step 3: Run isolated Compose smoke and recovery**

Run one heavyweight process at a time:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\smoke-server.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\test-recovery.ps1
```

After each run, verify its exact containers, processes, task ports, and temporary directories exited.

- [ ] **Step 4: Run Android/Compose E2E**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\run.ps1
```

Expected: BookBox signs in over HTTP without certificate interaction, completes the existing catalog/transfer journey, preserves source mutation boundaries, and cleans its exact process/container/port tree.

- [ ] **Step 5: Run the release build**

Run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1
```

Expected: the release gate passes and produces the BookBox debug APK, checksums, and metadata report.

- [ ] **Step 6: Verify repository state and commit fixes**

Run:

```powershell
git diff --check
git status --short
rg -n -i 'netstorage' android server deploy e2e README.md RELEASE_NOTES.md docs |
    Select-String -NotMatch 'migration-from-netstorage|2026-07-27-bookbox'
```

Expected: no unintended legacy identifiers; `storagecloud.zip` remains untracked and unchanged.

If verification required tracked fixes:

```powershell
git add android server deploy e2e docs README.md RELEASE_NOTES.md
git commit -m "test: complete BookBox release verification"
```

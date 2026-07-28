# BookBox Integration and Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Integrate the server, worker, PostgreSQL, and Android app into a reproducible Windows deployment with end-to-end verification, backup/recovery, and a sideloadable APK.

**Architecture:** Production Compose uses least-privilege mounts and secret files. A separate test Compose project uses a copied synthetic library and API 36 emulator. Release scripts prove service health, transfer resumption, destination integrity, and source mutation boundaries before producing artifacts.

**Tech Stack:** Docker Desktop and Compose, Go 1.26.5, PostgreSQL 18, Android API 36 emulator, Gradle 9.4.1, PowerShell 7/Windows PowerShell 5.1-compatible scripts, ADB.

## Global Constraints

- Complete the server transfer, catalog, and Android plans first.
- Production library path is `H:\Audiobooks`.
- Automated destructive or mutation tests never run against `H:\Audiobooks`.
- The API mount is read-only; only the isolated worker receives a read-write library mount.
- The worker exposes no inbound network port.
- Live passwords, token keys, TLS private keys, Google API keys, PostgreSQL data, cover caches, backups, and Android signing keys stay out of Git.
- Version one listens on LAN HTTPS port `8443` and does not open router ports.
- Version one creates no automatic discovery, playback, multi-user, or Play Store feature.
- PostgreSQL is the only application database; Android contains no SQLite or Room.

---

### Task 1: Production Compose, Secrets, and Health Gates

**Files:**
- Modify: `deploy/compose.yml`
- Create: `deploy/compose.test.yml`
- Create: `deploy/config.yml.example`
- Modify: `deploy/.env.example`
- Create: `deploy/scripts/new-secrets.ps1`
- Create: `deploy/scripts/wait-healthy.ps1`
- Create: `deploy/scripts/audit-compose.ps1`
- Create: `server/internal/httpapi/ready.go`
- Create: `server/internal/httpapi/ready_test.go`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `GET /v1/ready` that checks migrations and required roots
- Produces: one-command secret generation
- Produces: static Compose safety audit

- [ ] **Step 1: Write readiness tests**

```go
func TestReadyRequiresDatabaseAndReadableRoots(t *testing.T) {
    api := newReadyAPI(t, readiness{DB:false, Roots:true})
    rr := serve(t, api, "GET", "/v1/ready", nil)
    if rr.Code != http.StatusServiceUnavailable { t.Fatalf("status=%d", rr.Code) }
    api = newReadyAPI(t, readiness{DB:true, Roots:true})
    rr = serve(t, api, "GET", "/v1/ready", nil)
    if rr.Code != http.StatusOK { t.Fatalf("status=%d", rr.Code) }
}
```

Expose component names and boolean readiness without credentials, paths, or database errors.

- [ ] **Step 2: Write the Compose audit before final Compose changes**

```powershell
$resolved = docker compose -f deploy\compose.yml config --format json | ConvertFrom-Json
$api = $resolved.services.'bookbox-api'
$worker = $resolved.services.'bookbox-catalog-worker'
if ($api.volumes | Where-Object { $_.target -eq '/library/audiobooks' -and -not $_.read_only }) { throw 'API library mount must be read-only' }
if ($worker.ports -or $worker.expose) { throw 'Worker must have no inbound port' }
foreach ($service in @($api,$worker)) {
    if ($service.cap_drop -notcontains 'ALL') { throw 'Capabilities not dropped' }
    if ($service.read_only -ne $true) { throw 'Container root is writable' }
}
```

- [ ] **Step 3: Finalize production Compose**

Add restart policies, named networks, health/readiness checks, secret files, volume names, logging rotation, `init: true`, CPU/memory reservations that do not starve scans, and dependency health gates. Bind API port as `0.0.0.0:8443:8443`. Keep PostgreSQL and worker off the published network surface.

Use these secret files:

```text
deploy/secrets/postgres_password.txt
deploy/secrets/app_password_hash.txt
deploy/secrets/session_key.bin
deploy/secrets/google_books_key.txt
```

Google Books key may be an empty file. Reject empty values for the first three.

- [ ] **Step 4: Implement secret generation**

`new-secrets.ps1` must:

- Refuse to overwrite an existing non-empty secret.
- Generate 32 random bytes for PostgreSQL and session keys with `RandomNumberGenerator`.
- Ask for the app password twice through `Read-Host -AsSecureString`.
- invoke `bookbox-admin hash-password` through stdin-safe console handling without logging plaintext.
- Set restrictive Windows ACLs for the current user and SYSTEM.
- Print only created filenames and the next Compose command.

- [ ] **Step 5: Add a test override**

`compose.test.yml` uses a caller-supplied task-scoped library copy, port `18443`, test secrets, temporary named volumes prefixed by `bookbox-e2e`, fast scan interval, deterministic provider fixtures, and no access to `H:`.

- [ ] **Step 6: Verify and commit**

Run:

```powershell
docker compose -f deploy\compose.yml config
powershell -ExecutionPolicy Bypass -File deploy\scripts\audit-compose.ps1
Set-Location server
go test ./internal/httpapi -run TestReady -v
```

Expected: PASS.

```bash
git add .gitignore deploy server/internal/httpapi
git commit -m "chore(deploy): harden production Compose"
```

### Task 2: Full API 36 End-to-End Test

**Files:**
- Create: `e2e/run.ps1`
- Create: `e2e/stop.ps1`
- Create: `e2e/library-manifest.ps1`
- Create: `e2e/provider-fixtures/openlibrary-dune.json`
- Create: `e2e/provider-fixtures/googlebooks-dune.json`
- Create: `android/app/src/androidTest/AndroidManifest.xml`
- Create: `android/app/src/androidTest/java/com/rendref/bookbox/e2e/TestDocumentsProvider.kt`
- Create: `android/app/src/androidTest/java/com/rendref/bookbox/e2e/RealServerJourneyTest.kt`
- Modify: `android/app/build.gradle.kts`
- Modify: `deploy/compose.test.yml`

**Interfaces:**
- Produces: an isolated Compose project name per run
- Produces: instrumentation arguments `e2eBaseUrl`, `e2eFingerprint`, `e2eUsername`, and `e2ePassword`
- Produces: byte-for-byte destination verification

- [ ] **Step 1: Write source-tree manifest tooling**

`library-manifest.ps1` recursively records relative path, type, size, last-write UTC, and SHA-256 for files into sorted JSON. It must resolve the supplied path and refuse `H:\Audiobooks`, `H:\`, `C:\`, or any filesystem root.

- [ ] **Step 2: Build a deterministic synthetic library**

The test setup copies repository fixtures into a new directory under `$env:TEMP\bookbox-e2e\<guid>\library`:

```text
Author One/Normal Book/chapter01.mp3
Author One/Normal Book/chapter02.mp3
Author One/Normal Book/cover.jpg
Author Two/Shared Folder/Dune.m4b
Author Two/Shared Folder/Dune Messiah.m4b
Author Two/Shared Folder/folder.jpg
```

Generate valid short audio fixtures during setup with the worker image's FFmpeg and embed distinct title/author tags. Generate low-resolution and high-resolution test covers.

The test Compose override also runs `ghcr.io/shopify/toxiproxy:2.12.0` between Android and the API. The script adds and removes a one-shot TCP reset toxic through Toxiproxy's control API to prove range resumption without adding a test-only endpoint to production code.

- [ ] **Step 3: Write the real-server instrumentation journey**

The test must:

1. Connect to the supplied HTTPS address.
2. Assert the displayed fingerprint equals the supplied fingerprint and approve it.
3. Sign in.
4. Wait for catalog scan completion.
5. Search `Dune`, verify two cards, and open **View raw files**.
6. Select Normal Book and Dune Messiah.
7. Choose a test `DocumentsProvider` tree.
8. Start transfer, interrupt one response after half its bytes, and wait for resume.
9. Resolve one destination conflict with Keep both.
10. Verify final paths and SHA-256 values from the manifest.
11. Confirm no Android API request used an unapproved method or path.

`TestDocumentsProvider` stores documents under `targetContext.cacheDir/e2e-documents`, implements create/open/rename/delete for that Android-only tree, and records operations for assertions. The androidTest manifest exports it only inside the test APK with authority `com.rendref.bookbox.e2e.documents`.

- [ ] **Step 4: Implement bounded orchestration**

`e2e/run.ps1` must show progress every 30 seconds, use a unique Compose project name, start provider fixtures and Compose, wait for readiness, read the printed TLS fingerprint, verify an API 36 emulator is connected, run the one instrumentation class with arguments, compare source manifests, and call `stop.ps1` in `finally`.

If cover publication ran, allow only the expected `cover.jpg`/`folder.jpg` path and require its backup/audit record. Otherwise require identical source manifests.

- [ ] **Step 5: Implement exact cleanup verification**

`e2e/stop.ps1` removes only the unique Compose project and task temp directory. It verifies the project's containers, networks, and volumes exited/removed; checks no task-scoped port `18443` listener remains; and reports any owned ADB test process still running. It must not terminate unrelated Docker, Java, emulator, Node, Python, or browser processes.

- [ ] **Step 6: Run and commit**

Run: `powershell -ExecutionPolicy Bypass -File e2e\run.ps1`

Expected: PASS, with a final line `BookBox E2E passed; owned processes and ports are clean`.

```bash
git add e2e android/app/src/androidTest android/app/build.gradle.kts deploy/compose.test.yml
git commit -m "test: verify complete BookBox transfer journey"
```

### Task 3: Backup, Restore, Rotation, and Cover Recovery

**Files:**
- Create: `deploy/scripts/backup.ps1`
- Create: `deploy/scripts/restore.ps1`
- Create: `deploy/scripts/rotate-password.ps1`
- Create: `deploy/scripts/restore-cover.ps1`
- Create: `deploy/scripts/test-recovery.ps1`
- Create: `docs/backup-and-recovery.md`

**Interfaces:**
- Produces: timestamped backup directory with manifest and checksums
- Produces: restore into an empty test deployment
- Produces: password rotation that invalidates refresh tokens
- Produces: audited cover restoration

- [ ] **Step 1: Write the recovery test first**

The test creates a task-scoped deployment, inserts a confirmed metadata correction and cover event, runs backup, destroys only that test Compose project and its volumes, restores into new test volumes, then asserts the book, correction, certificate fingerprint, cover cache, backup image, and event match.

Run: `powershell -ExecutionPolicy Bypass -File deploy\scripts\test-recovery.ps1`

Expected: FAIL because backup scripts do not exist.

- [ ] **Step 2: Implement backup**

Back up:

- `pg_dump --format=custom` output.
- TLS certificate and key volume.
- Cover cache and original-cover backup volumes.
- Non-secret root/scan/quality configuration.
- A JSON manifest with SHA-256, byte size, app version, schema version, and UTC timestamp.

Write into a newly created explicit destination. Refuse filesystem roots and existing non-empty destinations. Do not include live plaintext secrets.

- [ ] **Step 3: Implement restore**

Require stopped API/worker services and empty destination volumes. Verify every manifest checksum, restore TLS/cache/backup files, run `pg_restore --clean --if-exists` only against the named empty test/production database after an explicit database-name match, start services, and verify readiness plus the original certificate fingerprint.

- [ ] **Step 4: Implement password and cover recovery**

`rotate-password.ps1` creates a new Argon2id hash, rotates session key bytes, restarts only the API, and verifies old refresh tokens receive 401. `restore-cover.ps1` accepts a cover event UUID, verifies the backup digest and destination confinement, queues a worker restore job, and adds a new audit event rather than erasing history.

- [ ] **Step 5: Verify and commit**

Run: `powershell -ExecutionPolicy Bypass -File deploy\scripts\test-recovery.ps1`

Expected: PASS with all test volumes and containers removed.

```bash
git add deploy/scripts docs/backup-and-recovery.md
git commit -m "feat(ops): add verified backup and recovery"
```

### Task 4: Galaxy Sideload Package and Operator Guide

**Files:**
- Create: `deploy/scripts/build-release.ps1`
- Create: `deploy/scripts/verify-release.ps1`
- Create: `docs/installation.md`
- Create: `docs/galaxy-s23-checklist.md`
- Create: `docs/operations.md`
- Create: `RELEASE_NOTES.md`
- Create: `README.md`
- Modify: `android/app/build.gradle.kts`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `artifacts/BookBox-v1.0.0-debug.apk`
- Produces: optional user-signed `artifacts/BookBox-v1.0.0-release.apk`
- Produces: SHA-256 checksums and APK metadata report

- [ ] **Step 1: Implement deterministic artifact building**

`build-release.ps1` runs all Go tests, Android unit/lint/instrumentation tests, Compose audits, and the E2E suite before copying the debug APK to `artifacts/BookBox-v1.0.0-debug.apk`.

If `local-secrets/bookbox-release.jks` and these environment variables exist, build the release APK:

```text
BOOKBOX_KEY_ALIAS
BOOKBOX_KEYSTORE_PASSWORD
BOOKBOX_KEY_PASSWORD
```

Read signing values through Gradle providers. Do not print them or commit `keystore.properties`. If signing material is absent, finish with a debug APK and a message that the debug APK is sideloadable.

- [ ] **Step 2: Verify APK metadata and signature**

`verify-release.ps1` runs `apkanalyzer manifest application-id`, `apkanalyzer manifest min-sdk`, `apkanalyzer manifest target-sdk`, and `apksigner verify --verbose --print-certs`. Require application ID `com.rendref.bookbox`, min/target 36, a valid signature, no debuggable flag on release, and only the approved permissions. Write artifact SHA-256 values to `artifacts/SHA256SUMS.txt`.

- [ ] **Step 3: Write operator documentation**

`README.md` links the design, four implementation plans, server setup, Android setup, catalog behavior, backup/recovery, and operations.

`docs/installation.md` gives exact steps for Docker Desktop, secret generation, `H:/Audiobooks`, firewall private profile, Compose start, fingerprint comparison, stable DHCP address, APK sideload, destination picker, and first scan.

`docs/operations.md` covers start/stop, health, logs, scan/rescan, provider failures, review status, cover audit/restore, password rotation, backup, restore, another named root, and update procedure.

- [ ] **Step 4: Run the release gate**

Run: `powershell -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1`

Expected: all tests pass and at least the debug APK exists.

Run: `powershell -ExecutionPolicy Bypass -File deploy\scripts\verify-release.ps1`

Expected: APK metadata, permissions, and signature pass; checksums exist.

- [ ] **Step 5: Complete the Galaxy checklist**

The checklist requires manual confirmation of:

- Private-LAN connection.
- Fingerprint comparison.
- Automatic sign-in after phone reboot.
- Search by title, author, genre, and year.
- Card-to-raw-files navigation.
- Destination selection.
- Screen-off transfer continuation and notification.
- Wi-Fi interruption/resume.
- Replace/Keep both/Skip conflicts.
- Source-library before/after check for Android actions.
- Expected worker-only cover publication.

Do not claim device verification until the user or implementer records each result.

- [ ] **Step 6: Commit**

```bash
git add .gitignore README.md RELEASE_NOTES.md deploy/scripts docs android/app/build.gradle.kts
git commit -m "release: prepare BookBox v1.0.0 sideload"
```

## Plan Completion Check

Run:

```powershell
powershell -ExecutionPolicy Bypass -File deploy\scripts\audit-compose.ps1
powershell -ExecutionPolicy Bypass -File deploy\scripts\test-recovery.ps1
powershell -ExecutionPolicy Bypass -File e2e\run.ps1
powershell -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1
powershell -ExecutionPolicy Bypass -File deploy\scripts\verify-release.ps1
git status --short
```

Expected: all automated gates pass; owned containers, volumes, processes, and ports are clean; artifacts and checksums exist; the only remaining manual gate is the documented Galaxy checklist.

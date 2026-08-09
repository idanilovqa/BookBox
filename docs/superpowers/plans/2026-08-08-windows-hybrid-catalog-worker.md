# Windows Hybrid Catalog Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the BookBox catalog worker as a recoverable native Windows Service that watches local Windows drives while the API and PostgreSQL remain in Docker.

**Architecture:** Preserve the Linux container worker, but let the same Go executable run interactively or under Windows Service Control Manager. A hybrid Compose override publishes PostgreSQL on loopback and leaves the container-worker profile disabled; Android continues to use the LAN-facing API.

**Tech Stack:** Go 1.26.5, `golang.org/x/sys/windows`, `fsnotify` 1.10.1, PostgreSQL 18, Docker Compose, PowerShell 5.1+, Windows Service Control Manager, Windows Event Log.

## Global Constraints

- Follow `docs/superpowers/specs/2026-08-08-windows-hybrid-catalog-worker-design.md`.
- Support local fixed and removable Windows drives only. Reject UNC paths, mapped network drives, and remote drive types.
- Keep Linux/NAS all-container operation through the `container-worker` Compose profile.
- Bind hybrid PostgreSQL to `127.0.0.1` only. The API remains the only LAN-facing component.
- Keep the API library mount read-only and the Windows worker free of inbound ports.
- Keep the six-hour full-root scan authoritative; filesystem events only accelerate scans.
- Never reconcile an unavailable root as an empty library or replace its last successful search projection.
- Keep root IDs identical across API and worker configurations; absolute paths are runtime-local.
- Use `%ProgramData%\BookBox` for worker configuration, secrets, backups, and bounded logs.
- Do not add tray UI, Docker management, mDNS, native PostgreSQL, network shares, or a native Windows API.
- Use strict TDD and one scoped commit per task.
- Run at most one Go, PowerShell, Android, or E2E test process at a time across all agents.
- Preserve path confinement, secret-file validation, atomic publication, and scoped cleanup.
- Run every Go command from `server` unless the step gives another working directory.

## File Map

- `server/internal/config`: canonical `path` alias and worker-only config loading.
- `server/internal/library`: local-root availability and Windows drive-type validation.
- `server/internal/scanner`: Linux descriptor probing and confined Windows path probing.
- `server/internal/monitor`: restart supervisor and native Windows event integration.
- `server/internal/workerlog`: bounded rotating service logs.
- `server/cmd/bookbox-catalog-worker`: shared program, retry loop, CLI, and SCM adapter.
- `deploy/compose*.yml`: container-worker profile and hybrid loopback PostgreSQL.
- `deploy/scripts`: mode-aware lifecycle, service installation, artifact gates.
- `deploy/windows`: Windows worker example configuration.
- `e2e`: native worker hybrid catalog-search journey.
- `docs`: operator and Android connection guidance.

---

### Task 1: Worker Configuration and Canonical Root Paths

**Files:**
- Modify: `server/internal/config/config.go`
- Modify: `server/internal/config/config_test.go`
- Create: `server/internal/config/config_windows_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`
- Create: `deploy/windows/catalog-worker.example.yml`

**Interfaces:**
- Produces `func LoadWorker(path string) (Config, error)`.
- Populates `Root.ContainerPath` from exactly one of YAML `path` or legacy `container_path`.
- Produces `WorkerSettings{FFProbePath, CoverBackupRoot, LogDirectory, GoogleBooksAPIKeyFile}` from an optional YAML `worker` section.
- Preserves API `Load`, including listen/auth validation.

- [ ] **Step 1: Write failing config tests**

Add tests proving canonical `path`, legacy compatibility, rejection when both fields appear, worker config without API fields, and API rejection without listen/auth.

```go
func TestLoadWorkerAcceptsCanonicalPathWithoutAPIFields(t *testing.T) {
	path := writeConfig(t, `roots:
  - {id: audiobooks, name: Audiobooks, path: /library/audiobooks}
database:
  host: 127.0.0.1
  port: 55432
  name: bookbox
  username: bookbox
  password_file: /run/secrets/postgres
  ssl_mode: disable`)
	cfg, err := LoadWorker(path)
	if err != nil || cfg.Roots[0].ContainerPath != "/library/audiobooks" {
		t.Fatalf("LoadWorker() = %#v, %v", cfg, err)
	}
}
```

- [ ] **Step 2: Run RED**

Run: `go test -count=1 ./internal/config -run 'TestLoadWorker|TestLoadRejectsAmbiguousRootPathFields' -v`

Expected: compile failure for missing `LoadWorker` and `ErrAmbiguousRootPath`.

- [ ] **Step 3: Implement strict role-aware loading**

```go
var ErrAmbiguousRootPath = errors.New("root path is ambiguous")

func Load(path string) (Config, error)       { return load(path, true) }
func LoadWorker(path string) (Config, error) { return load(path, false) }
```

Give `Root` a strict `UnmarshalYAML` that rejects both path fields, maps the selected value to `ContainerPath`, and rejects unknown root fields. Add:

```go
type WorkerSettings struct {
	FFProbePath    string `yaml:"ffprobe_path"`
	CoverBackupRoot string `yaml:"cover_backup_root"`
	LogDirectory   string `yaml:"log_directory"`
	GoogleBooksAPIKeyFile string `yaml:"google_books_api_key_file"`
}
```

`load(path, requireAPIFields)` always validates roots/database. It validates listen/auth only for the API and validates every nonblank worker path as absolute for the running OS.

- [ ] **Step 4: Add Windows build-tag tests**

In `config_windows_test.go`, assert `C:\Audiobooks` and `H:\Library` are syntactically absolute while relative paths fail. Task 2, not config parsing, owns drive-type and UNC policy.

- [ ] **Step 5: Switch the worker and add its example**

Call `config.LoadWorker`. The example uses `path: H:\Audiobooks`, `127.0.0.1:55432`, ProgramData secret paths, `ffprobe_path`, `cover_backup_root`, `log_directory`, and `google_books_api_key_file`; it contains no API listen/auth fields. Keep existing environment variables as container/test overrides, with explicit worker configuration taking precedence on Windows.

- [ ] **Step 6: Run GREEN and commit**

Run: `go test -count=1 ./internal/config ./cmd/bookbox-catalog-worker -v`

Expected: PASS.

```powershell
git add server/internal/config server/cmd/bookbox-catalog-worker/main.go deploy/windows/catalog-worker.example.yml
git commit -m "feat(worker): add native root configuration"
```

---

### Task 2: Unavailable-Root Protection and Windows Metadata Probing

**Files:**
- Create: `server/internal/library/localroot.go`
- Create: `server/internal/library/localroot_windows.go`
- Create: `server/internal/library/localroot_other.go`
- Create: `server/internal/library/localroot_windows_test.go`
- Modify: `server/internal/scanner/scanner.go`
- Create: `server/internal/scanner/probe_linux.go`
- Create: `server/internal/scanner/probe_windows.go`
- Create: `server/internal/scanner/probe_windows_test.go`
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/worker/worker_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`

**Interfaces:**
- Produces `library.ValidateLocalRoot(path string) error`.
- Produces `library.ErrRootUnavailable` and `library.ErrUnsupportedRoot`.
- Adds `ScanRootHandler.ValidateRoot func(string) error`, defaulting to `library.ValidateLocalRoot`.
- Consumes `Config.Worker.FFProbePath`, default `ffprobe` when blank.

- [ ] **Step 1: Write the publication-protection test**

Inject a validator returning `ErrRootUnavailable`. Assert `BeginScan` and `FinalizeSuccessfulScan` have zero calls and the previous snapshot sentinel remains unchanged.

```go
handler.ValidateRoot = func(string) error { return library.ErrRootUnavailable }
err := handler.Handle(ctx, scanJob("audio"))
if !errors.Is(err, library.ErrRootUnavailable) || repo.beginCalls != 0 || repo.finalizeCalls != 0 {
	t.Fatalf("unavailable root mutated catalog: err=%v repo=%+v", err, repo)
}
```

- [ ] **Step 2: Run RED**

Run: `go test -count=1 ./internal/worker -run TestScanRootRejectsUnavailableRootBeforeBeginScan -v`

Expected: compile failure because `ValidateRoot` does not exist.

- [ ] **Step 3: Implement platform root validation**

Common code checks absolute path, `os.Stat`, and directory type. Windows code rejects UNC and accepts only `windows.DRIVE_FIXED` and `windows.DRIVE_REMOVABLE` from `windows.GetDriveType`. Inject drive-type lookup in tests to cover remote, CD-ROM, RAM disk, unknown, and missing drive values.

```go
switch windows.GetDriveType(wideRoot) {
case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE:
	return nil
default:
	return ErrUnsupportedRoot
}
```

- [ ] **Step 4: Preflight before `BeginScan`**

Call the validator before creating a scan run. Wrap the error without publishing missing files, availability events, books, or snapshots.

- [ ] **Step 5: Write Windows probe RED tests**

Prove the scanner passes only a confined canonical absolute path to a path probe, accepts unchanged file identity, rejects replacement during probe, and never probes outside the root.

- [ ] **Step 6: Split OS probe implementations**

Keep inherited `/proc/self/fd` behavior in `probe_linux.go`. Windows code captures volume serial, file index, size, and modification time from the resolver-opened handle, calls `Probe` with the confined path, reopens through the resolver, and accepts metadata only when identity matches.

```go
type windowsFileIdentity struct {
	VolumeSerial, FileIndexHigh, FileIndexLow uint32
	Size, Modified int64
}
```

Return `media.ErrProbeFailed` on any identity or reopen failure; never fall back to an unvalidated path.

- [ ] **Step 7: Make ffprobe configurable**

Trim `cfg.Worker.FFProbePath`, default to `ffprobe`, reject line breaks, and pass it to `media.NewFFProbe`. Use `cfg.Worker.CoverBackupRoot`, with the existing Linux default only when blank. Test an absolute Windows path and newline rejection.

- [ ] **Step 8: Run GREEN, cross-compile, and commit**

Run: `go test -count=1 ./internal/library ./internal/scanner ./internal/worker ./cmd/bookbox-catalog-worker -v`

Run a Windows cross-build: `go test -c ./internal/scanner` with `GOOS=windows GOARCH=amd64 CGO_ENABLED=0`, then remove only the produced temporary binary.

```powershell
git add server/internal/library server/internal/scanner server/internal/worker server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): scan local Windows roots safely"
```

---

### Task 3: Restart Failed Filesystem Monitoring

**Files:**
- Create: `server/internal/monitor/supervisor.go`
- Create: `server/internal/monitor/supervisor_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`
- Modify: `server/cmd/bookbox-catalog-worker/main_test.go`

**Interfaces:**
- Produces `monitor.Runner { Run(context.Context) error }`.
- Produces `monitor.Supervisor{New, Warn, Wait, MinBackoff, MaxBackoff}`.

- [ ] **Step 1: Write deterministic RED tests**

Cover failure then recovery, `1s/2s/4s/4s` capped delays, cancellation during wait, and cancellation of a healthy monitor. Inject `Wait`; tests must not sleep.

- [ ] **Step 2: Run RED**

Run: `go test -count=1 ./internal/monitor -run TestSupervisor -v`

Expected: compile failure because `Supervisor` does not exist.

- [ ] **Step 3: Implement the supervisor**

```go
type Supervisor struct {
	New func() Runner
	Warn func(error)
	Wait func(context.Context, time.Duration) error
	MinBackoff, MaxBackoff time.Duration
}
```

Create a new `RootMonitor` after each failure, sanitize each warning, cap backoff, reset it after a stable run, and return nil on context cancellation.

- [ ] **Step 4: Wire it into main**

Use `New: func() monitor.Runner { return monitor.New(repository, cfg.Roots) }`. Keep the worker foreground-owned and join the supervisor before returning.

- [ ] **Step 5: Run GREEN and commit**

Run: `go test -count=1 ./internal/monitor ./cmd/bookbox-catalog-worker -v`

```powershell
git add server/internal/monitor server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): restart failed root monitoring"
```

---

### Task 4: Wait for Dockerized PostgreSQL at Service Startup

**Files:**
- Create: `server/cmd/bookbox-catalog-worker/retry.go`
- Create: `server/cmd/bookbox-catalog-worker/retry_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`

**Interfaces:**
- Produces `retryUntilReady(ctx, policy, attempt, warn) error`.
- Uses `retryPolicy{Initial: time.Second, Maximum: time.Minute, AttemptTimeout: 10*time.Second}`.
- Produces `runProgram(ctx context.Context, configPath string, logger *log.Logger) error`.

- [ ] **Step 1: Write retry RED tests**

Assert immediate success, capped `1s/2s/4s/8s/16s/32s/60s` delays, attempt timeouts, first/error-class/rate-limited warnings, and cancellation without another attempt. Inject wait and clock functions.

- [ ] **Step 2: Run RED**

Run: `go test -count=1 ./cmd/bookbox-catalog-worker -run TestRetryUntilReady -v`

Expected: compile failure because `retryUntilReady` does not exist.

- [ ] **Step 3: Implement context-aware readiness retry**

Wrap every ping in an `AttemptTimeout` child context. Warn on the first failure, when the sanitized error class changes, or after one minute of unchanged failures.

```go
for delay := policy.Initial; ; delay = min(delay*2, policy.Maximum) {
	attemptCtx, cancel := context.WithTimeout(ctx, policy.AttemptTimeout)
	err := attempt(attemptCtx)
	cancel()
	if err == nil { return nil }
	warnRateLimited(err)
	if err := policy.Wait(ctx, delay); err != nil { return ctx.Err() }
}
```

- [ ] **Step 4: Refactor worker construction around caller-owned context**

`runProgram` loads worker config, creates one pool, waits on `pool.Ping`, migrates after readiness, builds handlers, and runs worker plus monitor. Configuration and migration errors return; database unavailability during startup waits for cancellation.

- [ ] **Step 5: Run GREEN and commit**

Run: `go test -count=1 ./cmd/bookbox-catalog-worker -v`

```powershell
git add server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): wait for hybrid database startup"
```

---

### Task 5: Add Bounded Native Worker Logs

**Files:**
- Create: `server/internal/workerlog/rotating.go`
- Create: `server/internal/workerlog/rotating_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`

**Interfaces:**
- Produces `workerlog.Open(directory string, maxBytes int64, backups int) (io.WriteCloser, error)`.
- Service defaults to five 10 MiB files under `Config.Worker.LogDirectory`; the Windows example sets `%ProgramData%\BookBox\logs`.

- [ ] **Step 1: Write rotating-writer RED tests**

Use a temporary directory and 32-byte limit. Assert rotation order, maximum count, append after reopen, idempotent close, and rejection of symlinked directories or non-regular live files.

- [ ] **Step 2: Run RED**

Run: `go test -count=1 ./internal/workerlog -v`

Expected: package missing.

- [ ] **Step 3: Implement the writer**

Guard writes with a mutex. Before overflow, close the live file, rotate numbered backups from high to low, rename live to `.1`, and create a new live file. Reject non-directory/symlink targets before creation.

```go
func Open(directory string, maxBytes int64, backups int) (io.WriteCloser, error) {
	if maxBytes <= 0 || backups < 1 { return nil, ErrInvalidPolicy }
	// Validate directory ownership/type, then open catalog-worker.log.
}
```

- [ ] **Step 4: Wire logging**

Interactive mode keeps stderr. Service mode sends sanitized details to the rotating writer configured by `cfg.Worker.LogDirectory` and lifecycle messages to the Task 6 Event Log adapter.

- [ ] **Step 5: Run GREEN and commit**

Run: `go test -count=1 ./internal/workerlog ./cmd/bookbox-catalog-worker -v`

```powershell
git add server/internal/workerlog server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): bound native service logs"
```

---

### Task 6: Add Windows Service Control Manager Hosting

**Files:**
- Modify: `server/cmd/bookbox-catalog-worker/main.go`
- Create: `server/cmd/bookbox-catalog-worker/service_windows.go`
- Create: `server/cmd/bookbox-catalog-worker/service_other.go`
- Create: `server/cmd/bookbox-catalog-worker/service_windows_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main_test.go`

**Interfaces:**
- Produces `run --config <path>` and `service --config <path>`.
- Preserves bare `--config <path>` as interactive compatibility.
- Produces Windows `runWindowsService(name string, program func(context.Context) error) error`.

- [ ] **Step 1: Write parser and service RED tests**

Common tests cover explicit modes, legacy flags, unknown commands, extra arguments, and default config paths. Windows tests drive `Execute` channels and require `StartPending -> Running -> StopPending` for Stop and Shutdown.

- [ ] **Step 2: Run RED**

Run: `go test -count=1 ./cmd/bookbox-catalog-worker -run 'TestParseCommand|TestWindowsService' -v`

Expected: missing parser/service symbols.

- [ ] **Step 3: Implement shared command dispatch**

Parse mode before constructing the program. Interactive mode owns `signal.NotifyContext`; service mode receives cancellation from SCM. Both call `runProgram`.

- [ ] **Step 4: Implement the Windows SCM adapter**

Use `golang.org/x/sys/windows/svc`. Accept Stop and Shutdown only, cancel once, wait for `runProgram`, and return a nonzero service exit code if it exits unexpectedly.

```go
status <- svc.Status{State: svc.StartPending}
status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
// On Stop/Shutdown:
status <- svc.Status{State: svc.StopPending}
cancel()
```

Open and close the registered Event Log source with `eventlog.Open("BookBoxCatalogWorker")`.

- [ ] **Step 5: Add the non-Windows guard**

Return `errors.New("Windows service mode is unavailable on this platform")`; leave interactive Linux behavior unchanged.

- [ ] **Step 6: Run GREEN and cross-build**

Run: `go test -count=1 ./cmd/bookbox-catalog-worker -v`

Run: `go build -trimpath -o <task-temp>\bookbox-catalog-worker.exe ./cmd/bookbox-catalog-worker` with `GOOS=windows GOARCH=amd64 CGO_ENABLED=0`, then remove only that artifact.

- [ ] **Step 7: Commit**

```powershell
git add server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): run as a Windows service"
```

---

### Task 7: Add Container and Hybrid Compose Modes

**Files:**
- Modify: `deploy/compose.yml`
- Create: `deploy/compose.hybrid.yml`
- Modify: `deploy/.env.example`
- Modify: `deploy/scripts/start-bookbox.ps1`
- Modify: `deploy/scripts/stop-bookbox.ps1`
- Modify: `deploy/scripts/audit-compose.ps1`
- Create: `deploy/scripts/hybrid-compose.tests.ps1`

**Interfaces:**
- Adds `-DeploymentMode Container|Hybrid` to lifecycle/audit scripts.
- Adds `BOOKBOX_POSTGRES_HOST_PORT`, default `55432`, hybrid-only.

- [ ] **Step 1: Write hybrid Compose RED assertions**

Resolve both modes with `docker compose config --format json`. Container mode requires the worker profile and no PostgreSQL host port. Hybrid mode requires an inactive worker profile and exactly one `127.0.0.1 -> 5432` PostgreSQL mapping.

- [ ] **Step 2: Run RED**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/hybrid-compose.tests.ps1`

Expected: missing override and deployment-mode parameters.

- [ ] **Step 3: Add profile and override**

Add `profiles: ["container-worker"]` to `bookbox-catalog-worker`. Create:

```yaml
services:
  postgres:
    ports:
      - name: hybrid-postgres
        target: 5432
        published: "${BOOKBOX_POSTGRES_HOST_PORT:-55432}"
        host_ip: 127.0.0.1
        protocol: tcp
```

- [ ] **Step 4: Make lifecycle scripts mode-aware**

Container mode uses `--profile container-worker -f compose.yml`. Hybrid mode uses `-f compose.yml -f compose.hybrid.yml` without that profile. Persist the exact mode/file set in owned startup state so stop uses the same scope.

- [ ] **Step 5: Extend safety audits**

Retain worker hardening checks in container mode. In hybrid mode, reject wildcard/LAN PostgreSQL publishing and confirm the API still binds only to the selected private address.

- [ ] **Step 6: Run GREEN and commit**

Run sequentially:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/hybrid-compose.tests.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/startup-environment.tests.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/audit-compose.ps1 -DeploymentMode Hybrid
```

```powershell
git add deploy/compose.yml deploy/compose.hybrid.yml deploy/.env.example deploy/scripts
git commit -m "feat(deploy): add Windows hybrid compose mode"
```

---

### Task 8: Install and Remove the Owned Windows Service

**Files:**
- Create: `deploy/scripts/install-catalog-worker-service.ps1`
- Create: `deploy/scripts/remove-catalog-worker-service.ps1`
- Create: `deploy/scripts/catalog-worker-service.tests.ps1`
- Modify: `deploy/scripts/build-release.ps1`
- Modify: `deploy/scripts/verify-release.ps1`

**Interfaces:**
- Installer parameters: `-SourceExecutable`, `-ConfigFile`, `-FFProbeExecutable`, `-LibraryRoot[]`, `-InstallDirectory`.
- Service name/account: `BookBoxCatalogWorker` / `NT SERVICE\BookBoxCatalogWorker`.

- [ ] **Step 1: Write non-elevated RED contract tests**

Parse both scripts and require ownership preflight, delayed-auto, virtual account, unrestricted service SID, three restart actions, Event Log registration, explicit-root ACLs, manifest hashes, and scoped removal. Reject `taskkill`, wildcard deletion, broad `Stop-Process`, broad ProgramData deletion, and ACL mutations outside supplied roots.

- [ ] **Step 2: Run RED**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/catalog-worker-service.tests.ps1`

Expected: missing installer/remover failure.

- [ ] **Step 3: Implement guarded installation**

Require elevation. Validate the worker/config/ffprobe as regular files and each library root as an existing local directory. Refuse to replace a service or install directory unless its owned manifest matches. Stage files with the existing install transaction helper, then run:

```powershell
sc.exe create BookBoxCatalogWorker `
  binPath= ('"{0}" service --config "{1}"' -f $workerExe, $installedConfig) `
  start= delayed-auto `
  obj= 'NT SERVICE\BookBoxCatalogWorker' `
  password= '' `
  DisplayName= 'BookBox Catalog Worker'
sc.exe sidtype BookBoxCatalogWorker unrestricted
sc.exe failure BookBoxCatalogWorker reset= 86400 actions= restart/5000/restart/30000/restart/60000
sc.exe failureflag BookBoxCatalogWorker 1
```

Register the Event Log source with `New-EventLog -LogName Application -Source BookBoxCatalogWorker`. Grant the service SID Modify access only to recorded library roots and `%ProgramData%\BookBox`. Record paths, hashes, ACL targets, and service identity in `bookbox-catalog-worker-install.json`.

- [ ] **Step 4: Implement scoped removal**

Validate manifest and file hashes, stop/delete only `BookBoxCatalogWorker`, unregister only its Event Log source, and remove only recorded service-SID ACL entries. Preserve config, secrets, backups, and logs unless the user passes an explicit data-removal switch.

- [ ] **Step 5: Add the worker release artifact**

Build `artifacts/bookbox-catalog-worker.exe` using `GOOS=windows GOARCH=amd64 CGO_ENABLED=0`. Add it to `SHA256SUMS.txt`; verify PE architecture, checksum, and command help. Do not bundle ffprobe in this phase; require an explicit executable.

- [ ] **Step 6: Run GREEN and commit**

Run sequentially:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/catalog-worker-service.tests.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/build-release-process.tests.ps1
```

```powershell
git add deploy/scripts/install-catalog-worker-service.ps1 deploy/scripts/remove-catalog-worker-service.ps1 deploy/scripts/catalog-worker-service.tests.ps1 deploy/scripts/build-release.ps1 deploy/scripts/verify-release.ps1
git commit -m "feat(deploy): install the Windows catalog service"
```

Microsoft references for implementation details: [sc.exe create](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/sc-create), [virtual service accounts](https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/understand-service-accounts), and [service SIDs](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_sid_info).

---

### Task 9: Prove Native Windows Filesystem Events

**Files:**
- Create: `server/internal/monitor/root_monitor_windows_test.go`
- Modify: `server/internal/monitor/root_monitor_test.go`

**Interfaces:**
- Uses real `newFSNotifySource`, `RootMonitor`, and `monitor.Supervisor`.
- Produces a Windows-only integration test with no Docker filesystem boundary.

- [ ] **Step 1: Write the Windows integration test**

Use `//go:build windows`, a temporary directory, `Debounce: 100*time.Millisecond`, and a concurrency-safe fake repository. Exercise create, write, nested-directory create, rename, and remove. For each operation, wait at most five seconds for the expected root job. Confirm an outside-root change never enqueues.

- [ ] **Step 2: Run the native test**

Run: `go test -count=1 ./internal/monitor -run TestRootMonitorReceivesNativeWindowsEvents -v`

Expected: PASS or a concrete production event-registration failure. Do not replace the real event source with a fake.

- [ ] **Step 3: Fix only observed event defects**

If RED exposes coalescing or new-directory registration defects, adjust `root_monitor.go` while preserving generation-token debounce and sanitized errors. Assert eventual deduplicated enqueue rather than one OS event per filesystem action.

- [ ] **Step 4: Run GREEN and commit**

Run: `go test -count=1 ./internal/monitor -v`

```powershell
git add server/internal/monitor
git commit -m "test(worker): prove native Windows root events"
```

---

### Task 10: Convert Catalog Search E2E to the Hybrid Worker

**Files:**
- Modify: `e2e/run.ps1`
- Modify: `e2e/tests/catalog-search.tests.ps1`
- Modify: `e2e/tests/run-static.tests.ps1`
- Modify: `deploy/compose.test.yml`
- Create: `e2e/support/ffprobe-fixture/main.go`

**Interfaces:**
- Produces `e2e/run.ps1 -CatalogSearchOnly -DeploymentMode Hybrid`.
- Runs the production worker in interactive `run` mode.
- Uses a task-scoped external ffprobe fixture through `BOOKBOX_FFPROBE_PATH`.

- [ ] **Step 1: Write static RED assertions**

Require hybrid E2E to omit the container worker, publish PostgreSQL on loopback, build a Windows worker and probe fixture, launch them through the owned process runner, set fallback to six hours, and audit exact PIDs/ports/logs after interruption. Reject Docker stop/restart calls for the worker in hybrid mode.

- [ ] **Step 2: Run RED**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File e2e/tests/run-static.tests.ps1`

Expected: hybrid lifecycle assertions fail.

- [ ] **Step 3: Add deterministic ffprobe fixture**

The fixture accepts ffprobe's production arguments, derives deterministic title/author/narrator/duration from the task manifest, and emits the expected bounded JSON. It rejects paths outside the task temp root and never reads owner libraries.

- [ ] **Step 4: Launch the native worker**

Build both Windows executables in the pinned Go container. Write a worker config with Windows temp-root paths and loopback PostgreSQL. Launch with `Start-OwnedProcess`, capture task-scoped stdout/stderr, and wait for initial snapshots through the API.

- [ ] **Step 5: Replace the bind-event proof**

Create `Monitor Trigger\Needlebox Watched.mp3` directly in the Windows temp root. Require a changed root snapshot and new raw result before the six-hour fallback. Timeout output may include only sanitized worker status and scoped log names.

- [ ] **Step 6: Preserve the PostgreSQL-only proof**

Stop the exact native worker PID, withdraw both disposable roots inside `try/finally`, repeat audiobook/raw requests with stable IDs, then restore roots before cleanup.

- [ ] **Step 7: Run focused GREEN**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File e2e/tests/catalog-search.tests.ps1 -TimeoutSeconds 900`

Expected: initial publication, native event refresh, PostgreSQL-only requests, privacy checks, and scoped cleanup PASS.

- [ ] **Step 8: Audit and commit**

Confirm exact worker/fixture PIDs exited, Compose has no project containers, task ports are free, and the validated temp root is removed. Preserve logs on failure.

```powershell
git add e2e deploy/compose.test.yml
git commit -m "test(e2e): exercise the Windows hybrid catalog worker"
```

---

### Task 11: Documentation and Complete Verification

**Files:**
- Modify: `docs/catalog.md`
- Modify: `docs/operations.md`
- Modify: `docs/android.md`

**Interfaces:**
- Documents exact Container/Hybrid commands, local-drive limitation, API address, recovery, logs, and troubleshooting.

- [ ] **Step 1: Add documentation assertions first**

Extend static documentation/smoke checks to require `container-worker`, `compose.hybrid.yml`, `127.0.0.1`, `BookBoxCatalogWorker`, `%ProgramData%\BookBox`, six-hour fallback, and local-drive-only wording.

- [ ] **Step 2: Update operator and Android docs**

State that Android connects to the Windows machine's private-LAN API address, PostgreSQL remains loopback-only, the worker waits for Docker, a missing drive retains the prior catalog, ProgramData owns logs/backups, Linux uses the container profile, and Windows requires a local drive plus explicit ffprobe.

- [ ] **Step 3: Run the complete initial failure inventory**

Run every suite sequentially and preserve all genuine failures before fixes:

```powershell
Set-Location server; go test -count=1 ./...; Set-Location ..
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/hybrid-compose.tests.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/catalog-worker-service.tests.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File e2e/tests/catalog-search.tests.ps1 -TimeoutSeconds 900
```

Use the repository's Docker Go-test pattern if host Go is unavailable. Do not overlap Android and E2E.

- [ ] **Step 4: Fix the complete inventory and rerun focused gates**

Apply evidence-backed corrections only. Rerun each affected package or script before starting the final suite wave.

- [ ] **Step 5: Run final sequential gates**

```powershell
Set-Location server; go test -count=1 ./...; Set-Location ..
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/hybrid-compose.tests.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/catalog-worker-service.tests.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File e2e/tests/catalog-search.tests.ps1 -TimeoutSeconds 900
Set-Location android; .\gradlew.bat testDebugUnitTest lintDebug; Set-Location ..
powershell -NoProfile -ExecutionPolicy Bypass -File deploy/scripts/verify-release.ps1
git diff --check
git status --short
```

If release verification reports stale artifacts, run `deploy/scripts/build-release.ps1` once through the monitored process helper, prove its process tree exits, then rerun verification.

- [ ] **Step 6: Commit docs and final evidence-backed corrections**

```powershell
git add docs/catalog.md docs/operations.md docs/android.md
git commit -m "docs: operate the Windows hybrid server"
```

- [ ] **Step 7: Audit the branch**

Require a clean tree, list task commits, and record environment-blocked gates without claiming PASS. Do not mark implementation complete while native watcher, hybrid E2E, Android, or release verification remains incomplete.

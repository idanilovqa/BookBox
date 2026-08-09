# Windows Hybrid Catalog Worker Design

**Date:** 2026-08-08
**Status:** Approved design
**Scope:** Windows-hosted BookBox installations using local drives

## Decision

BookBox will support a Windows hybrid deployment in addition to its existing all-container deployment:

- PostgreSQL and the BookBox API remain Linux containers managed by Docker Compose.
- The catalog worker runs as a native Windows Service.
- The native worker watches and scans local Windows drives directly.
- Android continues to connect only to the BookBox API over the private LAN.
- Linux and NAS installations retain an all-container mode where the worker and library are genuinely in the same Linux filesystem event domain.

The first Windows hybrid release supports local fixed and removable drives only. It does not support mapped drives, UNC paths, or network shares.

## Rationale

Docker Desktop reliably exposes Windows bind-mounted files for ordinary reads, but it does not reliably translate Windows filesystem changes into Linux inotify events. A Linux catalog worker can therefore scan a Windows bind mount while still missing the real-time events required by `fsnotify`.

The failure is specific to filesystem notification delivery across the Windows-to-Docker Desktop boundary. It does not require moving PostgreSQL or the API out of Docker. Running only the filesystem-sensitive worker on Windows preserves native NTFS notifications while retaining the existing containerized database and API.

This also matches the intended future product boundary: a Windows installer or tray application can manage a stable native service without requiring the API, database, or Android client to know how catalog work is performed.

## Considered Approaches

### 1. Native Windows Service support in BookBox

The existing Go worker gains Windows Service lifecycle support while preserving normal console execution and the Linux container build.

BookBox selects this approach because it provides native filesystem behavior, controlled recovery, and a clean future installer boundary without adding a third-party service wrapper.

### 2. WinSW or NSSM wrapper

Wrapping the existing executable would reduce initial service-host code, but it would add an external executable and make installation, upgrades, logging, and recovery depend on wrapper-specific configuration.

BookBox rejects this approach because it already owns the Go worker and benefits from a first-class service lifecycle.

### 3. Tray application and supervisor now

A tray application could manage configuration, Docker, and the worker. This phase defers that application because it would add user interface design, updates, process supervision, and installer behavior before tests prove the service boundary.

## Runtime Topology

```text
Android APK
    |
    | HTTP to the Windows machine's private-LAN address
    v
Dockerized BookBox API
    |
    | Docker backend network
    v
Dockerized PostgreSQL
    ^
    | loopback-only PostgreSQL port
    |
Native Windows BookBox Catalog Worker service
    |
    | direct Windows filesystem access and notifications
    v
Local NTFS library roots
```

### Network boundaries

- The API binds to the configured private-LAN address and is the only BookBox service reachable by Android.
- PostgreSQL publishes a hybrid-only port on `127.0.0.1`, never on the LAN.
- The Windows worker has no inbound listening port.
- The worker connects to PostgreSQL through loopback and to approved metadata providers through outbound HTTPS.
- The API continues to use PostgreSQL through the private Docker backend network.
- The API retains a read-only bind mount of the library for approved file-serving behavior. It does not depend on filesystem notifications.

Android uses the Windows server's stable LAN address, for example `http://192.168.1.50:8787`. A router DHCP reservation is the initial stability mechanism. Hostname or mDNS discovery is a future convenience feature.

## Deployment Modes

BookBox retains two supported modes:

### Linux or NAS all-container mode

- PostgreSQL container
- API container
- Catalog-worker container
- Library stored in or mounted into a genuine Linux filesystem event domain

### Windows hybrid mode

- PostgreSQL container
- API container
- Native Windows catalog-worker service
- Library stored on an approved local Windows drive

Compose requires an explicit profile for the container worker. The hybrid Compose override leaves that profile disabled and publishes PostgreSQL on a configurable loopback-only host port. The base Compose network still exposes PostgreSQL to the API.

The Windows service does not start, stop, install, or update Docker Desktop. If Docker or PostgreSQL is unavailable, the service remains alive and waits for the database.

## Configuration

The first implementation uses separate runtime configuration files because the API and worker see different paths and database endpoints:

```text
deploy/config.yml
    API root: /library/audiobooks
    Database host: postgres

%ProgramData%\BookBox\catalog-worker.yml
    Worker root: H:\Audiobooks
    Database host: 127.0.0.1
```

Both configurations use the same stable root IDs and display names. Root IDs, not absolute paths, remain the database identity and cross-process contract.

Configuration uses `path` as the canonical root-path field. The loader accepts the existing `container_path` field as a compatibility alias during migration. Each process validates its selected path with the native operating-system path rules.

The future Windows configuration UI or installer may generate both runtime configurations from one logical root selection. That UI is outside this phase.

### Windows-owned files

```text
%ProgramData%\BookBox\
|-- catalog-worker.yml
|-- secrets\
|   |-- postgres-password
|   `-- google-books-key
|-- backups\
`-- logs\
```

No worker secret, backup, or mutable runtime file belongs in the repository or installation directory.

## Windows Service Model

- Service name: `BookBoxCatalogWorker`
- Startup: automatic, delayed
- Recovery: restart after unexpected process failure
- Identity: restricted BookBox virtual service identity
- Inbound ports: none
- Filesystem access: configured local roots and `%ProgramData%\BookBox` only

The setup process grants the service identity the minimum required access to selected roots and worker-owned application data. It does not support user-scoped mapped drive letters. Network-share support will require a later service-account design.

The worker executable supports both interactive and service execution:

```text
bookbox-catalog-worker.exe run --config C:\ProgramData\BookBox\catalog-worker.yml
bookbox-catalog-worker.exe service --config C:\ProgramData\BookBox\catalog-worker.yml
```

Interactive execution is the diagnostics and E2E surface. Service execution uses the Windows Service Control Manager lifecycle while sharing the same worker initialization and runtime code.

### Startup

1. Load and validate the worker configuration.
2. Validate that every configured root is an absolute supported local Windows path.
3. Connect to PostgreSQL with capped exponential backoff.
4. Synchronize configured root metadata.
5. Start the existing durable worker runtime and startup scan scheduling.
6. Register native recursive filesystem monitoring.
7. Keep the periodic full-root scan schedule authoritative.

### Shutdown

1. Stop accepting watcher events and cancel monitor retry timers.
2. Cancel pending noncommitted work.
3. Allow the active transaction to finish or roll back within the service stop bound.
4. Close database connections.
5. Report a clean stop to the Service Control Manager.

The service writes lifecycle failures to Windows Event Log and detailed sanitized diagnostics to bounded rotating files under `%ProgramData%\BookBox\logs`.

## Catalog Update Flow

```text
Windows file create, write, rename, or remove
    -> native filesystem event
    -> five-second per-root debounce
    -> durable scan_root job in PostgreSQL
    -> root scan and inference
    -> atomic catalog and search-projection publication
    -> API reads the new PostgreSQL snapshot
    -> Android receives the updated catalog
```

Filesystem monitoring accelerates convergence; it is not the source of correctness.

- A same-root event storm collapses into one scan.
- An event arriving during a running scan creates at most one durable follow-up scan.
- Service startup schedules normal scans.
- Manual scan jobs remain supported.
- The periodic scan remains authoritative, defaulting to six hours.
- Duplicate events may cause conservative extra work but cannot partially publish a catalog.

## Failure Handling

### Root or drive unavailable

Before scanning, the worker confirms that the root exists, is a directory, resolves to an allowed local volume, and is accessible to the service identity.

If a configured drive is unavailable:

- the worker does not interpret the entire library as deleted;
- it does not replace the previous catalog or search projection;
- it records a sanitized failed attempt;
- other roots continue independently; and
- root availability and monitor registration retry with backoff.

This guard applies before any missing-file reconciliation.

### PostgreSQL or Docker unavailable

At startup the service remains alive and retries the database connection with capped exponential backoff and rate-limited diagnostics. When PostgreSQL becomes available, initialization proceeds normally.

If PostgreSQL becomes unavailable during operation, active transactions roll back, previously published snapshots remain valid, and durable jobs remain retryable. Repeated fatal runtime failures may terminate the process so Windows Service recovery can restart it.

### Watcher unavailable

A watcher failure does not stop periodic or manual scans. A supervisor loop recreates the watcher with capped backoff. Errors are sanitized so configured absolute library paths are not exposed in user-facing responses or unsafe logs.

### Scan or publication failure

The existing atomic publication contract remains unchanged: a failed scan or projection build retains the previous catalog and search snapshot. The worker records failure without publishing partial data.

## Cover Ownership

The native Windows worker is the sole component allowed to mutate approved cover filenames in library roots. The API retains read-only access.

Cover backups move from the worker's Docker volume to `%ProgramData%\BookBox\backups`. Existing path confinement, approved-filename, backup-before-replacement, and atomic replacement rules remain mandatory.

## Verification Strategy

### Cross-platform unit tests

- Per-root debounce and stale timer protection
- Root ownership and path confinement
- Follow-up job deduplication
- Monitor cancellation and restart behavior
- Missing-drive protection
- Database retry state transitions
- Shared interactive/service shutdown behavior

### Native Windows watcher integration test

The test uses a disposable NTFS directory and the real production event source. It creates, writes, renames, and removes files and verifies the expected root scan jobs. It never uses a Linux watcher over a Docker Desktop bind mount.

### Windows hybrid E2E

The harness:

1. Starts a task-scoped Docker Compose project containing PostgreSQL and the API but no container worker.
2. Builds and launches the production Windows worker executable interactively.
3. Uses a disposable local Windows library and service-style configuration.
4. Verifies initial catalog and search publication.
5. Adds a recognized file and requires a watcher-triggered snapshot change before the six-hour fallback can run.
6. Stops the worker and temporarily withdraws the disposable library.
7. Repeats API searches and verifies that request-time results come only from PostgreSQL.
8. Restores the library and removes only the exact owned process, Compose project, ports, database, and temporary files.

Interactive worker execution avoids administrator requirements while exercising the same runtime used by the service.

### Windows Service lifecycle verification

Normal tests cover the service dispatcher, stop behavior, configuration, and setup-script generation without installing a service. An opt-in elevated test may install, start, stop, and delete a uniquely named task-scoped service.

### Linux container verification

Linux CI retains an all-container monitoring E2E using a library in the Linux filesystem event domain. Docker Desktop bind-mount behavior is not accepted as evidence for Linux watcher correctness.

## Security Properties

- PostgreSQL's host-published port is loopback-only.
- The API remains the only LAN-facing component.
- The worker accepts no inbound connections.
- Secrets remain in ACL-protected files rather than command-line arguments or environment dumps.
- The service identity receives only root-specific and ProgramData permissions.
- The API library mount remains read-only.
- Paths and provider errors are sanitized before reaching client-visible responses.
- Service and E2E tooling never logs database passwords, API keys, session credentials, or full private library paths.

## Explicit Non-Goals

This phase does not include:

- a tray application or graphical installer;
- mapped drives, UNC paths, or network shares;
- automatic Docker Desktop installation, startup, or upgrades;
- a native Windows API process;
- bundled native PostgreSQL;
- PostgreSQL exposure beyond loopback;
- automatic Android server discovery or mDNS;
- remote-access or internet-facing deployment; or
- removal of the supported Linux all-container mode.

## Future Evolution

The stable Windows Service boundary allows a later tray application to configure roots, display health, manage service actions, and guide Docker startup without changing the catalog protocol. A future Windows-native API or privately managed PostgreSQL installation can also replace individual Docker components while preserving the database schema, root IDs, worker jobs, and Android API contract.

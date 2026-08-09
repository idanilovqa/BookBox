# Operate BookBox

Run commands from the repository root. Set the address and port in each PowerShell session that runs the lifecycle scripts:

```powershell
$env:BOOKBOX_BIND_ADDRESS = '192.168.1.25'
$env:BOOKBOX_HOST_PORT = '8787'
```

## Start and stop

Choose one deployment mode. Linux and NAS installations use Container mode,
which enables the `container-worker` profile from `deploy\compose.yml`:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\start-bookbox.ps1 `
  -DeploymentMode Container
```

Windows installations with local NTFS libraries use Hybrid mode. The start
script adds `deploy\compose.hybrid.yml`, leaves the container worker disabled,
and publishes PostgreSQL at `127.0.0.1:55432`. The private-LAN API address
remains the only BookBox endpoint exposed to Android.

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\start-bookbox.ps1 `
  -DeploymentMode Hybrid `
  -HybridComposeFile deploy\compose.hybrid.yml
```

The stop script reads the owned startup state and uses the same Compose files,
profile, address, port, and environment file:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\stop-bookbox.ps1
```

The start script verifies the selected address, Windows Private profile, firewall rules, API readiness, discovery process, and `bookbox.local` resolution. An identical repeated start reconciles the same Compose scope and reuses the exact owned discovery process. It refuses mismatched or tampered startup state before invoking Docker. The stop script stops its owned discovery process and runs Compose `down --remove-orphans`. Named PostgreSQL and cover volumes remain.

If the logon task started discovery from its installed copy, stop it with the installed state directory:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\stop-bookbox.ps1 `
  -StateDirectory "$env:LOCALAPPDATA\BookBox\state" `
  -ComposeFile deploy\compose.yml
```

Do not run `docker compose down --volumes` against `bookbox-prod`.

## Install and operate the Windows catalog service

Build the release artifacts before installing the service. Copy and edit the
worker example so its root IDs match `deploy\config.yml`, its paths point to the
Windows host, and its database password file exists under ProgramData. Supply an
external ffprobe executable; the installer records its hash and grants the
service read-and-execute access without copying it.

```powershell
New-Item -ItemType Directory -Force "$env:ProgramData\BookBox" | Out-Null
Copy-Item deploy\windows\catalog-worker.example.yml `
  "$env:ProgramData\BookBox\catalog-worker.yml"

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\install-catalog-worker-service.ps1 `
  -SourceExecutable artifacts\bookbox-catalog-worker.exe `
  -ConfigFile "$env:ProgramData\BookBox\catalog-worker.yml" `
  -FFProbeExecutable 'C:\Tools\ffmpeg\bin\ffprobe.exe' `
  -LibraryRoot 'H:\Audiobooks'
```

The installer creates the delayed-start `BookBoxCatalogWorker` service. It
accepts local fixed and removable drives and rejects mapped drives and UNC
paths. Use the exact service name for routine operations:

```powershell
Get-Service -Name BookBoxCatalogWorker
Start-Service -Name BookBoxCatalogWorker
Restart-Service -Name BookBoxCatalogWorker
Stop-Service -Name BookBoxCatalogWorker
```

The service waits for Docker and PostgreSQL with capped retry delays, so you do
not need to restart it after Docker becomes ready. A missing drive prevents that
root's scan and retains the prior catalog. Other roots continue to run, and the
six-hour scan remains the fallback for missed filesystem events.

Remove the owned service from an elevated PowerShell window:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\remove-catalog-worker-service.ps1
```

Removal preserves the external ffprobe, configuration, secrets, logs, and
backups unless you pass `-RemoveData`. The ownership manifest guards the service,
managed executable, Event Log source, and exact ACL entries.

## Check service and discovery status

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml ps
Invoke-RestMethod http://bookbox.local:8787/v1/ready
Resolve-DnsName bookbox.local -Type A
Get-ScheduledTask -TaskPath '\BookBox\' -TaskName 'BookBox Discovery'
Get-NetFirewallRule -Group 'BookBox'
```

The DNS result must match `BOOKBOX_BIND_ADDRESS`. Use `http://<private-ip>:8787/v1/ready` to separate an mDNS problem from an API problem.

## Logs

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml logs --tail 200 bookbox-api
docker compose --env-file deploy\.env -f deploy\compose.yml logs --tail 200 bookbox-catalog-worker
docker compose --env-file deploy\.env -f deploy\compose.yml logs --tail 100 postgres
Get-Content deploy\.state\discovery.stderr.log -Tail 100
Get-Content "$env:LOCALAPPDATA\BookBox\state\discovery.stderr.log" -Tail 100
Get-Content "$env:ProgramData\BookBox\logs\catalog-worker.log" -Tail 200
```

Logs should contain no passwords, tokens, provider keys, Android document URIs, or private source paths.
The Windows service rotates `catalog-worker.log` at 10 MiB and retains four
backups. Cover backups live under `%ProgramData%\BookBox\backups`.

## Firewall boundary

The installer creates inbound TCP 8787 and UDP 5353 rules in the `BookBox` group. Each rule uses:

- Windows Private profile
- `LocalSubnet` remote scope
- The selected private local address

Do not broaden the profile, remote scope, or local address. Do not add router port forwarding. BookBox does not support guest networks, public networks, or WAN access.

HTTP traffic has no transport encryption. Other LAN devices can observe credentials, tokens, metadata, and audiobook bytes.

Remove discovery from elevated PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\remove-bookbox-discovery.ps1
```

The removal script validates its ownership manifest and refuses unrelated or modified targets.

## Scan and metadata review

The worker scans configured roots at startup, coalesces filesystem changes for
five seconds per root, and keeps a six-hour fallback scan. A create, write,
rename, or remove event normally publishes a fresh PostgreSQL search snapshot;
the fallback covers missed watcher events. Request a rescan from Android
Settings if freshness does not advance after a change. Provider failures do not
remove local metadata. An empty Google Books key disables Google Books while
Open Library remains available.

Both audiobook and raw search are served entirely from the last completed
PostgreSQL projection; API requests do not read the source mount. Audiobook
search may span multiple roots, while raw search is confined to its selected
root. HTTP 503 `search_index_building` is expected before a root's first
successful projection. A failed refresh keeps the older snapshot queryable, so
the app can label retained results stale instead of clearing them.

To request a manual refresh outside Android, authenticate and send:

```http
POST /v1/catalog/rescan
Content-Type: application/json

{"rootId":"audiobooks"}
```

Confirm freshness using the `completedAt` value in `/v1/books` `snapshots` or
the `/v1/browse/search` `snapshot`. Do not inspect or expose host source paths
when diagnosing search; use root IDs, scan-run IDs, and worker logs.

Run this status query inside PostgreSQL to report catalog progress without
printing a source path:

```sql
select count(*) active_files from source_files where missing_at is null;
select count(*) active_books from books where missing_at is null;
select root_id, generation from catalog_generations order by root_id;
select root_id, catalog_generation projection_generation
from library_search_snapshots order by root_id;
select status, count(*) from worker_jobs
where status in ('queued','dead') group by status order by status;
select root_id, status, started_at, finished_at
from scan_runs
where id in (select distinct on (root_id) id from scan_runs order by root_id, started_at desc)
order by root_id;
```

Active files and books should remain nonzero after a successful inventory. A
projection generation can trail the catalog generation while publication waits
in the queue. Inspect dead jobs by ID, kind, attempts, and sanitized error. Do
not print job payloads because older records may contain relative source paths.

Catalog corrections affect grouping and displayed metadata. The Android app
cannot edit audio tags. Source moves require the optional capability described
in `docs/catalog.md`.

## Reorganization operations

Leave `allow_library_reorganization` disabled unless the deployment needs
physical source moves. Enable it only after the API write probe and worker root
resolver point to the same confined root. Keep live roots disabled while running
disposable E2E verification.

Monitor operations without selecting `source_path` or `destination_path`:

```sql
select id, root_id, book_id, catalog_generation, actor, status,
       created_at, updated_at
from reorganization_operations
order by created_at desc;

select operation_id, state, count(*)
from reorganization_moves
group by operation_id, state
order by operation_id, state;
```

Treat `reorganization_operations.last_error` as sensitive diagnostic data: a
rollback failure can include confined relative source or destination paths.
Inspect it only during an authorized repair, and do not copy it into routine
monitoring or support output.

For `rolled_back`, verify the source tree with its normal backup or hash tool and
request a full inventory. For `needs_attention`, stop the catalog worker first.
Use the move journal and source backup to identify the exact unresolved file.
Verify its size, modification time, and digest at one confined relative path,
then restore the planned source location without overwriting another file. Mark
journal state only through an approved repair procedure, restart the worker, and
run a full inventory. Compare the catalog and projection generations after the
publication job succeeds.

The catalog worker can publish a supported cover image after it validates the path and bytes. It saves the replaced cover in the `cover_backups` volume and appends an audit event.

## Password and session rotation

Use `deploy\scripts\rotate-password.ps1` only with the parameters required by its help and current operator procedure. A password or session-key rotation invalidates prior sessions. Android downloads and transfer history remain on the phone, and the user must enter the new password.

## Update BookBox

Keep the current deployment mode during an update. Confirm that backups and the
Windows source library are available before changing the installation.

For a Container deployment, rebuild the release artifacts and the Compose
services with the `container-worker` profile:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\stop-bookbox.ps1 `
  -DeploymentMode Container
git status --short
git pull --ff-only
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\build-release.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\audit-compose.ps1 `
  -DeploymentMode Container
docker compose --env-file deploy\.env -f deploy\compose.yml `
  --profile container-worker build
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\start-bookbox.ps1 `
  -DeploymentMode Container
```

For a Hybrid deployment, record the installed `BookBoxCatalogWorker` config
path, external ffprobe path, and library roots. Run the build steps from the
repository root:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\stop-bookbox.ps1 `
  -DeploymentMode Hybrid
git status --short
git pull --ff-only
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\build-release.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\audit-compose.ps1 `
  -DeploymentMode Hybrid
docker compose --env-file deploy\.env `
  -f deploy\compose.yml `
  -f deploy\compose.hybrid.yml build
```

Open elevated PowerShell in the repository root. Replace only the owned service,
then restart Compose in Hybrid mode and start the new worker:

```powershell
$workerConfig = "$env:ProgramData\BookBox\catalog-worker.yml"
$ffprobeExecutable = 'C:\Tools\ffmpeg\bin\ffprobe.exe'
$libraryRoots = @('H:\Audiobooks')

powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\remove-catalog-worker-service.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\install-catalog-worker-service.ps1 `
  -SourceExecutable artifacts\bookbox-catalog-worker.exe `
  -ConfigFile $workerConfig `
  -FFProbeExecutable $ffprobeExecutable `
  -LibraryRoot $libraryRoots
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\start-bookbox.ps1 `
  -DeploymentMode Hybrid
Start-Service -Name BookBoxCatalogWorker
```

The removal command preserves config, secrets, logs, backups, and the external
ffprobe by default. Do not pass `-RemoveData` during an update. Preserve the
Windows source library; an update must not replace its path or delete its
contents.

## Release artifacts

`deploy\scripts\build-release.ps1` writes APKs, `bookbox-discovery.exe`, and
`bookbox-catalog-worker.exe` under `artifacts\`. `verify-release.ps1` checks the
`com.rendref.bookbox` application ID, API 36 SDK levels, permissions, signatures,
release debuggable state, and both executables' Windows AMD64 PE identity. It
also runs the worker's side-effect-free `help` command before publication.
Compare every artifact digest with `artifacts\SHA256SUMS.txt` before installing it.

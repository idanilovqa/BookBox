# Operate BookBox

Run commands from the repository root. Production commands use the fixed Compose project in `deploy\compose.yml` and the ignored `deploy\.env`.

## Service lifecycle

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml start
docker compose --env-file deploy\.env -f deploy\compose.yml stop
docker compose --env-file deploy\.env -f deploy\compose.yml restart bookbox-api
docker compose --env-file deploy\.env -f deploy\compose.yml ps
```

`stop` preserves PostgreSQL, TLS identity, cover cache, and original-cover backups. Do not run `down --volumes` against `bookbox-prod`.

Check readiness:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\wait-healthy.ps1 `
  -BaseUrl https://<reserved-LAN-address>:8443
```

## Logs

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml logs --tail 200 bookbox-api
docker compose --env-file deploy\.env -f deploy\compose.yml logs --tail 200 bookbox-catalog-worker
docker compose --env-file deploy\.env -f deploy\compose.yml logs --tail 100 postgres
```

The API log prints the certificate fingerprint at startup. Logs should contain no passwords, tokens, provider keys, Android document URIs, or absolute private source paths.

## Scan and rescan

The worker scans configured roots at startup and on its interval. Request a rescan from Android Settings after adding books. Watch the worker log for the scan result.

Provider failures do not remove local metadata. The worker keeps inferred or confirmed catalog data and retries provider work through bounded jobs. An empty Google Books key disables Google Books while Open Library remains active.

## Review metadata and grouping

Android marks ambiguous metadata or grouping as needing review. Open the card, compare candidates and raw files, then confirm the intended match or grouping. PostgreSQL preserves confirmed choices across scans.

The app cannot edit audio tags or rearrange Windows folders. Corrections affect the catalog grouping and displayed metadata.

## Cover audit and recovery

The worker accepts supported image bytes, recognized cover basenames, and a material quality improvement. It stores replaced bytes in the `cover_backups` volume under their SHA-256 digest before writing the new cover.

Review `cover_events` before recovery. Use the operator-only recovery procedure in [Backup and recovery](backup-and-recovery.md). The Android API cannot request cover restoration. Stop if the current cover digest differs from the selected event; a later change makes that event stale.

## Password and session rotation

Follow [Backup and recovery](backup-and-recovery.md) to rotate the Argon2id password hash and session key. Rotation recreates only the API. The procedure must prove that a refresh token issued before rotation returns HTTP 401 and that the new password signs in.

The Android app retains its downloaded files and history after sign-out. It asks for the new password at the next sign-in.

## Backup and restore

Back up PostgreSQL, the TLS volume, cover cache, original-cover backup volume, and non-secret root/quality configuration before an update. Store no app password, PostgreSQL password, session key, or Google Books key in the backup.

Restore into stopped, empty destination volumes. Verify every manifest digest before `pg_restore`, and compare the restored certificate fingerprint with the backup manifest. Use the commands and confirmation boundaries in [Backup and recovery](backup-and-recovery.md).

## Add another named root

1. Stop the API and worker.
2. Add a bind mount for the new host folder to both services.
3. Keep the API mount read-only.
4. Decide whether to accept a writable worker mount for cover publication on that root.
5. Add a unique root ID, display name, and container path to `deploy\config.yml`.
6. Run the Compose audit.
7. Recreate the API and worker, then request a scan.

Do not reuse a root ID for a different host folder. Android and PostgreSQL use the ID as persistent catalog identity.

## Update BookBox

```powershell
git status --short
git pull --ff-only
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\audit-compose.ps1
docker compose --env-file deploy\.env -f deploy\compose.yml build
docker compose --env-file deploy\.env -f deploy\compose.yml up -d
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\wait-healthy.ps1 `
  -BaseUrl https://<reserved-LAN-address>:8443
```

Make and verify a backup before pulling an update that changes migrations or container storage. Preserve the TLS volume so Android keeps the same certificate pin.

## Release artifacts

`deploy\scripts\build-release.ps1` runs server, Compose, recovery, E2E, Android unit, lint, and instrumentation gates. It copies the debug APK and an optional signed release APK into `artifacts\`.

`deploy\scripts\verify-release.ps1` rejects the wrong application ID, SDK levels, permission set, invalid signature, or debuggable release build. Compare the APK digest with `artifacts\SHA256SUMS.txt` before sideloading.

The approved permission set includes network access, network-state checks, user-initiated jobs, boot recovery, and `android.permission.POST_NOTIFICATIONS` for transfer progress. A new permission requires a review and an allowlist update before packaging.

The [Galaxy S23 Ultra checklist](galaxy-s23-checklist.md) remains a manual gate. Do not record device behavior from emulator results.

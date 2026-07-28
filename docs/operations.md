# Operate BookBox

Run commands from the repository root. Set the address and port in each PowerShell session that runs the lifecycle scripts:

```powershell
$env:BOOKBOX_BIND_ADDRESS = '192.168.1.25'
$env:BOOKBOX_HOST_PORT = '8787'
```

## Start and stop

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\start-bookbox.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\stop-bookbox.ps1
```

The start script verifies the selected address, Windows Private profile, firewall rules, API readiness, discovery process, and `bookbox.local` resolution. The stop script stops its owned discovery process and runs Compose `down --remove-orphans`. Named PostgreSQL and cover volumes remain.

If the logon task started discovery from its installed copy, stop it with the installed state directory:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\stop-bookbox.ps1 `
  -StateDirectory "$env:LOCALAPPDATA\BookBox\state" `
  -ComposeFile deploy\compose.yml
```

Do not run `docker compose down --volumes` against `bookbox-prod`.

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
```

Logs should contain no passwords, tokens, provider keys, Android document URIs, or private source paths.

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

The worker scans configured roots at startup and on its interval. Request a rescan from Android Settings after adding books. Provider failures do not remove local metadata. An empty Google Books key disables Google Books while Open Library remains available.

Catalog corrections affect grouping and displayed metadata. The Android app cannot edit audio tags or rearrange Windows folders.

The catalog worker can publish a supported cover image after it validates the path and bytes. It saves the replaced cover in the `cover_backups` volume and appends an audit event.

## Password and session rotation

Use `deploy\scripts\rotate-password.ps1` only with the parameters required by its help and current operator procedure. A password or session-key rotation invalidates prior sessions. Android downloads and transfer history remain on the phone, and the user must enter the new password.

## Update BookBox

Stop BookBox, verify a recovery plan, then update and rebuild:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\stop-bookbox.ps1
git status --short
git pull --ff-only
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\audit-compose.ps1
docker compose --env-file deploy\.env -f deploy\compose.yml build
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\start-bookbox.ps1
```

Preserve the Windows source library. An update must not replace the source path or delete its contents.

## Release artifacts

`deploy\scripts\build-release.ps1` writes APKs under `artifacts\` with names such as `BookBox-v1.0.0-debug.apk`. `verify-release.ps1` checks the `com.rendref.bookbox` application ID, API 36 SDK levels, permissions, signature, and release debuggable state. Compare each APK digest with `artifacts\SHA256SUMS.txt` before sideloading.

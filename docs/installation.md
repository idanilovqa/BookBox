# Install BookBox

These steps install the server on Windows and sideload the Android 16 app on a Galaxy S23 Ultra.

## Prepare Windows

Install Docker Desktop and select Linux containers. Confirm Docker Desktop can bind the drive that holds your audiobook library. The default library is:

```text
H:\Audiobooks
```

Reserve the Windows computer's LAN IPv4 address in your router's DHCP settings. Use an address from your private home network. Do not configure router port forwarding for BookBox.

Open PowerShell in the repository root:

```powershell
Copy-Item deploy\.env.example deploy\.env
Copy-Item deploy\config.yml.example deploy\config.yml
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\new-secrets.ps1
```

If the repository contains `deploy\config.example.yml` instead of `deploy\config.yml.example`, copy that file. The secret generator asks for the Android app password twice and writes restricted secret files without printing plaintext.

Edit `deploy\.env`:

```dotenv
BOOKBOX_LIBRARY_PATH=H:/Audiobooks
BOOKBOX_BIND_ADDRESS=192.168.1.50
HOST_PORT=8443
```

Replace `192.168.1.50` with the reserved Windows address. Avoid `0.0.0.0`; bind the service to the chosen private-LAN interface.

Add the same address to `tls.hosts` in `deploy\config.yml` before the first start:

```yaml
tls:
  cert_path: /var/lib/bookbox/tls/server.crt
  key_path: /var/lib/bookbox/tls/server.key
  hosts:
    - localhost
    - 127.0.0.1
    - 192.168.1.50
```

Keep the container root at `/library/audiobooks`. The host path belongs in `.env`.

## Validate and start

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml config
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\audit-compose.ps1
docker compose --env-file deploy\.env -f deploy\compose.yml up -d --build
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\wait-healthy.ps1 `
  -BaseUrl https://192.168.1.50:8443
docker compose --env-file deploy\.env -f deploy\compose.yml ps
```

The API, catalog worker, and PostgreSQL should report healthy. PostgreSQL and the worker publish no host ports.

## Open the private Windows Firewall port

Run an elevated PowerShell window:

```powershell
New-NetFirewallRule `
  -DisplayName 'BookBox HTTPS 8443' `
  -Direction Inbound `
  -Protocol TCP `
  -LocalPort 8443 `
  -Action Allow `
  -Profile Private
```

Change the port if `HOST_PORT` differs. Keep the rule on the Private profile.

## Record the server fingerprint

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml logs bookbox-api
```

Copy the full value after `BookBox certificate SHA-256:` from the local Windows console. Do not send the fingerprint through the same network path that you are verifying.

## Build or obtain the APK

The full local release gate creates a debug-signed sideload package:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1
```

The output is `artifacts\BookBox-v1.0.0-debug.apk`. `artifacts\SHA256SUMS.txt` records its digest. A signed release APK appears beside it when you configure the optional keystore described in [Release notes](../RELEASE_NOTES.md).

Copy the APK to the Galaxy. In Samsung Settings, allow the chosen file manager or browser to install unknown apps for this installation. Open the APK, install it, then revoke that permission if you do not use it for other packages.

## Connect once

1. Put the Galaxy and Windows computer on the same trusted home Wi-Fi.
2. Open BookBox.
3. Enter `https://192.168.1.50:8443`.
4. Inspect the certificate and compare the full fingerprint with the Windows console.
5. Approve the pin only after every character matches.
6. Sign in as `owner` with the app password from secret generation.

The app remembers the server address, certificate pin, and sign-in state. It does not use automatic discovery.

## Choose the Android destination

Tap **Choose Android folder**. Select or create an internal-storage folder such as `Audiobooks`, then grant access in Android's system picker. BookBox stores the persisted tree permission.

The app writes only to this Android tree. It cannot delete, move, rename, tag, or upload files in the Windows source library.

Android 16 asks for notification permission when you start the first background transfer. Grant it so the required transfer progress notification can appear. If you deny it, return to the app's notification settings and grant permission before testing background copies.

## Run the first scan

The worker scans at startup. Open Audiobooks and pull to refresh, or request a rescan from Settings. Large libraries take time because the worker probes audio files and may query metadata providers.

Use Folders mode to compare raw source grouping. Search by title, author, genre, or year. Open **View raw files** on a card before copying if the grouping needs review.

Continue with [Android usage](android.md), [operations](operations.md), and the [Galaxy checklist](galaxy-s23-checklist.md).

# Set up the BookBox server on Windows

BookBox exposes configured audiobook files through an authenticated HTTP API. Docker mounts the source library read-only in the API container. The API has no upload, rename, move, tag-edit, or delete route. The catalog worker can write validated cover images through its separate library mount and records each cover change.

## Prerequisites

- Windows 11 with Docker Desktop using Linux containers
- Go for building the Windows discovery executable
- PowerShell 5.1 or newer
- A trusted private home network
- `H:\Audiobooks`, or another source folder

Reserve the Windows computer's LAN IPv4 address in your router. Confirm that Windows assigns the interface a Private network profile:

```powershell
Get-NetConnectionProfile
Get-NetIPAddress -AddressFamily IPv4
```

BookBox rejects non-RFC1918 discovery addresses. Do not bind BookBox to `0.0.0.0`.

## Create configuration and secrets

Run from the repository root:

```powershell
Copy-Item deploy\.env.example deploy\.env
Copy-Item deploy\config.example.yml deploy\config.yml
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\new-secrets.ps1
```

The secret script asks for the application password twice. It writes a PostgreSQL password, Argon2id application password hash, raw session key, and optional empty Google Books key file under `deploy\secrets`. It refuses to overwrite non-empty core secrets.

Edit `deploy\.env`:

```dotenv
BOOKBOX_LIBRARY_PATH=H:/Audiobooks
BOOKBOX_CONFIG_HOST_FILE=./config.yml
BOOKBOX_POSTGRES_PASSWORD_HOST_FILE=./secrets/postgres_password.txt
BOOKBOX_PASSWORD_HASH_HOST_FILE=./secrets/app_password_hash.txt
BOOKBOX_SESSION_KEY_HOST_FILE=./secrets/session_key.bin
BOOKBOX_GOOGLE_BOOKS_KEY_HOST_FILE=./secrets/google_books_key.txt
BOOKBOX_BIND_ADDRESS=192.168.1.25
BOOKBOX_HOST_PORT=8787
```

Replace the example address with the reserved address assigned to the Windows computer. Keep secrets out of `.env` and `deploy\config.yml`.

## Validate and build

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml config
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\audit-compose.ps1
docker compose --env-file deploy\.env -f deploy\compose.yml build
```

Compose should resolve the project as `bookbox-prod`, publish only the API port, keep PostgreSQL on an internal network, and mount the API source library read-only.

## Install the discovery process

Build the executable:

```powershell
New-Item -ItemType Directory -Force artifacts | Out-Null
Push-Location server
go build -o ..\artifacts\bookbox-discovery.exe .\cmd\bookbox-discovery
Pop-Location
```

Open an elevated PowerShell window. Supply the selected address and port:

```powershell
$env:BOOKBOX_BIND_ADDRESS = '192.168.1.25'
$env:BOOKBOX_HOST_PORT = '8787'
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\install-bookbox-discovery.ps1 `
  -SourceExecutable artifacts\bookbox-discovery.exe
```

The installer copies the executable to `%LOCALAPPDATA%\BookBox\bin\bookbox-discovery.exe`, copies managed scripts under `%LOCALAPPDATA%\BookBox\scripts`, writes `bookbox-install.json`, registers `\BookBox\BookBox Discovery` for logon, and installs these inbound firewall rules:

| Name | Protocol | Port | Profile | Remote scope |
|---|---:|---:|---|---|
| BookBox HTTP 8787 | TCP | 8787 | Private | LocalSubnet |
| BookBox mDNS | UDP | 5353 | Private | LocalSubnet |

Each rule also binds to the selected private local address. The installer refuses to replace unrelated rules or scheduled tasks with matching names.

## Start and verify

Set the environment in the PowerShell session used to start BookBox:

```powershell
$env:BOOKBOX_BIND_ADDRESS = '192.168.1.25'
$env:BOOKBOX_HOST_PORT = '8787'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\start-bookbox.ps1
```

The script starts Compose, waits up to two minutes for `http://192.168.1.25:8787/v1/ready`, starts discovery, and verifies `bookbox.local`.

Inspect the result:

```powershell
Resolve-DnsName bookbox.local -Type A
Invoke-RestMethod http://bookbox.local:8787/v1/ready
docker compose --env-file deploy\.env -f deploy\compose.yml ps
```

Android should connect to `http://bookbox.local:8787`. Use `http://192.168.1.25:8787` as the fallback.

## Stop and remove discovery

Stop BookBox without deleting named volumes:

```powershell
$env:BOOKBOX_BIND_ADDRESS = '192.168.1.25'
$env:BOOKBOX_HOST_PORT = '8787'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\stop-bookbox.ps1
```

The stop script shuts down its owned discovery process, runs Compose `down --remove-orphans`, and checks that the selected TCP listener closed. Named database and cover volumes remain.

After a later logon starts the installed task, point the stop script at its state directory:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\stop-bookbox.ps1 `
  -StateDirectory "$env:LOCALAPPDATA\BookBox\state" `
  -ComposeFile deploy\compose.yml
```

To remove discovery, open elevated PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\remove-bookbox-discovery.ps1
```

The removal script checks the ownership manifest, installed-file hashes, task definition, and firewall scope before removal. It refuses missing, modified, external, or unrelated targets. Optional `-InstallDirectory`, `-TaskName`, and `-TaskPath` parameters support installations created with matching custom values.

## Security boundary

HTTP provides no transport encryption. A device on the LAN can capture credentials, tokens, catalog requests, and audiobook bytes. Use BookBox only on a trusted private LAN. Keep the two firewall rules on Private plus `LocalSubnet`; do not allow guest/public networks, WAN access, or router port forwarding.

QR-based HTTPS pairing and a Windows tray approval companion remain roadmap items. The current discovery process advertises the hostname and does not approve users or devices.

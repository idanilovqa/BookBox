# Install BookBox

These steps install the Windows server, headless discovery process, and Android 16 app.

## Network requirements

Use a Windows computer and Android phone on the same trusted private LAN. Reserve the Windows computer's IPv4 address in your router. Do not use guest Wi-Fi, a Windows Public network profile, WAN access, or router port forwarding.

BookBox v1 uses unencrypted HTTP. Other devices on the LAN can observe the username, password, session tokens, catalog data, and audiobook bytes. Use a private network you control.

## Prepare the server files

Install Docker Desktop with Linux containers and confirm that Docker can access your audiobook folder. Open PowerShell in the repository root:

```powershell
Copy-Item deploy\.env.example deploy\.env
Copy-Item deploy\config.example.yml deploy\config.yml
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\new-secrets.ps1
```

Set the source library, reserved Windows address, and port in `deploy\.env`:

```dotenv
BOOKBOX_LIBRARY_PATH=H:/Audiobooks
BOOKBOX_BIND_ADDRESS=192.168.1.25
BOOKBOX_HOST_PORT=8787
```

Use an RFC1918 address assigned to a Windows interface with a Private network profile. Keep `listen: ":8787"` in `deploy\config.yml` unless you change the container port and related deployment files.

Validate and build the deployment:

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml config
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\audit-compose.ps1
docker compose --env-file deploy\.env -f deploy\compose.yml build
```

## Build and install discovery

Build the verified release artifacts. This produces both the Android package and
`artifacts\bookbox-discovery.exe`:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1
```

Open an elevated PowerShell window in the repository root. Set the same private address and port, then install the executable, scheduled task, and firewall rules:

```powershell
$env:BOOKBOX_BIND_ADDRESS = '192.168.1.25'
$env:BOOKBOX_HOST_PORT = '8787'
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\install-bookbox-discovery.ps1 `
  -SourceExecutable artifacts\bookbox-discovery.exe
```

The installer creates two inbound Windows Firewall rules:

- TCP 8787 for BookBox HTTP
- UDP 5353 for mDNS

Both rules use the Private profile, the selected local address, and `LocalSubnet` as the remote scope. The installer copies the executable and managed scripts under `%LOCALAPPDATA%\BookBox`, writes an ownership manifest, and registers `\BookBox\BookBox Discovery` for logon. The process has no tray window.

## Start and verify

Start the Docker deployment and discovery process:

```powershell
$env:BOOKBOX_BIND_ADDRESS = '192.168.1.25'
$env:BOOKBOX_HOST_PORT = '8787'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\start-bookbox.ps1
```

The start script waits for the API, starts discovery, checks process ownership, and confirms that `bookbox.local` resolves to the selected address.

Run these checks on Windows:

```powershell
Resolve-DnsName bookbox.local -Type A
Invoke-RestMethod http://bookbox.local:8787/v1/ready
docker compose --env-file deploy\.env -f deploy\compose.yml ps
```

Use `http://192.168.1.25:8787` if an Android device cannot resolve `bookbox.local`.

Stop the deployment and the discovery process started by this command:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\stop-bookbox.ps1
```

After a later logon starts the installed task, pass its state directory when stopping:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\stop-bookbox.ps1 `
  -StateDirectory "$env:LOCALAPPDATA\BookBox\state" `
  -ComposeFile deploy\compose.yml
```

Remove discovery from an elevated PowerShell window:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\remove-bookbox-discovery.ps1
```

## Install Android

The release build above produces `artifacts\BookBox-v1.0.0-debug.apk`,
`artifacts\bookbox-discovery.exe`, their metadata, and a shared
`artifacts\SHA256SUMS.txt`. Compare both artifact digests with that file. Copy the
APK to the phone and install it. If Android asks for permission to install unknown
apps, grant permission to the file manager used for this installation and revoke
it afterward.

Open BookBox and enter `http://bookbox.local:8787`. Use the username from `deploy\config.yml` and the password entered during secret generation. Enter the private-IP URL if `.local` resolution fails.

Choose a dedicated Android destination folder with the system folder picker before the first copy. BookBox retains access to that folder without broad storage permission.

Continue with [Android usage](android.md), [operations](operations.md), and the [Galaxy checklist](galaxy-s23-checklist.md).

# Set up the netStorage server on Windows

This first server release exposes configured audiobook files over authenticated HTTPS. The API can browse and download from the library bind, but Docker mounts that bind read-only and the API has no upload, rename, move, tag-edit, or delete route. PostgreSQL stores the v1 catalog, scan state, indexed metadata, grouping and match reviews, worker jobs, and cover audit events.

## Prerequisites

- Windows 11 with Docker Desktop running Linux containers
- A private home network
- `H:\Audiobooks` (or another folder you choose)
- PowerShell 5.1 or newer

In Docker Desktop, allow access to the drive that contains the library. On current Docker Desktop releases this is normally available through WSL integration; if Docker reports that the bind source is unavailable, open Docker Desktop settings and grant the relevant drive/folder access. Do not continue until Docker can bind the folder.

Reserve the Windows computer's current LAN IPv4 address in the router's DHCP settings. Before the first API start, use that exact reserved address for `NETSTORAGE_BIND_ADDRESS` and add it under `tls.hosts` in `deploy\config.yml`. A stable local DNS name may be an additional SAN, but Android still needs a reachable reserved bind address. The server certificate is generated only when it is absent. Changing `tls.hosts` later does not rotate an existing certificate.

## Create configuration and secrets

Run these commands from the repository root:

```powershell
Copy-Item deploy\.env.example deploy\.env
Copy-Item deploy\config.example.yml deploy\config.yml
New-Item -ItemType Directory -Force deploy\secrets | Out-Null
```

Expected result: untracked local files `deploy\.env` and `deploy\config.yml` exist. Git ignores them and live secret files.

Edit `deploy\.env`. Set `NETSTORAGE_LIBRARY_PATH=H:/Audiobooks` and leave `HOST_PORT=8443` unless that port is already used. The safe example bind is `127.0.0.1`, which is local-only. For Android, replace it with the explicitly reserved LAN IPv4 address, such as `192.168.1.50`. Do not use `0.0.0.0`: it listens on every Windows interface, potentially including VPN and other non-home networks. All host-source variables are required.

Edit `deploy\config.yml` and add the reserved LAN address or stable DNS name before the first start:

```yaml
tls:
  cert_path: /var/lib/netstorage/tls/server.crt
  key_path: /var/lib/netstorage/tls/server.key
  hosts:
    - localhost
    - 127.0.0.1
    - 192.168.1.50 # replace with the reserved address
```

Keep the container paths and secret-file paths from the example unchanged. Secret values belong in files, never in `.env` or YAML.

Generate a printable PostgreSQL password and an exact raw 32-byte session key without a byte-order mark:

```powershell
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
$postgresBytes = New-Object byte[] 32
$sessionBytes = New-Object byte[] 32
$rng.GetBytes($postgresBytes)
$rng.GetBytes($sessionBytes)
$rng.Dispose()

$postgresPassword = [Convert]::ToBase64String($postgresBytes)
[IO.File]::WriteAllText(
    (Resolve-Path deploy\secrets).Path + '\postgres_password.txt',
    $postgresPassword,
    [Text.UTF8Encoding]::new($false)
)
[IO.File]::WriteAllBytes(
    (Resolve-Path deploy\secrets).Path + '\session_key.bin',
    $sessionBytes
)

[Array]::Clear($postgresBytes, 0, $postgresBytes.Length)
[Array]::Clear($sessionBytes, 0, $sessionBytes.Length)
$postgresPassword = $null
```

Restrict the local secrets to your Windows account and `SYSTEM`:

```powershell
$secretDirectory = (Resolve-Path deploy\secrets).Path
$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
& icacls $secretDirectory /inheritance:r /grant:r "${identity}:(OI)(CI)(F)" '*S-1-5-18:(OI)(CI)(F)'
if ($LASTEXITCODE -ne 0) { throw 'Failed to secure the secrets directory.' }
Get-ChildItem -LiteralPath $secretDirectory -File | ForEach-Object {
    & icacls $_.FullName /inheritance:r /grant:r "${identity}:(F)" '*S-1-5-18:(F)'
    if ($LASTEXITCODE -ne 0) { throw "Failed to secure secret file: $($_.Name)" }
}
```

Expected result: each `icacls` command reports one successfully processed file or directory and no failures.

## Generate the app password hash

Compose secrets do not exist yet, so bootstrap the admin binary by building the Dockerfile directly under a unique temporary image tag:

```powershell
$bootstrapTag = 'netstorage-bootstrap-' + [Guid]::NewGuid().ToString('N')
docker build -f deploy\Dockerfile.api -t $bootstrapTag .
```

Expected result: Docker reports a successful image build tagged with the generated name.

The following PowerShell reads the password twice without displaying it, sends it only over redirected stdin, accepts exactly one Argon2id output line, and writes UTF-8 without a byte-order mark:

```powershell
$firstSecure = Read-Host 'App password' -AsSecureString
$secondSecure = Read-Host 'Confirm app password' -AsSecureString

function Convert-SecureValue([Security.SecureString]$Value) {
    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Value)
    try {
        [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
    } finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer)
    }
}

$first = Convert-SecureValue $firstSecure
$second = Convert-SecureValue $secondSecure
if ($first -cne $second) {
    $first = $null
    $second = $null
    throw 'Passwords do not match.'
}

$startInfo = [Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = 'docker'
$startInfo.Arguments = "run --rm -i --entrypoint netstorage-admin $bootstrapTag hash-password --stdin"
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $true
$startInfo.RedirectStandardInput = $true
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$process = [Diagnostics.Process]::new()
$process.StartInfo = $startInfo
if (-not $process.Start()) { throw 'Could not start Docker.' }
$process.StandardInput.WriteLine($first)
$process.StandardInput.WriteLine($second)
$process.StandardInput.Close()
$hashOutput = $process.StandardOutput.ReadToEnd()
$hashError = $process.StandardError.ReadToEnd()
$process.WaitForExit()
$first = $null
$second = $null
if ($process.ExitCode -ne 0) {
    throw "Password hashing failed: $hashError"
}

$hashLines = @($hashOutput -split '\r?\n' | Where-Object { $_ })
if (
    $hashLines.Count -ne 1 -or
    $hashLines[0] -notmatch '^\$argon2id\$v=19\$m=65536,t=3,p=2\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$'
) {
    throw 'The admin command did not return exactly one valid Argon2id line.'
}
[IO.File]::WriteAllText(
    (Resolve-Path deploy\secrets).Path + '\app_password_hash.txt',
    $hashLines[0],
    [Text.UTF8Encoding]::new($false)
)
$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
& icacls deploy\secrets\app_password_hash.txt /inheritance:r /grant:r "${identity}:(F)" '*S-1-5-18:(F)'
if ($LASTEXITCODE -ne 0) { throw 'Failed to secure the app password hash.' }
docker image rm $bootstrapTag
```

Expected result: one secret file is created, `icacls` reports success, and Docker removes only the uniquely tagged bootstrap image. The plaintext password is not an argument, console output, environment variable, or file.

## Validate and start

Always supply the deploy env file when running Compose from the repository root:

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml config
docker compose --env-file deploy\.env -f deploy\compose.yml build
docker compose --env-file deploy\.env -f deploy\compose.yml up -d --wait --wait-timeout 180
docker compose --env-file deploy\.env -f deploy\compose.yml ps
```

The equivalent short commands from inside the `deploy` directory are:

```powershell
docker compose -f compose.yml config
docker compose -f compose.yml build
docker compose -f compose.yml up -d --wait --wait-timeout 180
docker compose -f compose.yml ps
```

Expected results:

- `config` renders the project as `netstorage-prod`, the audiobook destination as `/library/audiobooks` with `read_only: true`, and no published PostgreSQL port.
- `build` finishes both Go binaries and the nonroot runtime image.
- `up -d` creates the persistent PostgreSQL and TLS volumes and starts both services.
- `ps` shows `postgres` and `netstorage-api` as healthy, with HTTPS published on port `8443`.

The image uses UID/GID `10001`, a read-only root filesystem, no Linux capabilities, no-new-privileges, and a writable tmpfs only for copied mode-`0600` API secrets. The audiobook bind is read-only. TLS state is the only writable API volume.

## Firewall and Android connection

In an elevated PowerShell window, read `HOST_PORT` from the deploy environment and allow only that port on the private Windows Firewall profile:

```powershell
$hostPortSetting = @(
    Get-Content deploy\.env |
        Where-Object { $_ -match '^HOST_PORT=(?<port>[1-9][0-9]{0,4})$' }
)
if ($hostPortSetting.Count -ne 1) { throw 'deploy\.env must contain one valid HOST_PORT.' }
$hostPort = [int]($hostPortSetting[0] -replace '^HOST_PORT=', '')
if ($hostPort -gt 65535) { throw 'HOST_PORT is outside the TCP port range.' }
New-NetFirewallRule `
    -DisplayName "netStorage HTTPS $hostPort" `
    -Direction Inbound `
    -Protocol TCP `
    -LocalPort $hostPort `
    -Action Allow `
    -Profile Private
```

Do not forward port `8443` on the router. This service is intended for the home LAN.

Inspect the first API log:

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml logs netstorage-api
```

Find `netStorage certificate SHA-256: ...`. On the Galaxy S23 Ultra, enter `https://<reserved-address>:<HOST_PORT>` once and compare the entire fingerprint shown by Android with this local log before approving it. Never approve an unexpected fingerprint. Reject the connection if any character differs, even if the server address appears correct.

## Back up or intentionally rotate the TLS identity

The TLS volume contains the private key that Android pins. Back it up before an update or SAN change. Stop only the API, copy the volume to a protected local archive, secure that archive, and restart:

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml stop netstorage-api
$backupDirectory = (New-Item -ItemType Directory -Force "backups\netstorage-$(Get-Date -Format yyyyMMdd-HHmmss)").FullName
docker run --rm --read-only `
    -v netstorage-prod_tls_data:/source:ro `
    -v "${backupDirectory}:/backup" `
    alpine:3.22 `
    tar -czf /backup/netstorage-tls.tar.gz -C /source .
if ($LASTEXITCODE -ne 0) { throw 'TLS backup failed.' }
$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
& icacls "$backupDirectory\netstorage-tls.tar.gz" /inheritance:r /grant:r "${identity}:(F)" '*S-1-5-18:(F)'
if ($LASTEXITCODE -ne 0) { throw 'Failed to secure the TLS backup.' }
docker compose --env-file deploy\.env -f deploy\compose.yml start netstorage-api
```

Adding or changing `tls.hosts` does not update an existing certificate. To rotate deliberately, first make and verify the backup above, then stop and remove only the API container, remove only `netstorage-prod_tls_data`, and recreate the API:

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml stop netstorage-api
docker compose --env-file deploy\.env -f deploy\compose.yml rm -f netstorage-api
docker volume rm netstorage-prod_tls_data
docker compose --env-file deploy\.env -f deploy\compose.yml up -d --wait --wait-timeout 180
docker compose --env-file deploy\.env -f deploy\compose.yml logs netstorage-api
```

This intentionally changes the server identity. Verify the new fingerprint from the local Windows host and approve a fresh pin on Android. Do not approve it merely because a rotation was expected. Never remove `netstorage-prod_postgres_data` during certificate rotation.

## Routine operation

```powershell
# Start existing containers
docker compose --env-file deploy\.env -f deploy\compose.yml start

# Stop without deleting data
docker compose --env-file deploy\.env -f deploy\compose.yml stop

# Restart only the API
docker compose --env-file deploy\.env -f deploy\compose.yml restart netstorage-api

# Follow API logs
docker compose --env-file deploy\.env -f deploy\compose.yml logs -f --tail 100 netstorage-api

# Rebuild after an update
docker compose --env-file deploy\.env -f deploy\compose.yml build
docker compose --env-file deploy\.env -f deploy\compose.yml up -d --wait --wait-timeout 180
```

Never run `docker compose down --volumes` for the production project. It deletes the persistent TLS identity and PostgreSQL data. Back up the named volumes before any intentional migration or destructive maintenance.

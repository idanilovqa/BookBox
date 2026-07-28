$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$compose = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\compose.yml'))
if (-not (Test-Path -LiteralPath $compose -PathType Leaf)) {
    throw "Compose file does not exist: $compose"
}
$composeText = Get-Content -LiteralPath $compose -Raw
if ($composeText -notmatch [regex]::Escape('host_ip: "${BOOKBOX_BIND_ADDRESS:?Set BOOKBOX_BIND_ADDRESS to a private LAN address}"')) {
    throw 'Compose must require an explicit private LAN bind address.'
}

$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$dockerIgnore = Join-Path $repo '.dockerignore'
$requiredDockerIgnores = @(
    '.git',
    '.worktrees',
    'deploy/.env',
    'deploy/config.yml',
    'deploy/secrets/*',
    '**/.gradle',
    '**/build',
    '**/dist'
)
if (-not (Test-Path -LiteralPath $dockerIgnore -PathType Leaf)) {
    throw 'The root .dockerignore is missing.'
}
$dockerIgnoreLines = @(Get-Content -LiteralPath $dockerIgnore)
foreach ($requiredIgnore in $requiredDockerIgnores) {
    if ($dockerIgnoreLines -cnotcontains $requiredIgnore) {
        throw "The root .dockerignore is missing required exclusion: $requiredIgnore"
    }
}
$gitAttributes = Join-Path $repo '.gitattributes'
if (-not (Test-Path -LiteralPath $gitAttributes -PathType Leaf)) {
    throw 'The root .gitattributes is missing.'
}
$gitAttributeLines = @(Get-Content -LiteralPath $gitAttributes)
if ($gitAttributeLines -cnotcontains 'deploy/entrypoint-api.sh text eol=lf') {
    throw 'The API entrypoint is not protected from CRLF conversion.'
}
if ($gitAttributeLines -cnotcontains 'deploy/entrypoint-worker.sh text eol=lf') {
    throw 'The worker entrypoint is not protected from CRLF conversion.'
}
$environmentExample = Join-Path $repo 'deploy\.env.example'
$environmentExampleLines = @(Get-Content -LiteralPath $environmentExample)
if (-not @($environmentExampleLines | Where-Object {
    $_ -match '^BOOKBOX_BIND_ADDRESS=(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.)'
})) {
    throw 'The example API bind must use an RFC1918 private address.'
}
if ($environmentExampleLines -cnotcontains 'BOOKBOX_HOST_PORT=8787') {
    throw 'The example API host port must be 8787.'
}
$bindRecord = Get-NetIPAddress -AddressFamily IPv4 -ErrorAction Stop |
    Where-Object {
        $_.AddressState -eq 'Preferred' -and
        $_.IPAddress -match '^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.)'
    } |
    Where-Object {
        (Get-NetConnectionProfile -InterfaceIndex $_.InterfaceIndex -ErrorAction SilentlyContinue).
            NetworkCategory -eq 'Private'
    } |
    Select-Object -First 1
if (-not $bindRecord) {
    throw 'Smoke test requires an assigned RFC1918 address on a Windows Private network.'
}
$bindAddress = $bindRecord.IPAddress
$project = ('bookbox-smoke-' + [Guid]::NewGuid().ToString('N')).ToLowerInvariant()
$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
$scratch = [IO.Path]::GetFullPath((Join-Path $tempRoot $project))
if (
    -not [string]::Equals(
        [IO.Path]::GetDirectoryName($scratch),
        $tempRoot,
        [StringComparison]::OrdinalIgnoreCase
    ) -or
    -not [string]::Equals(
        [IO.Path]::GetFileName($scratch),
        $project,
        [StringComparison]::Ordinal
    )
) {
    throw "Unsafe smoke scratch path: $scratch"
}
$library = Join-Path $scratch 'library'
$secrets = Join-Path $scratch 'secrets'
$config = Join-Path $scratch 'config.yml'
$envFile = Join-Path $scratch '.env'
$ownedImages = @(
    "${project}-bookbox-api:latest"
    "${project}-bookbox-catalog-worker:latest"
)
$cleanupRequired = $false
$primaryError = $null
$baselineRunning = @(& docker ps -q)
if ($LASTEXITCODE -ne 0) {
    throw 'Unable to capture the baseline Docker container set.'
}

function Invoke-Docker {
    param([Parameter(Mandatory)][string[]] $Arguments)
    $previousErrorAction = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = @(& docker @Arguments 2>&1)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }
    if ($exitCode -ne 0) {
        throw "docker $($Arguments -join ' ') failed:`n$($output -join [Environment]::NewLine)"
    }
    return $output
}

function Get-SourceSnapshot {
    param([Parameter(Mandatory)][string] $Path)
    $entries = Get-ChildItem -LiteralPath $Path -Recurse -Force | Sort-Object FullName
    return @($entries | ForEach-Object {
        $relative = $_.FullName.Substring($Path.Length).TrimStart('\', '/')
        if ($_.PSIsContainer) {
            "directory|$relative|$($_.Attributes)|$($_.LastWriteTimeUtc.Ticks)"
        } else {
            $digest = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
            "file|$relative|$($_.Attributes)|$($_.Length)|$($_.LastWriteTimeUtc.Ticks)|$digest"
        }
    })
}

function Assert-NoOwnedResources {
    $filter = "label=com.docker.compose.project=$project"
    $containers = @(Invoke-Docker @('ps', '-aq', '--filter', $filter))
    $networks = @(Invoke-Docker @('network', 'ls', '-q', '--filter', $filter))
    $volumes = @(Invoke-Docker @('volume', 'ls', '-q', '--filter', $filter))
    $images = @()
    foreach ($ownedImage in $ownedImages) {
        $imageIDs = @(& docker image ls --quiet $ownedImage 2>&1)
        if ($LASTEXITCODE -ne 0) {
            throw "Could not inspect the owned smoke image: $ownedImage"
        }
        $images += $imageIDs
    }
    if (
        $containers.Count -ne 0 -or
        $networks.Count -ne 0 -or
        $volumes.Count -ne 0 -or
        $images.Count -ne 0
    ) {
        throw "Owned resources remain: containers=$($containers -join ',') networks=$($networks -join ',') volumes=$($volumes -join ',') images=$($images -join ',')"
    }
}

try {
    New-Item -ItemType Directory -Force $library, $secrets | Out-Null
    New-Item -ItemType Directory -Force (Join-Path $library 'Author\Book') | Out-Null

    $configText = @"
listen: ":8787"
roots:
  - id: audiobooks
    name: Audiobooks
    container_path: /library/audiobooks
auth:
  username: owner
  password_hash_file: /run/bookbox-secrets/app_password_hash
  session_key_file: /run/bookbox-secrets/session_key
database:
  host: postgres
  port: 5432
  name: bookbox
  username: bookbox
  password_file: /run/bookbox-secrets/postgres_password
  ssl_mode: disable
"@
    [IO.File]::WriteAllText($config, $configText, [Text.UTF8Encoding]::new($false))

    [IO.File]::WriteAllText(
        (Join-Path $secrets 'app_password_hash.txt'),
        'pending-build-generated-hash',
        [Text.UTF8Encoding]::new($false)
    )
    [IO.File]::WriteAllBytes(
        (Join-Path $secrets 'session_key.bin'),
        [Text.Encoding]::ASCII.GetBytes('0123456789abcdef0123456789abcdef')
    )
    [IO.File]::WriteAllText(
        (Join-Path $secrets 'postgres_password.txt'),
        'synthetic-smoke-postgres-password',
        [Text.UTF8Encoding]::new($false)
    )
    [IO.File]::WriteAllBytes(
        (Join-Path $secrets 'google_books_key.txt'),
        [byte[]]::new(0)
    )

    $toComposePath = {
        param([string] $Path)
        $Path.Replace('\', '/')
    }
    $envText = @(
        "BOOKBOX_LIBRARY_PATH=$(& $toComposePath $library)"
        "BOOKBOX_CONFIG_HOST_FILE=$(& $toComposePath $config)"
        "BOOKBOX_POSTGRES_PASSWORD_HOST_FILE=$(& $toComposePath (Join-Path $secrets 'postgres_password.txt'))"
        "BOOKBOX_PASSWORD_HASH_HOST_FILE=$(& $toComposePath (Join-Path $secrets 'app_password_hash.txt'))"
        "BOOKBOX_SESSION_KEY_HOST_FILE=$(& $toComposePath (Join-Path $secrets 'session_key.bin'))"
        "BOOKBOX_GOOGLE_BOOKS_KEY_HOST_FILE=$(& $toComposePath (Join-Path $secrets 'google_books_key.txt'))"
        "BOOKBOX_BIND_ADDRESS=$bindAddress"
        'BOOKBOX_HOST_PORT=0'
    ) -join [Environment]::NewLine
    [IO.File]::WriteAllText($envFile, $envText, [Text.UTF8Encoding]::new($false))

    $composeArgs = @('compose', '--env-file', $envFile, '-p', $project, '-f', $compose)
    $cleanupRequired = $true
    Invoke-Docker ($composeArgs + @('build')) | Out-Null

    $adminInput = "smoke-password`nsmoke-password`n"
    $passwordHashOutput = @(
        $adminInput |
            & docker run --rm -i --network none --read-only `
                --entrypoint /usr/local/bin/bookbox-admin `
                $ownedImages[0] hash-password --stdin 2>&1
    )
    if ($LASTEXITCODE -ne 0 -or $passwordHashOutput.Count -ne 1) {
        throw "Could not generate the smoke password hash:`n$($passwordHashOutput -join [Environment]::NewLine)"
    }
    $passwordHash = $passwordHashOutput[0].ToString().Trim()
    if ($passwordHash -notmatch '^\$argon2id\$') {
        throw 'The admin tool returned an invalid password hash.'
    }
    [IO.File]::WriteAllText(
        (Join-Path $secrets 'app_password_hash.txt'),
        $passwordHash,
        [Text.UTF8Encoding]::new($false)
    )

    Invoke-Docker ($composeArgs + @(
        'run', '--rm', '--no-deps', '--entrypoint', 'ffmpeg',
        'bookbox-catalog-worker',
        '-hide_banner', '-loglevel', 'error',
        '-f', 'lavfi', '-i', 'anullsrc=channel_layout=mono:sample_rate=8000',
        '-t', '0.25', '-c:a', 'aac', '-y',
        '/library/audiobooks/Author/Book/chapter.m4a'
    )) | Out-Null
    $fixedTimestamp = [DateTime]::SpecifyKind(
        [DateTime]::Parse('2020-01-02T03:04:05'),
        [DateTimeKind]::Utc
    )
    Get-ChildItem -LiteralPath $library -Recurse -Force |
        Sort-Object FullName -Descending |
        ForEach-Object { $_.LastWriteTimeUtc = $fixedTimestamp }
    $before = Get-SourceSnapshot $library

    Invoke-Docker ($composeArgs + @('up', '-d', '--wait', '--wait-timeout', '180')) | Out-Null

    $publishedPorts = @(
        Invoke-Docker ($composeArgs + @('port', 'bookbox-api', '8787')) |
            ForEach-Object { $_.ToString().Trim() } |
            Where-Object { $_ }
    )
    if (
        $publishedPorts.Count -ne 1 -or
        $publishedPorts[0] -notmatch ('^{0}:(?<port>[1-9][0-9]{{0,4}})$' -f
            [regex]::Escape($bindAddress))
    ) {
        throw "Unexpected Compose HTTP publication: $($publishedPorts -join ', ')"
    }
    $port = [int]$Matches.port
    if ($port -gt 65535) {
        throw "Compose returned an invalid HTTP port: $port"
    }

    $apiID = (Invoke-Docker ($composeArgs + @('ps', '-q', 'bookbox-api')) | Select-Object -First 1).Trim()
    if (-not $apiID) {
        throw 'Compose did not return an API container ID.'
    }
    $inspection = (Invoke-Docker @('inspect', $apiID) | ConvertFrom-Json)[0]
    $audioMount = @($inspection.Mounts | Where-Object Destination -eq '/library/audiobooks')
    if ($audioMount.Count -ne 1 -or $audioMount[0].RW -ne $false) {
        throw 'The API audiobook bind is missing or writable.'
    }
    if ($inspection.HostConfig.ReadonlyRootfs -ne $true) {
        throw 'The API root filesystem is not read-only.'
    }
    if ($inspection.Config.User -ne '10001:10001') {
        throw "Unexpected API user: $($inspection.Config.User)"
    }
    if (@($inspection.HostConfig.CapDrop) -notcontains 'ALL') {
        throw 'The API does not drop all capabilities.'
    }
    if (@($inspection.HostConfig.SecurityOpt) -notcontains 'no-new-privileges:true') {
        throw 'The API does not enable no-new-privileges.'
    }
    if ($inspection.HostConfig.Init -ne $true) {
        throw 'The API does not enable an init process.'
    }
    if (-not $inspection.HostConfig.Tmpfs.'/run/bookbox-secrets') {
        throw 'The API copied-secret tmpfs is missing.'
    }

    $workerID = (Invoke-Docker ($composeArgs + @(
        'ps', '-q', 'bookbox-catalog-worker'
    )) | Select-Object -First 1).Trim()
    if (-not $workerID) {
        throw 'Compose did not return a catalog worker container ID.'
    }
    $workerInspection = (Invoke-Docker @('inspect', $workerID) | ConvertFrom-Json)[0]
    $workerAudioMount = @(
        $workerInspection.Mounts |
            Where-Object Destination -eq '/library/audiobooks'
    )
    if ($workerAudioMount.Count -ne 1 -or $workerAudioMount[0].RW -ne $true) {
        throw 'The catalog worker audiobook bind is missing or read-only.'
    }
    if ($workerInspection.HostConfig.ReadonlyRootfs -ne $true) {
        throw 'The catalog worker root filesystem is not read-only.'
    }
    if ($workerInspection.Config.User -ne '10001:10001') {
        throw "Unexpected catalog worker user: $($workerInspection.Config.User)"
    }
    if (@($workerInspection.HostConfig.CapDrop) -notcontains 'ALL') {
        throw 'The catalog worker does not drop all capabilities.'
    }
    if (@($workerInspection.HostConfig.SecurityOpt) -notcontains 'no-new-privileges:true') {
        throw 'The catalog worker does not enable no-new-privileges.'
    }
    if ($workerInspection.HostConfig.Init -ne $true) {
        throw 'The catalog worker does not enable an init process.'
    }
    if (@($workerInspection.NetworkSettings.Ports.PSObject.Properties).Count -ne 0) {
        throw 'The catalog worker unexpectedly publishes a port.'
    }
    $workerDatabaseMode = (Invoke-Docker @(
        'exec', $workerID, 'stat', '-c', '%a %u %g',
        '/run/bookbox-secrets/postgres_password'
    ) | Select-Object -First 1).Trim()
    if ($workerDatabaseMode -ne '600 10001 10001') {
        throw "The worker database secret has unexpected mode/owner: $workerDatabaseMode"
    }

    foreach ($secretName in @(
        'app_password_hash',
        'session_key',
        'postgres_password'
    )) {
        $mode = (Invoke-Docker @(
            'exec', $apiID, 'stat', '-c', '%a %u %g',
            "/run/bookbox-secrets/$secretName"
        ) | Select-Object -First 1).Trim()
        if ($mode -ne '600 10001 10001') {
            throw "Copied secret $secretName has unexpected mode/owner: $mode"
        }
    }
    $sourceHashDigest = (Get-FileHash -LiteralPath (Join-Path $secrets 'app_password_hash.txt') -Algorithm SHA256).Hash.ToLowerInvariant()
    $sourceKeyDigest = (Get-FileHash -LiteralPath (Join-Path $secrets 'session_key.bin') -Algorithm SHA256).Hash.ToLowerInvariant()
    $sourceDatabaseDigest = (Get-FileHash -LiteralPath (Join-Path $secrets 'postgres_password.txt') -Algorithm SHA256).Hash.ToLowerInvariant()
    $containerHashLine = @(Invoke-Docker @('exec', $apiID, 'sha256sum', '/run/bookbox-secrets/app_password_hash'))[0].ToString()
    $containerKeyLine = @(Invoke-Docker @('exec', $apiID, 'sha256sum', '/run/bookbox-secrets/session_key'))[0].ToString()
    $containerDatabaseLine = @(Invoke-Docker @('exec', $apiID, 'sha256sum', '/run/bookbox-secrets/postgres_password'))[0].ToString()
    $workerDatabaseLine = @(Invoke-Docker @('exec', $workerID, 'sha256sum', '/run/bookbox-secrets/postgres_password'))[0].ToString()
    $containerHashDigest = ($containerHashLine -split '\s+')[0]
    $containerKeyDigest = ($containerKeyLine -split '\s+')[0]
    $containerDatabaseDigest = ($containerDatabaseLine -split '\s+')[0]
    $workerDatabaseDigest = ($workerDatabaseLine -split '\s+')[0]
    if (
        $containerHashDigest -ne $sourceHashDigest -or
        $containerKeyDigest -ne $sourceKeyDigest -or
        $containerDatabaseDigest -ne $sourceDatabaseDigest -or
        $workerDatabaseDigest -ne $sourceDatabaseDigest
    ) {
        throw 'A copied container secret differs from its source.'
    }

    $healthBody = @(& curl.exe --fail --silent --show-error "http://${bindAddress}:$port/v1/health")
    if ($LASTEXITCODE -ne 0) {
        throw 'The host HTTP health request failed.'
    }
    $health = $healthBody -join ''
    if (($health | ConvertFrom-Json).status -ne 'ok') {
        throw "Unexpected API health response: $health"
    }

    $loginFile = Join-Path $scratch 'login.json'
    [IO.File]::WriteAllText(
        $loginFile,
        '{"username":"owner","password":"smoke-password"}',
        [Text.UTF8Encoding]::new($false)
    )
    $loginBody = @(
        & curl.exe --fail --silent --show-error `
            --header 'Content-Type: application/json' `
            --data-binary "@$loginFile" `
            "http://${bindAddress}:$port/v1/session"
    )
    if ($LASTEXITCODE -ne 0) {
        throw 'The smoke login request failed.'
    }
    $accessToken = (($loginBody -join '') | ConvertFrom-Json).accessToken
    if (-not $accessToken) {
        throw 'The smoke login response did not contain an access token.'
    }

    $books = $null
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        $booksBody = @(
            & curl.exe --fail --silent --show-error `
                --header "Authorization: Bearer $accessToken" `
                "http://${bindAddress}:$port/v1/books?limit=10"
        )
        if ($LASTEXITCODE -ne 0) {
            throw 'The authenticated catalog request failed.'
        }
        $books = ($booksBody -join '') | ConvertFrom-Json
        if (@($books.items).Count -gt 0) {
            break
        }
        Start-Sleep -Seconds 1
    }
    if (@($books.items).Count -ne 1) {
        $workerLogs = Invoke-Docker ($composeArgs + @(
            'logs', '--no-color', 'bookbox-catalog-worker'
        ))
        throw "The startup scan did not index exactly one audiobook:`n$($workerLogs -join [Environment]::NewLine)"
    }
    if ($books.items[0].sourceFolder -ne 'Author/Book') {
        throw "The indexed audiobook has an unexpected source folder: $($books.items[0].sourceFolder)"
    }

    $after = Get-SourceSnapshot $library
    $sourceDifferences = @(Compare-Object $before $after)
    if ($sourceDifferences.Count -ne 0) {
        $differenceText = @($sourceDifferences | ForEach-Object {
            "$($_.SideIndicator) $($_.InputObject)"
        }) -join [Environment]::NewLine
        throw "The synthetic audiobook source tree changed:`n$differenceText"
    }
} catch {
    $caught = $_
    $primaryError = $caught
    if ($cleanupRequired) {
        try {
            $composeStatus = Invoke-Docker ($composeArgs + @('ps', '--all'))
            $composeLogs = Invoke-Docker ($composeArgs + @(
                'logs', '--no-color', '--tail', '200'
            ))
            $primaryError = [Exception]::new(
                "$($caught.Exception.Message)`nCompose status:`n" +
                "$($composeStatus -join [Environment]::NewLine)`nCompose logs:`n" +
                "$($composeLogs -join [Environment]::NewLine)",
                $caught.Exception
            )
        } catch {
            Write-Warning "Could not capture failed smoke diagnostics: $_"
        }
    }
} finally {
    if ($cleanupRequired) {
        try {
            Invoke-Docker ($composeArgs + @('down', '--volumes', '--remove-orphans', '--timeout', '10')) | Out-Null
        } catch {
            if ($null -eq $primaryError) {
                $primaryError = $_
            } else {
                Write-Warning $_
            }
        }
    }
    foreach ($ownedImage in $ownedImages) {
        $ownedImageID = @(& docker image ls --quiet $ownedImage 2>&1)
        if ($LASTEXITCODE -ne 0) {
            if ($null -eq $primaryError) {
                $primaryError = "Could not inspect the owned smoke image: $ownedImage"
            }
        } elseif ($ownedImageID.Count -gt 0) {
            try {
                Invoke-Docker @('image', 'rm', $ownedImage) | Out-Null
            } catch {
                if ($null -eq $primaryError) {
                    $primaryError = $_
                } else {
                    Write-Warning $_
                }
            }
        }
    }
    try {
        Assert-NoOwnedResources
        foreach ($containerID in $baselineRunning) {
            if (-not $containerID) { continue }
            $running = (Invoke-Docker @('inspect', '--format', '{{.State.Running}}', $containerID) | Select-Object -First 1).Trim()
            if ($running -ne 'true') {
                throw "Baseline container is no longer running: $containerID"
            }
        }
    } catch {
        if ($null -eq $primaryError) {
            $primaryError = $_
        } else {
            Write-Warning $_
        }
    }
    if (Test-Path -LiteralPath $scratch) {
        if (
            -not [string]::Equals(
                [IO.Path]::GetDirectoryName($scratch),
                $tempRoot,
                [StringComparison]::OrdinalIgnoreCase
            ) -or
            -not [string]::Equals(
                [IO.Path]::GetFileName($scratch),
                $project,
                [StringComparison]::Ordinal
            )
        ) {
            throw "Refusing to remove unsafe smoke scratch path: $scratch"
        }
        Remove-Item -LiteralPath $scratch -Recurse -Force
    }
}

if ($null -ne $primaryError) {
    throw $primaryError
}

Write-Host "PASS: $project indexed one real audiobook, preserved its source, and was fully removed."

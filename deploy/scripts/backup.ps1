[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $Destination,
    [Parameter(Mandatory)]
    [string] $ProjectName,
    [Parameter(Mandatory)]
    [string[]] $ComposeFile,
    [Parameter(Mandatory)]
    [string] $EnvironmentFile,
    [string] $DatabaseName = 'bookbox',
    [Parameter(Mandatory)]
    [string] $ConfigFile,
    [string] $AppVersion = 'development'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Invoke-Docker {
    param([string[]] $Arguments)
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = @(& docker @Arguments 2>&1)
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "Docker command failed:`n$($output -join [Environment]::NewLine)"
    }
    return $output
}

function Get-ServiceVolume {
    param($Resolved, [string] $Service, [string] $Target)
    $mounts = @($Resolved.services.$Service.volumes | Where-Object {
        $_.type -eq 'volume' -and $_.target -eq $Target
    })
    if ($mounts.Count -ne 1) {
        throw "$Service must have exactly one volume at $Target."
    }
    $property = $Resolved.volumes.PSObject.Properties[$mounts[0].source]
    if (-not $property -or -not $property.Value.name) {
        throw "Could not resolve the project volume at $Target."
    }
    $name = [string] $property.Value.name
    if ($name -notlike "$ProjectName*") {
        throw "Volume is outside the requested recovery project: $name"
    }
    return $name
}

function Export-Volume {
    param([string] $Volume, [string] $FileName)
    Invoke-Docker @(
        'run', '--rm', '--network', 'none', '--read-only',
        '--mount', "type=volume,src=$Volume,dst=/source,readonly",
        '--mount', "type=bind,src=$($script:destination.Replace('\','/')),dst=/output",
        '--entrypoint', 'tar', 'postgres:18-alpine',
        '-czf', "/output/$FileName", '-C', '/source', '.'
    ) | Out-Null
}

if ($ProjectName -notlike 'bookbox-recovery-*') {
    throw 'This build permits backup only for a bookbox-recovery-* synthetic project.'
}
$recoveryRoot = [IO.Path]::GetFullPath(
    (Join-Path ([IO.Path]::GetTempPath()) 'bookbox-recovery')
).TrimEnd('\') + '\'
$script:destination = [IO.Path]::GetFullPath($Destination)
if (-not $script:destination.StartsWith(
    $recoveryRoot,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "Synthetic backup destination must be beneath $recoveryRoot"
}
if (Test-Path -LiteralPath $script:destination) {
    if (-not (Test-Path -LiteralPath $script:destination -PathType Container) -or
        @(Get-ChildItem -LiteralPath $script:destination -Force).Count -ne 0) {
        throw 'Backup destination must be a new or empty directory.'
    }
} else {
    [IO.Directory]::CreateDirectory($script:destination) | Out-Null
}

$environment = [IO.Path]::GetFullPath($EnvironmentFile)
$config = [IO.Path]::GetFullPath($ConfigFile)
$composeFiles = @($ComposeFile | ForEach-Object { [IO.Path]::GetFullPath($_) })
foreach ($path in @($composeFiles) + @($environment, $config)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required recovery input not found: $path"
    }
}
$composeArgs = @('compose', '--env-file', $environment, '-p', $ProjectName)
foreach ($path in $composeFiles) {
    $composeArgs += @('-f', $path)
}
$resolved = ((Invoke-Docker (@($composeArgs) + @(
    'config', '--format', 'json'
))) -join "`n") | ConvertFrom-Json
if ([string] $resolved.name -cne $ProjectName -or
    [string] $resolved.services.postgres.environment.POSTGRES_DB -cne $DatabaseName) {
    throw 'Resolved recovery project or database identity does not match.'
}
$running = @(Invoke-Docker (@($composeArgs) + @(
    'ps', '--status', 'running', '--services'
)))
foreach ($service in @('postgres', 'bookbox-api', 'bookbox-catalog-worker')) {
    if ($running -notcontains $service) {
        throw "Service must be running before backup: $service"
    }
}

$postgresID = @(
    Invoke-Docker (@($composeArgs) + @('ps', '-q', 'postgres'))
)[0].ToString().Trim()
$dumpName = "/tmp/bookbox-backup-$([Guid]::NewGuid().ToString('N')).dump"
try {
    Invoke-Docker @(
        'exec', $postgresID, 'pg_dump', '--username', 'bookbox',
        '--dbname', $DatabaseName, '--format=custom', '--file', $dumpName
    ) | Out-Null
    Invoke-Docker @(
        'cp', "${postgresID}:$dumpName",
        (Join-Path $script:destination 'database.dump')
    ) | Out-Null
} finally {
    @(& docker exec $postgresID rm -f $dumpName 2>&1) | Out-Null
}
$schemaVersion = [int64] (@(
    Invoke-Docker @(
        'exec', $postgresID, 'psql', '--username', 'bookbox',
        '--dbname', $DatabaseName, '--tuples-only', '--no-align',
        '--command', 'select coalesce(max(version),0) from schema_migrations'
    )
)[0].ToString().Trim())

$tlsVolume = Get-ServiceVolume $resolved 'bookbox-api' '/var/lib/bookbox'
$cacheVolume = Get-ServiceVolume $resolved 'bookbox-catalog-worker' '/var/lib/bookbox/covers'
$coverBackupVolume = Get-ServiceVolume $resolved 'bookbox-catalog-worker' '/var/lib/bookbox/backups'
Export-Volume $tlsVolume 'tls_data.tar.gz'
Export-Volume $cacheVolume 'cover_cache.tar.gz'
Export-Volume $coverBackupVolume 'cover_backups.tar.gz'
Copy-Item -LiteralPath $config -Destination (Join-Path $script:destination 'config.yml')

$certificateCopy = Join-Path $script:destination '.certificate.crt'
Invoke-Docker @(
    'run', '--rm', '--network', 'none', '--read-only',
    '--mount', "type=volume,src=$tlsVolume,dst=/source,readonly",
    '--mount', "type=bind,src=$($script:destination.Replace('\','/')),dst=/output",
    '--entrypoint', 'sh', 'postgres:18-alpine', '-c',
    'test -f /source/tls/server.crt && cp /source/tls/server.crt /output/.certificate.crt'
) | Out-Null
try {
    $certificate = New-Object Security.Cryptography.X509Certificates.X509Certificate2($certificateCopy)
    $algorithm = [Security.Cryptography.SHA256]::Create()
    try {
        $fingerprint = ([BitConverter]::ToString(
            $algorithm.ComputeHash($certificate.RawData)
        )).Replace('-', ':')
    } finally {
        $algorithm.Dispose()
        $certificate.Dispose()
    }
} finally {
    Remove-Item -LiteralPath $certificateCopy -Force -ErrorAction SilentlyContinue
}

$entries = [Collections.Generic.List[object]]::new()
foreach ($definition in @(
    @('database.dump', 'postgres-custom'),
    @('tls_data.tar.gz', 'docker-volume-tar-gzip'),
    @('cover_cache.tar.gz', 'docker-volume-tar-gzip'),
    @('cover_backups.tar.gz', 'docker-volume-tar-gzip'),
    @('config.yml', 'configuration')
)) {
    $path = Join-Path $script:destination $definition[0]
    $item = Get-Item -LiteralPath $path
    $entries.Add([ordered]@{
        name = $definition[0]
        kind = $definition[1]
        bytes = [int64] $item.Length
        sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    })
}
[IO.File]::WriteAllText(
    (Join-Path $script:destination 'manifest.json'),
    ([ordered]@{
        formatVersion = 1
        createdAtUtc = [DateTime]::UtcNow.ToString('o')
        appVersion = $AppVersion
        schemaVersion = $schemaVersion
        databaseName = $DatabaseName
        projectName = $ProjectName
        certificateFingerprintSha256 = $fingerprint
        files = @($entries)
    } | ConvertTo-Json -Depth 8),
    [Text.UTF8Encoding]::new($false)
)
Write-Host "PASS: synthetic recovery backup created at $script:destination"

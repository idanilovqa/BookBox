[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $BackupDirectory,
    [Parameter(Mandatory)]
    [string] $ProjectName,
    [Parameter(Mandatory)]
    [string[]] $ComposeFile,
    [Parameter(Mandatory)]
    [string] $EnvironmentFile,
    [string] $DatabaseName = 'bookbox',
    [string] $BaseUrl = 'http://127.0.0.1:18787',
    [ValidateRange(30, 1800)]
    [int] $TimeoutSeconds = 300
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

function Assert-EmptyVolume {
    param([string] $Volume)
    Invoke-Docker @('volume', 'create', $Volume) | Out-Null
    $contents = @(
        Invoke-Docker @(
            'run', '--rm', '--network', 'none', '--read-only',
            '--mount', "type=volume,src=$Volume,dst=/target",
            '--entrypoint', 'sh', 'postgres:18-alpine', '-c',
            'find /target -mindepth 1 -maxdepth 1 -print -quit'
        )
    )
    if (($contents -join '').Trim()) {
        throw "Restore destination volume is not empty: $Volume"
    }
}

function Import-Volume {
    param([string] $Volume, [string] $Archive)
    Invoke-Docker @(
        'run', '--rm', '--network', 'none',
        '--mount', "type=volume,src=$Volume,dst=/target",
        '--mount', "type=bind,src=$($script:backup.Replace('\','/')),dst=/input,readonly",
        '--entrypoint', 'tar', 'postgres:18-alpine',
        '-xzf', "/input/$Archive", '-C', '/target'
    ) | Out-Null
}

if ($ProjectName -notlike 'bookbox-recovery-*') {
    throw 'This build permits restore only for a bookbox-recovery-* synthetic project.'
}
$recoveryRoot = [IO.Path]::GetFullPath(
    (Join-Path ([IO.Path]::GetTempPath()) 'bookbox-recovery')
).TrimEnd('\') + '\'
$script:backup = [IO.Path]::GetFullPath($BackupDirectory)
if (-not $script:backup.StartsWith(
    $recoveryRoot,
    [StringComparison]::OrdinalIgnoreCase
)) {
    throw "Synthetic backup input must be beneath $recoveryRoot"
}
$manifestPath = Join-Path $script:backup 'manifest.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw 'Backup manifest is missing.'
}
$manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
if ($manifest.formatVersion -ne 1 -or
    [string] $manifest.databaseName -cne $DatabaseName) {
    throw 'Backup format or database identity does not match.'
}
$allowed = @(
    'database.dump',
    'cover_cache.tar.gz',
    'cover_backups.tar.gz',
    'config.yml'
)
$listedNames = @($manifest.files | ForEach-Object { [string] $_.name })
$manifestNames = @($listedNames | Sort-Object -Unique -CaseSensitive)
if (
    $listedNames.Count -ne $allowed.Count -or
    $manifestNames.Count -ne $allowed.Count -or
    @(Compare-Object $allowed $manifestNames -CaseSensitive).Count -ne 0
) {
    throw 'Backup manifest must contain the exact unique approved file set.'
}
foreach ($entry in @($manifest.files)) {
    $name = [string] $entry.name
    if ($allowed -cnotcontains $name -or [IO.Path]::GetFileName($name) -cne $name) {
        throw "Backup manifest has an unapproved file: $name"
    }
    $path = Join-Path $script:backup $name
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Backup file is missing: $name"
    }
    $item = Get-Item -LiteralPath $path
    $digest = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($item.Length -ne [int64] $entry.bytes -or
        $digest -cne [string] $entry.sha256) {
        throw "Backup checksum or size mismatch: $name"
    }
}

$environment = [IO.Path]::GetFullPath($EnvironmentFile)
$composeFiles = @($ComposeFile | ForEach-Object { [IO.Path]::GetFullPath($_) })
foreach ($path in @($composeFiles) + @($environment)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required recovery deployment file not found: $path"
    }
}
$composeArgs = @(
    'compose', '--profile', 'container-worker',
    '--env-file', $environment, '-p', $ProjectName
)
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
$running = @(
    Invoke-Docker (@($composeArgs) + @(
        'ps', '--status', 'running', '--services'
    )) | Where-Object { -not [string]::IsNullOrWhiteSpace("$_") }
)
if ($running.Count -ne 0) {
    throw "All recovery-project services must be stopped: $($running -join ', ')"
}

$configMount = @($resolved.services.'bookbox-api'.volumes | Where-Object {
    $_.type -eq 'bind' -and $_.target -eq '/etc/bookbox/config.yml'
})
if ($configMount.Count -ne 1) {
    throw 'Could not resolve the recovery configuration mount.'
}
$activeConfig = [IO.Path]::GetFullPath([string] $configMount[0].source)
if (-not (Test-Path -LiteralPath $activeConfig -PathType Leaf) -or
    (Get-FileHash -LiteralPath $activeConfig -Algorithm SHA256).Hash -cne
    (Get-FileHash -LiteralPath (Join-Path $script:backup 'config.yml') -Algorithm SHA256).Hash) {
    throw 'Active recovery configuration does not match the backed-up non-secret configuration.'
}

$postgresVolume = Get-ServiceVolume $resolved 'postgres' '/var/lib/postgresql'
$cacheVolume = Get-ServiceVolume $resolved 'bookbox-catalog-worker' '/var/lib/bookbox/covers'
$coverBackupVolume = Get-ServiceVolume $resolved 'bookbox-catalog-worker' '/var/lib/bookbox/backups'
foreach ($volume in @($postgresVolume, $cacheVolume, $coverBackupVolume)) {
    Assert-EmptyVolume $volume
}
Import-Volume $cacheVolume 'cover_cache.tar.gz'
Import-Volume $coverBackupVolume 'cover_backups.tar.gz'

Invoke-Docker (@($composeArgs) + @('up', '-d', 'postgres')) | Out-Null
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
$postgresID = ''
do {
    $postgresID = @(
        Invoke-Docker (@($composeArgs) + @('ps', '-q', 'postgres'))
    )[0].ToString().Trim()
    $health = if ($postgresID) {
        (@(& docker inspect --format '{{.State.Health.Status}}' $postgresID 2>$null) -join '').Trim()
    } else {
        ''
    }
    if ($health -eq 'healthy') {
        break
    }
    if ([DateTime]::UtcNow -ge $deadline) {
        throw 'PostgreSQL restore target did not become healthy.'
    }
    Start-Sleep -Seconds 2
} while ($true)

$dumpName = "/tmp/bookbox-restore-$([Guid]::NewGuid().ToString('N')).dump"
try {
    Invoke-Docker @(
        'cp', (Join-Path $script:backup 'database.dump'),
        "${postgresID}:$dumpName"
    ) | Out-Null
    Invoke-Docker @(
        'exec', $postgresID, 'pg_restore', '--username', 'bookbox',
        '--dbname', $DatabaseName, '--clean', '--if-exists',
        '--exit-on-error', '--no-owner', '--no-privileges', $dumpName
    ) | Out-Null
} finally {
    @(& docker exec $postgresID rm -f $dumpName 2>&1) | Out-Null
}
Invoke-Docker (@($composeArgs) + @(
    'up', '-d', 'bookbox-api', 'bookbox-catalog-worker'
)) | Out-Null
& (Join-Path $PSScriptRoot 'wait-healthy.ps1') `
    -BaseUrl $BaseUrl `
    -TimeoutSeconds $TimeoutSeconds
if ($LASTEXITCODE -ne 0) {
    throw 'Restored recovery deployment did not become ready.'
}
Write-Host "PASS: synthetic backup restored into project $ProjectName."

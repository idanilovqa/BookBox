[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$deployRoot = Split-Path -Parent $PSScriptRoot
$baseCompose = Join-Path $deployRoot 'compose.yml'
$hybridCompose = Join-Path $deployRoot 'compose.hybrid.yml'
$environment = Join-Path $deployRoot '.env.example'
$lifecycleModule = Join-Path $PSScriptRoot 'compose-lifecycle.ps1'
$repoRoot = Split-Path -Parent $deployRoot
$catalogDocumentation = Join-Path $repoRoot 'docs\catalog.md'
$operationsDocumentation = Join-Path $repoRoot 'docs\operations.md'
$androidDocumentation = Join-Path $repoRoot 'docs\android.md'

foreach ($required in @(
        $baseCompose,
        $hybridCompose,
        $environment,
        $lifecycleModule,
        $catalogDocumentation,
        $operationsDocumentation,
        $androidDocumentation
    )) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required hybrid deployment file is missing: $required"
    }
}
. $lifecycleModule

function Assert-DocumentationSemantics {
    param(
        [Parameter(Mandatory)][string] $Path,
        [Parameter(Mandatory)][string[]] $Patterns
    )

    $text = Get-Content -Raw -LiteralPath $Path
    foreach ($pattern in $Patterns) {
        if ($text -notmatch $pattern) {
            throw "Documentation '$Path' is missing required hybrid-worker semantics matching: $pattern"
        }
    }
}

Assert-DocumentationSemantics -Path $catalogDocumentation -Patterns @(
    'container-worker',
    'compose\.hybrid\.yml',
    '127\.0\.0\.1',
    'BookBoxCatalogWorker',
    '%ProgramData%\\BookBox',
    'six-hour',
    'fixed',
    'removable',
    'mapped',
    'UNC',
    'external ffprobe',
    'prior\s+catalog'
)
Assert-DocumentationSemantics -Path $operationsDocumentation -Patterns @(
    '-DeploymentMode Container',
    '-DeploymentMode Hybrid',
    'container-worker',
    'compose\.hybrid\.yml',
    '127\.0\.0\.1',
    'private-LAN API',
    'BookBoxCatalogWorker',
    'install-catalog-worker-service\.ps1',
    'remove-catalog-worker-service\.ps1',
    '%ProgramData%\\BookBox',
    'logs',
    'backups',
    'Docker.*wait|wait.*Docker',
    'missing drive',
    'prior catalog',
    'external ffprobe',
    'fixed',
    'removable',
    'mapped',
    'UNC',
    'bookbox-catalog-worker\.exe',
    'SHA256SUMS\.txt',
    'PE',
    'side-effect-free.*help|help.*side-effect-free'
)
Assert-DocumentationSemantics -Path $androidDocumentation -Patterns @(
    'private-LAN API',
    '127\.0\.0\.1',
    'PostgreSQL',
    'BookBoxCatalogWorker',
    'six-hour',
    'missing\s+drive|drive\s+is\s+missing',
    'prior catalog',
    'does not connect.*PostgreSQL|never connects.*PostgreSQL',
    'does not\s+connect.*worker|never\s+connects.*worker'
)

$operationsText = Get-Content -Raw -LiteralPath $operationsDocumentation
$updateSectionMatch = [regex]::Match(
    $operationsText,
    '(?ms)^## Update BookBox\s*(?<body>.*?)(?=^## |\z)'
)
if (-not $updateSectionMatch.Success) {
    throw 'Operations documentation is missing the Update BookBox section.'
}
$updateSection = $updateSectionMatch.Groups['body'].Value
foreach ($pattern in @(
        '-DeploymentMode Container',
        '-DeploymentMode Hybrid',
        'build-release\.ps1',
        'BookBoxCatalogWorker',
        'remove-catalog-worker-service\.ps1',
        'install-catalog-worker-service\.ps1'
    )) {
    if ($updateSection -notmatch $pattern) {
        throw "The Update BookBox procedure is missing required mode-aware semantics matching: $pattern"
    }
}
if ($updateSection -match '(?m)^docker compose .* -f deploy\\compose\.yml build\s*$') {
    throw 'The Update BookBox procedure must not use a raw base-Compose-only build.'
}

function Assert-ThrowsLike {
    param([scriptblock] $Action, [string] $Pattern)
    try { & $Action } catch {
        if ($_.Exception.Message -match $Pattern) { return }
        throw
    }
    throw "Expected failure matching: $Pattern"
}

function Resolve-ComposeJson {
    param([string[]] $Arguments)
    $raw = @(& docker @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "Compose resolution failed:`n$($raw -join [Environment]::NewLine)"
    }
    ($raw -join [Environment]::NewLine) | ConvertFrom-Json
}

$containerScope = Resolve-BookBoxComposeScope `
    -DeploymentMode Container `
    -ComposeFile $baseCompose `
    -HybridComposeFile $hybridCompose
$hybridScope = Resolve-BookBoxComposeScope `
    -DeploymentMode Hybrid `
    -ComposeFile $baseCompose `
    -HybridComposeFile $hybridCompose

if (@($containerScope.ComposeFiles).Count -ne 1 -or
    @($containerScope.Profiles).Count -ne 1 -or
    $containerScope.Profiles[0] -cne 'container-worker') {
    throw 'Container mode must use only the base file and container-worker profile.'
}
if (@($hybridScope.ComposeFiles).Count -ne 2 -or @($hybridScope.Profiles).Count -ne 0) {
    throw 'Hybrid mode must use base plus hybrid overlay with no enabled profile.'
}

$containerArguments = Get-BookBoxComposeArguments `
    -Scope $containerScope `
    -EnvironmentFile $environment `
    -ProjectName 'bookbox-hybrid-contract-container' `
    -Command @('config', '--format', 'json')
$hybridArguments = Get-BookBoxComposeArguments `
    -Scope $hybridScope `
    -EnvironmentFile $environment `
    -ProjectName 'bookbox-hybrid-contract-native' `
    -Command @('config', '--format', 'json')
$container = Resolve-ComposeJson -Arguments $containerArguments
$hybrid = Resolve-ComposeJson -Arguments $hybridArguments

$containerWorker = $container.services.'bookbox-catalog-worker'
if (-not $containerWorker -or @($containerWorker.profiles) -notcontains 'container-worker') {
    throw 'Container model does not include the explicitly profiled worker.'
}
if ($container.services.postgres.PSObject.Properties['ports']) {
    throw 'Container PostgreSQL must not publish a host port.'
}
$hybridPort = @($hybrid.services.postgres.ports)
if ($hybridPort.Count -ne 1 -or
    [string]$hybridPort[0].name -cne 'hybrid-postgres' -or
    [string]$hybridPort[0].target -cne '5432' -or
    [string]$hybridPort[0].published -cne '55432' -or
    [string]$hybridPort[0].host_ip -cne '127.0.0.1' -or
    [string]$hybridPort[0].protocol -cne 'tcp') {
    throw 'Hybrid PostgreSQL must publish exactly 127.0.0.1:55432 to TCP 5432.'
}
if ($hybrid.services.PSObject.Properties['bookbox-catalog-worker']) {
	throw 'Hybrid model must not activate the container worker profile.'
}

$stateRoot = Join-Path ([IO.Path]::GetTempPath()) "bookbox-compose-state-$([Guid]::NewGuid().ToString('N'))"
try {
    $createdState = Ensure-BookBoxComposeState `
        -StateDirectory $stateRoot `
        -Scope $hybridScope `
        -Address '192.168.1.25' `
        -Port 8787
    if (-not $createdState.Created -or $createdState.Record.DeploymentMode -cne 'Hybrid') {
        throw 'First Hybrid reconciliation did not create validated owned state.'
    }
    $record = Read-BookBoxComposeState `
        -StateDirectory $stateRoot `
        -ComposeFile $baseCompose `
        -HybridComposeFile $hybridCompose
    if ($record.DeploymentMode -cne 'Hybrid' -or @($record.ComposeFiles).Count -ne 2 -or
        @($record.Profiles).Count -ne 0 -or
        -not $record.PSObject.Properties['EnvironmentFile']) {
        throw 'Owned startup state did not preserve the exact hybrid scope.'
    }
    $reconciled = Ensure-BookBoxComposeState `
        -StateDirectory $stateRoot `
        -Scope $hybridScope `
        -Address '192.168.1.25' `
        -Port 8787
    if ($reconciled.Created -or $reconciled.Record.DeploymentMode -cne 'Hybrid') {
        throw 'Identical Hybrid state was not reconciled as an existing owned deployment.'
    }

    $differentMode = [pscustomobject]@{
        DeploymentMode = 'Container'
        ComposeFiles = @($hybridScope.ComposeFiles)
        Profiles = @($hybridScope.Profiles)
    }
    $differentFiles = [pscustomobject]@{
        DeploymentMode = 'Hybrid'
        ComposeFiles = @($hybridScope.ComposeFiles[0])
        Profiles = @()
    }
    $differentProfiles = [pscustomobject]@{
        DeploymentMode = 'Hybrid'
        ComposeFiles = @($hybridScope.ComposeFiles)
        Profiles = @('container-worker')
    }
    foreach ($changed in @(
            @{ Scope = $differentMode; EnvironmentFile = $null; Address = '192.168.1.25'; Port = 8787 },
            @{ Scope = $differentFiles; EnvironmentFile = $null; Address = '192.168.1.25'; Port = 8787 },
            @{ Scope = $differentProfiles; EnvironmentFile = $null; Address = '192.168.1.25'; Port = 8787 },
            @{ Scope = $hybridScope; EnvironmentFile = $environment; Address = '192.168.1.25'; Port = 8787 },
            @{ Scope = $hybridScope; EnvironmentFile = $null; Address = '192.168.1.26'; Port = 8787 },
            @{ Scope = $hybridScope; EnvironmentFile = $null; Address = '192.168.1.25'; Port = 8788 }
        )) {
        Assert-ThrowsLike -Pattern 'Refusing|mismatch|state' -Action {
            Ensure-BookBoxComposeState `
                -StateDirectory $stateRoot `
                -Scope $changed.Scope `
                -Address $changed.Address `
                -Port $changed.Port `
                -EnvironmentFile $changed.EnvironmentFile
        }
    }
    Assert-ThrowsLike -Pattern 'already exists|active deployment' -Action {
        Write-BookBoxComposeState `
            -StateDirectory $stateRoot `
            -Scope $containerScope `
            -Address '192.168.1.25' `
            -Port 8787
    }

    $statePath = Join-Path $stateRoot 'compose-startup.json'
    $tampered = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
    $tampered.ComposeFiles = @((Join-Path $stateRoot 'unrelated.yml'))
    $tampered | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $statePath -Encoding UTF8
    Assert-ThrowsLike -Pattern 'Refusing|scope|state' -Action {
        Read-BookBoxComposeState `
            -StateDirectory $stateRoot `
            -ComposeFile $baseCompose `
            -HybridComposeFile $hybridCompose
    }
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) {
        throw 'Invalid state was deleted instead of being preserved for diagnosis.'
    }
} finally {
    $expectedParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
    $deleteTarget = [IO.Path]::GetFullPath($stateRoot).TrimEnd('\')
    if (-not [string]::Equals([IO.Path]::GetDirectoryName($deleteTarget), $expectedParent,
            [StringComparison]::OrdinalIgnoreCase) -or
        [IO.Path]::GetFileName($deleteTarget) -notlike 'bookbox-compose-state-*') {
        throw "Refusing unsafe test cleanup target: $deleteTarget"
    }
    if (Test-Path -LiteralPath $stateRoot) {
        Remove-Item -LiteralPath $stateRoot -Recurse -Force
    }
}

$startText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'start-bookbox.ps1')
$stopText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'stop-bookbox.ps1')
$auditText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'audit-compose.ps1')
foreach ($text in @($startText, $stopText, $auditText)) {
    if ($text -notmatch 'DeploymentMode') {
        throw 'A deployment lifecycle script is not mode-aware.'
    }
}
if ($stopText -notmatch 'Read-BookBoxComposeState' -or
    $stopText -match '(?m)^\s*& docker compose -f \$composeFile') {
    throw 'Stop must consume validated owned Compose state instead of reconstructing scope.'
}
if ($startText -notmatch 'Ensure-BookBoxComposeState' -or
    $startText -notmatch 'Remove-BookBoxComposeState') {
    throw 'Start must persist exact Compose state and retire it only after scoped rollback.'
}
if ($startText -notmatch "-Command\s+@\('up',\s*'-d',\s*'--remove-orphans'\)" -or
    $startText -notmatch 'if \(\$composeStateCreated\)') {
    throw 'Start must reconcile the exact scope and roll back only state created by this invocation.'
}
if ($stopText -notmatch 'composeState\.EnvironmentFile') {
    throw 'Stop must use the env-file choice recorded at startup.'
}
if ($auditText -notmatch '\$apiPorts\.Count -ne 1' -or
    $auditText -notmatch '\$startupEnvironment\.Address' -or
    $auditText -notmatch 'RFC1918 private IPv4') {
    throw 'Audit must require exactly one API publication on the selected private address.'
}

Write-Output 'PASS: container and Windows hybrid Compose modes are isolated and lifecycle-owned.'

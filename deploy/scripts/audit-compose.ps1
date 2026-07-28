param(
    [string] $ComposeFile = (Join-Path $PSScriptRoot '..\compose.yml'),
    [string] $EnvironmentFile,
    [string] $ProjectName = 'bookbox-e2e-audit'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$compose = [IO.Path]::GetFullPath($ComposeFile)
if (-not (Test-Path -LiteralPath $compose -PathType Leaf)) {
    throw "Compose file not found: $compose"
}
if (-not $EnvironmentFile) {
    $productionEnvironment = Join-Path (Split-Path $compose) '.env'
    $EnvironmentFile = if (Test-Path -LiteralPath $productionEnvironment) {
        $productionEnvironment
    } else {
        Join-Path (Split-Path $compose) '.env.example'
    }
}
$environment = [IO.Path]::GetFullPath($EnvironmentFile)
if (-not (Test-Path -LiteralPath $environment -PathType Leaf)) {
    throw "Compose environment file not found: $environment"
}

$raw = @(& docker compose --env-file $environment -p $ProjectName -f $compose config --format json 2>&1)
if ($LASTEXITCODE -ne 0) {
    throw "Compose resolution failed:`n$($raw -join [Environment]::NewLine)"
}
$resolved = ($raw -join [Environment]::NewLine) | ConvertFrom-Json
$api = $resolved.services.'bookbox-api'
$worker = $resolved.services.'bookbox-catalog-worker'
$postgres = $resolved.services.postgres
if (-not $api -or -not $worker -or -not $postgres) {
    throw 'Production Compose must define API, catalog worker, and PostgreSQL.'
}

$apiLibrary = @($api.volumes | Where-Object target -eq '/library/audiobooks')
if ($apiLibrary.Count -ne 1 -or $apiLibrary[0].read_only -ne $true) {
    throw 'API library mount must be read-only.'
}
$workerLibrary = @($worker.volumes | Where-Object target -eq '/library/audiobooks')
$workerReadOnly = if (
    $workerLibrary.Count -eq 1 -and
    $workerLibrary[0].PSObject.Properties['read_only']
) {
    $workerLibrary[0].read_only
} else {
    $false
}
if ($workerLibrary.Count -ne 1 -or $workerReadOnly -eq $true) {
    throw 'Worker library mount must be writable for approved cover publication.'
}
$composeSource = Get-Content -LiteralPath $compose -Raw
if ($composeSource -notmatch 'Explicitly approved risk: Docker grants this worker mount-level write') {
    throw 'Worker writable-library risk disclosure is missing.'
}
$resourceDefinitions = [regex]::Match(
    $composeSource,
    '(?ms)^volumes:\s.*\z'
).Value
if (-not $resourceDefinitions -or $resourceDefinitions -match '(?m)^    name:\s') {
    throw 'Volumes and networks must use Compose project-scoped names.'
}
if (
    $worker.PSObject.Properties['ports'] -or
    $worker.PSObject.Properties['expose'] -or
    $postgres.PSObject.Properties['ports'] -or
    $postgres.PSObject.Properties['expose']
) {
    throw 'Worker and PostgreSQL must have no inbound published or exposed port.'
}
foreach ($serviceName in @('bookbox-api', 'bookbox-catalog-worker')) {
    $service = $resolved.services.$serviceName
    if (@($service.cap_drop) -notcontains 'ALL') {
        throw "$serviceName does not drop all capabilities."
    }
    if ($service.read_only -ne $true) {
        throw "$serviceName container root is writable."
    }
    if ($service.init -ne $true) {
        throw "$serviceName does not enable init."
    }
    if (@($service.security_opt) -notcontains 'no-new-privileges:true') {
        throw "$serviceName does not enable no-new-privileges."
    }
    if ($service.user -ne '10001:10001') {
        throw "$serviceName does not use the dedicated non-root user."
    }
}
if ($api.ports[0].host_ip -ne '127.0.0.1' -and -not (Test-Path -LiteralPath (Join-Path (Split-Path $compose) '.env'))) {
    throw 'Example deployment must default the API bind to loopback.'
}
foreach ($secret in @('postgres_password', 'app_password_hash', 'session_key', 'google_books_key')) {
    if (-not $resolved.secrets.$secret.file) {
        throw "Secret $secret is not file-backed."
    }
}
foreach ($serviceName in @('postgres', 'bookbox-api', 'bookbox-catalog-worker')) {
    if (-not $resolved.services.$serviceName.healthcheck) {
        throw "$serviceName lacks a health gate."
    }
}

$testCompose = Join-Path (Split-Path $compose) 'compose.test.yml'
if (-not (Test-Path -LiteralPath $testCompose -PathType Leaf)) {
    throw "Test Compose override not found: $testCompose"
}
$safePlaceholder = [IO.Path]::Combine(
    [IO.Path]::GetTempPath(),
    'bookbox-e2e-audit',
    'library'
).Replace('\', '/')
$testVariables = @{
    BOOKBOX_TEST_LIBRARY_PATH = $safePlaceholder
    BOOKBOX_TEST_CONFIG_HOST_FILE = "$safePlaceholder/config.yml"
    BOOKBOX_TEST_POSTGRES_PASSWORD_HOST_FILE = "$safePlaceholder/postgres.txt"
    BOOKBOX_TEST_PASSWORD_HASH_HOST_FILE = "$safePlaceholder/password.txt"
    BOOKBOX_TEST_SESSION_KEY_HOST_FILE = "$safePlaceholder/session.bin"
    BOOKBOX_TEST_GOOGLE_BOOKS_KEY_HOST_FILE = "$safePlaceholder/google.txt"
}
foreach ($entry in $testVariables.GetEnumerator()) {
    [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
}
$testRaw = @(
    & docker compose --env-file $environment -p $ProjectName `
        -f $compose -f $testCompose config --format json 2>&1
)
if ($LASTEXITCODE -ne 0) {
    throw "Test Compose resolution failed:`n$($testRaw -join [Environment]::NewLine)"
}
$testResolved = ($testRaw -join [Environment]::NewLine) | ConvertFrom-Json
if ($testResolved.name -ne $ProjectName) {
    throw 'Test Compose did not honor the caller project name.'
}
foreach ($resourceGroup in @($testResolved.volumes, $testResolved.networks)) {
    foreach ($property in $resourceGroup.PSObject.Properties) {
        if ($property.Value.name -notlike "$ProjectName*") {
            throw "Test resource is not project-scoped: $($property.Value.name)"
        }
    }
}
foreach ($service in $testResolved.services.PSObject.Properties.Value) {
    $volumesProperty = $service.PSObject.Properties['volumes']
    $serviceVolumes = if ($null -eq $volumesProperty) {
        @()
    } else {
        @($volumesProperty.Value)
    }
    foreach ($mount in $serviceVolumes) {
        if ($mount.type -eq 'bind' -and $mount.source -match '^(?i)H:') {
            throw 'Test Compose must never bind an H: path.'
        }
    }
}
$expectedConfig = "$safePlaceholder/config.yml"
foreach ($serviceName in @('bookbox-api', 'bookbox-catalog-worker')) {
    $service = $testResolved.services.$serviceName
    $libraryMounts = @($service.volumes | Where-Object target -eq '/library/audiobooks')
    $configMounts = @($service.volumes | Where-Object target -eq '/etc/bookbox/config.yml')
    if ($libraryMounts.Count -ne 1 -or $libraryMounts[0].source -ne $safePlaceholder) {
        throw "$serviceName silently inherited the production library mount."
    }
    if ($configMounts.Count -ne 1 -or $configMounts[0].source -ne $expectedConfig) {
        throw "$serviceName silently inherited the production config mount."
    }
}

Write-Host 'PASS: production Compose keeps API source read-only, isolates worker/PostgreSQL ports, and applies container hardening.'

param(
    [string] $ComposeFile = (Join-Path $PSScriptRoot '..\compose.yml'),
    [string] $HybridComposeFile = (Join-Path $PSScriptRoot '..\compose.hybrid.yml'),
    [string] $EnvironmentFile,
    [string] $ProjectName = 'bookbox-e2e-audit',
    [ValidateSet('Container', 'Hybrid')]
    [string] $DeploymentMode = 'Container'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$compose = [IO.Path]::GetFullPath($ComposeFile)
if (-not (Test-Path -LiteralPath $compose -PathType Leaf)) {
    throw "Compose file not found: $compose"
}
$hybridCompose = [IO.Path]::GetFullPath($HybridComposeFile)
. (Join-Path $PSScriptRoot 'compose-lifecycle.ps1')
$composeScope = Resolve-BookBoxComposeScope `
    -DeploymentMode $DeploymentMode `
    -ComposeFile $compose `
    -HybridComposeFile $hybridCompose
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
. (Join-Path $PSScriptRoot 'startup-environment.ps1')
$startupEnvironment = Read-BookBoxStartupEnvironment -Path $environment
$selectedAddress = $null
$isPrivateAddress = if (
    [Net.IPAddress]::TryParse([string]$startupEnvironment.Address, [ref]$selectedAddress)
) {
    $addressBytes = $selectedAddress.GetAddressBytes()
    $addressBytes.Count -eq 4 -and (
        $addressBytes[0] -eq 10 -or
        ($addressBytes[0] -eq 172 -and $addressBytes[1] -ge 16 -and $addressBytes[1] -le 31) -or
        ($addressBytes[0] -eq 192 -and $addressBytes[1] -eq 168)
    )
} else {
    $false
}
if (-not $isPrivateAddress) {
    throw 'BOOKBOX_BIND_ADDRESS must be an RFC1918 private IPv4 address.'
}

$composeArguments = Get-BookBoxComposeArguments `
    -Scope $composeScope `
    -EnvironmentFile $environment `
    -ProjectName $ProjectName `
    -Command @('config', '--format', 'json')
$raw = @(& docker @composeArguments 2>&1)
if ($LASTEXITCODE -ne 0) {
    throw "Compose resolution failed:`n$($raw -join [Environment]::NewLine)"
}
$resolved = ($raw -join [Environment]::NewLine) | ConvertFrom-Json
$api = $resolved.services.'bookbox-api'
$workerProperty = $resolved.services.PSObject.Properties['bookbox-catalog-worker']
$worker = if ($workerProperty) { $workerProperty.Value } else { $null }
$postgres = $resolved.services.postgres
if (-not $api -or -not $postgres) {
	throw 'Production Compose must activate API and PostgreSQL.'
}
if ($DeploymentMode -eq 'Container' -and -not $worker) {
	throw 'Container mode must activate the catalog worker profile.'
}
if ($DeploymentMode -eq 'Hybrid' -and $worker) {
	throw 'Hybrid mode must leave the catalog worker profile inactive.'
}

$apiLibrary = @($api.volumes | Where-Object target -eq '/library/audiobooks')
if ($apiLibrary.Count -ne 1 -or $apiLibrary[0].read_only -ne $true) {
    throw 'API library mount must be read-only.'
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
if ($DeploymentMode -eq 'Container') {
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
	if ($worker.PSObject.Properties['ports'] -or $worker.PSObject.Properties['expose']) {
		throw 'Worker must have no inbound published or exposed port.'
	}
	if (@($worker.profiles) -notcontains 'container-worker') {
		throw 'Catalog worker must remain behind the container-worker profile.'
	}
	if ($postgres.PSObject.Properties['ports'] -or $postgres.PSObject.Properties['expose']) {
        throw 'Container PostgreSQL must have no inbound published or exposed port.'
    }
} else {
    $postgresPorts = @($postgres.ports)
    if ($postgres.PSObject.Properties['expose'] -or
        $postgresPorts.Count -ne 1 -or
        [string]$postgresPorts[0].name -cne 'hybrid-postgres' -or
        [string]$postgresPorts[0].target -cne '5432' -or
        [string]$postgresPorts[0].host_ip -cne '127.0.0.1' -or
        [string]$postgresPorts[0].protocol -cne 'tcp' -or
        [string]$postgresPorts[0].published -notmatch '^[1-9][0-9]{0,4}$' -or
        [int]$postgresPorts[0].published -gt 65535) {
        throw 'Hybrid PostgreSQL must publish exactly one named TCP port on 127.0.0.1.'
    }
}
$hardenedServices = @('bookbox-api')
if ($DeploymentMode -eq 'Container') {
    $hardenedServices += 'bookbox-catalog-worker'
}
foreach ($serviceName in $hardenedServices) {
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
$apiPorts = @($api.ports)
if (
    $apiPorts.Count -ne 1 -or
    [string]$apiPorts[0].target -cne '8787' -or
    [string]$apiPorts[0].published -cne [string]$startupEnvironment.Port -or
    [string]$apiPorts[0].name -cne 'http' -or
    [string]$apiPorts[0].host_ip -cne [string]$startupEnvironment.Address
) {
    throw 'Production API must publish exactly one named HTTP port on the selected private address.'
}
$requiredSecrets = @('postgres_password', 'app_password_hash', 'session_key')
if ($DeploymentMode -eq 'Container') {
	$requiredSecrets += 'google_books_key'
}
foreach ($secret in $requiredSecrets) {
    if (-not $resolved.secrets.$secret.file) {
        throw "Secret $secret is not file-backed."
    }
}
$healthServices = @('postgres', 'bookbox-api')
if ($DeploymentMode -eq 'Container') {
    $healthServices += 'bookbox-catalog-worker'
}
foreach ($serviceName in $healthServices) {
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
$testScope = [pscustomobject]@{
    DeploymentMode = 'Container'
    ComposeFiles = @($compose, $testCompose)
    Profiles = @('container-worker')
}
$testArguments = Get-BookBoxComposeArguments `
    -Scope $testScope `
    -EnvironmentFile $environment `
    -ProjectName $ProjectName `
    -Command @('config', '--format', 'json')
$testRaw = @(& docker @testArguments 2>&1)
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

Write-Host "PASS: $DeploymentMode Compose keeps API and database publication within the approved network scope."

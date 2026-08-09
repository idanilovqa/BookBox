[CmdletBinding()]
param(
    [string] $Address,
    [Nullable[int]] $Port,
    [string] $StateDirectory,
    [string] $ComposeFile,
    [string] $HybridComposeFile,
    [ValidateSet('Container', 'Hybrid')]
    [string] $DeploymentMode
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$deployRoot = Split-Path -Parent $PSScriptRoot
if (-not $ComposeFile) {
    $ComposeFile = Join-Path $deployRoot 'compose.yml'
}
$composeFile = [System.IO.Path]::GetFullPath($ComposeFile)
if (-not $HybridComposeFile) {
    $HybridComposeFile = Join-Path $deployRoot 'compose.hybrid.yml'
}
$hybridComposeFile = [System.IO.Path]::GetFullPath($HybridComposeFile)
if (-not $StateDirectory) {
    $StateDirectory = Join-Path $deployRoot '.state'
}
. (Join-Path $PSScriptRoot 'discovery-process.ps1')
. (Join-Path $PSScriptRoot 'compose-lifecycle.ps1')

$composeState = Read-BookBoxComposeState `
    -StateDirectory $StateDirectory `
    -ComposeFile $composeFile `
    -HybridComposeFile $hybridComposeFile `
    -AllowedEnvironmentFile (Join-Path $deployRoot '.env')
if ($PSBoundParameters.ContainsKey('DeploymentMode') -and
    $DeploymentMode -cne [string]$composeState.DeploymentMode) {
    throw 'Requested deployment mode does not match the owned startup state.'
}
$composeScope = Resolve-BookBoxComposeScope `
    -DeploymentMode ([string]$composeState.DeploymentMode) `
    -ComposeFile $composeFile `
    -HybridComposeFile $hybridComposeFile
$composeEnvironment = if ([string]::IsNullOrWhiteSpace([string]$composeState.EnvironmentFile)) {
    $null
} else {
    [string]$composeState.EnvironmentFile
}
$composeDownArguments = Get-BookBoxComposeArguments `
    -Scope $composeScope `
    -EnvironmentFile $composeEnvironment `
    -Command @('down', '--remove-orphans')
$composePSArguments = Get-BookBoxComposeArguments `
    -Scope $composeScope `
    -EnvironmentFile $composeEnvironment `
    -Command @('ps', '-q')
if (-not $PSBoundParameters.ContainsKey('Address')) {
    $Address = [string]$composeState.Address
}
if (-not $PSBoundParameters.ContainsKey('Port')) {
    $Port = [int]$composeState.Port
}

Stop-BookBoxDiscovery -StateDirectory $StateDirectory
if (Test-BookBoxDiscovery -StateDirectory $StateDirectory) {
    throw 'BookBox discovery is still running.'
}

& docker @composeDownArguments
if ($LASTEXITCODE -ne 0) {
    throw "Docker Compose stop failed with exit code $LASTEXITCODE."
}
$remaining = @(& docker @composePSArguments 2>&1 | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
if ($LASTEXITCODE -ne 0 -or $remaining.Count -ne 0) {
    throw 'Owned Compose project still has containers after shutdown.'
}
Remove-BookBoxComposeState -StateDirectory $StateDirectory

if ($Address) {
    $listener = Get-NetTCPConnection `
        -State Listen `
        -LocalAddress $Address `
        -LocalPort ([int]$Port) `
        -ErrorAction SilentlyContinue
    if ($listener) {
        throw "A listener remains on ${Address}:$Port after BookBox stopped."
    }
}
Write-Output 'BookBox containers and discovery process are stopped.'

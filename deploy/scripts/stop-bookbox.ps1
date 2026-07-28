[CmdletBinding()]
param(
    [string] $Address = $env:BOOKBOX_BIND_ADDRESS,
    [int] $Port = $(if ($env:BOOKBOX_HOST_PORT) { [int]$env:BOOKBOX_HOST_PORT } else { 8787 }),
    [string] $StateDirectory,
    [string] $ComposeFile
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$deployRoot = Split-Path -Parent $PSScriptRoot
if (-not $ComposeFile) {
    $ComposeFile = Join-Path $deployRoot 'compose.yml'
}
$composeFile = [System.IO.Path]::GetFullPath($ComposeFile)
if (-not $StateDirectory) {
    $StateDirectory = Join-Path $deployRoot '.state'
}
. (Join-Path $PSScriptRoot 'discovery-process.ps1')

Stop-BookBoxDiscovery -StateDirectory $StateDirectory
if (Test-BookBoxDiscovery -StateDirectory $StateDirectory) {
    throw 'BookBox discovery is still running.'
}

& docker compose -f $composeFile down --remove-orphans
if ($LASTEXITCODE -ne 0) {
    throw "Docker Compose stop failed with exit code $LASTEXITCODE."
}

if ($Address) {
    $listener = Get-NetTCPConnection `
        -State Listen `
        -LocalAddress $Address `
        -LocalPort $Port `
        -ErrorAction SilentlyContinue
    if ($listener) {
        throw "A listener remains on ${Address}:$Port after BookBox stopped."
    }
}
Write-Output 'BookBox containers and discovery process are stopped.'

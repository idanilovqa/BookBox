[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$modulePath = Join-Path $PSScriptRoot 'startup-environment.ps1'
if (-not (Test-Path -LiteralPath $modulePath -PathType Leaf)) {
    throw 'Startup environment loader does not exist.'
}
. $modulePath

$fixtureRoot = Join-Path ([System.IO.Path]::GetTempPath()) "bookbox-startup-env-$([Guid]::NewGuid().ToString('N'))"
$fixturePath = Join-Path $fixtureRoot '.env'

try {
    $null = New-Item -ItemType Directory -Path $fixtureRoot
    @(
        '# Startup settings'
        ''
        'BOOKBOX_LIBRARY_PATH=H:/Audiobooks'
        'BOOKBOX_BIND_ADDRESS=10.0.0.164'
        'BOOKBOX_HOST_PORT=8787'
    ) | Set-Content -LiteralPath $fixturePath -Encoding UTF8

    $settings = Read-BookBoxStartupEnvironment -Path $fixturePath
    if ($settings.Address -cne '10.0.0.164') {
        throw "Unexpected startup address: $($settings.Address)"
    }
    if ($settings.Port -ne 8787) {
        throw "Unexpected startup port: $($settings.Port)"
    }

    $startText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'start-bookbox.ps1')
    if ($startText -notmatch 'Read-BookBoxStartupEnvironment') {
        throw 'Startup script does not load deploy .env.'
    }
    if ($startText -notmatch '(?s)\$needsStartupEnvironment\s*=.*?if \(\$needsStartupEnvironment\).*?Read-BookBoxStartupEnvironment') {
        throw 'Startup script must skip deploy .env when address and port were explicitly supplied.'
    }
    $installText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'install-bookbox-discovery.ps1')
    if ($installText -notmatch [regex]::Escape("'startup-environment.ps1'")) {
        throw 'Discovery installer does not include the startup environment loader.'
    }
    if ($installText -notmatch [regex]::Escape("'compose-lifecycle.ps1'")) {
        throw 'Discovery installer does not include the Compose lifecycle module.'
    }

    Write-Output 'PASS: startup environment loads address and port from deploy .env.'
} finally {
    $expectedParent = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\')
    $deleteTarget = [System.IO.Path]::GetFullPath($fixtureRoot).TrimEnd('\')
    if (
        -not [string]::Equals(
            [System.IO.Path]::GetDirectoryName($deleteTarget),
            $expectedParent,
            [System.StringComparison]::OrdinalIgnoreCase
        ) -or
        [System.IO.Path]::GetFileName($deleteTarget) -notlike 'bookbox-startup-env-*'
    ) {
        throw "Refusing unsafe test cleanup target: $deleteTarget"
    }
    if (Test-Path -LiteralPath $fixtureRoot) {
        Remove-Item -LiteralPath $fixtureRoot -Recurse -Force
    }
}

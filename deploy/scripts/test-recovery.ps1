[CmdletBinding()]
param(
    [ValidateRange(60, 1800)]
    [int] $TimeoutSeconds = 600
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

& (Join-Path $PSScriptRoot 'test-recovery-static.tests.ps1')
& (Join-Path $PSScriptRoot 'synthetic-recovery-scope.tests.ps1')
& (Join-Path $PSScriptRoot 'test-recovery-process.tests.ps1')
& (Join-Path $PSScriptRoot 'test-recovery-synthetic.ps1') `
    -TimeoutSeconds $TimeoutSeconds

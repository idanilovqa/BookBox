[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

& (Join-Path $PSScriptRoot 'test-recovery-synthetic.ps1') `
    -ProcessRunnerSelfTest

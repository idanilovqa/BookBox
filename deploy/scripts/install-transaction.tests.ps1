[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$modulePath = Join-Path $PSScriptRoot 'install-transaction.ps1'
if (-not (Test-Path -LiteralPath $modulePath -PathType Leaf)) {
    throw 'Install transaction module is missing.'
}
. $modulePath

$root = Join-Path ([IO.Path]::GetTempPath()) "bookbox-install-transaction-$([Guid]::NewGuid().ToString('N'))"
$install = Join-Path $root 'BookBox'
$stage = Join-Path $root 'stage'
$backup = Join-Path $root 'backup'
$createdResource = Join-Path $root 'created-rule'
$preexistingResource = Join-Path $root 'preexisting-rule'
$null = New-Item -ItemType Directory -Path $install -Force
Set-Content -LiteralPath (Join-Path $install 'old.txt') -Value 'old'
Set-Content -LiteralPath $preexistingResource -Value 'owned-before'

try {
    try {
        Invoke-BookBoxInstallTransaction `
            -InstallDirectory $install `
            -StageDirectory $stage `
            -BackupDirectory $backup `
            -PrepareStage {
                $null = New-Item -ItemType Directory -Path $stage -Force
                Set-Content -LiteralPath (Join-Path $stage 'new.txt') -Value 'new'
            } `
            -CreateExternalResources {
                param($rollbackActions)
                Set-Content -LiteralPath $createdResource -Value 'new-rule'
                $rollbackActions.Add({
                    Remove-Item -LiteralPath $createdResource -Force
                })
                throw 'injected post-mutation failure'
            }
        throw 'Expected injected transaction failure.'
    } catch {
        if ($_.Exception.Message -notmatch 'injected post-mutation failure') {
            throw
        }
    }
    if (-not (Test-Path -LiteralPath (Join-Path $install 'old.txt') -PathType Leaf)) {
        throw 'Install rollback did not restore the prior owned files.'
    }
    if (Test-Path -LiteralPath (Join-Path $install 'new.txt')) {
        throw 'Install rollback left newly installed files.'
    }
    if (Test-Path -LiteralPath $createdResource) {
        throw 'Install rollback left a newly created external resource.'
    }
    if (-not (Test-Path -LiteralPath $preexistingResource -PathType Leaf)) {
        throw 'Install rollback removed a pre-existing owned resource.'
    }
    if (
        (Test-Path -LiteralPath $stage) -or
        (Test-Path -LiteralPath $backup)
    ) {
        throw 'Install rollback left staging or backup state.'
    }
    Write-Output 'PASS: install transaction rolls back new mutations and preserves owned state.'
} finally {
    $resolved = [IO.Path]::GetFullPath($root)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
    if (
        [IO.Path]::GetDirectoryName($resolved) -ne $temp -or
        [IO.Path]::GetFileName($resolved) -notlike 'bookbox-install-transaction-*'
    ) {
        throw "Refusing unsafe transaction-test cleanup target: $resolved"
    }
    if (Test-Path -LiteralPath $resolved) {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}

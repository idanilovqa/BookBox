[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$scriptPath = Join-Path $PSScriptRoot 'test-recovery.ps1'
if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
    throw 'Synthetic recovery runner is missing.'
}
$runnerPath = Join-Path $PSScriptRoot 'test-recovery-synthetic.ps1'
if (-not (Test-Path -LiteralPath $runnerPath -PathType Leaf)) {
    throw 'Synthetic recovery implementation is missing.'
}
$operationPath = Join-Path $PSScriptRoot 'invoke-synthetic-recovery.ps1'
if (-not (Test-Path -LiteralPath $operationPath -PathType Leaf)) {
    throw 'Synthetic recovery operation wrapper is missing.'
}
$restorePath = Join-Path $PSScriptRoot 'restore.ps1'
if (-not (Test-Path -LiteralPath $restorePath -PathType Leaf)) {
    throw 'Synthetic recovery restore implementation is missing.'
}
$backupPath = Join-Path $PSScriptRoot 'backup.ps1'
$rotatePath = Join-Path $PSScriptRoot 'rotate-password.ps1'
foreach ($requiredPath in @($backupPath, $rotatePath)) {
    if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
        throw "Synthetic recovery operation is missing: $requiredPath"
    }
}
$text = (Get-Content -LiteralPath $scriptPath -Raw) +
    [Environment]::NewLine +
    (Get-Content -LiteralPath $runnerPath -Raw) +
    [Environment]::NewLine +
    (Get-Content -LiteralPath $operationPath -Raw) +
    [Environment]::NewLine +
    (Get-Content -LiteralPath $restorePath -Raw) +
    [Environment]::NewLine +
    (Get-Content -LiteralPath $backupPath -Raw) +
    [Environment]::NewLine +
    (Get-Content -LiteralPath $rotatePath -Raw)
$restoreText = Get-Content -LiteralPath $restorePath -Raw
if (
    $restoreText -notmatch 'Sort-Object\s+-Unique\s+-CaseSensitive' -or
    $restoreText -notmatch '@\(Compare-Object\s+\$allowed\s+\$manifestNames\s+-CaseSensitive\)\.Count' -or
    $restoreText -notmatch '\$allowed\s+-cnotcontains\s+\$name'
) {
    throw 'Restore must require the case-sensitive exact unique backup manifest filename set and handle an empty comparison under StrictMode.'
}

$requirements = [ordered]@{
    'unique recovery project' = 'bookbox-recovery-\$runID'
    'task-scoped temporary root' = "Join-Path .*'bookbox-recovery'"
    'synthetic library' = 'BOOKBOX_RECOVERY_LIBRARY_PATH'
    'no cover restore dependency' = 'restore-cover\.ps1'
    'backup invocation' = 'backup\.ps1'
    'PowerShell 5 array wrapper' = 'invoke-synthetic-recovery\.ps1'
    'two Compose files in JSON' = 'ComposeFile\s*=\s*@\(\$baseCompose,\s*\$testCompose\)'
    'stopped empty restore' = 'restore\.ps1'
    'ignore blank stopped-service output' = '(?s)\$running\s*=.*?Where-Object.*?IsNullOrWhiteSpace'
    'password and session rotation' = 'rotate-password\.ps1'
    'database restore probe' = 'recovery_probe'
    'TLS fingerprint verification' = 'certificateFingerprintSha256'
    'post-restore fingerprint expression' = 'if\s*\(\(Get-Fingerprint\)\s+-cne\s+\$fingerprint\)'
    'cover cache probe' = 'cover-cache-probe'
    'cover backup probe' = 'cover-backup-probe'
    'old refresh rejection' = 'rotate-password\.ps1'
    'bounded progress' = 'nextProgress'
    'live process API' = '\[Diagnostics\.Process\]::new'
    'asynchronous process output' = 'ReadToEndAsync'
    'finally cleanup' = '(?s)finally\s*\{.*?down.*?--volumes'
    'project resource audit' = 'com\.docker\.compose\.project'
    'task ports' = '18443.*18474'
}

foreach ($entry in $requirements.GetEnumerator()) {
    $matched = $text -match $entry.Value
    if ($entry.Key -eq 'no cover restore dependency') {
        if ($matched) {
            throw 'Synthetic recovery must not depend on operator cover restoration.'
        }
        continue
    }
    if (-not $matched) {
        throw "Missing synthetic recovery requirement: $($entry.Key)"
    }
}

if ($text -match '(?i)H:\\') {
    throw 'Synthetic recovery must never access H:.'
}
if ($text -match '(?i)-Production|C:\\Repositories\\BookBox\\backups') {
    throw 'Synthetic recovery must not enable or target production recovery.'
}
if ($text -match '(?i)(taskkill|Stop-Process\s+-Name|docker\s+system\s+prune)') {
    throw 'Synthetic recovery must not use broad process or Docker cleanup.'
}
if ($text -match '(?i)Start-Process') {
    throw 'Synthetic recovery must use a live Process API for typed exit codes.'
}

foreach ($nativeDockerPath in @(
    $operationPath,
    $backupPath,
    $restorePath,
    $rotatePath
)) {
    $nativeDockerText = Get-Content -Raw -LiteralPath $nativeDockerPath
    if (
        $nativeDockerText -notmatch '\$ErrorActionPreference\s*=\s*''Continue''' -or
        $nativeDockerText -notmatch '\$LASTEXITCODE'
    ) {
        throw "Recovery Docker wrapper must use exit codes, not stderr: $nativeDockerPath"
    }
}

Write-Output 'PASS: synthetic recovery runner has bounded isolated safety gates.'

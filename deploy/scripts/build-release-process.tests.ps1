[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$scriptPath = Join-Path $PSScriptRoot 'build-release.ps1'
$text = Get-Content -Raw -LiteralPath $scriptPath
$verifyPath = Join-Path $PSScriptRoot 'verify-release.ps1'
$verifyText = Get-Content -Raw -LiteralPath $verifyPath

if ($text -notmatch [regex]::Escape('e2e\process-runner.ps1')) {
    throw 'Release build must load the verified captured-process runner.'
}

$monitor = [regex]::Match(
    $text,
    '(?s)function Invoke-MonitoredProcess\s*\{.*?(?=\r?\nfunction Initialize-JavaAndGradle)'
)
if (-not $monitor.Success) {
    throw 'Release monitored-process function is missing.'
}
if ($monitor.Value -notmatch 'Invoke-BookBoxCapturedProcess') {
    throw 'Release monitor does not use the verified captured-process runner.'
}
if ($monitor.Value -match '\bStart-Process\b') {
    throw 'Release monitor still uses the PowerShell 5.1 blank-exit-code path.'
}
if ($monitor.Value -notmatch '\.TimedOut' -or $monitor.Value -notmatch '\.ExitCode') {
    throw 'Release monitor must distinguish timeout and typed nonzero exit.'
}
if ($text -notmatch [regex]::Escape(':/workspace:ro')) {
    throw 'Go release checks must mount the full repository read-only.'
}
if ($text -notmatch [regex]::Escape("'-w', '/workspace/server'")) {
    throw 'Go release checks must run from the server directory in that repository mount.'
}
if (
    $text -notmatch '\$env:ANDROID_HOME\s*=\s*\$sdk' -or
    $text -notmatch '\$env:ANDROID_SDK_ROOT\s*=\s*\$sdk'
) {
    throw 'Release builds must export the resolved Android SDK to every Gradle lane.'
}
if (
    [regex]::Matches(
        $text,
        [regex]::Escape('-Pkotlin.compiler.execution.strategy=in-process')
    ).Count -lt 2
) {
    throw 'Every no-daemon Gradle release lane must keep the Kotlin compiler in-process.'
}
foreach ($sidecar in @('APK_METADATA.json', 'SHA256SUMS.txt')) {
    if ($text -notmatch [regex]::Escape($sidecar)) {
        throw "Release startup must remove the prior $sidecar."
    }
}
$buildArtifactRemoval = [regex]::Match(
    $text,
    '(?s)foreach\s*\(\$path\s+in\s+@\(\s*' +
    '\$debugArtifact,\s*\$releaseArtifact,\s*' +
    '\$metadataArtifact,\s*\$checksumsArtifact\s*' +
    '\)\)\s*\{\s*Remove-Item\s+-LiteralPath\s+\$path'
)
$buildEnvironmentInitialization = $text.LastIndexOf('Initialize-JavaAndGradle')
if (
    -not $buildArtifactRemoval.Success -or
    $buildEnvironmentInitialization -lt 0 -or
    $buildArtifactRemoval.Index -gt $buildEnvironmentInitialization
) {
    throw 'Release startup must invalidate final artifacts before environment initialization.'
}
if (
    $verifyText -notmatch '\.tmp-' -or
    $verifyText -notmatch 'Move-Item\s+-LiteralPath'
) {
    throw 'Verified release sidecars must be staged before publication.'
}
$verifySidecarRemoval = $verifyText.IndexOf(
    'Remove-Item -LiteralPath $metadataPath, $checksumsPath'
)
$verifyFirstValidation = $verifyText.IndexOf('if ($apks.Count -eq 0)')
if (
    $verifySidecarRemoval -lt 0 -or
    $verifyFirstValidation -lt 0 -or
    $verifySidecarRemoval -gt $verifyFirstValidation
) {
    throw 'Release verification must invalidate final sidecars before validation can fail.'
}
if (
    $verifyText -notmatch
    '@\(Compare-Object\s+\$approvedPermissions\s+\$permissions\)\.Count'
) {
    throw 'Release permission verification must handle an empty comparison under StrictMode.'
}

& (Join-Path $PSScriptRoot '..\..\e2e\tests\process-runner.runtime.tests.ps1')

Write-Output 'PASS: release orchestration uses the verified process runner.'

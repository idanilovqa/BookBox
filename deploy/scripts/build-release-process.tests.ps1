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
    $text -notmatch [regex]::Escape(
        "'(?m)^ARG BOOKBOX_VERSION=1\.0\.0\r?$'"
    )
) {
    throw 'Release version-stamp validation must accept Windows CRLF checkouts.'
}
if (
    [regex]::Matches(
        $text,
        [regex]::Escape('-Pkotlin.compiler.execution.strategy=in-process')
    ).Count -lt 2
) {
    throw 'Every no-daemon Gradle release lane must keep the Kotlin compiler in-process.'
}
foreach ($sidecar in @(
    'APK_METADATA.json',
    'DISCOVERY_METADATA.json',
    'SHA256SUMS.txt'
)) {
    if ($text -notmatch [regex]::Escape($sidecar)) {
        throw "Release startup must remove the prior $sidecar."
    }
}
foreach ($required in @(
    'bookbox-catalog-worker.exe',
    './cmd/bookbox-catalog-worker',
    'catalog-worker-service.tests.ps1'
)) {
    if ($text -notmatch [regex]::Escape($required)) {
        throw "Release build is missing the native worker artifact gate: $required"
    }
}
if ($text -notmatch '(?s)foreach\s*\(\$path\s+in\s+@\(.*?\$workerArtifact.*?Remove-Item\s+-LiteralPath\s+\$path') {
    throw 'Release startup does not invalidate the prior catalog worker artifact.'
}
if ($text -notmatch 'go build -mod=readonly -trimpath -o /artifacts/bookbox-catalog-worker\.exe \./cmd/bookbox-catalog-worker') {
    throw 'Release does not build the production catalog worker command explicitly.'
}
foreach ($required in @(
    'bookbox-discovery.exe',
    '$goVerificationAndBuild',
    "-join ' '",
    "'GOOS=windows'",
    "'GOARCH=amd64'",
    "'CGO_ENABLED=0'",
    'discovery-process.tests.ps1',
    'install-transaction.tests.ps1'
)) {
    if ($text -notmatch [regex]::Escape($required)) {
        throw "Release build is missing the discovery release gate: $required"
    }
}
$workerPE = $verifyText.IndexOf('$workerMetadata = Get-PEMetadata -Path $workerPath')
$workerHelp = $verifyText.IndexOf("Invoke-Captured `$workerPath @('help')")
$workerChecksum = $verifyText.IndexOf('$checksumLines += "$($workerMetadata.sha256)  $($workerMetadata.file)"')
$sidecarPublication = $verifyText.IndexOf('Move-Item -LiteralPath $checksumsTemporary')
if ($workerPE -lt 0 -or $workerHelp -lt 0 -or $workerChecksum -lt 0 -or
    $sidecarPublication -lt 0 -or $workerPE -gt $workerChecksum -or
    $workerHelp -gt $workerChecksum -or $workerChecksum -gt $sidecarPublication) {
    throw 'Worker PE/help/checksum validation must precede atomic checksum publication.'
}
foreach ($required in @(
    'bookbox-catalog-worker.exe',
    'Get-PEMetadata',
    "@('help')",
    '0x5A4D',
    '0x00004550',
    '0x8664'
)) {
    if ($verifyText -notmatch [regex]::Escape($required)) {
        throw "Release verification does not validate the catalog worker: $required"
    }
}
$buildArtifactRemoval = [regex]::Match(
    $text,
    '(?s)foreach\s*\(\$path\s+in\s+@\(\s*' +
    '\$debugArtifact,\s*\$releaseArtifact,\s*\$discoveryArtifact,\s*\$workerArtifact,\s*' +
    '\$metadataArtifact,\s*\$discoveryMetadataArtifact,\s*' +
    '\$checksumsArtifact\s*' +
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
if (
    $verifyText -notmatch (
        '(?s)catch\s*\{\s*Remove-Item\s+-LiteralPath\s+' +
        '\$metadataPath,\s*\$discoveryMetadataPath,\s*\$checksumsPath.*?' +
        '\bthrow\b'
    )
) {
    throw 'Failed sidecar publication must remove every partial final sidecar.'
}
$verifySidecarRemoval = $verifyText.IndexOf(
    'Remove-Item -LiteralPath $metadataPath, $discoveryMetadataPath, $checksumsPath'
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
foreach ($required in @(
    'bookbox-discovery.exe',
    'DISCOVERY_METADATA.json',
    '0x5A4D',
    '0x00004550',
    '0x8664'
)) {
    if ($verifyText -notmatch [regex]::Escape($required)) {
        throw "Release verification does not validate the discovery executable: $required"
    }
}

& (Join-Path $PSScriptRoot '..\..\e2e\tests\process-runner.runtime.tests.ps1')

Write-Output 'PASS: release orchestration uses the verified process runner.'

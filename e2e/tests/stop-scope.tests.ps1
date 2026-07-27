[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'stop.ps1'
$e2eRoot = Join-Path ([System.IO.Path]::GetTempPath()) 'netstorage-e2e'
$runID = [Guid]::NewGuid().ToString('N')
$taskRoot = Join-Path $e2eRoot $runID
$stopText = Get-Content -Raw -LiteralPath $scriptPath
$dockerHelper = [regex]::Match(
    $stopText,
    '(?s)function Invoke-DockerChecked\s*\{.*?(?=\r?\nfunction Get-ProjectResources)'
)
if (
    -not $dockerHelper.Success -or
    $dockerHelper.Value -notmatch '\$ErrorActionPreference\s*=\s*''Continue''' -or
    $dockerHelper.Value -notmatch '\$exitCode\s*=\s*\$LASTEXITCODE'
) {
    throw 'Cleanup must treat Docker progress on stderr as output and use its exit code.'
}

function Assert-ThrowsLike {
    param(
        [Parameter(Mandatory)]
        [scriptblock] $Action,

        [Parameter(Mandatory)]
        [string] $Pattern
    )

    try {
        & $Action
    } catch {
        if ($_.Exception.Message -match $Pattern) {
            return
        }
        throw
    }
    throw "Expected failure matching: $Pattern"
}

$null = New-Item -ItemType Directory -Path $taskRoot -Force
try {
    & $scriptPath `
        -ProjectName "netstorage-e2e-$runID" `
        -TaskRoot $taskRoot `
        -ValidateOnly

    Assert-ThrowsLike -Pattern 'project name' -Action {
        & $scriptPath `
            -ProjectName 'netstorage-prod' `
            -TaskRoot $taskRoot `
            -ValidateOnly
    }
    Assert-ThrowsLike -Pattern 'task root' -Action {
        & $scriptPath `
            -ProjectName "netstorage-e2e-$runID" `
            -TaskRoot 'H:\Audiobooks' `
            -ValidateOnly
    }
    Assert-ThrowsLike -Pattern 'same run identifier' -Action {
        $otherRun = [Guid]::NewGuid().ToString('N')
        & $scriptPath `
            -ProjectName "netstorage-e2e-$otherRun" `
            -TaskRoot $taskRoot `
            -ValidateOnly
    }

    $stopText = Get-Content -Raw -LiteralPath $scriptPath
    if ($stopText -notmatch "'--rmi', 'local'") {
        throw 'E2E cleanup must remove only Compose-local project images.'
    }
    if (
        -not $stopText.Contains(
            "foreach (`$kind in @('container', 'network', 'volume', 'image'))"
        )
    ) {
        throw 'E2E cleanup must verify no project-labelled image remains.'
    }
    if (
        $stopText -notmatch
            '(?s)Stop-ExactEmulatorSerial.*?-Arguments @\(''-s'', \$Serial, ''emu'', ''kill''\).*?-TimeoutMilliseconds 10000'
    ) {
        throw 'E2E cleanup must boundedly stop only the exact owned emulator serial.'
    }
    if ($stopText -notmatch 'Stop-ExactOwnedProcessTree') {
        throw 'E2E cleanup must stop only recorded long-lived process trees.'
    }
    if ($stopText -notmatch 'Test-OriginalParentChain') {
        throw 'E2E cleanup must reject a recorded PID outside the original runner tree.'
    }
    if (
        $stopText -notmatch
            '(?s)function Stop-ExactEmulatorSerial.*?Invoke-NetStorageCapturedProcess.*?TimeoutMilliseconds 10000'
    ) {
        throw 'Exact emulator shutdown must use the shared bounded process runner.'
    }
    if (
        $stopText -notmatch
            '(?s)function Stop-ExactEmulatorSerial.*?Stop-ExactOwnedProcessTree'
    ) {
        throw 'Exact emulator shutdown must terminate only its exact client tree on timeout.'
    }
    if (
        $stopText -notmatch
            '(?s)foreach \(\$processID in \$OwnedLongLivedProcessIds\).*?Stop-ExactOwnedProcessTree'
    ) {
        throw 'Cleanup must explicitly stop each recorded emulator/qemu/adb process tree.'
    }
    if ($stopText -match '(?i)\badb(?:\.exe)?\s+kill-server\b') {
        throw 'E2E cleanup must never stop an unrelated global adb server.'
    }
    if ($stopText -match '(?i)taskkill[^\r\n]*(adb|emulator|qemu)') {
        throw 'E2E cleanup must never broadly terminate Android tooling.'
    }

    Write-Output 'PASS: E2E cleanup accepts only matching task-scoped identifiers.'
} finally {
    $expectedParent = [System.IO.Path]::GetFullPath($e2eRoot).TrimEnd('\')
    $deleteTarget = [System.IO.Path]::GetFullPath($taskRoot).TrimEnd('\')
    if (
        -not [string]::Equals(
            [System.IO.Path]::GetDirectoryName($deleteTarget),
            $expectedParent,
            [System.StringComparison]::OrdinalIgnoreCase
        ) -or
        [System.IO.Path]::GetFileName($deleteTarget) -ne $runID
    ) {
        throw "Refusing unsafe test cleanup target: $deleteTarget"
    }
    if (Test-Path -LiteralPath $taskRoot) {
        Remove-Item -LiteralPath $taskRoot -Recurse -Force
    }
}

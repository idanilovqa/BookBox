[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $ProjectName,

    [Parameter(Mandatory)]
    [string] $TaskRoot,

    [string] $EnvironmentFile,

    [ValidateSet('Container', 'Hybrid')]
    [string] $DeploymentMode = 'Container',

    [int[]] $OwnedProcessIds = @(),

    [int[]] $OwnedLongLivedProcessIds = @(),

    [string] $AdbPath,

    [string] $EmulatorSerial,

    [int[]] $TaskPorts = @(18787, 18474),

    [string] $NativeWorkerExecutable,

    [string] $NativeWorkerConfig,

    [string] $FFProbeFixtureExecutable,

    [string] $FFProbePIDDirectory,

    [switch] $ValidateOnly
)

$ErrorActionPreference = 'Stop'
$processRunner = Join-Path $PSScriptRoot 'process-runner.ps1'
. $processRunner

function Get-ValidatedScope {
    param(
        [Parameter(Mandatory)]
        [string] $ComposeProject,

        [Parameter(Mandatory)]
        [string] $RunRoot
    )

    if ($ComposeProject -notmatch '^bookbox-e2e-([0-9a-f]{32})$') {
        throw 'E2E cleanup project name is not task-scoped.'
    }
    $projectRunID = $Matches[1]
    $expectedParent = [System.IO.Path]::GetFullPath(
        (Join-Path ([System.IO.Path]::GetTempPath()) 'bookbox-e2e')
    ).TrimEnd('\')
    $resolvedRoot = [System.IO.Path]::GetFullPath($RunRoot).TrimEnd('\')
    if (
        -not [string]::Equals(
            [System.IO.Path]::GetDirectoryName($resolvedRoot),
            $expectedParent,
            [System.StringComparison]::OrdinalIgnoreCase
        ) -or
        [System.IO.Path]::GetFileName($resolvedRoot) -notmatch '^[0-9a-f]{32}$'
    ) {
        throw 'E2E cleanup task root is outside the task-scoped temp directory.'
    }
    if (
        -not [string]::Equals(
            [System.IO.Path]::GetFileName($resolvedRoot),
            $projectRunID,
            [System.StringComparison]::OrdinalIgnoreCase
        )
    ) {
        throw 'E2E project and task root must use the same run identifier.'
    }
    [pscustomobject]@{
        RunID = $projectRunID
        Root = $resolvedRoot
        Parent = $expectedParent
    }
}

function Invoke-DockerChecked {
    param(
        [Parameter(Mandatory)]
        [string[]] $Arguments
    )

    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = @(& docker @Arguments 2>&1)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($exitCode -ne 0) {
        throw "Docker command failed: docker $($Arguments -join ' ')`n$(
            $output -join [Environment]::NewLine
        )"
    }
    @($output)
}

function Get-ProjectResources {
    param(
        [Parameter(Mandatory)]
        [ValidateSet('container', 'network', 'volume', 'image')]
        [string] $Kind,

        [Parameter(Mandatory)]
        [string] $ComposeProject
    )

    $label = "label=com.docker.compose.project=$ComposeProject"
    switch ($Kind) {
        'container' {
            Invoke-DockerChecked @(
                'ps', '--all', '--filter', $label, '--format', '{{.ID}}'
            )
        }
        'network' {
            Invoke-DockerChecked @(
                'network', 'ls', '--filter', $label, '--format', '{{.ID}}'
            )
        }
        'volume' {
            Invoke-DockerChecked @(
                'volume', 'ls', '--filter', $label, '--format', '{{.Name}}'
            )
        }
        'image' {
            Invoke-DockerChecked @(
                'image', 'ls', '--filter', $label,
                '--format', '{{.Repository}}:{{.Tag}}'
            )
        }
    }
}

function Test-OriginalParentChain {
    param(
        [Parameter(Mandatory)][int] $RootProcessId,
        [Parameter(Mandatory)][object[]] $Processes,
        [Parameter(Mandatory)][int] $OriginalParentId
    )

    $current = $Processes | Where-Object {
        [int] $_.ProcessId -eq $RootProcessId
    } | Select-Object -First 1
    $visited = [Collections.Generic.HashSet[int]]::new()
    while ($null -ne $current -and $visited.Add([int] $current.ProcessId)) {
        $parentID = [int] $current.ParentProcessId
        if ($parentID -eq $OriginalParentId) {
            return $true
        }
        if ($parentID -le 0) {
            return $false
        }
        $current = $Processes | Where-Object {
            [int] $_.ProcessId -eq $parentID
        } | Select-Object -First 1
    }
    return $false
}

function Stop-ExactOwnedProcessTree {
    param([Parameter(Mandatory)][int] $RootProcessId)

    $processes = @(Get-CimInstance Win32_Process -ErrorAction Stop)
    $root = $processes | Where-Object {
        [int] $_.ProcessId -eq $RootProcessId
    } | Select-Object -First 1
    if ($null -eq $root) {
        return
    }
    $rootName = [IO.Path]::GetFileNameWithoutExtension([string] $root.Name)
    $isTaskProcessName = $rootName -match '^(adb|emulator|qemu-system-.+|bookbox-catalog-worker|ffprobe-fixture)$'
    if (-not $isTaskProcessName) {
        # A completed task PID was recycled for an unrelated executable.
        return
    }
    $isAndroid = $rootName -match '^(adb|emulator|qemu-system-.+)$'
    $isNativeWorker = $rootName -eq 'bookbox-catalog-worker' -and
        -not [string]::IsNullOrWhiteSpace($script:nativeWorkerExecutablePath) -and
        [string]::Equals(
            [IO.Path]::GetFullPath([string] $root.ExecutablePath),
            $script:nativeWorkerExecutablePath,
            [StringComparison]::OrdinalIgnoreCase
        ) -and
        "$($root.CommandLine)".IndexOf(
            $script:nativeWorkerConfigPath,
            [StringComparison]::OrdinalIgnoreCase
        ) -ge 0
    $isProbeFixture = $rootName -eq 'ffprobe-fixture' -and
        -not [string]::IsNullOrWhiteSpace($script:ffprobeFixtureExecutablePath) -and
        [string]::Equals(
            [IO.Path]::GetFullPath([string] $root.ExecutablePath),
            $script:ffprobeFixtureExecutablePath,
            [StringComparison]::OrdinalIgnoreCase
        )
    if ($isTaskProcessName -and -not ($isAndroid -or $isNativeWorker -or $isProbeFixture)) {
        throw "Refusing to stop unexpected owned process $RootProcessId ($rootName)."
    }
    if (-not (Test-OriginalParentChain `
        -RootProcessId $RootProcessId `
        -Processes $processes `
        -OriginalParentId $PID
    )) {
        throw "Refusing recorded PID $RootProcessId outside the original E2E runner tree."
    }

    $owned = [Collections.Generic.HashSet[int]]::new()
    $owned.Add($RootProcessId) | Out-Null
    do {
        $added = $false
        foreach ($process in $processes) {
            if (
                $owned.Contains([int] $process.ParentProcessId) -and
                $owned.Add([int] $process.ProcessId)
            ) {
                $added = $true
            }
        }
    } while ($added)

    $remaining = [Collections.Generic.HashSet[int]]::new()
    foreach ($processID in $owned) {
        $remaining.Add($processID) | Out-Null
    }
    while ($remaining.Count -gt 0) {
        $leaves = @($remaining | Where-Object {
            $candidate = $_
            -not ($processes | Where-Object {
                $remaining.Contains([int] $_.ProcessId) -and
                [int] $_.ParentProcessId -eq $candidate
            })
        })
        if ($leaves.Count -eq 0) {
            $leaves = @($remaining)
        }
        foreach ($processID in $leaves) {
            Stop-Process -Id $processID -Force -ErrorAction SilentlyContinue
            $remaining.Remove($processID) | Out-Null
        }
    }
}

function Stop-ExactEmulatorSerial {
    param(
        [Parameter(Mandatory)][string] $Executable,
        [Parameter(Mandatory)][string] $Serial
    )

    $result = Invoke-BookBoxCapturedProcess `
        -FilePath $Executable `
        -Arguments @('-s', $Serial, 'emu', 'kill') `
        -WorkingDirectory $PSScriptRoot `
        -TimeoutMilliseconds 10000 `
        -OnTimeout {
            param([int] $processID)
            Stop-ExactOwnedProcessTree -RootProcessId $processID
        }
    if ($result.TimedOut) {
        throw "Timed out stopping exact task-scoped emulator $Serial."
    }
    if ($result.ExitCode -ne 0) {
        throw "adb could not stop exact task-scoped emulator $Serial (exit $($result.ExitCode)):`n$($result.StandardOutput)`n$($result.StandardError)"
    }
}

$scope = Get-ValidatedScope -ComposeProject $ProjectName -RunRoot $TaskRoot
$script:nativeWorkerExecutablePath = ''
$script:nativeWorkerConfigPath = ''
$script:ffprobeFixtureExecutablePath = ''
if (-not [string]::IsNullOrWhiteSpace($NativeWorkerExecutable)) {
    $script:nativeWorkerExecutablePath = [IO.Path]::GetFullPath($NativeWorkerExecutable)
    $script:nativeWorkerConfigPath = [IO.Path]::GetFullPath($NativeWorkerConfig)
    $script:ffprobeFixtureExecutablePath = [IO.Path]::GetFullPath($FFProbeFixtureExecutable)
    foreach ($expected in @(
        @{ Actual = $script:nativeWorkerExecutablePath; Name = 'bookbox-catalog-worker.exe' }
        @{ Actual = $script:nativeWorkerConfigPath; Name = 'worker-config.yml' }
        @{ Actual = $script:ffprobeFixtureExecutablePath; Name = 'ffprobe-fixture.exe' }
    )) {
        $expectedPath = Join-Path $scope.Root $expected.Name
        if (-not [string]::Equals($expected.Actual, $expectedPath, [StringComparison]::OrdinalIgnoreCase)) {
            throw "E2E native cleanup path is outside the exact task artifact: $($expected.Name)"
        }
    }
}
if ($ValidateOnly) {
    return
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$baseCompose = Join-Path $repoRoot 'deploy\compose.yml'
$testCompose = Join-Path $repoRoot 'deploy\compose.test.yml'
$hybridCompose = Join-Path $repoRoot 'deploy\compose.hybrid.yml'
if ([string]::IsNullOrWhiteSpace($EnvironmentFile)) {
    $EnvironmentFile = Join-Path $scope.Root 'compose.env'
}
$environmentPath = [System.IO.Path]::GetFullPath($EnvironmentFile)
if (-not (Test-Path -LiteralPath $environmentPath -PathType Leaf)) {
    throw 'E2E Compose environment file is missing.'
}

$cleanupErrors = [Collections.Generic.List[string]]::new()
$safeToDelete = $true
try {
    $cleanupComposeArgs = @(
        'compose',
        '--project-name', $ProjectName,
        '--env-file', $environmentPath,
        '-f', $baseCompose,
        '-f', $testCompose
    )
    if ($DeploymentMode -eq 'Container') {
        $cleanupComposeArgs = @('compose', '--profile', 'container-worker') +
            $cleanupComposeArgs[1..($cleanupComposeArgs.Count - 1)]
    } else {
        $cleanupComposeArgs += @('-f', $hybridCompose)
    }
    $cleanupComposeArgs += @(
        'down',
        '--volumes',
        '--rmi', 'local',
        '--remove-orphans',
        '--timeout', '20'
    )
    $null = Invoke-DockerChecked $cleanupComposeArgs
} catch {
    $cleanupErrors.Add("Compose cleanup: $($_.Exception.Message)")
}

$remaining = @()
try {
    foreach ($kind in @('container', 'network', 'volume', 'image')) {
        foreach ($resource in @(Get-ProjectResources -Kind $kind -ComposeProject $ProjectName)) {
            if (-not [string]::IsNullOrWhiteSpace($resource)) {
                $remaining += "$kind=$resource"
            }
        }
    }
} catch {
    $safeToDelete = $false
    $cleanupErrors.Add("Docker resource audit: $($_.Exception.Message)")
}
if ($remaining.Count -ne 0) {
    $safeToDelete = $false
    $cleanupErrors.Add("Task-scoped Docker resources remain: $($remaining -join ', ')")
}

if (-not [string]::IsNullOrWhiteSpace($EmulatorSerial)) {
    if ($EmulatorSerial -notmatch '^emulator-([0-9]{4,5})$') {
        throw 'E2E cleanup emulator serial is not task-scoped.'
    }
    $consolePort = [int] $Matches[1]
    if (
        $consolePort % 2 -ne 0 -or
        $TaskPorts -notcontains $consolePort -or
        $TaskPorts -notcontains ($consolePort + 1)
    ) {
        throw 'E2E cleanup emulator serial is outside the recorded task ports.'
    }
    if (
        [string]::IsNullOrWhiteSpace($AdbPath) -or
        -not (Test-Path -LiteralPath $AdbPath -PathType Leaf) -or
        [IO.Path]::GetFileName($AdbPath) -ine 'adb.exe'
    ) {
        throw 'E2E cleanup adb path is invalid.'
    }
    try {
        Stop-ExactEmulatorSerial -Executable $AdbPath -Serial $EmulatorSerial
    } catch {
        $cleanupErrors.Add("Exact emulator stop: $($_.Exception.Message)")
    }
}

foreach ($processID in $OwnedLongLivedProcessIds) {
    if ($processID -gt 0) {
        try {
            Stop-ExactOwnedProcessTree -RootProcessId $processID
        } catch {
            $cleanupErrors.Add("Owned process $processID cleanup: $($_.Exception.Message)")
        }
    }
}

if (-not [string]::IsNullOrWhiteSpace($FFProbePIDDirectory)) {
    $markerRoot = [IO.Path]::GetFullPath($FFProbePIDDirectory).TrimEnd('\')
    $expectedMarkerRoot = (Join-Path $scope.Root 'ffprobe-pids').TrimEnd('\')
    if (-not [string]::Equals($markerRoot, $expectedMarkerRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'E2E ffprobe PID marker directory is outside the task scope.'
    }
    $fixtureProcessIds = [Collections.Generic.List[int]]::new()
    foreach ($marker in @(Get-ChildItem -LiteralPath $markerRoot -ErrorAction Stop)) {
        $markerMatch = [regex]::Match($marker.Name, '^([1-9][0-9]{0,9})\.pid$')
        if (-not $markerMatch.Success -or $marker.PSIsContainer -or
            $marker.Attributes -band [IO.FileAttributes]::ReparsePoint -or
            $marker.Length -gt 16 -or
            (Get-Content -Raw -LiteralPath $marker.FullName).Trim() -cne $markerMatch.Groups[1].Value) {
            throw "Invalid task-scoped ffprobe PID marker: $($marker.Name)"
        }
        $fixturePID = [int] $markerMatch.Groups[1].Value
        $fixtureProcessIds.Add($fixturePID) | Out-Null
        $fixtureProcess = Get-CimInstance Win32_Process -Filter "ProcessId = $fixturePID" -ErrorAction SilentlyContinue
        if ($null -ne $fixtureProcess -and [string]::Equals(
            [IO.Path]::GetFullPath([string] $fixtureProcess.ExecutablePath),
            $script:ffprobeFixtureExecutablePath,
            [StringComparison]::OrdinalIgnoreCase
        )) {
            try {
                Stop-ExactOwnedProcessTree -RootProcessId $fixturePID
            } catch {
                $cleanupErrors.Add("Owned ffprobe fixture $fixturePID cleanup: $($_.Exception.Message)")
            }
        }
    }
    foreach ($fixturePID in $fixtureProcessIds) {
        $remainingFixture = Get-CimInstance Win32_Process -Filter "ProcessId = $fixturePID" -ErrorAction SilentlyContinue
        if ($null -ne $remainingFixture -and [string]::Equals(
            [IO.Path]::GetFullPath([string] $remainingFixture.ExecutablePath),
            $script:ffprobeFixtureExecutablePath,
            [StringComparison]::OrdinalIgnoreCase
        )) {
            $safeToDelete = $false
            $cleanupErrors.Add("Owned ffprobe fixture remains: $fixturePID")
        }
    }
}

$listeners = foreach ($port in $TaskPorts) {
    Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue
}
if (@($listeners).Count -ne 0) {
    $safeToDelete = $false
    $listenerSummary = @($listeners | ForEach-Object {
        "$($_.LocalAddress):$($_.LocalPort) pid=$($_.OwningProcess)"
    })
    $cleanupErrors.Add("Task-scoped port listener remains: $($listenerSummary -join ', ')")
}

$ownedAlive = foreach ($processID in $OwnedLongLivedProcessIds) {
    if ($processID -le 0) {
        continue
    }
    $ownedProcess = Get-CimInstance Win32_Process -Filter "ProcessId = $processID" -ErrorAction SilentlyContinue
    if ($null -eq $ownedProcess) { continue }
    $ownedName = [IO.Path]::GetFileNameWithoutExtension([string] $ownedProcess.Name)
    $isTaskProcessName = $ownedName -match '^(adb|emulator|qemu-system-.+|bookbox-catalog-worker|ffprobe-fixture)$'
    if ($isTaskProcessName) { $ownedProcess }
}
if (@($ownedAlive).Count -ne 0) {
    $safeToDelete = $false
    $processSummary = @($ownedAlive | ForEach-Object {
        "$($_.ProcessId):$([IO.Path]::GetFileNameWithoutExtension([string]$_.Name))"
    })
    $cleanupErrors.Add("Owned E2E process remains: $($processSummary -join ', ')")
}

if ($safeToDelete -and (Test-Path -LiteralPath $scope.Root)) {
    $deleteTarget = [System.IO.Path]::GetFullPath($scope.Root).TrimEnd('\')
    if (
        -not [string]::Equals(
            [System.IO.Path]::GetDirectoryName($deleteTarget),
            $scope.Parent,
            [System.StringComparison]::OrdinalIgnoreCase
        ) -or
        [System.IO.Path]::GetFileName($deleteTarget) -ne $scope.RunID
    ) {
        throw "Refusing unsafe E2E cleanup target: $deleteTarget"
    }
    Remove-Item -LiteralPath $deleteTarget -Recurse -Force
}

if ($cleanupErrors.Count -ne 0) {
    throw "E2E cleanup reported:`n- $($cleanupErrors -join "`n- ")"
}

Write-Output "Removed task-scoped E2E project $ProjectName and temporary data."

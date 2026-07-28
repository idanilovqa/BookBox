[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $ProjectName,

    [Parameter(Mandatory)]
    [string] $TaskRoot,

    [string] $EnvironmentFile,

    [int[]] $OwnedProcessIds = @(),

    [int[]] $OwnedLongLivedProcessIds = @(),

    [string] $AdbPath,

    [string] $EmulatorSerial,

    [int[]] $TaskPorts = @(18443, 18474),

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
    if ($rootName -notmatch '^(adb|emulator|qemu-system-.+)$') {
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
if ($ValidateOnly) {
    return
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$baseCompose = Join-Path $repoRoot 'deploy\compose.yml'
$testCompose = Join-Path $repoRoot 'deploy\compose.test.yml'
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
    $null = Invoke-DockerChecked @(
        'compose',
        '--project-name', $ProjectName,
        '--env-file', $environmentPath,
        '-f', $baseCompose,
        '-f', $testCompose,
        'down',
        '--volumes',
        '--rmi', 'local',
        '--remove-orphans',
        '--timeout', '20'
    )
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

$ownedAlive = foreach ($processID in @(
    $OwnedProcessIds + $OwnedLongLivedProcessIds
)) {
    if ($processID -le 0) {
        continue
    }
    Get-Process -Id $processID -ErrorAction SilentlyContinue
}
if (@($ownedAlive).Count -ne 0) {
    $safeToDelete = $false
    $processSummary = @($ownedAlive | ForEach-Object {
        "$($_.Id):$($_.ProcessName)"
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

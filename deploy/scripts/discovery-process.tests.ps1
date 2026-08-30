[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$modulePath = Join-Path $PSScriptRoot 'discovery-process.ps1'
if (-not (Test-Path -LiteralPath $modulePath -PathType Leaf)) {
    throw 'Discovery lifecycle module does not exist.'
}
. $modulePath

$startText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'start-bookbox.ps1')
$installText = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'install-bookbox-discovery.ps1')
$removePath = Join-Path $PSScriptRoot 'remove-bookbox-discovery.ps1'
if (-not (Test-Path -LiteralPath $removePath -PathType Leaf)) {
    throw 'Safe BookBox discovery removal script is missing.'
}
$removeText = Get-Content -Raw -LiteralPath $removePath
foreach ($source in @($installText, $removeText)) {
    if ($source -notmatch 'function\s+Test-TaskUserIdentity(?s:.*?)\.Translate\((?s:.*?)SecurityIdentifier(?s:.*?)Identity\.User\.Value') {
        throw 'Task ownership must compare normalized security identifiers.'
    }
    if ($source -match '\[string\]\$(?:existingTask\.)?Principal\.UserId\s+-cne\s+\$identity\.Name') {
        throw 'Task ownership must not compare normalized account-name strings literally.'
    }
}
foreach ($required in @(
    'BookBox HTTP \$Port',
    'BookBox mDNS',
    "Protocol = 'TCP'",
    "Protocol = 'UDP'",
    'LocalSubnet',
    "Profile Private",
    'RunLevel Limited',
    '-Address "\{2\}" -Port \{3\}',
    '_bookbox\._tcp\.local',
    '-Type SRV',
    'Refusing to overwrite an unrelated scheduled task'
    'bookbox-install\.json'
    '\$installedStartScript'
    '\$existingTask\.Principal'
    '\$existingTask\.Triggers'
    'TaskPath'
    '\$dnsDeadline\s*=\s*\[DateTime\]::UtcNow\.AddSeconds'
    'while\s*\(\[DateTime\]::UtcNow\s+-lt\s+\$dnsDeadline'
    'Start-Sleep\s+-Milliseconds\s+500'
    'startup-error\.log'
    'trap\s*\{(?s:.*?)WriteAllText(?s:.*?)exit\s+1'
)) {
    if (($startText + [Environment]::NewLine + $installText) -notmatch $required) {
        throw "Missing private-LAN lifecycle guard: $required"
    }
}
if ($startText -match 'New-NetFirewallRule|Remove-NetFirewallRule') {
    throw 'Limited startup must validate pre-provisioned firewall rules without mutating them.'
}
if ($installText -match 'New-NetFirewallRule(?s:.*?)-PassThru') {
    throw 'Firewall installation must support hosts where New-NetFirewallRule has no PassThru parameter.'
}
foreach ($required in @(
    'New-NetFirewallRule(?s:.*?)-Name\s+\$definition\.Name',
    '\$createdRuleName\s*=\s*\$definition\.Name'
)) {
    if ($installText -notmatch $required) {
        throw "Firewall rollback must use the deterministic created rule name: $required"
    }
}
foreach ($required in @(
    '\$powerShellExecutable\s*=\s*Join-Path\s+\$PSHOME\s+''powershell\.exe''',
    'New-ScheduledTaskAction(?s:.*?)-Execute\s+\$powerShellExecutable'
)) {
    if ($installText -notmatch $required) {
        throw "Scheduled task must use the exact Windows PowerShell host path: $required"
    }
}
if ($installText -match 'New-ScheduledTaskAction(?s:.*?)-WorkingDirectory') {
    throw 'Scheduled task must not depend on an optional working directory.'
}
if ($removeText -notmatch '\$managedWorkingDirectory\s*=\s*Join-Path\s+\$installRoot\s+''scripts''' -or
    $removeText -notmatch '\$workingDirectoryOwned\s*=(?s:.{0,500}?)\[string\]::IsNullOrWhiteSpace(?s:.{0,400}?)\$managedWorkingDirectory') {
    throw 'Task removal must accept only an empty or exact legacy managed working directory.'
}
if ($installText -notmatch 'IsNullOrWhiteSpace\(\[string\]\$existingActions\[0\]\.WorkingDirectory\)') {
    throw 'Task installation must reject an existing action with an unmanaged working directory.'
}
$preflightIndex = $installText.IndexOf('# Complete ownership preflight')
$copyIndex = $installText.IndexOf('Copy-Item -LiteralPath')
$firewallMutationIndex = $installText.IndexOf('New-NetFirewallRule')
if (
    $preflightIndex -lt 0 -or
    $copyIndex -lt 0 -or
    $firewallMutationIndex -lt 0 -or
    $preflightIndex -gt $copyIndex -or
    $preflightIndex -gt $firewallMutationIndex
) {
    throw 'Installer must complete task/firewall ownership preflight before file or firewall mutations.'
}
foreach ($required in @(
    'Unregister-ScheduledTask',
    'Remove-NetFirewallRule -Name',
    'bookbox-install\.json',
    'startup-error\.log',
    'Refusing to remove',
    'Stop-BookBoxDiscovery'
)) {
    if ($removeText -notmatch $required) {
        throw "Missing safe removal guard: $required"
    }
}
if ($removeText -match 'Remove-NetFirewallRule\s+-DisplayName|Stop-Process\s+-Name|taskkill') {
    throw 'Removal script contains a broad cleanup action.'
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

$taskState = Join-Path ([System.IO.Path]::GetTempPath()) "bookbox-discovery-$([Guid]::NewGuid().ToString('N'))"
$fakeExecutable = Join-Path $taskState 'bookbox-discovery-test.exe'
$sourcePowerShell = Join-Path $env:SystemRoot `
    'System32\WindowsPowerShell\v1.0\powershell.exe'
if (-not (Test-Path -LiteralPath $sourcePowerShell -PathType Leaf)) {
    throw "Windows PowerShell fixture executable is missing: $sourcePowerShell"
}
$stopSignal = Join-Path $taskState 'discovery.stop'
$escapedStopSignal = $stopSignal.Replace("'", "''")
$arguments = @(
    '-NoProfile',
    '-Command',
    "[Console]::Out.WriteLine('discovery-ready'); [Console]::Error.WriteLine('discovery-warning'); while (-not (Test-Path -LiteralPath '$escapedStopSignal')) { Start-Sleep -Milliseconds 100 }"
)

$null = New-Item -ItemType Directory -Path $taskState -Force
Copy-Item -LiteralPath $sourcePowerShell -Destination $fakeExecutable

$record = $null
$orphan = $null
try {
    $record = Start-BookBoxDiscovery `
        -Executable $fakeExecutable `
        -Address '192.168.1.25' `
        -Port 8787 `
        -StateDirectory $taskState `
        -Arguments $arguments

    if (-not (Get-Process -Id $record.ProcessId -ErrorAction SilentlyContinue)) {
        throw 'Discovery process did not start.'
    }
    if (-not (Test-BookBoxDiscovery -StateDirectory $taskState)) {
        throw 'Discovery process ownership validation failed.'
    }
    try {
        $sameRecord = Start-BookBoxDiscovery `
            -Executable $fakeExecutable `
            -Address '192.168.1.25' `
            -Port 8787 `
            -StateDirectory $taskState `
            -Arguments $arguments
    } catch {
        $startupFailure = $_
        $null = Stop-BookBoxDiscovery -StateDirectory $taskState
        $record = $null
        throw $startupFailure
    }
    if (
        [int]$sameRecord.ProcessId -ne [int]$record.ProcessId -or
        [int]$sameRecord.SupervisorProcessId -ne [int]$record.SupervisorProcessId
    ) {
        throw 'Repeated startup launched a second discovery process instead of reusing exact owned state.'
    }
    $stdoutPath = Join-Path $taskState 'discovery.stdout.log'
    $stderrPath = Join-Path $taskState 'discovery.stderr.log'
    $logDeadline = [DateTime]::UtcNow.AddSeconds(3)
    do {
        $stdoutDuringRun = if (Test-Path -LiteralPath $stdoutPath) {
            Get-Content -Raw -LiteralPath $stdoutPath
        } else { '' }
        $stderrDuringRun = if (Test-Path -LiteralPath $stderrPath) {
            Get-Content -Raw -LiteralPath $stderrPath
        } else { '' }
        if (
            $stdoutDuringRun -match 'discovery-ready' -and
            $stderrDuringRun -match 'discovery-warning'
        ) {
            break
        }
        Start-Sleep -Milliseconds 50
    } while ([DateTime]::UtcNow -lt $logDeadline)
    if ($stdoutDuringRun -notmatch 'discovery-ready') {
        throw 'Discovery stdout was not streamed while the process was running.'
    }
    if ($stderrDuringRun -notmatch 'discovery-warning') {
        throw 'Discovery stderr was not streamed while the process was running.'
    }

    $recordPath = Join-Path $taskState 'discovery-process.json'
    $tampered = Get-Content -Raw -LiteralPath $recordPath | ConvertFrom-Json
    $tampered.Executable = Join-Path $taskState 'different.exe'
    $tampered | ConvertTo-Json | Set-Content -LiteralPath $recordPath -Encoding UTF8
    Assert-ThrowsLike -Pattern 'executable' -Action {
        Stop-BookBoxDiscovery -StateDirectory $taskState
    }
    if (-not (Get-Process -Id $record.ProcessId -ErrorAction SilentlyContinue)) {
        throw 'Executable mismatch stopped the owned process.'
    }

    $record | ConvertTo-Json | Set-Content -LiteralPath $recordPath -Encoding UTF8
    $tampered = Get-Content -Raw -LiteralPath $recordPath | ConvertFrom-Json
    $tampered.StartTimeUtcTicks = [long]$tampered.StartTimeUtcTicks + 1
    $tampered | ConvertTo-Json | Set-Content -LiteralPath $recordPath -Encoding UTF8
    Assert-ThrowsLike -Pattern 'PID reuse|start time' -Action {
        Stop-BookBoxDiscovery -StateDirectory $taskState
    }
    if (-not (Get-Process -Id $record.ProcessId -ErrorAction SilentlyContinue)) {
        throw 'PID-reuse mismatch stopped the owned process.'
    }

    $record | ConvertTo-Json | Set-Content -LiteralPath $recordPath -Encoding UTF8
    $unrelated = Get-Content -Raw -LiteralPath $recordPath | ConvertFrom-Json
    $unrelated.ProcessId = $PID
    $unrelated | ConvertTo-Json | Set-Content -LiteralPath $recordPath -Encoding UTF8
    Assert-ThrowsLike -Pattern 'executable' -Action {
        Stop-BookBoxDiscovery -StateDirectory $taskState
    }
    if (-not (Get-Process -Id $PID -ErrorAction SilentlyContinue)) {
        throw 'Unrelated process was stopped.'
    }

    $record | ConvertTo-Json | Set-Content -LiteralPath $recordPath -Encoding UTF8
    $stopped = Stop-BookBoxDiscovery -StateDirectory $taskState
    if (Get-Process -Id $record.ProcessId -ErrorAction SilentlyContinue) {
        throw 'Owned discovery process did not stop.'
    }
    if ([string]$stopped.StandardOutput -notmatch 'discovery-ready') {
        throw 'Discovery stdout was not captured.'
    }
    if ([string]$stopped.StandardError -notmatch 'discovery-warning') {
        throw 'Discovery stderr was not captured.'
    }
    if ($stopped.Forced) {
        throw 'Discovery ignored its task-owned graceful stop signal.'
    }
    foreach ($leaf in @(
        'discovery-process.json',
        'discovery.stdout.log',
        'discovery.stderr.log',
        'discovery.arguments.json',
        'discovery.bootstrap.json',
        'discovery.stop',
        'discovery.accepted'
    )) {
        if (Test-Path -LiteralPath (Join-Path $taskState $leaf)) {
            throw "Owned discovery state file was not cleaned: $leaf"
        }
    }

    $orphanScript = Join-Path $taskState 'orphan-supervisor.ps1'
    @"
while (-not (Test-Path -LiteralPath '$escapedStopSignal')) {
    Start-Sleep -Milliseconds 100
}
"@ | Set-Content -LiteralPath $orphanScript -Encoding UTF8
    $orphanInfo = [Diagnostics.ProcessStartInfo]::new()
    $orphanInfo.FileName = $sourcePowerShell
    $orphanInfo.Arguments = "-NoProfile -File `"$orphanScript`""
    $orphanInfo.UseShellExecute = $false
    $orphanInfo.CreateNoWindow = $true
    $orphan = [Diagnostics.Process]::new()
    $orphan.StartInfo = $orphanInfo
    if (-not $orphan.Start()) {
        throw 'Could not start orphan-supervisor fixture.'
    }
    $staleRecord = [pscustomobject]@{
        ProcessId = 2147483000
        Executable = $fakeExecutable
        StartTimeUtcTicks = 0
        SupervisorProcessId = $orphan.Id
        SupervisorExecutable = $sourcePowerShell
        SupervisorStartTimeUtcTicks = $orphan.StartTime.ToUniversalTime().Ticks
        Address = '192.168.1.25'
        Port = 8787
    }
    $staleRecord | ConvertTo-Json | Set-Content -LiteralPath $recordPath -Encoding UTF8
    $record = Start-BookBoxDiscovery `
        -Executable $fakeExecutable `
        -Address '192.168.1.25' `
        -Port 8787 `
        -StateDirectory $taskState `
        -Arguments $arguments
    if (-not $orphan.WaitForExit(3000)) {
        throw 'Stale live supervisor was orphaned when its child had already exited.'
    }
    $stopped = Stop-BookBoxDiscovery -StateDirectory $taskState

    $failureState = Join-Path $taskState 'launch-failure'
    $null = New-Item -ItemType Directory -Path $failureState -Force
    $failureStop = Join-Path $failureState 'stop'
    $failureArguments = Join-Path $failureState 'arguments.json'
    $failureBootstrap = Join-Path $failureState 'bootstrap.json'
    $failureAccepted = Join-Path $failureState 'accepted'
    @(
        '-NoProfile',
        '-Command',
        "while (-not (Test-Path -LiteralPath '$($failureStop.Replace("'", "''"))')) { Start-Sleep -Milliseconds 50 }"
    ) | ConvertTo-Json | Set-Content -LiteralPath $failureArguments -Encoding UTF8
    $failureInfo = [Diagnostics.ProcessStartInfo]::new()
    $failureInfo.FileName = $sourcePowerShell
    $failureInfo.Arguments = (
        '-NoProfile -ExecutionPolicy Bypass -File "{0}" ' +
        '-Executable "{1}" -ArgumentsFile "{2}" -BootstrapFile "{3}" ' +
        '-StdOutFile "{4}" -StdErrFile "{5}" -AcceptanceFile "{6}" ' +
        '-StopFile "{7}" -AcceptanceTimeoutMilliseconds 300'
    ) -f `
        (Join-Path $PSScriptRoot 'discovery-supervisor.ps1'),
        $fakeExecutable,
        $failureArguments,
        $failureBootstrap,
        (Join-Path $failureState 'stdout.log'),
        (Join-Path $failureState 'stderr.log'),
        $failureAccepted,
        $failureStop
    $failureInfo.UseShellExecute = $false
    $failureInfo.CreateNoWindow = $true
    $failureSupervisor = [Diagnostics.Process]::new()
    $failureSupervisor.StartInfo = $failureInfo
    if (-not $failureSupervisor.Start()) {
        throw 'Could not start launch-failure supervisor fixture.'
    }
    $failureDeadline = [DateTime]::UtcNow.AddSeconds(3)
    while (
        -not (Test-Path -LiteralPath $failureBootstrap) -and
        -not $failureSupervisor.HasExited -and
        [DateTime]::UtcNow -lt $failureDeadline
    ) {
        Start-Sleep -Milliseconds 25
        $failureSupervisor.Refresh()
    }
    if (-not (Test-Path -LiteralPath $failureBootstrap)) {
        throw 'Supervisor did not publish child identity for launch-failure cleanup.'
    }
    $failedChild = Get-Content -Raw -LiteralPath $failureBootstrap | ConvertFrom-Json
    if (-not $failureSupervisor.WaitForExit(3000)) {
        throw 'Unaccepted discovery supervisor did not retire itself.'
    }
    if (Get-Process -Id ([int]$failedChild.ProcessId) -ErrorAction SilentlyContinue) {
        throw 'Unaccepted discovery launch orphaned its child process.'
    }

    Write-Output 'PASS: discovery lifecycle protects process identity and cleans owned state.'
} finally {
    $owned = if ($record) {
        Get-Process -Id ([int]$record.ProcessId) -ErrorAction SilentlyContinue
    } else {
        $null
    }
    if ($owned) {
        Stop-Process -Id $owned.Id -Force
        $owned.WaitForExit()
    }
    if ($orphan -and -not $orphan.HasExited) {
        $null = New-Item -ItemType File -Path $stopSignal -Force
        if (-not $orphan.WaitForExit(2000)) {
            Stop-Process -Id $orphan.Id -Force
            $orphan.WaitForExit()
        }
    }
    $expectedParent = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath()).TrimEnd('\')
    $deleteTarget = [System.IO.Path]::GetFullPath($taskState).TrimEnd('\')
    if (
        -not [string]::Equals(
            [System.IO.Path]::GetDirectoryName($deleteTarget),
            $expectedParent,
            [System.StringComparison]::OrdinalIgnoreCase
        ) -or
        [System.IO.Path]::GetFileName($deleteTarget) -notlike 'bookbox-discovery-*'
    ) {
        throw "Refusing unsafe test cleanup target: $deleteTarget"
    }
    if (Test-Path -LiteralPath $taskState) {
        Remove-Item -LiteralPath $taskState -Recurse -Force
    }
}

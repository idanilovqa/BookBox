[CmdletBinding()]
param()

Set-StrictMode -Version Latest

function Get-BookBoxDiscoveryPaths {
    param(
        [Parameter(Mandatory)]
        [string] $StateDirectory
    )

    $directory = [System.IO.Path]::GetFullPath($StateDirectory)
    [pscustomobject]@{
        Directory = $directory
        Record = Join-Path $directory 'discovery-process.json'
        StdOut = Join-Path $directory 'discovery.stdout.log'
        StdErr = Join-Path $directory 'discovery.stderr.log'
        Arguments = Join-Path $directory 'discovery.arguments.json'
        Bootstrap = Join-Path $directory 'discovery.bootstrap.json'
        StopSignal = Join-Path $directory 'discovery.stop'
        Accepted = Join-Path $directory 'discovery.accepted'
    }
}

function Get-BookBoxProcessExecutable {
    param(
        [Parameter(Mandatory)]
        [Diagnostics.Process] $Process
    )

    try {
        return [System.IO.Path]::GetFullPath($Process.Path)
    } catch {
        throw "Cannot validate executable for process $($Process.Id): $($_.Exception.Message)"
    }
}

function Get-BookBoxDiscoveryRecord {
    param(
        [Parameter(Mandatory)]
        [string] $StateDirectory
    )

    $paths = Get-BookBoxDiscoveryPaths -StateDirectory $StateDirectory
    if (-not (Test-Path -LiteralPath $paths.Record -PathType Leaf)) {
        return $null
    }
    Get-Content -Raw -LiteralPath $paths.Record | ConvertFrom-Json
}

function Assert-BookBoxDiscoveryOwnership {
    param(
        [Parameter(Mandatory)]
        [object] $Record
    )

    $process = Get-Process -Id ([int]$Record.ProcessId) -ErrorAction SilentlyContinue
    if (-not $process) {
        return $null
    }
    $actualExecutable = Get-BookBoxProcessExecutable -Process $process
    $expectedExecutable = [System.IO.Path]::GetFullPath([string]$Record.Executable)
    if (-not [string]::Equals(
        $actualExecutable,
        $expectedExecutable,
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
        throw "Refusing discovery process action: executable does not match recorded path."
    }
    $actualTicks = $process.StartTime.ToUniversalTime().Ticks
    if ($actualTicks -ne [long]$Record.StartTimeUtcTicks) {
        throw 'Refusing discovery process action: PID reuse or start time mismatch.'
    }
    $process
}

function Assert-BookBoxDiscoverySupervisorOwnership {
    param(
        [Parameter(Mandatory)]
        [object] $Record
    )

    $supervisor = Get-Process -Id ([int]$Record.SupervisorProcessId) -ErrorAction SilentlyContinue
    if (-not $supervisor) {
        return $null
    }
    $actualExecutable = Get-BookBoxProcessExecutable -Process $supervisor
    $expectedExecutable = [System.IO.Path]::GetFullPath([string]$Record.SupervisorExecutable)
    if (-not [string]::Equals(
        $actualExecutable,
        $expectedExecutable,
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
        throw 'Refusing discovery process action: supervisor executable does not match.'
    }
    if ($supervisor.StartTime.ToUniversalTime().Ticks -ne [long]$Record.SupervisorStartTimeUtcTicks) {
        throw 'Refusing discovery process action: supervisor PID reuse or start time mismatch.'
    }
    $supervisor
}

function Remove-BookBoxDiscoveryFiles {
    param(
        [Parameter(Mandatory)]
        [object] $Paths
    )

    Remove-Item -LiteralPath $Paths.Record, $Paths.StdOut, $Paths.StdErr,
        $Paths.Arguments, $Paths.Bootstrap, $Paths.StopSignal, $Paths.Accepted `
        -Force -ErrorAction SilentlyContinue
}

function Retire-BookBoxStaleDiscovery {
    param(
        [Parameter(Mandatory)]
        [object] $Record,

        [Parameter(Mandatory)]
        [object] $Paths
    )

    $child = Assert-BookBoxDiscoveryOwnership -Record $Record
    $supervisor = Assert-BookBoxDiscoverySupervisorOwnership -Record $Record
    if ($child) {
        throw 'BookBox discovery is already running.'
    }
    if ($supervisor) {
        $null = New-Item -ItemType File -Path $Paths.StopSignal -Force
        if (-not $supervisor.WaitForExit(5000)) {
            Stop-Process -Id $supervisor.Id
            if (-not $supervisor.WaitForExit(2000)) {
                throw "Stale discovery supervisor $($supervisor.Id) did not stop."
            }
        }
    }
    Remove-BookBoxDiscoveryFiles -Paths $Paths
}

function Start-BookBoxDiscovery {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $Executable,

        [Parameter(Mandatory)]
        [ValidatePattern('^\d{1,3}(?:\.\d{1,3}){3}$')]
        [string] $Address,

        [Parameter(Mandatory)]
        [ValidateRange(1, 65535)]
        [int] $Port,

        [Parameter(Mandatory)]
        [string] $StateDirectory,

        [string[]] $Arguments
    )

    $paths = Get-BookBoxDiscoveryPaths -StateDirectory $StateDirectory
    $resolvedExecutable = (Resolve-Path -LiteralPath $Executable -ErrorAction Stop).Path
    if (-not (Test-Path -LiteralPath $resolvedExecutable -PathType Leaf)) {
        throw "Discovery executable is not a file: $resolvedExecutable"
    }
    $existing = Get-BookBoxDiscoveryRecord -StateDirectory $paths.Directory
    if ($existing) {
        $existingChild = Assert-BookBoxDiscoveryOwnership -Record $existing
        $existingSupervisor = Assert-BookBoxDiscoverySupervisorOwnership -Record $existing
        if ($existingChild -and $existingSupervisor) {
            if (
                -not [string]::Equals(
                    [System.IO.Path]::GetFullPath([string]$existing.Executable),
                    [System.IO.Path]::GetFullPath($resolvedExecutable),
                    [System.StringComparison]::OrdinalIgnoreCase
                ) -or
                [string]$existing.Address -cne $Address -or
                [int]$existing.Port -ne $Port
            ) {
                throw 'Refusing to reuse a discovery process with a different executable or endpoint.'
            }
            $existing | Add-Member -NotePropertyName Created -NotePropertyValue $false -Force
            return $existing
        }
        Retire-BookBoxStaleDiscovery -Record $existing -Paths $paths
    }

    $null = New-Item -ItemType Directory -Path $paths.Directory -Force
    Remove-Item -LiteralPath $paths.StdOut, $paths.StdErr, $paths.Arguments,
        $paths.Bootstrap, $paths.StopSignal, $paths.Accepted `
        -Force -ErrorAction SilentlyContinue
    if (-not $PSBoundParameters.ContainsKey('Arguments')) {
        $Arguments = @(
            '-address', $Address,
            '-port', [string]$Port,
            '-hostname', 'bookbox.local',
            '-stop-file', $paths.StopSignal
        )
    }

    @($Arguments) | ConvertTo-Json | Set-Content -LiteralPath $paths.Arguments -Encoding UTF8
    $supervisorScript = Join-Path $PSScriptRoot 'discovery-supervisor.ps1'
    $supervisorExecutable = (Get-Process -Id $PID -ErrorAction Stop).Path
    $supervisorArguments = @(
        '-NoProfile',
        '-ExecutionPolicy', 'Bypass',
        '-File', $supervisorScript,
        '-Executable', $resolvedExecutable,
        '-ArgumentsFile', $paths.Arguments,
        '-BootstrapFile', $paths.Bootstrap,
        '-StdOutFile', $paths.StdOut,
        '-StdErrFile', $paths.StdErr,
        '-AcceptanceFile', $paths.Accepted,
        '-StopFile', $paths.StopSignal
    )
    $quotedArguments = foreach ($argument in $supervisorArguments) {
        $value = [string]$argument
        if ($value -match '[\s"]') {
            '"{0}"' -f ($value -replace '(\\*)"', '$1$1\"')
        } else {
            $value
        }
    }
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $supervisorExecutable
    $startInfo.Arguments = $quotedArguments -join ' '
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.WindowStyle = [Diagnostics.ProcessWindowStyle]::Hidden
    $supervisor = [Diagnostics.Process]::new()
    $supervisor.StartInfo = $startInfo
    if (-not $supervisor.Start()) {
        throw 'Windows refused to start the BookBox discovery supervisor.'
    }
    try {
        $deadline = [DateTime]::UtcNow.AddSeconds(5)
        while (
            -not (Test-Path -LiteralPath $paths.Bootstrap -PathType Leaf) -and
            -not $supervisor.HasExited -and
            [DateTime]::UtcNow -lt $deadline
        ) {
            Start-Sleep -Milliseconds 50
            $supervisor.Refresh()
        }
        if (-not (Test-Path -LiteralPath $paths.Bootstrap -PathType Leaf)) {
            throw 'BookBox discovery did not publish process identity within five seconds.'
        }

        $bootstrap = Get-Content -Raw -LiteralPath $paths.Bootstrap | ConvertFrom-Json
        $record = [pscustomobject]@{
            ProcessId = [int]$bootstrap.ProcessId
            Executable = [string]$bootstrap.Executable
            StartTimeUtcTicks = [long]$bootstrap.StartTimeUtcTicks
            SupervisorProcessId = $supervisor.Id
            SupervisorExecutable = Get-BookBoxProcessExecutable -Process $supervisor
            SupervisorStartTimeUtcTicks = $supervisor.StartTime.ToUniversalTime().Ticks
            Address = $Address
            Port = $Port
        }
        $null = Assert-BookBoxDiscoveryOwnership -Record $record
        $null = Assert-BookBoxDiscoverySupervisorOwnership -Record $record
        $record | ConvertTo-Json | Set-Content -LiteralPath $paths.Record -Encoding UTF8
        $null = New-Item -ItemType File -Path $paths.Accepted -Force
        $record | Add-Member -NotePropertyName Created -NotePropertyValue $true -Force
        $record
    } catch {
        $launchFailure = $_
        $null = New-Item -ItemType File -Path $paths.StopSignal -Force
        if (-not $supervisor.HasExited -and -not $supervisor.WaitForExit(7000)) {
            Stop-Process -Id $supervisor.Id
            $supervisor.WaitForExit()
        }
        if (Test-Path -LiteralPath $paths.Bootstrap -PathType Leaf) {
            try {
                $failedBootstrap = Get-Content -Raw -LiteralPath $paths.Bootstrap | ConvertFrom-Json
                $failedChild = Get-Process -Id ([int]$failedBootstrap.ProcessId) -ErrorAction SilentlyContinue
                if ($failedChild) {
                    $failedPath = Get-BookBoxProcessExecutable -Process $failedChild
                    if (
                        [string]::Equals(
                            $failedPath,
                            [System.IO.Path]::GetFullPath([string]$failedBootstrap.Executable),
                            [System.StringComparison]::OrdinalIgnoreCase
                        ) -and
                        $failedChild.StartTime.ToUniversalTime().Ticks -eq
                            [long]$failedBootstrap.StartTimeUtcTicks
                    ) {
                        Stop-Process -Id $failedChild.Id
                        $failedChild.WaitForExit()
                    }
                }
            } catch {
                # The supervisor handshake remains authoritative when bootstrap
                # content cannot be parsed safely.
            }
        }
        Remove-BookBoxDiscoveryFiles -Paths $paths
        throw $launchFailure
    }
}

function Test-BookBoxDiscovery {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $StateDirectory
    )

    $record = Get-BookBoxDiscoveryRecord -StateDirectory $StateDirectory
    if (-not $record) {
        return $false
    }
    $child = Assert-BookBoxDiscoveryOwnership -Record $record
    $supervisor = Assert-BookBoxDiscoverySupervisorOwnership -Record $record
    return [bool]($child -and $supervisor)
}

function Stop-BookBoxDiscovery {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $StateDirectory
    )

    $paths = Get-BookBoxDiscoveryPaths -StateDirectory $StateDirectory
    $record = Get-BookBoxDiscoveryRecord -StateDirectory $paths.Directory
    if (-not $record) {
        return
    }
    $process = Assert-BookBoxDiscoveryOwnership -Record $record
    $supervisor = Assert-BookBoxDiscoverySupervisorOwnership -Record $record
    $forced = $false
    if ($process) {
        $null = New-Item -ItemType File -Path $paths.StopSignal -Force
        if (-not $process.WaitForExit(5000)) {
            $forced = $true
            Stop-Process -Id $process.Id
            if (-not $process.WaitForExit(2000)) {
                throw "Discovery process $($process.Id) did not stop within seven seconds."
            }
        }
    }
    if ($supervisor -and -not $supervisor.WaitForExit(5000)) {
        Stop-Process -Id $supervisor.Id
        if (-not $supervisor.WaitForExit(2000)) {
            throw "Discovery supervisor $($supervisor.Id) did not stop."
        }
    }
    $stdout = if (Test-Path -LiteralPath $paths.StdOut) {
        Get-Content -Raw -LiteralPath $paths.StdOut
    } else { '' }
    $stderr = if (Test-Path -LiteralPath $paths.StdErr) {
        Get-Content -Raw -LiteralPath $paths.StdErr
    } else { '' }
    Remove-BookBoxDiscoveryFiles -Paths $paths
    [pscustomobject]@{
        ProcessId = [int]$record.ProcessId
        StandardOutput = $stdout
        StandardError = $stderr
        Forced = $forced
    }
}

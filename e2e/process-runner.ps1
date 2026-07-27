function ConvertTo-NetStorageProcessArgument {
    param([Parameter(Mandatory)][AllowEmptyString()][string] $Value)

    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    '"' + $Value.Replace('"', '\"') + '"'
}

function Invoke-NetStorageCapturedProcess {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string] $FilePath,
        [Parameter(Mandatory)][string[]] $Arguments,
        [Parameter(Mandatory)][string] $WorkingDirectory,
        [Parameter(Mandatory)]
        [ValidateRange(1, 7200000)]
        [int] $TimeoutMilliseconds,
        [scriptblock] $OnStarted,
        [scriptblock] $OnProgress,
        [ValidateRange(100, 60000)]
        [int] $ProgressIntervalMilliseconds = 30000,
        [scriptblock] $OnTimeout
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.Arguments = ($Arguments | ForEach-Object {
        ConvertTo-NetStorageProcessArgument $_
    }) -join ' '
    $startInfo.WorkingDirectory = $WorkingDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $stdoutTask = $null
    $stderrTask = $null
    try {
        if (-not $process.Start()) {
            throw "Unable to start captured process: $FilePath"
        }
        $processID = $process.Id
        if ($null -ne $OnStarted) {
            $null = & $OnStarted $processID
        }
        # Drain both redirected streams concurrently while the child runs.
        # Waiting before starting these reads can deadlock on full pipe buffers.
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMilliseconds)
        $nextProgress = [DateTime]::UtcNow.AddMilliseconds(
            $ProgressIntervalMilliseconds
        )
        $timedOut = $false
        while (-not $process.WaitForExit(250)) {
            $now = [DateTime]::UtcNow
            if ($now -ge $deadline) {
                $timedOut = $true
                if ($null -ne $OnTimeout) {
                    $null = & $OnTimeout $processID
                } else {
                    Stop-Process -Id $processID -Force -ErrorAction SilentlyContinue
                }
                if (-not $process.WaitForExit(15000)) {
                    throw "Captured process $processID did not exit after timeout cleanup."
                }
                break
            }
            if ($null -ne $OnProgress -and $now -ge $nextProgress) {
                & $OnProgress $processID
                $nextProgress = $now.AddMilliseconds(
                    $ProgressIntervalMilliseconds
                )
            }
        }
        $process.WaitForExit()
        $process.Refresh()
        $streamTasks = [System.Threading.Tasks.Task[]] @(
            $stdoutTask,
            $stderrTask
        )
        if (-not [System.Threading.Tasks.Task]::WaitAll($streamTasks, 10000)) {
            throw "Captured output streams for process $processID did not close."
        }
        [int] $exitCode = $process.ExitCode
        [pscustomobject]@{
            ProcessId = [int] $processID
            ExitCode = $exitCode
            StandardOutput = [string] $stdoutTask.Result
            StandardError = [string] $stderrTask.Result
            TimedOut = [bool] $timedOut
        }
    } finally {
        $process.Dispose()
    }
}

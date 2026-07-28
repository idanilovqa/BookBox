[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $Executable,

    [Parameter(Mandatory)]
    [string] $ArgumentsFile,

    [Parameter(Mandatory)]
    [string] $BootstrapFile,

    [Parameter(Mandatory)]
    [string] $StdOutFile,

    [Parameter(Mandatory)]
    [string] $StdErrFile,

    [Parameter(Mandatory)]
    [string] $AcceptanceFile,

    [Parameter(Mandatory)]
    [string] $StopFile,

    [ValidateRange(100, 30000)]
    [int] $AcceptanceTimeoutMilliseconds = 5000
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$decodedArguments = Get-Content -Raw -LiteralPath $ArgumentsFile | ConvertFrom-Json
$arguments = [Collections.Generic.List[string]]::new()
foreach ($item in $decodedArguments) {
    $arguments.Add([string]$item)
}
$startInfo = [Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = $Executable
$quotedArguments = foreach ($argument in $arguments) {
    $value = [string]$argument
    if ($value -match '[\s"]') {
        '"{0}"' -f ($value -replace '(\\*)"', '$1$1\"')
    } else {
        $value
    }
}
$startInfo.Arguments = $quotedArguments -join ' '
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $true
$startInfo.WindowStyle = [Diagnostics.ProcessWindowStyle]::Hidden
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true

$process = [Diagnostics.Process]::new()
$process.StartInfo = $startInfo
if (-not $process.Start()) {
    throw 'Windows refused to start BookBox discovery.'
}
$stdoutStream = [IO.FileStream]::new(
    $StdOutFile,
    [IO.FileMode]::Create,
    [IO.FileAccess]::Write,
    [IO.FileShare]::ReadWrite
)
$stderrStream = [IO.FileStream]::new(
    $StdErrFile,
    [IO.FileMode]::Create,
    [IO.FileAccess]::Write,
    [IO.FileShare]::ReadWrite
)
$stdoutTask = $process.StandardOutput.BaseStream.CopyToAsync($stdoutStream)
$stderrTask = $process.StandardError.BaseStream.CopyToAsync($stderrStream)

[pscustomobject]@{
    ProcessId = $process.Id
    Executable = [System.IO.Path]::GetFullPath($process.Path)
    StartTimeUtcTicks = $process.StartTime.ToUniversalTime().Ticks
} | ConvertTo-Json | Set-Content -LiteralPath $BootstrapFile -Encoding UTF8

$accepted = $false
$acceptanceDeadline = [DateTime]::UtcNow.AddMilliseconds($AcceptanceTimeoutMilliseconds)
while (-not $process.HasExited -and [DateTime]::UtcNow -lt $acceptanceDeadline) {
    if (Test-Path -LiteralPath $AcceptanceFile -PathType Leaf) {
        $accepted = $true
        break
    }
    Start-Sleep -Milliseconds 25
    $process.Refresh()
}
if (-not $accepted -and -not $process.HasExited) {
    $null = New-Item -ItemType File -Path $StopFile -Force
    if (-not $process.WaitForExit(5000)) {
        Stop-Process -Id $process.Id
        $process.WaitForExit()
    }
}

try {
    $process.WaitForExit()
    $stdoutTask.GetAwaiter().GetResult()
    $stderrTask.GetAwaiter().GetResult()
    $stdoutStream.Flush()
    $stderrStream.Flush()
    if (-not $accepted) {
        exit 125
    }
    exit $process.ExitCode
} finally {
    $stdoutStream.Dispose()
    $stderrStream.Dispose()
}

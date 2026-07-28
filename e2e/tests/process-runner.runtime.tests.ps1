[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$e2eRoot = Split-Path -Parent $PSScriptRoot
$runnerPath = Join-Path $e2eRoot 'process-runner.ps1'
if (-not (Test-Path -LiteralPath $runnerPath -PathType Leaf)) {
    throw 'Shared E2E process runner is missing.'
}
. $runnerPath

foreach ($expectedExitCode in @(0, 7)) {
    $result = Invoke-BookBoxCapturedProcess `
        -FilePath $env:ComSpec `
        -Arguments @('/d', '/c', "exit $expectedExitCode") `
        -WorkingDirectory $e2eRoot `
        -TimeoutMilliseconds 10000
    if ($result.TimedOut) {
        throw "cmd.exe exit $expectedExitCode unexpectedly timed out."
    }
    if ($result.ExitCode -isnot [int]) {
        throw "ExitCode must be a typed Int32; got $($result.ExitCode.GetType())."
    }
    if ($result.ExitCode -ne $expectedExitCode) {
        throw "Expected cmd.exe exit $expectedExitCode; got $($result.ExitCode)."
    }
    if (Get-Process -Id $result.ProcessId -ErrorAction SilentlyContinue) {
        throw "Captured cmd.exe process $($result.ProcessId) is still running."
    }
}

Write-Output 'PASS: captured process runner preserves typed exit codes 0 and 7.'

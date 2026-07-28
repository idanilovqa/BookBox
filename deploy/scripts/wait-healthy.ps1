param(
    [string] $BaseUrl = 'http://127.0.0.1:8787',
    [ValidateRange(1, 1800)]
    [int] $TimeoutSeconds = 180
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$uri = $BaseUrl.TrimEnd('/') + '/v1/ready'
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
$nextProgress = [DateTime]::UtcNow
$lastState = 'not checked'
while ([DateTime]::UtcNow -lt $deadline) {
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $body = @(& curl.exe --fail --silent --show-error `
            --connect-timeout 3 --max-time 5 $uri 2>$null)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($exitCode -eq 0) {
        try {
            $status = (($body -join '') | ConvertFrom-Json)
            if ($status.ready -eq $true) {
                Write-Host 'PASS: BookBox API is ready (database, migrations, roots).'
                exit 0
            }
            $lastState = 'components not ready'
        } catch {
            $lastState = 'invalid readiness response'
        }
    } else {
        $lastState = 'endpoint unavailable'
    }
    if ([DateTime]::UtcNow -ge $nextProgress) {
        $remaining = [Math]::Max(
            0,
            [int][Math]::Ceiling(($deadline - [DateTime]::UtcNow).TotalSeconds)
        )
        Write-Host "Waiting for BookBox readiness: $lastState; ${remaining}s remaining"
        $nextProgress = [DateTime]::UtcNow.AddSeconds(10)
    }
    Start-Sleep -Seconds 2
}
throw "BookBox readiness timed out after $TimeoutSeconds seconds ($lastState)."

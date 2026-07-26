[CmdletBinding()]
param(
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')]
    [string] $Version = '1.0.0'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Invoke-Checked {
    param(
        [Parameter(Mandatory)][string] $Label,
        [Parameter(Mandatory)][scriptblock] $Command
    )
    Write-Host "==> $Label"
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$Label failed with exit code $LASTEXITCODE."
    }
}

function ConvertTo-ProcessArgument {
    param([string] $Value)
    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    return '"' + $Value.Replace('"', '\"') + '"'
}

function Stop-OwnedProcessTree {
    param([int] $RootProcessId)
    $processes = @(Get-CimInstance Win32_Process -ErrorAction Stop)
    $owned = [Collections.Generic.HashSet[int]]::new()
    $owned.Add($RootProcessId) | Out-Null
    do {
        $added = $false
        foreach ($process in $processes) {
            if ($owned.Contains([int] $process.ParentProcessId) -and
                $owned.Add([int] $process.ProcessId)) {
                $added = $true
            }
        }
    } while ($added)
    foreach ($id in @($owned) | Sort-Object -Descending) {
        Stop-Process -Id $id -Force -ErrorAction SilentlyContinue
    }
    return @($owned)
}

function Invoke-MonitoredProcess {
    param(
        [string] $Label,
        [string] $FilePath,
        [string[]] $Arguments,
        [string] $WorkingDirectory,
        [ValidateRange(30, 7200)][int] $TimeoutSeconds,
        [string] $OwnedDockerContainer
    )
    Write-Host "==> $Label"
    $logDirectory = Join-Path $script:artifacts '.release-logs'
    [IO.Directory]::CreateDirectory($logDirectory) | Out-Null
    $key = [Guid]::NewGuid().ToString('N')
    $stdout = Join-Path $logDirectory "$key.stdout.log"
    $stderr = Join-Path $logDirectory "$key.stderr.log"
    $argumentText = (@($Arguments | ForEach-Object {
        ConvertTo-ProcessArgument $_
    }) -join ' ')
    $process = Start-Process `
        -FilePath $FilePath `
        -ArgumentList $argumentText `
        -WorkingDirectory $WorkingDirectory `
        -NoNewWindow `
        -PassThru `
        -RedirectStandardOutput $stdout `
        -RedirectStandardError $stderr
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    try {
        while (-not $process.WaitForExit(10000)) {
            $elapsed = [int](
                [DateTime]::UtcNow - $process.StartTime.ToUniversalTime()
            ).TotalSeconds
            Write-Host "$Label is running (${elapsed}s elapsed)."
            if ([DateTime]::UtcNow -ge $deadline) {
                foreach ($path in @($stdout, $stderr)) {
                    if (Test-Path -LiteralPath $path -PathType Leaf) {
                        Get-Content -LiteralPath $path -Tail 100 | Write-Host
                    }
                }
                $stoppedIDs = @(Stop-OwnedProcessTree $process.Id)
                if ($OwnedDockerContainer) {
                    @(& docker rm -f $OwnedDockerContainer 2>&1) | Out-Null
                }
                $exitDeadline = [DateTime]::UtcNow.AddSeconds(5)
                do {
                    $remaining = @($stoppedIDs | Where-Object {
                        Get-Process -Id $_ -ErrorAction SilentlyContinue
                    })
                    if ($remaining.Count -eq 0) {
                        break
                    }
                    Start-Sleep -Milliseconds 200
                } while ([DateTime]::UtcNow -lt $exitDeadline)
                if ($remaining.Count -gt 0) {
                    throw "$Label timed out and owned process IDs remain: $($remaining -join ', ')."
                }
                if ($OwnedDockerContainer -and
                    (@(& docker ps -a --filter "name=^/$OwnedDockerContainer$" -q) -join '').Trim()) {
                    throw "$Label timed out and its exact Docker container remains: $OwnedDockerContainer"
                }
                throw "$Label timed out after $TimeoutSeconds seconds; its exact process tree was stopped."
            }
        }
        $process.WaitForExit()
        foreach ($path in @($stdout, $stderr)) {
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                Get-Content -LiteralPath $path | Write-Host
            }
        }
        if ($process.ExitCode -ne 0) {
            throw "$Label failed with exit code $($process.ExitCode)."
        }
    } finally {
        Remove-Item -LiteralPath $stdout, $stderr -Force -ErrorAction SilentlyContinue
    }
}

function Initialize-JavaAndGradle {
    $roots = [Collections.Generic.List[string]]::new()
    $projectJdkRoot = Join-Path $script:repo '.tooling\jdk17'
    if (Test-Path -LiteralPath $projectJdkRoot -PathType Container) {
        $roots.Add($projectJdkRoot)
        foreach ($directory in Get-ChildItem -LiteralPath $projectJdkRoot -Directory) {
            $roots.Add($directory.FullName)
        }
    }
    if ($env:JAVA_HOME) {
        $roots.Add([IO.Path]::GetFullPath($env:JAVA_HOME))
    }
    $javaHome = @($roots | Where-Object {
        Test-Path -LiteralPath (Join-Path $_ 'bin\java.exe') -PathType Leaf
    } | Select-Object -First 1)
    if ($javaHome.Count -ne 1) {
        throw 'JDK 17 was not found. Install it or populate .tooling\jdk17.'
    }
    $env:JAVA_HOME = $javaHome[0]
    $env:PATH = (Join-Path $env:JAVA_HOME 'bin') + ';' + $env:PATH
    $env:GRADLE_USER_HOME = Join-Path $script:repo '.tooling\gradle-home'
    [IO.Directory]::CreateDirectory($env:GRADLE_USER_HOME) | Out-Null
}

function Get-AndroidSdk {
    if ($env:ANDROID_HOME -and (Test-Path -LiteralPath $env:ANDROID_HOME -PathType Container)) {
        return [IO.Path]::GetFullPath($env:ANDROID_HOME)
    }
    $localProperties = Join-Path $script:repo 'android\local.properties'
    if (Test-Path -LiteralPath $localProperties -PathType Leaf) {
        $setting = @(Get-Content -LiteralPath $localProperties | Where-Object {
            $_ -match '^sdk\.dir=(?<path>.+)$'
        })
        if ($setting.Count -eq 1) {
            return [IO.Path]::GetFullPath(
                ($setting[0] -replace '^sdk\.dir=', '').Replace('\\', '\')
            )
        }
    }
    $taskSdk = Join-Path $script:repo '.tooling\android-sdk'
    if (Test-Path -LiteralPath $taskSdk -PathType Container) {
        return $taskSdk
    }
    throw 'Android SDK not found. Set ANDROID_HOME or android/local.properties.'
}

function Get-LatestBuildTool {
    param([string] $Sdk, [string] $Name)
    $tools = @(Get-ChildItem -LiteralPath (Join-Path $Sdk 'build-tools') -Directory |
        Sort-Object {
            try { [Version] $_.Name } catch { [Version] '0.0' }
        } -Descending |
        ForEach-Object { Join-Path $_.FullName $Name } |
        Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
    if ($tools.Count -eq 0) {
        throw "Android build tool not found: $Name"
    }
    return $tools[0]
}

$script:repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$server = Join-Path $script:repo 'server'
$android = Join-Path $script:repo 'android'
$serverMain = Get-Content -LiteralPath (
    Join-Path $server 'cmd\api\main.go'
) -Raw
$apiDockerfile = Get-Content -LiteralPath (
    Join-Path $script:repo 'deploy\Dockerfile.api'
) -Raw
if ($serverMain -notmatch 'var\s+version\s*=\s*"dev"' -or
    $apiDockerfile -notmatch '(?m)^ARG NETSTORAGE_VERSION=1\.0\.0$' -or
    $apiDockerfile -notmatch [regex]::Escape('-X main.version=${NETSTORAGE_VERSION}')) {
    throw 'Release blocker: the API must keep a dev source default and stamp NETSTORAGE_VERSION=1.0.0 through Docker ldflags.'
}
$artifacts = Join-Path $script:repo 'artifacts'
[IO.Directory]::CreateDirectory($artifacts) | Out-Null
$script:artifacts = $artifacts
Initialize-JavaAndGradle
$debugArtifact = Join-Path $artifacts "netStorage-v$Version-debug.apk"
$releaseArtifact = Join-Path $artifacts "netStorage-v$Version-release.apk"
foreach ($path in @($debugArtifact, $releaseArtifact)) {
    Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
}

$goContainer = "netstorage-release-go-$PID"
if ((@(& docker ps -a --filter "name=^/$goContainer$" -q) -join '').Trim()) {
    throw "Owned Go test container name already exists: $goContainer"
}
Invoke-MonitoredProcess `
    -Label 'Go tests and vet' `
    -FilePath 'docker.exe' `
    -Arguments @(
        'run', '--rm', '--name', $goContainer, '--read-only',
        '--tmpfs', '/tmp:rw,exec,nosuid,nodev',
        '-e', 'GOCACHE=/tmp/go-cache',
        '-e', 'GOMODCACHE=/tmp/go-mod',
        '-v', "$($server.Replace('\','/')):/src:ro",
        '-w', '/src',
        'golang:1.26.5',
        'sh', '-c',
        'go test -mod=readonly -p=1 -race ./... && go vet -mod=readonly ./...'
    ) `
    -WorkingDirectory $script:repo `
    -TimeoutSeconds 1800 `
    -OwnedDockerContainer $goContainer
Invoke-MonitoredProcess `
    -Label 'Compose safety audit' `
    -FilePath 'powershell.exe' `
    -Arguments @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass',
        '-File', (Join-Path $script:repo 'deploy\scripts\audit-compose.ps1')
    ) `
    -WorkingDirectory $script:repo `
    -TimeoutSeconds 300
Write-Host 'PostgreSQL-critical coverage comes from the mandatory recovery and real-server E2E gates below.'
Invoke-MonitoredProcess `
    -Label 'Backup and recovery test' `
    -FilePath 'powershell.exe' `
    -Arguments @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass',
        '-File', (Join-Path $script:repo 'deploy\scripts\test-recovery.ps1'),
        '-TimeoutSeconds', '1800'
    ) `
    -WorkingDirectory $script:repo `
    -TimeoutSeconds 2400
Invoke-MonitoredProcess `
    -Label 'API 36 end-to-end journey' `
    -FilePath 'powershell.exe' `
    -Arguments @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass',
        '-File', (Join-Path $script:repo 'e2e\run.ps1'),
        '-TimeoutSeconds', '1800'
    ) `
    -WorkingDirectory $script:repo `
    -TimeoutSeconds 2400
Invoke-MonitoredProcess `
    -Label 'Android unit, lint, instrumentation, and debug build' `
    -FilePath (Join-Path $android 'gradlew.bat') `
    -Arguments @(
        '--no-daemon',
        'testDebugUnitTest',
        'lintDebug',
        'connectedDebugAndroidTest',
        'assembleDebug'
    ) `
    -WorkingDirectory $android `
    -TimeoutSeconds 2400

$debugSource = Join-Path $android 'app\build\outputs\apk\debug\app-debug.apk'
if (-not (Test-Path -LiteralPath $debugSource -PathType Leaf)) {
    throw "Debug APK was not produced: $debugSource"
}
Copy-Item -LiteralPath $debugSource -Destination $debugArtifact
Write-Host "Created $debugArtifact"

$keystore = Join-Path $script:repo 'local-secrets\netstorage-release.jks'
$signingVariables = @(
    'NETSTORAGE_KEY_ALIAS',
    'NETSTORAGE_KEYSTORE_PASSWORD',
    'NETSTORAGE_KEY_PASSWORD'
)
$availableSigningValues = @($signingVariables | Where-Object {
    -not [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($_))
})
if ((Test-Path -LiteralPath $keystore -PathType Leaf) -and
    $availableSigningValues.Count -eq $signingVariables.Count) {
    Invoke-MonitoredProcess `
        -Label 'Android unsigned release build' `
        -FilePath (Join-Path $android 'gradlew.bat') `
        -Arguments @('--no-daemon', 'assembleRelease') `
        -WorkingDirectory $android `
        -TimeoutSeconds 1200
    $unsigned = Join-Path $android 'app\build\outputs\apk\release\app-release-unsigned.apk'
    if (-not (Test-Path -LiteralPath $unsigned -PathType Leaf)) {
        throw "Unsigned release APK was not produced: $unsigned"
    }
    $sdk = Get-AndroidSdk
    $zipalign = Get-LatestBuildTool $sdk 'zipalign.exe'
    $apksigner = Get-LatestBuildTool $sdk 'apksigner.bat'
    $aligned = Join-Path $artifacts ".netStorage-v$Version-aligned.apk"
    try {
        Invoke-Checked 'Align release APK' {
            & $zipalign -f -p 4 $unsigned $aligned
        }
        Invoke-Checked 'Sign release APK' {
            & $apksigner sign `
                --ks $keystore `
                --ks-key-alias $env:NETSTORAGE_KEY_ALIAS `
                --ks-pass 'env:NETSTORAGE_KEYSTORE_PASSWORD' `
                --key-pass 'env:NETSTORAGE_KEY_PASSWORD' `
                --out $releaseArtifact `
                $aligned
        }
    } finally {
        Remove-Item -LiteralPath $aligned -Force -ErrorAction SilentlyContinue
    }
    Write-Host "Created $releaseArtifact"
} elseif ((Test-Path -LiteralPath $keystore) -or $availableSigningValues.Count -gt 0) {
    throw 'Signing is partially configured. Provide the keystore and all three signing environment variables.'
} else {
    Write-Host 'Signing material is absent; the debug APK is complete and sideloadable.'
}

Invoke-Checked 'APK metadata, permissions, signatures, and checksums' {
    & powershell.exe -NoProfile -ExecutionPolicy Bypass `
        -File (Join-Path $script:repo 'deploy\scripts\verify-release.ps1') `
        -Version $Version
}
Write-Host "PASS: netStorage v$Version release artifacts passed every automated gate."

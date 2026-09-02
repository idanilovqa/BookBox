[CmdletBinding()]
param(
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')]
    [string] $Version = '1.1.0',

    [ValidateRange(1, 2147483647)]
    [int] $VersionCode = 2
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot '..\..\e2e\process-runner.ps1')

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
    $startedAt = [DateTime]::UtcNow
    $stoppedIDs = [Collections.Generic.List[int]]::new()
    $cleanupErrors = [Collections.Generic.List[string]]::new()
    try {
        $result = Invoke-BookBoxCapturedProcess `
            -FilePath $FilePath `
            -Arguments $Arguments `
            -WorkingDirectory $WorkingDirectory `
            -TimeoutMilliseconds ($TimeoutSeconds * 1000) `
            -ProgressIntervalMilliseconds 10000 `
            -OnProgress {
                param([int] $ProcessId)
                $null = $ProcessId
                $elapsed = [int](
                    [DateTime]::UtcNow - $startedAt
                ).TotalSeconds
                Write-Host "$Label is running (${elapsed}s elapsed)."
            } `
            -OnTimeout {
                param([int] $ProcessId)
                try {
                    foreach ($ownedID in @(Stop-OwnedProcessTree $ProcessId)) {
                        $stoppedIDs.Add([int] $ownedID)
                    }
                } catch {
                    $cleanupErrors.Add($_.Exception.Message)
                    Stop-Process -Id $ProcessId -Force -ErrorAction SilentlyContinue
                }
                if ($OwnedDockerContainer) {
                    @(& docker rm -f $OwnedDockerContainer 2>&1) | Out-Null
                }
            }

        [IO.File]::WriteAllText(
            $stdout,
            $result.StandardOutput,
            [Text.UTF8Encoding]::new($false)
        )
        [IO.File]::WriteAllText(
            $stderr,
            $result.StandardError,
            [Text.UTF8Encoding]::new($false)
        )
        foreach ($path in @($stdout, $stderr)) {
            if (Test-Path -LiteralPath $path -PathType Leaf) {
                Get-Content -LiteralPath $path | Write-Host
            }
        }

        if ($result.TimedOut) {
            $elapsed = [int](
                [DateTime]::UtcNow - $startedAt
            ).TotalSeconds
            $remaining = @($stoppedIDs | Where-Object {
                Get-Process -Id $_ -ErrorAction SilentlyContinue
            })
            if ($remaining.Count -gt 0) {
                throw "$Label timed out and owned process IDs remain: $($remaining -join ', ')."
            }
            if ($OwnedDockerContainer -and
                (@(& docker ps -a --filter "name=^/$OwnedDockerContainer$" -q) -join '').Trim()) {
                throw "$Label timed out and its exact Docker container remains: $OwnedDockerContainer"
            }
            if ($cleanupErrors.Count -gt 0) {
                throw "$Label timed out after ${elapsed}s and cleanup reported: $(
                    $cleanupErrors -join '; '
                )"
            }
            throw "$Label timed out after ${elapsed}s; its exact process tree was stopped."
        }
        if ($result.ExitCode -ne 0) {
            throw "$Label failed with exit code $($result.ExitCode)."
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

function Get-AndroidApkOutput {
    param([Parameter(Mandatory)][string] $Directory)
    $resolvedDirectory = [IO.Path]::GetFullPath($Directory).TrimEnd('\')
    $metadataPath = Join-Path $resolvedDirectory 'output-metadata.json'
    if (-not (Test-Path -LiteralPath $metadataPath -PathType Leaf)) {
        throw "Android output metadata was not produced: $metadataPath"
    }
    $metadata = Get-Content -Raw -LiteralPath $metadataPath | ConvertFrom-Json
    $outputs = @($metadata.elements | ForEach-Object { [string] $_.outputFile })
    if ($outputs.Count -ne 1 -or [string]::IsNullOrWhiteSpace($outputs[0])) {
        throw "Android output metadata must name exactly one APK: $metadataPath"
    }
    $candidate = [IO.Path]::GetFullPath((Join-Path $resolvedDirectory $outputs[0]))
    if ([IO.Path]::GetDirectoryName($candidate).TrimEnd('\') -cne $resolvedDirectory -or
        [IO.Path]::GetExtension($candidate) -cne '.apk' -or
        -not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
        throw "Android output metadata contains an unsafe or missing APK: $metadataPath"
    }
    return $candidate
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
$artifacts = Join-Path $script:repo 'artifacts'
[IO.Directory]::CreateDirectory($artifacts) | Out-Null
$script:artifacts = $artifacts
$debugArtifact = Join-Path $artifacts "BookBox-v$Version-debug.apk"
$releaseArtifact = Join-Path $artifacts "BookBox-v$Version-release.apk"
$discoveryArtifact = Join-Path $artifacts 'bookbox-discovery.exe'
$workerArtifact = Join-Path $artifacts 'bookbox-catalog-worker.exe'
$metadataArtifact = Join-Path $artifacts 'APK_METADATA.json'
$discoveryMetadataArtifact = Join-Path $artifacts 'DISCOVERY_METADATA.json'
$checksumsArtifact = Join-Path $artifacts 'SHA256SUMS.txt'
foreach ($path in @(
    $debugArtifact,
    $releaseArtifact,
    $discoveryArtifact,
    $workerArtifact,
    $metadataArtifact,
    $discoveryMetadataArtifact,
    $checksumsArtifact
)) {
    Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
}
$server = Join-Path $script:repo 'server'
$android = Join-Path $script:repo 'android'
$serverMain = Get-Content -LiteralPath (
    Join-Path $server 'cmd\bookbox-api\main.go'
) -Raw
$apiDockerfile = Get-Content -LiteralPath (
    Join-Path $script:repo 'deploy\Dockerfile.api'
) -Raw
$androidBuild = Get-Content -LiteralPath (
    Join-Path $android 'app\build.gradle.kts'
) -Raw
if ($serverMain -notmatch 'var\s+version\s*=\s*"dev"' -or
    $apiDockerfile -notmatch "(?m)^ARG BOOKBOX_VERSION=$([regex]::Escape($Version))\r?$" -or
    $apiDockerfile -notmatch [regex]::Escape('-X main.version=${BOOKBOX_VERSION}')) {
    throw "Release blocker: the API must keep a dev source default and stamp BOOKBOX_VERSION=$Version through Docker ldflags."
}
if ($androidBuild -notmatch [regex]::Escape("val bookBoxVersionName = `"$Version`"") -or
    $androidBuild -notmatch [regex]::Escape("val bookBoxVersionCode = $VersionCode")) {
    throw "Release blocker: Android must declare version $Version ($VersionCode)."
}
Initialize-JavaAndGradle
$sdk = Get-AndroidSdk
$env:ANDROID_HOME = $sdk
$env:ANDROID_SDK_ROOT = $sdk

$goContainer = "bookbox-release-go-$PID"
if ((@(& docker ps -a --filter "name=^/$goContainer$" -q) -join '').Trim()) {
    throw "Owned Go test container name already exists: $goContainer"
}
$goVerificationAndBuild = @(
    'GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go test -mod=readonly -p=1 -race ./... &&'
    'GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go vet -mod=readonly ./... &&'
    'go build -mod=readonly -trimpath -o /artifacts/bookbox-discovery.exe ./cmd/bookbox-discovery &&'
    'go build -mod=readonly -trimpath -o /artifacts/bookbox-catalog-worker.exe ./cmd/bookbox-catalog-worker &&'
    'go test -mod=readonly -c -o /artifacts/covers-windows.test.exe ./internal/covers'
) -join ' '
Invoke-MonitoredProcess `
    -Label 'Go tests and vet' `
    -FilePath 'docker.exe' `
    -Arguments @(
        'run', '--rm', '--name', $goContainer, '--read-only',
        '--tmpfs', '/tmp:rw,exec,nosuid,nodev',
        '-e', 'GOCACHE=/tmp/go-cache',
        '-e', 'GOMODCACHE=/tmp/go-mod',
        '-e', 'GOOS=windows',
        '-e', 'GOARCH=amd64',
        '-e', 'CGO_ENABLED=0',
        '-v', "$($script:repo.Replace('\','/')):/workspace:ro",
        '-v', "$($artifacts.Replace('\','/')):/artifacts:rw",
        '-w', '/workspace/server',
        'golang:1.26.5',
        'sh', '-c',
        $goVerificationAndBuild
    ) `
    -WorkingDirectory $script:repo `
    -TimeoutSeconds 1800 `
    -OwnedDockerContainer $goContainer
if (-not (Test-Path -LiteralPath $discoveryArtifact -PathType Leaf)) {
    throw "Discovery executable was not produced: $discoveryArtifact"
}
if (-not (Test-Path -LiteralPath $workerArtifact -PathType Leaf)) {
    throw "Catalog worker executable was not produced: $workerArtifact"
}
Write-Host "Created $discoveryArtifact"
Write-Host "Created $workerArtifact"
Invoke-MonitoredProcess `
    -Label 'Native Windows cover publication tests' `
    -FilePath (Join-Path $artifacts 'covers-windows.test.exe') `
    -Arguments @('-test.timeout=60s') `
    -WorkingDirectory $script:repo `
    -TimeoutSeconds 90
foreach ($gate in @(
    'discovery-process.tests.ps1',
    'install-transaction.tests.ps1',
    'catalog-worker-service.tests.ps1'
)) {
    Invoke-MonitoredProcess `
        -Label "Windows deployment gate: $gate" `
        -FilePath 'powershell.exe' `
        -Arguments @(
            '-NoProfile', '-ExecutionPolicy', 'Bypass',
            '-File', (Join-Path $script:repo "deploy\scripts\$gate")
        ) `
        -WorkingDirectory $script:repo `
        -TimeoutSeconds 300
}
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
    -Label 'Android unit, lint, and debug build' `
    -FilePath (Join-Path $android 'gradlew.bat') `
    -Arguments @(
        '--no-daemon',
        '-Pkotlin.compiler.execution.strategy=in-process',
        'testDebugUnitTest',
        'lintDebug',
        'assembleDebug'
    ) `
    -WorkingDirectory $android `
    -TimeoutSeconds 2400

$debugSource = Get-AndroidApkOutput (Join-Path $android 'app\build\outputs\apk\debug')
Copy-Item -LiteralPath $debugSource -Destination $debugArtifact
Write-Host "Created $debugArtifact"

$keystore = Join-Path $script:repo 'local-secrets\bookbox-release.jks'
$signingVariables = @(
    'BOOKBOX_KEY_ALIAS',
    'BOOKBOX_KEYSTORE_PASSWORD',
    'BOOKBOX_KEY_PASSWORD'
)
$availableSigningValues = @($signingVariables | Where-Object {
    -not [string]::IsNullOrWhiteSpace([Environment]::GetEnvironmentVariable($_))
})
if ((Test-Path -LiteralPath $keystore -PathType Leaf) -and
    $availableSigningValues.Count -eq $signingVariables.Count) {
    Invoke-MonitoredProcess `
        -Label 'Android unsigned release build' `
        -FilePath (Join-Path $android 'gradlew.bat') `
        -Arguments @(
            '--no-daemon',
            '-Pkotlin.compiler.execution.strategy=in-process',
            'assembleRelease'
        ) `
        -WorkingDirectory $android `
        -TimeoutSeconds 1200
    $unsigned = Get-AndroidApkOutput (Join-Path $android 'app\build\outputs\apk\release')
    $sdk = Get-AndroidSdk
    $zipalign = Get-LatestBuildTool $sdk 'zipalign.exe'
    $apksigner = Get-LatestBuildTool $sdk 'apksigner.bat'
    $aligned = Join-Path $artifacts ".BookBox-v$Version-aligned.apk"
    try {
        Invoke-Checked 'Align release APK' {
            & $zipalign -f -p 4 $unsigned $aligned
        }
        Invoke-Checked 'Sign release APK' {
            & $apksigner sign `
                --ks $keystore `
                --ks-key-alias $env:BOOKBOX_KEY_ALIAS `
                --ks-pass 'env:BOOKBOX_KEYSTORE_PASSWORD' `
                --key-pass 'env:BOOKBOX_KEY_PASSWORD' `
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

Invoke-Checked 'APK and discovery metadata, signatures, and checksums' {
    & powershell.exe -NoProfile -ExecutionPolicy Bypass `
        -File (Join-Path $script:repo 'deploy\scripts\verify-release.ps1') `
        -Version $Version -VersionCode $VersionCode
}
Write-Host "PASS: BookBox v$Version release artifacts passed every automated gate."

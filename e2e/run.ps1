[CmdletBinding()]
param(
    [ValidateRange(60, 1800)]
    [int] $TimeoutSeconds = 600
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$processRunner = Join-Path $PSScriptRoot 'process-runner.ps1'
. $processRunner

$repoRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$baseCompose = Join-Path $repoRoot 'deploy\compose.yml'
$testCompose = Join-Path $repoRoot 'deploy\compose.test.yml'
$manifestScript = Join-Path $PSScriptRoot 'library-manifest.ps1'
$runID = [Guid]::NewGuid().ToString('N')
$project = "bookbox-e2e-$runID"
$e2eParent = [IO.Path]::GetFullPath(
    (Join-Path ([IO.Path]::GetTempPath()) 'bookbox-e2e')
)
$taskRoot = [IO.Path]::GetFullPath((Join-Path $e2eParent $runID))
$library = Join-Path $taskRoot 'library'
$secrets = Join-Path $taskRoot 'secrets'
$logs = Join-Path $taskRoot 'logs'
$config = Join-Path $taskRoot 'config.yml'
$environmentFile = Join-Path $taskRoot 'compose.env'
$sourceBefore = Join-Path $taskRoot 'source-before.json'
$sourceAfter = Join-Path $taskRoot 'source-after.json'
$ownedProcessIds = [Collections.Generic.List[int]]::new()
$ownedLongLivedProcessIds = [Collections.Generic.List[int]]::new()
$taskPorts = [Collections.Generic.List[int]]::new()
$taskPorts.Add(18787)
$taskPorts.Add(18474)
$adb = ''
$emulatorSerial = ''
$composeArgs = @(
    'compose',
    '--project-name', $project,
    '--env-file', $environmentFile,
    '-f', $baseCompose,
    '-f', $testCompose
)
$username = 'owner'
$password = 'bookbox-e2e-password'
$primaryFailure = $null
$adbInvocationID = 0

function Write-Utf8 {
    param(
        [Parameter(Mandatory)][string] $Path,
        [Parameter(Mandatory)][AllowEmptyString()][string] $Value
    )
    [IO.File]::WriteAllText($Path, $Value, [Text.UTF8Encoding]::new($false))
}

function Invoke-DockerChecked {
    param([Parameter(Mandatory)][string[]] $Arguments)
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = @(& docker @Arguments 2>&1)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($exitCode -ne 0) {
        throw "docker $($Arguments -join ' ') failed:`n$($output -join [Environment]::NewLine)"
    }
    @($output)
}

function ConvertTo-ProcessArgument {
    param([Parameter(Mandatory)][string] $Value)
    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    '"' + $Value.Replace('"', '\"') + '"'
}

function Stop-OwnedProcessTree {
    param([Parameter(Mandatory)][int] $RootProcessId)
    $processes = @(Get-CimInstance Win32_Process -ErrorAction Stop)
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
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    do {
        $alive = @($owned | Where-Object {
            Get-Process -Id $_ -ErrorAction SilentlyContinue
        })
        if ($alive.Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Owned process tree did not exit: $($alive -join ', ')"
}

function Invoke-MonitoredProcess {
    param(
        [Parameter(Mandatory)][string] $FilePath,
        [Parameter(Mandatory)][string[]] $Arguments,
        [Parameter(Mandatory)][string] $Name,
        [Parameter(Mandatory)][string] $WorkingDirectory
    )
    $stdout = Join-Path $logs "$Name.stdout.log"
    $stderr = Join-Path $logs "$Name.stderr.log"
    $result = Invoke-BookBoxCapturedProcess `
        -FilePath $FilePath `
        -Arguments $Arguments `
        -WorkingDirectory $WorkingDirectory `
        -TimeoutMilliseconds ($TimeoutSeconds * 1000) `
        -OnStarted {
            param([int] $processID)
            $ownedProcessIds.Add($processID) | Out-Null
        } `
        -OnProgress {
            param([int] $processID)
            Write-Host "$Name is still running (pid=$processID)."
        } `
        -ProgressIntervalMilliseconds 30000 `
        -OnTimeout {
            param([int] $processID)
            Stop-OwnedProcessTree -RootProcessId $processID
        }
    Write-Utf8 -Path $stdout -Value $result.StandardOutput
    Write-Utf8 -Path $stderr -Value $result.StandardError
    if ($result.TimedOut) {
        throw "$Name timed out after $TimeoutSeconds seconds; see $stdout and $stderr"
    }
    if ($result.ExitCode -ne 0) {
        throw "$Name failed with exit $($result.ExitCode):`n$($result.StandardOutput)`n$($result.StandardError)"
    }
}

function Invoke-BoundedAdb {
    param(
        [Parameter(Mandatory)][string] $AdbPath,
        [Parameter(Mandatory)][string[]] $Arguments,
        [Parameter(Mandatory)][string] $Name,
        [switch] $AllowNonZeroExit
    )

    $script:adbInvocationID++
    $logName = "adb-$($script:adbInvocationID)-$Name"
    $stdout = Join-Path $logs "$logName.stdout.log"
    $stderr = Join-Path $logs "$logName.stderr.log"
    $result = Invoke-BookBoxCapturedProcess `
        -FilePath $AdbPath `
        -Arguments $Arguments `
        -WorkingDirectory $taskRoot `
        -TimeoutMilliseconds 10000 `
        -OnTimeout {
            param([int] $processID)
            Stop-OwnedProcessTree -RootProcessId $processID
        }
    Write-Utf8 -Path $stdout -Value $result.StandardOutput
    Write-Utf8 -Path $stderr -Value $result.StandardError
    $output = @($result.StandardOutput, $result.StandardError) |
        ForEach-Object { @("$_" -split '\r?\n') } |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    if ($result.TimedOut) {
        throw "adb $Name timed out after 10 seconds; see $stdout and $stderr"
    }
    if ($result.ExitCode -ne 0 -and -not $AllowNonZeroExit) {
        throw "adb $Name failed with exit $($result.ExitCode):`n$($output -join [Environment]::NewLine)"
    }
    [pscustomobject]@{
        ExitCode = $result.ExitCode
        Output = @($output)
    }
}

function Start-OwnedAdbServer {
    param([Parameter(Mandatory)][string] $AdbPath)

    if (Get-Process -Name adb -ErrorAction SilentlyContinue) {
        throw 'Refusing to start beside an existing adb process.'
    }
    if (Get-NetTCPConnection -State Listen -LocalPort 5037 -ErrorAction SilentlyContinue) {
        throw 'Refusing to use an existing adb server on port 5037.'
    }
    $stdout = Join-Path $logs 'adb-server.stdout.log'
    $stderr = Join-Path $logs 'adb-server.stderr.log'
    $process = Start-Process `
        -FilePath $AdbPath `
        -ArgumentList ((@('nodaemon', 'server') | ForEach-Object {
            ConvertTo-ProcessArgument $_
        }) -join ' ') `
        -WorkingDirectory $taskRoot `
        -WindowStyle Hidden `
        -RedirectStandardOutput $stdout `
        -RedirectStandardError $stderr `
        -PassThru
    $ownedLongLivedProcessIds.Add($process.Id)
    $taskPorts.Add(5037)
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) {
            $process.WaitForExit()
            $process.Refresh()
            $captured = @()
            if (Test-Path -LiteralPath $stdout -PathType Leaf) {
                $captured += @(Get-Content -LiteralPath $stdout)
            }
            if (Test-Path -LiteralPath $stderr -PathType Leaf) {
                $captured += @(Get-Content -LiteralPath $stderr)
            }
            throw "Owned adb server exited with $($process.ExitCode):`n$($captured -join [Environment]::NewLine)"
        }
        if (Get-NetTCPConnection -State Listen -LocalPort 5037 -ErrorAction SilentlyContinue) {
            return
        }
        Start-Sleep -Milliseconds 250
        $process.Refresh()
    }
    Stop-OwnedProcessTree -RootProcessId $process.Id
    throw 'Owned adb server did not listen on port 5037 within 10 seconds.'
}

function Register-OwnedAndroidProcesses {
    param(
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [int[]] $BeforeProcessIds,
        [Parameter(Mandatory)][int] $LauncherProcessId,
        [Parameter(Mandatory)][string] $AvdName
    )

    $androidProcesses = @(
        Get-CimInstance Win32_Process -ErrorAction Stop |
            Where-Object {
                $_.Name -in @('emulator.exe', 'qemu-system-x86_64.exe')
            }
    )
    $ownedFamily = [Collections.Generic.HashSet[int]]::new()
    $ownedFamily.Add($LauncherProcessId) | Out-Null
    do {
        $added = $false
        foreach ($process in $androidProcesses) {
            if (
                $BeforeProcessIds -notcontains [int] $process.ProcessId -and
                (
                    $ownedFamily.Contains([int] $process.ParentProcessId) -or
                    "$($process.CommandLine)".IndexOf(
                        $AvdName,
                        [StringComparison]::OrdinalIgnoreCase
                    ) -ge 0
                ) -and
                $ownedFamily.Add([int] $process.ProcessId)
            ) {
                $added = $true
            }
        }
    } while ($added)
    foreach ($processID in $ownedFamily) {
        if (
            $BeforeProcessIds -notcontains $processID -and
            -not $ownedLongLivedProcessIds.Contains($processID)
        ) {
            $ownedLongLivedProcessIds.Add($processID)
        }
    }
}

function Wait-Toxiproxy {
    $deadline = [DateTime]::UtcNow.AddSeconds(120)
    $nextProgress = [DateTime]::UtcNow
    while ([DateTime]::UtcNow -lt $deadline) {
        $previous = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'Continue'
            $null = & curl.exe --fail --silent --show-error `
                --connect-timeout 2 --max-time 3 `
                http://127.0.0.1:18474/version 2>$null
            $exitCode = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
        if ($exitCode -eq 0) {
            return
        }
        if ([DateTime]::UtcNow -ge $nextProgress) {
            Write-Output 'Waiting for task-scoped Toxiproxy control port 18474.'
            $nextProgress = [DateTime]::UtcNow.AddSeconds(30)
        }
        Start-Sleep -Seconds 2
    }
    throw 'Toxiproxy control endpoint did not become ready.'
}

function Assert-ApiVersion {
    $body = @(
        & curl.exe --fail --silent --show-error `
            --connect-timeout 3 --max-time 10 `
            'http://127.0.0.1:18787/v1/health'
    )
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to read the public runtime health version.'
    }
    try {
        $health = ($body -join [Environment]::NewLine) | ConvertFrom-Json
    } catch {
        throw "Runtime health response is not valid JSON: $($body -join ' ')"
    }
    if ($health.status -cne 'ok' -or $health.version -cne '1.0.0') {
        throw "Runtime health must report status=ok and version=1.0.0; got $($body -join ' ')"
    }
}

function Get-AdbDeviceEntries {
    param([Parameter(Mandatory)][string] $AdbPath)

    $result = Invoke-BoundedAdb -AdbPath $AdbPath -Arguments @('devices') -Name 'devices'
    @($result.Output | ForEach-Object {
        $line = "$_".Trim()
        if ($line -match '^(\S+)\s+(\S+)$' -and $Matches[1] -ne 'List') {
            [pscustomobject]@{
                Serial = $Matches[1]
                State = $Matches[2]
            }
        }
    })
}

function Get-TaskEmulatorPort {
    foreach ($candidate in 5554..5682 | Where-Object { $_ % 2 -eq 0 }) {
        $listeners = @(
            Get-NetTCPConnection `
                -State Listen `
                -LocalPort @($candidate, ($candidate + 1)) `
                -ErrorAction SilentlyContinue
        )
        if ($listeners.Count -eq 0) {
            return $candidate
        }
    }
    throw 'No free task-scoped Android emulator console/adb port pair is available.'
}

function Wait-TaskEmulatorBoot {
    param(
        [Parameter(Mandatory)][string] $AdbPath,
        [Parameter(Mandatory)][string] $Serial,
        [Parameter(Mandatory)][int] $EmulatorProcessId,
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [int[]] $AndroidProcessIdsBefore,
        [Parameter(Mandatory)][string] $AvdName
    )

    $deadline = [DateTime]::UtcNow.AddSeconds([Math]::Min($TimeoutSeconds, 300))
    $nextProgress = [DateTime]::UtcNow
    $startupDeadline = [DateTime]::UtcNow.AddSeconds(10)
    while ([DateTime]::UtcNow -lt $deadline) {
        Register-OwnedAndroidProcesses `
            -BeforeProcessIds $AndroidProcessIdsBefore `
            -LauncherProcessId $EmulatorProcessId `
            -AvdName $AvdName
        $androidAlive = @($ownedLongLivedProcessIds | Where-Object {
            $candidate = Get-Process -Id $_ -ErrorAction SilentlyContinue
            $null -ne $candidate -and
                $candidate.ProcessName -match '^(emulator|qemu-system-)'
        })
        if (
            $androidAlive.Count -eq 0 -and
            [DateTime]::UtcNow -ge $startupDeadline
        ) {
            throw 'Task-scoped emulator/qemu process exited before boot.'
        }
        $entries = @(Get-AdbDeviceEntries -AdbPath $AdbPath)
        $unexpected = @($entries | Where-Object { $_.Serial -ne $Serial })
        if ($unexpected.Count -ne 0) {
            throw "Refusing ambiguous Android device selection while booting $Serial."
        }
        $target = @($entries | Where-Object {
            $_.Serial -eq $Serial -and $_.State -eq 'device'
        })
        if ($target.Count -eq 1) {
            $boot = Invoke-BoundedAdb `
                -AdbPath $AdbPath `
                -Arguments @('-s', $Serial, 'shell', 'getprop', 'sys.boot_completed') `
                -Name 'boot-property' `
                -AllowNonZeroExit
            if ($boot.ExitCode -eq 0 -and ($boot.Output -join '').Trim() -eq '1') {
                return
            }
        }
        if ([DateTime]::UtcNow -ge $nextProgress) {
            Write-Output "Waiting for task-scoped API 36 emulator $Serial to boot."
            $nextProgress = [DateTime]::UtcNow.AddSeconds(30)
        }
        Start-Sleep -Seconds 2
    }
    throw "Task-scoped emulator $Serial did not boot within the bounded timeout."
}

function Save-FailureDiagnostics {
    param([Parameter(Mandatory)][Exception] $Failure)
    $failureParent = Join-Path ([IO.Path]::GetTempPath()) 'bookbox-e2e-failures'
    $failureRoot = Join-Path $failureParent $runID
    $null = New-Item -ItemType Directory -Path $failureRoot -Force
    Write-Utf8 `
        -Path (Join-Path $failureRoot 'failure.txt') `
        -Value $Failure.ToString()
    if (Test-Path -LiteralPath $logs) {
        Copy-Item -LiteralPath $logs -Destination $failureRoot -Recurse -Force
    }
    foreach ($manifest in @($sourceBefore, $sourceAfter)) {
        if (Test-Path -LiteralPath $manifest -PathType Leaf) {
            Copy-Item -LiteralPath $manifest -Destination $failureRoot -Force
        }
    }
    try {
        Invoke-DockerChecked ($composeArgs + @('ps', '--all')) |
            Set-Content -LiteralPath (Join-Path $failureRoot 'compose-ps.txt')
        Invoke-DockerChecked ($composeArgs + @(
            'logs', '--no-color', '--tail', '500'
        )) | Set-Content -LiteralPath (Join-Path $failureRoot 'compose.log')
    } catch {
        Write-Utf8 `
            -Path (Join-Path $failureRoot 'diagnostics-error.txt') `
            -Value $_.Exception.ToString()
    }
    Write-Warning "E2E failure diagnostics preserved at $failureRoot"
}

if (
    -not [string]::Equals(
        [IO.Path]::GetDirectoryName($taskRoot),
        $e2eParent,
        [StringComparison]::OrdinalIgnoreCase
    ) -or
    [IO.Path]::GetFileName($taskRoot) -ne $runID
) {
    throw 'Refusing an unsafe E2E temporary root.'
}
foreach ($port in @(18787, 18474)) {
    if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) {
        throw "Task-scoped port $port is already in use."
    }
}

try {
    $null = New-Item -ItemType Directory -Path `
        $library, $secrets, $logs -Force
    $normal = Join-Path $library 'Author One\Normal Book'
    $shared = Join-Path $library 'Author Two\Shared Folder'
    $null = New-Item -ItemType Directory -Path $normal, $shared -Force

    $composePath = {
        param([string] $Path)
        [IO.Path]::GetFullPath($Path).Replace('\', '/')
    }
    $environment = @(
        "BOOKBOX_LIBRARY_PATH=$(& $composePath $library)"
        "BOOKBOX_TEST_LIBRARY_PATH=$(& $composePath $library)"
        "BOOKBOX_CONFIG_HOST_FILE=$(& $composePath $config)"
        "BOOKBOX_TEST_CONFIG_HOST_FILE=$(& $composePath $config)"
        "BOOKBOX_POSTGRES_PASSWORD_HOST_FILE=$(& $composePath (Join-Path $secrets 'postgres_password.txt'))"
        "BOOKBOX_TEST_POSTGRES_PASSWORD_HOST_FILE=$(& $composePath (Join-Path $secrets 'postgres_password.txt'))"
        "BOOKBOX_PASSWORD_HASH_HOST_FILE=$(& $composePath (Join-Path $secrets 'app_password_hash.txt'))"
        "BOOKBOX_TEST_PASSWORD_HASH_HOST_FILE=$(& $composePath (Join-Path $secrets 'app_password_hash.txt'))"
        "BOOKBOX_SESSION_KEY_HOST_FILE=$(& $composePath (Join-Path $secrets 'session_key.bin'))"
        "BOOKBOX_TEST_SESSION_KEY_HOST_FILE=$(& $composePath (Join-Path $secrets 'session_key.bin'))"
        "BOOKBOX_GOOGLE_BOOKS_KEY_HOST_FILE=$(& $composePath (Join-Path $secrets 'google_books_key.txt'))"
        "BOOKBOX_TEST_GOOGLE_BOOKS_KEY_HOST_FILE=$(& $composePath (Join-Path $secrets 'google_books_key.txt'))"
        'BOOKBOX_BIND_ADDRESS=127.0.0.1'
        'BOOKBOX_HOST_PORT=18787'
    ) -join [Environment]::NewLine
    # Create this before any fallible fixture setup so finally cleanup can
    # always resolve the unique Compose project and remove the task root.
    Write-Utf8 -Path $environmentFile -Value $environment

    Write-Utf8 -Path $config -Value @'
listen: ":8787"
roots:
  - id: audiobooks
    name: Audiobooks
    container_path: /library/audiobooks
auth:
  username: owner
  password_hash_file: /run/bookbox-secrets/app_password_hash
  session_key_file: /run/bookbox-secrets/session_key
database:
  host: postgres
  port: 5432
  name: bookbox
  username: bookbox
  password_file: /run/bookbox-secrets/postgres_password
  ssl_mode: disable
'@
    Write-Utf8 `
        -Path (Join-Path $secrets 'app_password_hash.txt') `
        -Value 'pending-build-generated-hash'
    Write-Utf8 `
        -Path (Join-Path $secrets 'postgres_password.txt') `
        -Value 'bookbox-e2e-postgres-password'
    [IO.File]::WriteAllBytes(
        (Join-Path $secrets 'session_key.bin'),
        [Text.Encoding]::ASCII.GetBytes(
            '0123456789abcdef0123456789abcdef'
        )
    )
    Write-Utf8 `
        -Path (Join-Path $secrets 'google_books_key.txt') `
        -Value 'fixture-only-google-books-key'

    $docker = (Get-Command docker -ErrorAction Stop).Source
    Invoke-MonitoredProcess `
        -FilePath $docker `
        -Arguments ($composeArgs + @('build')) `
        -Name 'compose-build' `
        -WorkingDirectory $repoRoot

    $apiImage = "${project}-bookbox-api:latest"
    $adminInput = "$password`n$password`n"
    $hashOutput = @(
        $adminInput |
            & docker run --rm -i --network none --read-only `
                --entrypoint /usr/local/bin/bookbox-admin `
                $apiImage hash-password --stdin 2>&1
    )
    if ($LASTEXITCODE -ne 0) {
        throw "Unable to generate E2E password hash:`n$($hashOutput -join [Environment]::NewLine)"
    }
    $passwordHash = @($hashOutput | Where-Object {
        $_ -is [string] -and $_ -match '^\$argon2id\$'
    }) | Select-Object -Last 1
    if ([string]::IsNullOrWhiteSpace($passwordHash)) {
        throw 'The admin tool did not return a password hash.'
    }
    Write-Utf8 `
        -Path (Join-Path $secrets 'app_password_hash.txt') `
        -Value $passwordHash

    $ffmpegBase = $composeArgs + @(
        'run', '--rm', '--no-deps', '--entrypoint', 'ffmpeg',
        'bookbox-catalog-worker',
        '-hide_banner', '-loglevel', 'error',
        '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=44100',
        '-t', '12', '-b:a', '96k', '-y'
    )
    Invoke-DockerChecked ($ffmpegBase + @(
        '-metadata', 'title=Normal Book',
        '-metadata', 'artist=Author One',
        '/library/audiobooks/Author One/Normal Book/chapter01.mp3'
    )) | Out-Null
    Invoke-DockerChecked ($ffmpegBase + @(
        '-metadata', 'title=Normal Book',
        '-metadata', 'artist=Author One',
        '/library/audiobooks/Author One/Normal Book/chapter02.mp3'
    )) | Out-Null
    Invoke-DockerChecked ($ffmpegBase + @(
        '-c:a', 'aac',
        '-metadata', 'title=Dune',
        '-metadata', 'artist=Frank Herbert',
        '/library/audiobooks/Author Two/Shared Folder/Dune.m4b'
    )) | Out-Null
    Invoke-DockerChecked ($ffmpegBase + @(
        '-c:a', 'aac',
        '-metadata', 'title=Dune Messiah',
        '-metadata', 'artist=Frank Herbert',
        '/library/audiobooks/Author Two/Shared Folder/Dune Messiah.m4b'
    )) | Out-Null
    $imageBase = $composeArgs + @(
        'run', '--rm', '--no-deps', '--entrypoint', 'ffmpeg',
        'bookbox-catalog-worker',
        '-hide_banner', '-loglevel', 'error'
    )
    Invoke-DockerChecked ($imageBase + @(
        '-f', 'lavfi', '-i', 'color=c=navy:s=1200x1200:d=0.1',
        '-frames:v', '1', '-y',
        '/library/audiobooks/Author One/Normal Book/cover.jpg'
    )) | Out-Null
    Invoke-DockerChecked ($imageBase + @(
        '-f', 'lavfi', '-i', 'color=c=gray:s=64x64:d=0.1',
        '-frames:v', '1', '-y',
        '/library/audiobooks/Author Two/Shared Folder/folder.jpg'
    )) | Out-Null

    $providerFixtureText = @(
        Get-Content -Raw -LiteralPath (
            Join-Path $PSScriptRoot 'provider-fixtures\openlibrary-dune.json'
        )
        Get-Content -Raw -LiteralPath (
            Join-Path $PSScriptRoot 'provider-fixtures\googlebooks-dune.json'
        )
    ) -join [Environment]::NewLine
    if ($providerFixtureText -match '"cover_i"\s*:' -or
        $providerFixtureText -match '"imageLinks"\s*:'
    ) {
        throw 'E2E metadata fixtures must not offer a replacement cover.'
    }

    & $manifestScript -Path $library -OutputPath $sourceBefore
    Invoke-MonitoredProcess `
        -FilePath $docker `
        -Arguments ($composeArgs + @(
            'up', '--detach', '--build', '--wait', '--wait-timeout', '180'
        )) `
        -Name 'compose-up' `
        -WorkingDirectory $repoRoot

    Wait-Toxiproxy
    $proxyBody = '{"name":"bookbox","listen":"0.0.0.0:18787","upstream":"bookbox-api:8787"}'
    $proxyConfig = Join-Path $taskRoot 'toxiproxy-proxy.json'
    Write-Utf8 -Path $proxyConfig -Value $proxyBody
    $null = & curl.exe --fail --silent --show-error `
        --request POST `
        --header 'Content-Type: application/json' `
        --data-binary "@$proxyConfig" `
        http://127.0.0.1:18474/proxies
    if ($LASTEXITCODE -ne 0) {
        throw 'Unable to configure the task-scoped Toxiproxy route.'
    }
    & (Join-Path $repoRoot 'deploy\scripts\wait-healthy.ps1') `
        -BaseUrl 'http://127.0.0.1:18787' `
        -TimeoutSeconds 180
    Assert-ApiVersion

    $localTooling = Join-Path $repoRoot '.tooling'
    $localJdk = Join-Path $localTooling 'jdk17\jdk-17.0.19+10'
    $localGradleHome = Join-Path $localTooling 'gradle-home'
    $localSdk = Join-Path $localTooling 'android-sdk'
    if (Test-Path -LiteralPath $localJdk -PathType Container) {
        $env:JAVA_HOME = $localJdk
    }
    if (Test-Path -LiteralPath $localGradleHome -PathType Container) {
        $env:GRADLE_USER_HOME = $localGradleHome
    }
    if (
        [string]::IsNullOrWhiteSpace($env:ANDROID_SDK_ROOT) -and
        [string]::IsNullOrWhiteSpace($env:ANDROID_HOME) -and
        (Test-Path -LiteralPath $localSdk -PathType Container)
    ) {
        $env:ANDROID_SDK_ROOT = $localSdk
        $env:ANDROID_HOME = $localSdk
    }
    $sdkRoot = @($env:ANDROID_SDK_ROOT, $env:ANDROID_HOME) |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
        Select-Object -First 1
    if ([string]::IsNullOrWhiteSpace($sdkRoot)) {
        throw 'ANDROID_SDK_ROOT or ANDROID_HOME must point to an API 36 SDK.'
    }
    $adb = Join-Path $sdkRoot 'platform-tools\adb.exe'
    if (-not (Test-Path -LiteralPath $adb -PathType Leaf)) {
        throw "adb was not found under $sdkRoot"
    }

    $avdManager = Join-Path $sdkRoot 'cmdline-tools\latest\bin\avdmanager.bat'
    $emulator = Join-Path $sdkRoot 'emulator\emulator.exe'
    $systemImagePackage = 'system-images;android-36;google_apis;x86_64'
    $systemImageDirectory = Join-Path `
        $sdkRoot `
        'system-images\android-36\google_apis\x86_64'
    if (
        -not (Test-Path -LiteralPath $avdManager -PathType Leaf) -or
        -not (Test-Path -LiteralPath $emulator -PathType Leaf) -or
        -not (Test-Path -LiteralPath $systemImageDirectory -PathType Container)
    ) {
        throw 'The already-installed task-local API 36 emulator tooling is incomplete.'
    }

    $env:ANDROID_USER_HOME = Join-Path $taskRoot 'android-user'
    $env:ANDROID_AVD_HOME = Join-Path $taskRoot 'avd'
    $null = New-Item -ItemType Directory -Path `
        $env:ANDROID_USER_HOME, $env:ANDROID_AVD_HOME -Force
    $avdName = "bookbox_api36_$runID"

    Start-OwnedAdbServer -AdbPath $adb
    $existingDevices = @(Get-AdbDeviceEntries -AdbPath $adb)
    if ($existingDevices.Count -ne 0) {
        throw "Refusing ambiguous Android device selection: found $($existingDevices.Count) pre-existing device(s)."
    }

    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $avdOutput = @(
            'no' |
                & $avdManager `
                    'create', 'avd', '--force', `
                    '--name', $avdName, `
                    '--package', $systemImagePackage 2>&1
        )
        $avdExitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($avdExitCode -ne 0) {
        throw "Unable to create task-scoped API 36 AVD: $($avdOutput -join [Environment]::NewLine)"
    }
    $avdConfig = Join-Path $env:ANDROID_AVD_HOME "$avdName.avd\config.ini"
    if (
        -not (Test-Path -LiteralPath $avdConfig -PathType Leaf) -or
        (Get-Content -Raw -LiteralPath $avdConfig) -notmatch
            'image\.sysdir\.1\s*=\s*system-images[\\/]android-36[\\/]google_apis[\\/]x86_64'
    ) {
        throw 'Task-scoped AVD config does not reference the installed API 36 image.'
    }

    if (@(Get-AdbDeviceEntries -AdbPath $adb).Count -ne 0) {
        throw 'Refusing ambiguous Android device selection before emulator launch.'
    }
    $emulatorPort = Get-TaskEmulatorPort
    $taskPorts.Add($emulatorPort)
    $taskPorts.Add($emulatorPort + 1)
    $emulatorSerial = "emulator-$emulatorPort"
    $androidProcessIdsBefore = @(
        Get-Process -Name emulator, qemu-system-x86_64 -ErrorAction SilentlyContinue |
            ForEach-Object { $_.Id }
    )
    $emulatorProcess = Start-Process `
        -FilePath $emulator `
        -ArgumentList ((@(
            '-avd', $avdName,
            '-no-window',
            '-port', "$emulatorPort",
            '-no-audio',
            '-no-boot-anim',
            '-no-snapshot',
            '-wipe-data',
            '-gpu', 'swiftshader_indirect'
        ) | ForEach-Object { ConvertTo-ProcessArgument $_ }) -join ' ') `
        -WorkingDirectory $taskRoot `
        -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $logs 'emulator.stdout.log') `
        -RedirectStandardError (Join-Path $logs 'emulator.stderr.log') `
        -PassThru
    $ownedLongLivedProcessIds.Add($emulatorProcess.Id)
    Start-Sleep -Seconds 1
    Register-OwnedAndroidProcesses `
        -BeforeProcessIds $androidProcessIdsBefore `
        -LauncherProcessId $emulatorProcess.Id `
        -AvdName $avdName
    Wait-TaskEmulatorBoot `
        -AdbPath $adb `
        -Serial $emulatorSerial `
        -EmulatorProcessId $emulatorProcess.Id `
        -AndroidProcessIdsBefore $androidProcessIdsBefore `
        -AvdName $avdName
    Register-OwnedAndroidProcesses `
        -BeforeProcessIds $androidProcessIdsBefore `
        -LauncherProcessId $emulatorProcess.Id `
        -AvdName $avdName
    $devices = @(Get-AdbDeviceEntries -AdbPath $adb)
    if (
        $devices.Count -ne 1 -or
        $devices[0].Serial -ne $emulatorSerial -or
        $devices[0].State -ne 'device'
    ) {
        throw 'The task-scoped emulator is not the only selectable Android device.'
    }
    $sdkProbe = Invoke-BoundedAdb `
        -AdbPath $adb `
        -Arguments @(
            '-s', $emulatorSerial, 'shell', 'getprop', 'ro.build.version.sdk'
        ) `
        -Name 'sdk-property'
    $sdkVersion = ($sdkProbe.Output -join '').Trim()
    if ($sdkVersion -ne '36') {
        throw "Task-scoped Android emulator is API $sdkVersion; API 36 is required."
    }
    $env:ANDROID_SERIAL = $emulatorSerial

    $gradle = Join-Path $repoRoot 'android\gradlew.bat'
    Invoke-MonitoredProcess `
        -FilePath $env:ComSpec `
        -Arguments (@('/d', '/s', '/c', 'call', $gradle) + @(
            '--no-daemon',
            '-Pkotlin.compiler.execution.strategy=in-process',
            '--console=plain',
            ':app:connectedDebugAndroidTest',
            '-Pandroid.testInstrumentationRunnerArguments.notClass=com.rendref.bookbox.e2e.RealServerJourneyTest'
        )) `
        -Name 'android-api36-instrumentation' `
        -WorkingDirectory (Join-Path $repoRoot 'android')
    Invoke-MonitoredProcess `
        -FilePath $env:ComSpec `
        -Arguments (@('/d', '/s', '/c', 'call', $gradle) + @(
            '--no-daemon',
            '-Pkotlin.compiler.execution.strategy=in-process',
            '--console=plain',
            ':app:connectedDebugAndroidTest',
            '-Pandroid.testInstrumentationRunnerArguments.class=com.rendref.bookbox.e2e.RealServerJourneyTest',
            '-Pandroid.testInstrumentationRunnerArguments.e2eBaseUrl=http://10.0.2.2:18787/',
            "-Pandroid.testInstrumentationRunnerArguments.e2eUsername=$username",
            "-Pandroid.testInstrumentationRunnerArguments.e2ePassword=$password",
            '-Pandroid.testInstrumentationRunnerArguments.e2eToxiproxyPort=18474'
        )) `
        -Name 'android-api36-e2e' `
        -WorkingDirectory (Join-Path $repoRoot 'android')

    & $manifestScript -Path $library -OutputPath $sourceAfter
    $beforeJson = Get-Content -Raw -LiteralPath $sourceBefore
    $afterJson = Get-Content -Raw -LiteralPath $sourceAfter
    if ($beforeJson -cne $afterJson) {
        throw 'The synthetic Windows source tree changed during Android E2E.'
    }
} catch {
    $primaryFailure = $_
    Save-FailureDiagnostics -Failure $_.Exception
} finally {
    try {
        & (Join-Path $PSScriptRoot 'stop.ps1') `
            -ProjectName $project `
            -TaskRoot $taskRoot `
            -EnvironmentFile $environmentFile `
            -OwnedProcessIds $ownedProcessIds.ToArray() `
            -OwnedLongLivedProcessIds $ownedLongLivedProcessIds.ToArray() `
            -AdbPath $adb `
            -EmulatorSerial $emulatorSerial `
            -TaskPorts $taskPorts.ToArray()
    } catch {
        if ($null -eq $primaryFailure) {
            $primaryFailure = $_
        } else {
            Write-Warning "Cleanup also failed: $($_.Exception.Message)"
        }
    }
}

if ($null -ne $primaryFailure) {
    throw $primaryFailure
}
Write-Output 'BookBox E2E passed; owned processes and ports are clean'

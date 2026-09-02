[CmdletBinding()]
param(
    [ValidateRange(60, 1800)]
    [int] $TimeoutSeconds = 600,

    [switch] $ProcessRunnerSelfTest
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'windows-powershell-process.ps1')

$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$baseCompose = Join-Path $repo 'deploy\compose.yml'
$testCompose = Join-Path $repo 'deploy\compose.recovery.test.yml'
$runID = [Guid]::NewGuid().ToString('N')
$project = "bookbox-recovery-$runID"
$parent = [IO.Path]::GetFullPath(
    (Join-Path ([IO.Path]::GetTempPath()) 'bookbox-recovery')
)
$root = [IO.Path]::GetFullPath((Join-Path $parent $runID))
$library = Join-Path $root 'library'
$secrets = Join-Path $root 'secrets'
$logs = Join-Path $root 'logs'
$backup = Join-Path $root 'backup'
$config = Join-Path $root 'config.yml'
$environment = Join-Path $root 'compose.env'
$currentPasswordFile = Join-Path $root 'current-password.txt'
$newPasswordFile = Join-Path $root 'new-password.txt'
$currentPassword = 'bookbox-recovery-current'
$newPassword = 'bookbox-recovery-rotated'
$primaryFailure = $null
$ownedProcesses = [Collections.Generic.List[int]]::new()
$compose = @(
    'compose', '--profile', 'container-worker',
    '--project-name', $project, '--env-file', $environment,
    '-f', $baseCompose, '-f', $testCompose
)

function Write-Utf8 {
    param([string] $Path, [AllowEmptyString()][string] $Value)
    [IO.File]::WriteAllText($Path, $Value, [Text.UTF8Encoding]::new($false))
}

function Invoke-Docker {
    param([string[]] $Arguments)
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = @(& docker @Arguments 2>&1)
        $code = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previous
    }
    if ($code -ne 0) {
        throw "docker $($Arguments -join ' ') failed:`n$($output -join [Environment]::NewLine)"
    }
    @($output)
}

function Convert-Argument {
    param([string] $Value)
    if ($Value -notmatch '[\s"]') { return $Value }
    '"' + $Value.Replace('"', '\"') + '"'
}

function Stop-OwnedTree {
    param([int] $ProcessId)
    $all = @(Get-CimInstance Win32_Process -ErrorAction Stop)
    $owned = [Collections.Generic.HashSet[int]]::new()
    $owned.Add($ProcessId) | Out-Null
    do {
        $changed = $false
        foreach ($item in $all) {
            if (
                $owned.Contains([int] $item.ParentProcessId) -and
                $owned.Add([int] $item.ProcessId)
            ) {
                $changed = $true
            }
        }
    } while ($changed)
    $remaining = [Collections.Generic.HashSet[int]]::new()
    foreach ($id in $owned) {
        $remaining.Add($id) | Out-Null
    }
    while ($remaining.Count -gt 0) {
        $leaves = @($remaining | Where-Object {
            $candidate = $_
            -not ($all | Where-Object {
                $remaining.Contains([int] $_.ProcessId) -and
                [int] $_.ParentProcessId -eq $candidate
            })
        })
        if ($leaves.Count -eq 0) {
            $leaves = @($remaining)
        }
        foreach ($id in $leaves) {
            Stop-Process -Id $id -Force -ErrorAction SilentlyContinue
            $remaining.Remove($id) | Out-Null
        }
    }
}

function Invoke-LiveProcess {
    [OutputType([pscustomobject])]
    param(
        [Parameter(Mandatory)]
        [string] $File,

        [string[]] $Arguments = @(),

        [Parameter(Mandatory)]
        [string] $WorkingDirectory,

        [ValidateRange(1, 1800)]
        [int] $MaximumSeconds,

        [scriptblock] $OnStarted,

        [scriptblock] $OnProgress
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $File
    $startInfo.Arguments = (
        @($Arguments | ForEach-Object { Convert-Argument $_ }) -join ' '
    )
    $startInfo.WorkingDirectory = $WorkingDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    Set-BookBoxWindowsPowerShellModulePath -StartInfo $startInfo

    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $started = $false
    try {
        if (-not $process.Start()) {
            throw "Failed to start process: $File"
        }
        $started = $true
        if ($OnStarted) {
            & $OnStarted ([int] $process.Id)
        }

        # Read both pipes concurrently before waiting so neither child pipe can
        # fill and deadlock a short-lived or verbose process.
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $deadline = [DateTime]::UtcNow.AddSeconds($MaximumSeconds)
        $nextProgress = [DateTime]::UtcNow.AddSeconds(30)
        while (-not $process.WaitForExit(500)) {
            if ([DateTime]::UtcNow -ge $deadline) {
                Stop-OwnedTree ([int] $process.Id)
                [void] $process.WaitForExit(10000)
                throw "Process timed out after $MaximumSeconds seconds: $File"
            }
            if ($OnProgress -and [DateTime]::UtcNow -ge $nextProgress) {
                & $OnProgress ([int] $process.Id)
                $nextProgress = [DateTime]::UtcNow.AddSeconds(30)
            }
        }

        # The parameterless wait completes native exit bookkeeping before the
        # typed ExitCode read on Windows PowerShell 5.1.
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        [pscustomobject]@{
            ProcessId = [int] $process.Id
            ExitCode = [int] $process.ExitCode
            StandardOutput = [string] $stdout
            StandardError = [string] $stderr
        }
    } catch {
        if ($started -and -not $process.HasExited) {
            Stop-OwnedTree ([int] $process.Id)
            [void] $process.WaitForExit(10000)
        }
        throw
    } finally {
        $process.Dispose()
    }
}

function Invoke-Monitored {
    param(
        [string] $Name,
        [string] $File,
        [string[]] $Arguments,
        [int] $MaximumSeconds = $TimeoutSeconds
    )
    Write-Output "Starting synthetic recovery step: $Name"
    $stdout = Join-Path $logs "$Name.stdout.log"
    $stderr = Join-Path $logs "$Name.stderr.log"
    $result = Invoke-LiveProcess `
        -File $File `
        -Arguments $Arguments `
        -WorkingDirectory $repo `
        -MaximumSeconds $MaximumSeconds `
        -OnStarted {
            param([int] $ProcessId)
            $ownedProcesses.Add($ProcessId)
        } `
        -OnProgress {
            param([int] $ProcessId)
            Write-Host "$Name is still running (pid=$ProcessId)."
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
    if ($result.ExitCode -ne 0) {
        throw "$Name failed with exit $($result.ExitCode)`:`n$(
            $result.StandardOutput
        )`n$($result.StandardError)"
    }
}

if ($ProcessRunnerSelfTest) {
    $success = Invoke-LiveProcess `
        -File $env:ComSpec `
        -Arguments @('/d', '/c', 'exit 0') `
        -WorkingDirectory $repo `
        -MaximumSeconds 10
    if (
        $success.ExitCode -isnot [int] -or
        $success.ExitCode -ne 0
    ) {
        throw 'Live process runner did not return typed exit code 0.'
    }

    $failure = Invoke-LiveProcess `
        -File $env:ComSpec `
        -Arguments @('/d', '/c', 'exit 7') `
        -WorkingDirectory $repo `
        -MaximumSeconds 10
    if (
        $failure.ExitCode -isnot [int] -or
        $failure.ExitCode -ne 7
    ) {
        throw 'Live process runner did not return typed exit code 7.'
    }

    $windowsPowerShell = Invoke-LiveProcess `
        -File 'powershell.exe' `
        -Arguments @(
            '-NoProfile', '-Command',
            '(Get-Command Get-FileHash -ErrorAction Stop).Name'
        ) `
        -WorkingDirectory $repo `
        -MaximumSeconds 10
    if (
        $windowsPowerShell.ExitCode -ne 0 -or
        $windowsPowerShell.StandardOutput.Trim() -cne 'Get-FileHash'
    ) {
        throw "Recovery runner cannot resolve Windows PowerShell built-in modules: $($windowsPowerShell.StandardError)"
    }

    Write-Output 'PASS: recovery process runner preserves exit codes and Windows PowerShell modules.'
    return
}

function Get-Container {
    param([string] $Service)
    $id = (@(Invoke-Docker ($compose + @('ps', '-q', $Service))) -join '').Trim()
    if (-not $id) { throw "Missing recovery container: $Service" }
    $id
}

function Assert-VolumeFile {
    param([string] $Volume, [string] $Name, [string] $Expected)
    $actual = @(Invoke-Docker @(
        'run', '--rm', '--network', 'none', '--read-only',
        '--mount', "type=volume,src=$Volume,dst=/source,readonly",
        '--entrypoint', 'cat', 'postgres:18-alpine', "/source/$Name"
    )) -join ''
    if ($actual -cne $Expected) {
        throw "Restored probe mismatch: $Volume/$Name"
    }
}

function Get-Resources {
    param([ValidateSet('container', 'network', 'volume', 'image')][string] $Kind)
    $label = "label=com.docker.compose.project=$project"
    switch ($Kind) {
        container { Invoke-Docker @('ps', '-a', '--filter', $label, '--format', '{{.ID}}') }
        network { Invoke-Docker @('network', 'ls', '--filter', $label, '--format', '{{.ID}}') }
        volume { Invoke-Docker @('volume', 'ls', '--filter', $label, '--format', '{{.Name}}') }
        image { Invoke-Docker @('image', 'ls', '--filter', $label, '--format', '{{.Repository}}:{{.Tag}}') }
    }
}

if (
    -not [string]::Equals(
        [IO.Path]::GetDirectoryName($root), $parent,
        [StringComparison]::OrdinalIgnoreCase
    ) -or
    [IO.Path]::GetFileName($root) -ne $runID
) {
    throw 'Unsafe synthetic recovery root.'
}
foreach ($port in @(18787, 18474)) {
    if (Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue) {
        throw "Synthetic recovery port is already in use: $port"
    }
}

try {
    foreach ($directory in @($library, $secrets, $logs)) {
        [IO.Directory]::CreateDirectory($directory) | Out-Null
    }
    Write-Utf8 (Join-Path $library 'README.txt') 'Synthetic fixture only.'
    $path = {
        param([string] $Value)
        [IO.Path]::GetFullPath($Value).Replace('\', '/')
    }
    Write-Utf8 $environment (@(
        "BOOKBOX_LIBRARY_PATH=$(& $path $library)"
        "BOOKBOX_RECOVERY_LIBRARY_PATH=$(& $path $library)"
        "BOOKBOX_CONFIG_HOST_FILE=$(& $path $config)"
        "BOOKBOX_RECOVERY_CONFIG_HOST_FILE=$(& $path $config)"
        "BOOKBOX_POSTGRES_PASSWORD_HOST_FILE=$(& $path (Join-Path $secrets 'postgres_password.txt'))"
        "BOOKBOX_RECOVERY_POSTGRES_PASSWORD_HOST_FILE=$(& $path (Join-Path $secrets 'postgres_password.txt'))"
        "BOOKBOX_PASSWORD_HASH_HOST_FILE=$(& $path (Join-Path $secrets 'app_password_hash.txt'))"
        "BOOKBOX_RECOVERY_PASSWORD_HASH_HOST_FILE=$(& $path (Join-Path $secrets 'app_password_hash.txt'))"
        "BOOKBOX_SESSION_KEY_HOST_FILE=$(& $path (Join-Path $secrets 'session_key.bin'))"
        "BOOKBOX_RECOVERY_SESSION_KEY_HOST_FILE=$(& $path (Join-Path $secrets 'session_key.bin'))"
        "BOOKBOX_GOOGLE_BOOKS_KEY_HOST_FILE=$(& $path (Join-Path $secrets 'google_books_key.txt'))"
        "BOOKBOX_RECOVERY_GOOGLE_BOOKS_KEY_HOST_FILE=$(& $path (Join-Path $secrets 'google_books_key.txt'))"
        'BOOKBOX_BIND_ADDRESS=127.0.0.1'
        'BOOKBOX_HOST_PORT=18787'
    ) -join [Environment]::NewLine)
    Write-Utf8 $config @'
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
    Write-Utf8 (Join-Path $secrets 'postgres_password.txt') 'recovery-postgres'
    Write-Utf8 (Join-Path $secrets 'app_password_hash.txt') 'pending'
    Write-Utf8 (Join-Path $secrets 'google_books_key.txt') ''
    [IO.File]::WriteAllBytes(
        (Join-Path $secrets 'session_key.bin'),
        [Text.Encoding]::ASCII.GetBytes('0123456789abcdef0123456789abcdef')
    )
    Write-Utf8 $currentPasswordFile $currentPassword
    Write-Utf8 $newPasswordFile $newPassword

    $docker = (Get-Command docker -ErrorAction Stop).Source
    Invoke-Monitored 'build' $docker ($compose + @('build'))
    $image = "${project}-bookbox-api:latest"
    $hashOutput = @(
        "$currentPassword`n$currentPassword`n" |
            & docker run --rm -i --network none --read-only `
                --entrypoint /usr/local/bin/bookbox-admin `
                $image hash-password --stdin 2>&1
    )
    if ($LASTEXITCODE -ne 0) { throw 'Password hashing failed.' }
    $hash = @($hashOutput | Where-Object {
        $_ -is [string] -and $_ -match '^\$argon2id\$'
    }) | Select-Object -Last 1
    if (-not $hash) { throw 'Argon2id hash was not returned.' }
    Write-Utf8 (Join-Path $secrets 'app_password_hash.txt') $hash

    Invoke-Monitored 'up-source' $docker ($compose + @(
        'up', '-d', '--wait', '--wait-timeout', '180'
    )) 300
    $postgres = Get-Container 'postgres'
    Invoke-Docker @(
        'exec', $postgres, 'psql', '-U', 'bookbox', '-d', 'bookbox',
        '-v', 'ON_ERROR_STOP=1', '-c',
        "create table recovery_probe(value text primary key); insert into recovery_probe values ('database-restored');"
    ) | Out-Null

    $cacheVolume = "${project}_recovery_cover_cache"
    $backupVolume = "${project}_recovery_cover_backups"
    foreach ($probe in @(
        @($cacheVolume, 'cover-cache-probe', 'cache-restored'),
        @($backupVolume, 'cover-backup-probe', 'backup-restored')
    )) {
        Invoke-Docker @(
            'run', '--rm', '--network', 'none',
            '--mount', "type=volume,src=$($probe[0]),dst=/target",
            '--entrypoint', 'sh', 'postgres:18-alpine', '-c',
            "printf '%s' '$($probe[2])' > '/target/$($probe[1])'"
        ) | Out-Null
    }
    $operationFile = Join-Path $root 'recovery-operation.json'
    Write-Utf8 $operationFile ([ordered]@{
        Destination = $backup
        ProjectName = $project
        ComposeFile = @($baseCompose, $testCompose)
        EnvironmentFile = $environment
        ConfigFile = $config
        AppVersion = '1.0.0'
    } | ConvertTo-Json -Depth 4)
    Invoke-Monitored 'backup' 'powershell.exe' @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass',
        '-File', (Join-Path $PSScriptRoot 'invoke-synthetic-recovery.ps1'),
        '-Operation', 'backup', '-ArgumentsFile', $operationFile
    )
    $manifest = Get-Content (Join-Path $backup 'manifest.json') -Raw |
        ConvertFrom-Json
    if ($manifest.appVersion -cne '1.0.0') {
        throw 'Backup manifest did not preserve the application version.'
    }

    Invoke-Monitored 'down-before-restore' $docker ($compose + @(
        'down', '--volumes', '--remove-orphans', '--timeout', '10'
    )) 180
    Write-Utf8 $operationFile ([ordered]@{
        BackupDirectory = $backup
        ProjectName = $project
        ComposeFile = @($baseCompose, $testCompose)
        EnvironmentFile = $environment
        BaseUrl = 'http://127.0.0.1:18787'
        TimeoutSeconds = [Math]::Min($TimeoutSeconds, 1800)
    } | ConvertTo-Json -Depth 4)
    Invoke-Monitored 'restore' 'powershell.exe' @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass',
        '-File', (Join-Path $PSScriptRoot 'invoke-synthetic-recovery.ps1'),
        '-Operation', 'restore', '-ArgumentsFile', $operationFile
    ) ([Math]::Min($TimeoutSeconds + 300, 1800))

    $postgres = Get-Container 'postgres'
    $probe = @(Invoke-Docker @(
        'exec', $postgres, 'psql', '-U', 'bookbox', '-d', 'bookbox',
        '-tA', '-c', 'select value from recovery_probe'
    )) -join ''
    if ($probe.Trim() -cne 'database-restored') {
        throw 'PostgreSQL recovery_probe was not restored.'
    }
    Assert-VolumeFile $cacheVolume 'cover-cache-probe' 'cache-restored'
    Assert-VolumeFile $backupVolume 'cover-backup-probe' 'backup-restored'

    Write-Utf8 $operationFile ([ordered]@{
        ProjectName = $project
        ComposeFile = @($baseCompose, $testCompose)
        EnvironmentFile = $environment
        CurrentPasswordFile = $currentPasswordFile
        NewPasswordFile = $newPasswordFile
        BaseUrl = 'http://127.0.0.1:18787'
        Username = 'owner'
        TimeoutSeconds = [Math]::Min($TimeoutSeconds, 600)
    } | ConvertTo-Json -Depth 4)
    Invoke-Monitored 'rotate-password-session' 'powershell.exe' @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass',
        '-File', (Join-Path $PSScriptRoot 'invoke-synthetic-recovery.ps1'),
        '-Operation', 'rotate', '-ArgumentsFile', $operationFile
    ) ([Math]::Min($TimeoutSeconds + 120, 900))
    Write-Output 'PASS: synthetic recovery restored PostgreSQL, config, cover volumes, password, and sessions.'
} catch {
    $primaryFailure = $_
    $failureRoot = Join-Path (
        Join-Path ([IO.Path]::GetTempPath()) 'bookbox-recovery-failures'
    ) $runID
    [IO.Directory]::CreateDirectory($failureRoot) | Out-Null
    Write-Utf8 (Join-Path $failureRoot 'failure.txt') $_.Exception.ToString()
    if (Test-Path $logs) {
        Copy-Item $logs -Destination $failureRoot -Recurse -Force
    }
    Write-Warning "Recovery diagnostics preserved at $failureRoot"
} finally {
    $safeToDelete = $true
    if (Test-Path $environment) {
        try {
            $docker = (Get-Command docker -ErrorAction Stop).Source
            Invoke-Monitored 'cleanup' $docker ($compose + @(
                'down', '--volumes', '--rmi', 'local',
                '--remove-orphans', '--timeout', '10'
            )) 180
        } catch {
            if ($null -eq $primaryFailure) { $primaryFailure = $_ }
            else { Write-Warning "Cleanup also failed: $($_.Exception.Message)" }
        }
    }
    try {
        $remaining = @()
        foreach ($kind in @('container', 'network', 'volume', 'image')) {
            $remaining += @(Get-Resources $kind | Where-Object {
                -not [string]::IsNullOrWhiteSpace([string] $_)
            })
        }
        $remaining += @(Invoke-Docker @(
            'image', 'ls', '--filter', "reference=$project-*",
            '--format', '{{.Repository}}:{{.Tag}}'
        ) | Where-Object {
            -not [string]::IsNullOrWhiteSpace([string] $_)
        })
        if ($remaining.Count -ne 0) {
            $safeToDelete = $false
            throw "Recovery resources remain: $($remaining -join ', ')"
        }
        $listeners = foreach ($port in @(18787, 18474)) {
            Get-NetTCPConnection -State Listen -LocalPort $port `
                -ErrorAction SilentlyContinue
        }
        if (@($listeners).Count -ne 0) {
            $safeToDelete = $false
            throw 'Recovery task port remains in use.'
        }
        $processExitDeadline = [DateTime]::UtcNow.AddSeconds(5)
        do {
            $alive = foreach ($id in $ownedProcesses) {
                Get-Process -Id $id -ErrorAction SilentlyContinue
            }
            if (@($alive).Count -eq 0 -or
                [DateTime]::UtcNow -ge $processExitDeadline) {
                break
            }
            Start-Sleep -Milliseconds 100
        } while ($true)
        if (@($alive).Count -ne 0) {
            $safeToDelete = $false
            throw 'Recovery child process remains alive.'
        }
    } catch {
        $safeToDelete = $false
        if ($null -eq $primaryFailure) { $primaryFailure = $_ }
        else { Write-Warning "Audit also failed: $($_.Exception.Message)" }
    }
    if ($safeToDelete -and (Test-Path $root)) {
        $target = [IO.Path]::GetFullPath($root).TrimEnd('\')
        if (
            -not [string]::Equals(
                [IO.Path]::GetDirectoryName($target), $parent,
                [StringComparison]::OrdinalIgnoreCase
            ) -or
            [IO.Path]::GetFileName($target) -ne $runID
        ) {
            throw "Unsafe recovery cleanup target: $target"
        }
        Remove-Item -LiteralPath $target -Recurse -Force
    }
}

if ($null -ne $primaryFailure) { throw $primaryFailure }

[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'run.ps1'
if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
    throw 'E2E run script is missing.'
}
$runText = Get-Content -Raw -LiteralPath $scriptPath
$runnerPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'process-runner.ps1'
if (-not (Test-Path -LiteralPath $runnerPath -PathType Leaf)) {
    throw 'Shared E2E process runner is missing.'
}
$runnerText = Get-Content -Raw -LiteralPath $runnerPath
$composeTestPath = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'deploy\compose.test.yml'
$hybridComposePath = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'deploy\compose.hybrid.yml'
$fixturePath = Join-Path (Split-Path -Parent $PSScriptRoot) 'support\ffprobe-fixture\main.go'
$composeTestText = Get-Content -Raw -LiteralPath $composeTestPath
$hybridComposeText = Get-Content -Raw -LiteralPath $hybridComposePath
$stopPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'stop.ps1'
$stopText = Get-Content -Raw -LiteralPath $stopPath
$catalogPath = Join-Path $PSScriptRoot 'catalog-search.tests.ps1'
$stagedCatalogPath = Join-Path $PSScriptRoot 'staged-catalog.tests.ps1'
$stagedCatalogText = Get-Content -Raw -LiteralPath $stagedCatalogPath
foreach ($powershellPath in @($scriptPath, $stopPath, $catalogPath, $stagedCatalogPath)) {
    $tokens = $null
    $parseErrors = $null
    $null = [Management.Automation.Language.Parser]::ParseFile(
        $powershellPath,
        [ref] $tokens,
        [ref] $parseErrors
    )
    if ($parseErrors.Count -ne 0) {
        throw "PowerShell syntax error in $([IO.Path]::GetFileName($powershellPath)): $($parseErrors[0].Message)"
    }
}

$requirements = [ordered]@{
    'unique project' = 'bookbox-e2e-\$runID'
    'task-scoped temp root' = "Join-Path .*'bookbox-e2e'"
    'thirty-second progress' = '30'
    'API 36 check' = 'ro\.build\.version\.sdk'
    'Toxiproxy readiness' = '18474'
    'Toxiproxy JSON file upload' = '(?s)toxiproxy-proxy\.json.*?--data-binary.*?@\$proxyConfig'
    'runtime version stamp' = "(?s)/v1/health.*?version -cne '1\.0\.0'"
    'base URL argument' = 'e2eBaseUrl'
    'username argument' = 'e2eUsername'
    'password argument' = 'e2ePassword'
    'source manifest before' = 'source-before\.json'
    'source manifest after' = 'source-after\.json'
    'Argon2 password hash' = '\^\\\$argon2id\\\$'
    'project-local JDK' = '(?s)localJdkRoot.*?jdk17.*?bin\\java\.exe'
    'project-local Gradle home' = "localGradleHome"
    'task-scoped AVD home' = 'ANDROID_AVD_HOME\s*=\s*Join-Path\s+\$taskRoot\s+''avd'''
    'unique AVD name' = 'bookbox_api36_\$runID'
    'installed API 36 image' = 'system-images;android-36;google_apis;x86_64'
    'create AVD without download' = '(?s)avdmanager\.bat.*?echo no\|call.*?create avd --force.*?--package \{2\}.*?\$systemImagePackage'
    'headless owned emulator' = '(?s)Start-Process.*?-FilePath \$emulator.*?''-no-window'''
    'task emulator port' = '''-port'', "\$emulatorPort"'
    'task emulator serial' = 'emulator-\$emulatorPort'
    'boot completion wait' = 'sys\.boot_completed'
    'reject pre-existing device' = 'Refusing ambiguous Android device selection'
    'pin Gradle device' = 'ANDROID_SERIAL\s*=\s*\$emulatorSerial'
    'record emulator and adb PIDs' = 'ownedLongLivedProcessIds'
    'shared process runner import' = '\.\s+\$processRunner'
    'bounded adb helper' = 'function Invoke-BoundedAdb'
    'captured adb output split into lines' = '(?s)function Invoke-BoundedAdb.*?-split ''\\r\?\\n'''
    'captured monitored process' = '(?s)function Invoke-MonitoredProcess.*?Invoke-BookBoxCapturedProcess'
    'captured bounded adb' = '(?s)function Invoke-BoundedAdb.*?Invoke-BookBoxCapturedProcess'
    'explicit owned adb server' = '(?s)function Start-OwnedAdbServer.*?''nodaemon'', ''server''.*?RedirectStandardOutput.*?RedirectStandardError.*?ownedLongLivedProcessIds\.Add'
    'owned adb exit refresh' = '(?s)function Start-OwnedAdbServer(?:(?!\r?\nfunction ).)*?WaitForExit\(\)(?:(?!\r?\nfunction ).)*?Refresh\(\)(?:(?!\r?\nfunction ).)*?ExitCode'
    'bounded adb server readiness' = '(?s)function Start-OwnedAdbServer.*?AddSeconds\(10\).*?LocalPort 5037'
    'refuse pre-existing adb process' = '(?s)function Start-OwnedAdbServer.*?Get-Process -Name adb.*?Refusing to start beside an existing adb process'
    'record adb port' = 'taskPorts\.Add\(5037\)'
    'bounded adb devices' = 'Invoke-BoundedAdb\s+.*?-Arguments @\(''devices''\)'
    'bounded boot property' = '(?s)Invoke-BoundedAdb\s+.*?''sys\.boot_completed'''
    'bounded SDK property' = '(?s)Invoke-BoundedAdb\s+.*?''ro\.build\.version\.sdk'''
    'owned qemu registration' = '(?s)function Register-OwnedAndroidProcesses.*?qemu-system-x86_64\.exe.*?ownedLongLivedProcessIds\.Add'
    'empty Android baseline accepted' = '(?s)function Register-OwnedAndroidProcesses.*?\[AllowEmptyCollection\(\)\].*?\[int\[\]\] \$BeforeProcessIds'
    'empty boot baseline accepted' = '(?s)function Wait-TaskEmulatorBoot.*?\[AllowEmptyCollection\(\)\].*?\[int\[\]\] \$AndroidProcessIdsBefore'
    'non-E2E instrumentation pass' = 'notClass=com\.rendref\.bookbox\.e2e\.RealServerJourneyTest'
    'parameterized real-server pass' = 'class=com\.rendref\.bookbox\.e2e\.RealServerJourneyTest'
    'cover publication disabled' = 'must not offer a replacement cover'
    'owned process tree' = 'Stop-OwnedProcessTree'
    'failure diagnostics' = 'bookbox-e2e-failures'
    'finally cleanup' = '(?s)finally\s*\{.*stop\.ps1'
    'final success line' =
        'BookBox E2E passed; owned processes and ports are clean'
    'hybrid deployment parameter' = '(?s)\[ValidateSet\(''Container'', ''Hybrid''\)\].*?\[string\] \$DeploymentMode'
    'hybrid catalog gate' = '(?s)CatalogSearchOnly.*?DeploymentMode.*?Hybrid'
    'focused staged catalog gate' = '(?s)StagedCatalogOnly.*?staged-catalog\.tests\.ps1'
    'focused staged hybrid gate' = "(?s)StagedCatalogOnly.*?DeploymentMode -ne 'Hybrid'"
    'catalog search excludes staged reorganization' = '(?s)if\s*\(-not \$StagedCatalogOnly -and -not \$CatalogSearchOnly\)\s*\{\s*\$stagedCatalog'
    'staged disposable library argument' = '(?s)staged-catalog\.tests\.ps1.*?-LibraryPath \$library'
    'disposable reorganization opt-in' = '(?s)bookbox-e2e-\$runID.*?allow_library_reorganization:\s*true'
    'hybrid compose override' = 'compose\.hybrid\.yml'
    'loopback PostgreSQL port' = 'BOOKBOX_POSTGRES_HOST_PORT=55432'
    'native worker build' = 'bookbox-catalog-worker\.exe'
    'native fixture build' = 'ffprobe-fixture\.exe'
    'pinned Go image' = 'golang:1\.26\.5'
    'native worker config' = 'worker-config\.yml'
    'external probe path' = 'BOOKBOX_FFPROBE_PATH'
    'native worker interactive mode' = '''run'', ''--config'', \$workerConfig'
    'owned native worker' = '(?s)function Start-OwnedProcess.*?ownedLongLivedProcessIds\.Add.*?Start-OwnedProcess.*?native-worker'
    'six-hour fallback' = 'BOOKBOX_TEST_SCAN_INTERVAL=6h'
    'exact native PID stop' = 'Stop-OwnedNativeWorker\s+-Process\s+\$nativeWorkerProcess'
    'exact task root audit' = '(?s)taskRoot.*?OwnedLongLivedProcessIds.*?TaskPorts'
    'hybrid dummy fixtures' = "(?s)DeploymentMode\s+-eq\s+'Container'.*?ffmpegBase.*?else\s*\{.*?task-scoped ffprobe fixture input"
    'fixture PID marker directory' = 'BOOKBOX_FFPROBE_FIXTURE_PID_DIRECTORY'
    'stable database-only order' = '(?s)databaseOnlySearch\.BookOrder.*?stoppedWorkerSearch\.BookOrder.*?databaseOnlySearch\.RawOrder.*?stoppedWorkerSearch\.RawOrder'
    'runtime PostgreSQL binding gate' = "Assert-TaskPortBound\s+-Service\s+'postgres'\s+-ContainerPort\s+5432\s+-HostPort\s+55432\s+-Name\s+'PostgreSQL'"
    'runtime PostgreSQL TCP gate' = '(?s)function Assert-TaskPortBound.*?TcpClient.*?ConnectAsync.*?127\.0\.0\.1'
    'array-safe binding capture' = '(?s)\$actual\s*=\s*@\(\s*@\(Invoke-DockerChecked.*?Where-Object.*?\)'
    'hybrid native stop before container stop' = "(?s)if\s*\(\s*\`$DeploymentMode\s+-eq\s+'Hybrid'\s*\)\s*\{\s*Stop-OwnedNativeWorker.*?\}\s*else\s*\{\s*Invoke-DockerChecked.*?'stop'.*?bookbox-catalog-worker"
    'container-only worker restart' = "(?s)if\s*\(\s*\`$DeploymentMode\s+-eq\s+'Container'.*?\)\s*\{\s*Invoke-DockerChecked.*?'up'.*?bookbox-catalog-worker"
    'container monitored-file startup scan' = "(?s)Copy-Item.*?if\s*\(\s*\`$DeploymentMode\s+-eq\s+'Container'\s*\)\s*\{\s*Invoke-DockerChecked.*?'restart'.*?bookbox-catalog-worker.*?\}\s*\`$monitoredSearch"
    'hybrid-only PostgreSQL reservation' = "(?s)if\s*\(\s*\`$DeploymentMode\s+-eq\s+'Hybrid'\s*\)\s*\{.*?taskPorts\.Add\(55432\)"
    'hybrid-only PostgreSQL environment' = "(?s)if\s*\(\s*\`$DeploymentMode\s+-eq\s+'Hybrid'\s*\)\s*\{.*?BOOKBOX_POSTGRES_HOST_PORT=55432"
    'hybrid-only PostgreSQL gate' = "(?s)if\s*\(\s*\`$DeploymentMode\s+-eq\s+'Hybrid'\s*\)\s*\{.*?Assert-TaskPortBound.*?postgres"
    'provider runtime binding gate' = "Assert-TaskPortBound\s+-Service\s+'provider-fixtures'\s+-ContainerPort\s+8080\s+-HostPort\s+18080"
    'generalized runtime binding helper' = '(?s)function Assert-TaskPortBound.*?\$Service.*?\$ContainerPort.*?\$HostPort'
    'identity-safe native worker stop' = '(?s)function Stop-OwnedNativeWorker.*?\$Process\.HasExited.*?MainModule\.FileName.*?CommandLine.*?\$Process\.Kill\(\).*?\$Process\.WaitForExit\(15000\)'
}
foreach ($entry in $requirements.GetEnumerator()) {
    if ($runText -notmatch $entry.Value) {
        throw "Missing E2E orchestration requirement: $($entry.Key)"
    }
}

if ($runText -match '(?i)Remove-Item[^\r\n]*H:\\') {
    throw 'E2E run script must never remove anything on H:.'
}
if ($runText -match '(?i)BOOKBOX_TEST_LIBRARY_PATH=H:') {
    throw 'E2E run script must never mount H: as its synthetic library.'
}
if ($runText -match '(?i)tls|certificate|fingerprint|https://|8443|18443|--insecure|--no-check-certificate') {
    throw 'E2E run script still contains TLS behavior.'
}
if ($runText -notmatch '18787') {
    throw 'E2E run script must use the task-scoped HTTP port 18787.'
}
if (
    [regex]::Matches(
        $runText,
        [regex]::Escape('-Pkotlin.compiler.execution.strategy=in-process')
    ).Count -lt 2
) {
    throw 'Every no-daemon E2E Gradle lane must keep the Kotlin compiler in-process.'
}
if ($runText -match '(?i)\bsdkmanager(?:\.bat)?\b') {
    throw 'E2E must use the already-installed API 36 image without downloads.'
}
if ($runText -match '(?i)\badb(?:\.exe)?\s+kill-server\b') {
    throw 'E2E must never stop an unrelated global adb server.'
}
if ($runText -match '(?i)\bstart-server\b') {
    throw 'E2E must own an explicit nodaemon adb server instead of a daemon handoff.'
}
foreach ($stagedRequirement in ([ordered]@{
    'bounded staged SQL process' = 'Invoke-BookBoxCapturedProcess'
    'task-scoped staged library' = 'expectedLibraryPath'
    'generated temporary task root' = '(?s)expectedTaskParent.*?GetTempPath.*?bookbox-e2e'
    'task project binding' = 'expectedProjectName\s*=\s*"bookbox-e2e-\$taskLeaf"'
    'loopback destructive base URL' = "(?s)BaseUrl\.TrimEnd\('/'\).*?http://127\.0\.0\.1:18787"
    'exact staged Compose binding' = 'expectedComposeFiles'
    'unchanged fingerprint inventory' = '/v1/catalog/rescan'
    'physical move manifest' = 'Assert-MovedFileManifest'
    'physical rollback manifest' = 'Assert-FileManifestUnchanged'
    'second move failure' = 'new\.move_order=2'
    'real-media duration tolerance' = "\(media->>'durationMs'\)::bigint between 11500 and 12500"
}).GetEnumerator()) {
    if ($stagedCatalogText -notmatch $stagedRequirement.Value) {
        throw "Missing staged-catalog safety requirement: $($stagedRequirement.Key)"
    }
}
if ($composeTestText -notmatch '(?s)provider-fixtures:.*?published:\s*"?\$\{BOOKBOX_TEST_PROVIDER_HOST_PORT:-18080\}"?.*?host_ip:\s*127\.0\.0\.1') {
    throw 'Hybrid E2E provider fixtures must publish a task-scoped loopback port.'
}
if ($hybridComposeText -notmatch '(?s)postgres:.*?networks:.*?backend.*?hybrid-postgres-host' -or
    $hybridComposeText -notmatch '(?s)networks:.*?hybrid-postgres-host:' -or
    [regex]::Matches($hybridComposeText, 'hybrid-postgres-host').Count -ne 2) {
    throw 'Hybrid PostgreSQL must alone join a dedicated non-internal host bridge while retaining backend.'
}
if (-not (Test-Path -LiteralPath $fixturePath -PathType Leaf)) {
    throw 'Task-scoped external ffprobe fixture source is missing.'
}
$fixtureText = Get-Content -Raw -LiteralPath $fixturePath
foreach ($canonicalCheck in @(
    'samePath\(manifestAbsolute, manifestPath\)',
    'samePath\(inputAbsolute, input\)',
    'samePath\(pidAbsolute, pidRoot\)'
)) {
    if ($fixtureText -notmatch $canonicalCheck) {
        throw "ffprobe fixture must reject a reparse-point candidate: $canonicalCheck"
    }
}
if ($stopText -notmatch '(?s)DeploymentMode.*?compose\.hybrid\.yml.*?cleanupComposeArgs' -or
    $stopText -notmatch '(?s)ffprobe-pids.*?ExecutablePath.*?ffprobeFixtureExecutablePath') {
    throw 'E2E cleanup must use exact hybrid Compose scope and PID-reuse-safe fixture auditing.'
}
foreach ($pidReuseContract in @(
    '(?s)function Stop-ExactOwnedProcessTree.*?\$isTaskProcessName.*?if\s*\(-not \$isTaskProcessName\)\s*\{.*?return',
    '(?s)if\s*\(\$isTaskProcessName -and -not \(\$isAndroid -or \$isNativeWorker -or \$isProbeFixture\)\).*?Refusing to stop',
    '(?s)\$ownedAlive.*?Get-CimInstance Win32_Process.*?\$isTaskProcessName'
)) {
    if ($stopText -notmatch $pidReuseContract) {
        throw "E2E cleanup is not PID-reuse safe: $pidReuseContract"
    }
}
$nativeStop = [regex]::Match(
    $runText,
    '(?s)function Stop-OwnedNativeWorker\s*\{.*?(?=\r?\nfunction )'
)
if (-not $nativeStop.Success -or
    $nativeStop.Value -match 'Stop-OwnedProcessTree|Stop-Process|-RootProcessId') {
    throw 'Native worker stop must use the validated original process handle, not an ID-only kill helper.'
}
if ($runText -match '&\s+\$(?:adb|AdbPath)\b') {
    throw 'Every adb invocation must use the bounded monitored helper.'
}
if (
    $runText -match '&\s+\$avdManager\b' -or
    $runText -notmatch '(?s)\$env:ComSpec.*?/d.*?/s.*?/c.*?call.*?\$avdManager'
) {
    throw 'AVD creation must invoke avdmanager.bat through the Windows batch interpreter.'
}
if (
    $runnerText -notmatch 'System\.Diagnostics\.ProcessStartInfo' -or
    $runnerText -notmatch 'UseShellExecute\s*=\s*\$false' -or
    $runnerText -notmatch 'CreateNoWindow\s*=\s*\$true' -or
    $runnerText -notmatch 'ReadToEndAsync\(\)' -or
    $runnerText -notmatch '\[int\]\s+\$exitCode\s*='
) {
    throw 'Shared process runner must use deadlock-safe Diagnostics.Process capture.'
}

Write-Output 'PASS: E2E orchestration contains bounded isolated safety gates.'

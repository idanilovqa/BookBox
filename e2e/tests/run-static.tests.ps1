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

$requirements = [ordered]@{
    'unique project' = 'bookbox-e2e-\$runID'
    'task-scoped temp root' = "Join-Path .*'bookbox-e2e'"
    'thirty-second progress' = '30'
    'API 36 check' = 'ro\.build\.version\.sdk'
    'Toxiproxy readiness' = '18474'
    'Toxiproxy JSON file upload' = '(?s)toxiproxy-proxy\.json.*?--data-binary.*?@\$proxyConfig'
    'runtime version stamp' = "(?s)/v1/health.*?version -cne '1\.0\.0'"
    'TLS fingerprint argument' = 'e2eFingerprint'
    'base URL argument' = 'e2eBaseUrl'
    'username argument' = 'e2eUsername'
    'password argument' = 'e2ePassword'
    'source manifest before' = 'source-before\.json'
    'source manifest after' = 'source-after\.json'
    'Argon2 password hash' = '\^\\\$argon2id\\\$'
    'project-local JDK' = "jdk17\\jdk-17\.0\.19\+10"
    'project-local Gradle home' = "localGradleHome"
    'task-scoped AVD home' = 'ANDROID_AVD_HOME\s*=\s*Join-Path\s+\$taskRoot\s+''avd'''
    'unique AVD name' = 'bookbox_api36_\$runID'
    'installed API 36 image' = 'system-images;android-36;google_apis;x86_64'
    'create AVD without download' = '(?s)avdmanager\.bat.*?''create'', ''avd''.*?''--package'', \$systemImagePackage'
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
if ($runText -match '(?i)\bsdkmanager(?:\.bat)?\b') {
    throw 'E2E must use the already-installed API 36 image without downloads.'
}
if ($runText -match '(?i)\badb(?:\.exe)?\s+kill-server\b') {
    throw 'E2E must never stop an unrelated global adb server.'
}
if ($runText -match '(?i)\bstart-server\b') {
    throw 'E2E must own an explicit nodaemon adb server instead of a daemon handoff.'
}
if ($runText -match '&\s+\$(?:adb|AdbPath)\b') {
    throw 'Every adb invocation must use the bounded monitored helper.'
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

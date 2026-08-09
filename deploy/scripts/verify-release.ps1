[CmdletBinding()]
param(
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')]
    [string] $Version = '1.0.0'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Invoke-Captured {
    param([string] $Tool, [string[]] $Arguments)
    $output = @(& $Tool @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "$([IO.Path]::GetFileName($Tool)) failed:`n$($output -join [Environment]::NewLine)"
    }
    return ($output -join [Environment]::NewLine).Trim()
}

function Get-PEMetadata {
    param([Parameter(Mandatory)][string] $Path)
    $item = Get-Item -LiteralPath $Path
    if ($item.Length -le 0) { throw "$($item.Name) is empty." }
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $reader = $null
    try {
        $reader = [IO.BinaryReader]::new($stream)
        if ($reader.ReadUInt16() -ne 0x5A4D) { throw "$($item.Name) lacks a DOS header." }
        $stream.Position = 0x3c
        $offset = $reader.ReadInt32()
        if ($offset -lt 0x40 -or $offset -gt ($stream.Length - 6)) { throw "$($item.Name) has an invalid PE offset." }
        $stream.Position = $offset
        if ($reader.ReadUInt32() -ne 0x00004550) { throw "$($item.Name) lacks a PE signature." }
        if ($reader.ReadUInt16() -ne 0x8664) { throw "$($item.Name) is not AMD64." }
    } finally {
        if ($null -ne $reader) { $reader.Dispose() } else { $stream.Dispose() }
    }
    [ordered]@{
        file = $item.Name
        bytes = [int64]$item.Length
        sha256 = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
        operatingSystem = 'windows'
        architecture = 'amd64'
        format = 'PE'
    }
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

function Initialize-Java {
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
$metadataPath = Join-Path $artifacts 'APK_METADATA.json'
$discoveryMetadataPath = Join-Path $artifacts 'DISCOVERY_METADATA.json'
$checksumsPath = Join-Path $artifacts 'SHA256SUMS.txt'
Remove-Item -LiteralPath $metadataPath, $discoveryMetadataPath, $checksumsPath `
    -Force -ErrorAction SilentlyContinue
$discoveryPath = Join-Path $artifacts 'bookbox-discovery.exe'
if (-not (Test-Path -LiteralPath $discoveryPath -PathType Leaf)) {
    throw "BookBox discovery executable does not exist: $discoveryPath"
}
$workerPath = Join-Path $artifacts 'bookbox-catalog-worker.exe'
if (-not (Test-Path -LiteralPath $workerPath -PathType Leaf)) {
    throw "BookBox catalog worker executable does not exist: $workerPath"
}
$apks = @(@(
    Join-Path $artifacts "BookBox-v$Version-debug.apk"
    Join-Path $artifacts "BookBox-v$Version-release.apk"
) | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
if ($apks.Count -eq 0) {
    throw "No BookBox v$Version APK exists in $artifacts."
}
Initialize-Java
$sdk = Get-AndroidSdk
$apkanalyzerCandidates = @(@(
    Join-Path $sdk 'cmdline-tools\latest\bin\apkanalyzer.bat'
    Join-Path $sdk 'tools\bin\apkanalyzer.bat'
) | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
if ($apkanalyzerCandidates.Count -eq 0) {
    throw 'apkanalyzer.bat was not found in the Android SDK.'
}
$apkanalyzer = $apkanalyzerCandidates[0]
$apksigner = Get-LatestBuildTool $sdk 'apksigner.bat'
$approvedPermissions = @(
    'android.permission.ACCESS_NETWORK_STATE',
    'android.permission.INTERNET',
    'android.permission.POST_NOTIFICATIONS',
    'android.permission.RECEIVE_BOOT_COMPLETED',
    'android.permission.RUN_USER_INITIATED_JOBS',
    'com.rendref.bookbox.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION'
) | Sort-Object
$metadata = [Collections.Generic.List[object]]::new()
foreach ($apk in $apks) {
    $applicationID = Invoke-Captured $apkanalyzer @('manifest', 'application-id', $apk)
    $versionName = Invoke-Captured $apkanalyzer @('manifest', 'version-name', $apk)
    $versionCode = Invoke-Captured $apkanalyzer @('manifest', 'version-code', $apk)
    $minimum = Invoke-Captured $apkanalyzer @('manifest', 'min-sdk', $apk)
    $target = Invoke-Captured $apkanalyzer @('manifest', 'target-sdk', $apk)
    $debuggableRaw = Invoke-Captured $apkanalyzer @('manifest', 'debuggable', $apk)
    $debuggableMatches = @(
        [regex]::Matches($debuggableRaw, '(?i)\b(true|false)\b') |
        ForEach-Object { $_.Groups[1].Value.ToLowerInvariant() } |
        Sort-Object -Unique
    )
    if ($debuggableMatches.Count -ne 1) {
        throw "Could not parse debuggable state for $([IO.Path]::GetFileName($apk)): $debuggableRaw"
    }
    $debuggable = $debuggableMatches[0]
    $permissionRaw = Invoke-Captured $apkanalyzer @('manifest', 'permissions', $apk)
    $permissions = @(
        $permissionRaw -split '\r?\n' |
        ForEach-Object { $_.Trim() } |
        Where-Object {
            $_ -match '^android\.permission\.[A-Z0-9_]+$' -or
            $_ -ceq 'com.rendref.bookbox.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION'
        }
    ) | Sort-Object -Unique
    $unparsedPermissions = @($permissionRaw -split '\r?\n' | ForEach-Object {
        $_.Trim()
    } | Where-Object {
        $_ -and
        $_ -notmatch '^android\.permission\.[A-Z0-9_]+$' -and
        $_ -cne 'com.rendref.bookbox.DYNAMIC_RECEIVER_NOT_EXPORTED_PERMISSION'
    })
    if ($unparsedPermissions.Count -gt 0) {
        throw "Could not parse permissions for $([IO.Path]::GetFileName($apk)): $($unparsedPermissions -join ', ')"
    }
    if ($applicationID -cne 'com.rendref.bookbox') {
        throw "$([IO.Path]::GetFileName($apk)) application ID is $applicationID."
    }
    if ($versionName -cne $Version -or $versionCode -ne '1') {
        throw "$([IO.Path]::GetFileName($apk)) has version $versionName ($versionCode), expected $Version (1)."
    }
    if ($minimum -ne '36' -or $target -ne '36') {
        throw "$([IO.Path]::GetFileName($apk)) requires min/target SDK 36."
    }
    if (@(Compare-Object $approvedPermissions $permissions).Count -ne 0) {
        throw "$([IO.Path]::GetFileName($apk)) has an unapproved permission set: $($permissions -join ', ')"
    }
    if ($apk -like '*-release.apk' -and $debuggable -notmatch '^(?i:false)$') {
        throw 'Release APK is debuggable.'
    }
    Invoke-Captured $apksigner @(
        'verify', '--verbose', '--print-certs', $apk
    ) | Out-Null
    $item = Get-Item -LiteralPath $apk
    $metadata.Add([ordered]@{
        file = $item.Name
        bytes = [int64] $item.Length
        sha256 = (Get-FileHash -LiteralPath $apk -Algorithm SHA256).Hash.ToLowerInvariant()
        applicationId = $applicationID
        versionName = $versionName
        versionCode = [int] $versionCode
        minSdk = [int] $minimum
        targetSdk = [int] $target
        debuggable = [bool] ($debuggable -match '^(?i:true)$')
        permissions = $permissions
        signatureVerified = $true
    })
}
$metadata = @($metadata | Sort-Object file)
$discoveryMetadata = Get-PEMetadata -Path $discoveryPath
$workerMetadata = Get-PEMetadata -Path $workerPath
$help = Invoke-Captured $workerPath @('help')
if ($help -notmatch '^usage: bookbox-catalog-worker') {
    throw 'Catalog worker help output is missing or unsafe.'
}
$checksumLines = @($metadata | ForEach-Object {
    "$($_.sha256)  $($_.file)"
})
$checksumLines += "$($discoveryMetadata.sha256)  $($discoveryMetadata.file)"
$checksumLines += "$($workerMetadata.sha256)  $($workerMetadata.file)"
$checksumLines = @($checksumLines | Sort-Object)
$publicationID = [Guid]::NewGuid().ToString('N')
$metadataTemporary = "$metadataPath.tmp-$publicationID"
$discoveryMetadataTemporary = "$discoveryMetadataPath.tmp-$publicationID"
$checksumsTemporary = "$checksumsPath.tmp-$publicationID"
try {
    [IO.File]::WriteAllText(
        $metadataTemporary,
        ($metadata | ConvertTo-Json -Depth 6),
        [Text.UTF8Encoding]::new($false)
    )
    [IO.File]::WriteAllText(
        $discoveryMetadataTemporary,
        ($discoveryMetadata | ConvertTo-Json -Depth 3),
        [Text.UTF8Encoding]::new($false)
    )
    [IO.File]::WriteAllLines(
        $checksumsTemporary,
        $checksumLines,
        [Text.UTF8Encoding]::new($false)
    )
    Move-Item -LiteralPath $metadataTemporary -Destination $metadataPath -Force
    Move-Item -LiteralPath $discoveryMetadataTemporary `
        -Destination $discoveryMetadataPath -Force
    Move-Item -LiteralPath $checksumsTemporary -Destination $checksumsPath -Force
} catch {
    Remove-Item -LiteralPath $metadataPath, $discoveryMetadataPath, $checksumsPath `
        -Force -ErrorAction SilentlyContinue
    throw
} finally {
    Remove-Item -LiteralPath $metadataTemporary, $discoveryMetadataTemporary, $checksumsTemporary `
        -Force -ErrorAction SilentlyContinue
}
Write-Host "PASS: verified $($metadata.Count) APK(s), discovery, and catalog worker; wrote release metadata and SHA256SUMS.txt."

[CmdletBinding()]
param(
    [Parameter(Mandatory)][string] $SourceExecutable,
    [Parameter(Mandatory)][string] $ConfigFile,
    [Parameter(Mandatory)][string] $FFProbeExecutable,
    [Parameter(Mandatory)][string[]] $LibraryRoot,
    [string] $InstallDirectory = (Join-Path $env:ProgramFiles 'BookBox\CatalogWorker')
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$serviceName = 'BookBoxCatalogWorker'
$serviceAccount = 'NT SERVICE\BookBoxCatalogWorker'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this installer from an elevated PowerShell window.'
}
if (@($LibraryRoot).Count -lt 1) {
    throw 'At least one LibraryRoot is required.'
}

function Assert-SafeNativeArgument {
    param([Parameter(Mandatory)][string] $Value, [Parameter(Mandatory)][string] $Name)
    if ([string]::IsNullOrWhiteSpace($Value) -or $Value.IndexOf('"') -ge 0 -or
        $Value.IndexOf("`r") -ge 0 -or $Value.IndexOf("`n") -ge 0) {
        throw "$Name contains characters that are unsafe for Windows service arguments."
    }
}

Assert-SafeNativeArgument $SourceExecutable 'SourceExecutable'
Assert-SafeNativeArgument $ConfigFile 'ConfigFile'
Assert-SafeNativeArgument $FFProbeExecutable 'FFProbeExecutable'
Assert-SafeNativeArgument $InstallDirectory 'InstallDirectory'
foreach ($candidateRoot in $LibraryRoot) {
    Assert-SafeNativeArgument $candidateRoot 'LibraryRoot'
}

function Assert-SafePathChain {
    param([Parameter(Mandatory)][string] $Path)
    $current = [IO.Path]::GetFullPath($Path)
    while ($true) {
        if (Test-Path -LiteralPath $current) {
            $item = Get-Item -LiteralPath $current -Force
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Reparse points are not allowed: $current"
            }
        }
        $parent = [IO.Path]::GetDirectoryName($current)
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $current) { break }
        $current = $parent
    }
}

function Resolve-SafeFile {
    param([Parameter(Mandatory)][string] $Path)
    $full = [IO.Path]::GetFullPath($Path)
    Assert-SafePathChain $full
    $item = Get-Item -LiteralPath $full -Force -ErrorAction Stop
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Expected a regular file: $full"
    }
    $full
}

function Resolve-LocalLibraryRoot {
    param([Parameter(Mandatory)][string] $Path)
    $full = [IO.Path]::GetFullPath($Path)
    $pathRoot = [IO.Path]::GetPathRoot($full)
    if ($full.Length -gt $pathRoot.Length) { $full = $full.TrimEnd('\') }
    if ($full -match '^(\\|//)') { throw "UNC library roots are unsupported: $full" }
    Assert-SafePathChain $full
    if (-not (Test-Path -LiteralPath $full -PathType Container)) {
        throw "Library root is not a directory: $full"
    }
    $volumeRoot = [IO.Path]::GetPathRoot($full).TrimEnd('\')
    if ($volumeRoot -notmatch '^[A-Za-z]:$') { throw "Unsupported library root: $full" }
    $disk = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='$volumeRoot'" -ErrorAction Stop
    if (-not $disk -or [int]$disk.DriveType -notin @(2, 3) -or $disk.ProviderName) {
        throw "Library root is not on a local fixed or removable drive: $full"
    }
    $full
}

function Invoke-SC {
    param([Parameter(Mandatory)][string[]] $Arguments)
    $output = @(& sc.exe @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw "sc.exe failed for $($Arguments[0]) with exit code $LASTEXITCODE`: $($output -join ' ')"
    }
}

function Wait-ForServiceAbsent {
    param([Parameter(Mandatory)][string] $Name, [int] $TimeoutSeconds = 30)
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $service = Get-CimInstance Win32_Service -Filter "Name='$Name'" -ErrorAction SilentlyContinue
        if (-not $service) { return }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Timed out waiting for exact rollback service deletion: $Name"
}

function Remove-OwnedService {
    param([Parameter(Mandatory)][string] $Name)
    $service = Get-CimInstance Win32_Service -Filter "Name='$Name'" -ErrorAction SilentlyContinue
    if (-not $service) { return }
    Invoke-SC @('delete', $Name)
    Wait-ForServiceAbsent -Name $Name -TimeoutSeconds 30
}

function Get-ServiceSecurityIdentifier {
    param([Parameter(Mandatory)][string] $Name)
    $sha1 = [Security.Cryptography.SHA1]::Create()
    try {
        $hash = $sha1.ComputeHash([Text.Encoding]::Unicode.GetBytes($Name.ToUpperInvariant()))
    } finally {
        $sha1.Dispose()
    }
    $parts = @(0..4 | ForEach-Object { [BitConverter]::ToUInt32($hash, $_ * 4) })
    [Security.Principal.SecurityIdentifier]::new(
        ('S-1-5-80-{0}-{1}-{2}-{3}-{4}' -f $parts[0], $parts[1], $parts[2], $parts[3], $parts[4])
    )
}

function Get-NearestExistingPath {
    param([Parameter(Mandatory)][string] $Path)
    $current = [IO.Path]::GetFullPath($Path)
    while (-not (Test-Path -LiteralPath $current)) {
        $parent = [IO.Path]::GetDirectoryName($current)
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $current) {
            throw "No existing ACL parent was found for: $Path"
        }
        $current = $parent
    }
    $current
}

function New-RecordedAccessRule {
    param(
        [string] $Path,
        [Security.Principal.SecurityIdentifier] $Sid,
        [Security.AccessControl.FileSystemRights] $Rights,
        [Security.AccessControl.InheritanceFlags] $Inheritance
    )
    $propagation = [Security.AccessControl.PropagationFlags]::None
    $rule = [Security.AccessControl.FileSystemAccessRule]::new(
        $Sid, $Rights, $Inheritance, $propagation,
        [Security.AccessControl.AccessControlType]::Allow
    )
    $acl = Get-Acl -LiteralPath $Path
    $acl.AddAccessRule($rule)
    Set-Acl -LiteralPath $Path -AclObject $acl
    [pscustomobject]@{
        Path = [IO.Path]::GetFullPath($Path)
        Sid = $Sid.Value
        Rights = [int]$Rights
        InheritanceFlags = [int]$Inheritance
        PropagationFlags = [int]$propagation
        AccessControlType = [int][Security.AccessControl.AccessControlType]::Allow
    }
}

$source = Resolve-SafeFile $SourceExecutable
$config = Resolve-SafeFile $ConfigFile
$ffprobe = Resolve-SafeFile $FFProbeExecutable
$installRoot = [IO.Path]::GetFullPath($InstallDirectory).TrimEnd('\')
if ($installRoot -eq [IO.Path]::GetPathRoot($installRoot).TrimEnd('\')) {
    throw 'InstallDirectory must not be a drive root.'
}
Assert-SafePathChain ([IO.Path]::GetDirectoryName($installRoot))
$dataRoot = [IO.Path]::GetFullPath((Join-Path $env:ProgramData 'BookBox')).TrimEnd('\')
if (-not $config.StartsWith($dataRoot + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw "ConfigFile must be under $dataRoot."
}
$roots = @($LibraryRoot | ForEach-Object { Resolve-LocalLibraryRoot $_ } | Sort-Object -Unique)
if ($roots.Count -ne $LibraryRoot.Count) { throw 'LibraryRoot entries must be unique.' }

$manifestPath = Join-Path $installRoot 'bookbox-catalog-worker-install.json'
$installedExe = Join-Path $installRoot 'bookbox-catalog-worker.exe'
$binPath = ('"{0}" service --config "{1}"' -f $installedExe, $config)
$existingManifest = $null
if (Test-Path -LiteralPath $installRoot) {
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        if (@(Get-ChildItem -LiteralPath $installRoot -Force).Count -gt 0) {
            throw "Refusing to overwrite an unowned install directory: $installRoot"
        }
    } else {
        $existingManifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
        if ([int]$existingManifest.SchemaVersion -ne 1 -or
            [string]$existingManifest.Service.Name -cne $serviceName -or
            [string]$existingManifest.Service.Account -cne $serviceAccount) {
            throw 'Refusing to replace an installation with a foreign manifest.'
        }
        foreach ($file in @($existingManifest.ManagedFiles)) {
            $path = [IO.Path]::GetFullPath((Join-Path $installRoot ([string]$file.Path)))
            if (-not $path.StartsWith($installRoot + '\', [StringComparison]::OrdinalIgnoreCase) -or
                -not (Test-Path -LiteralPath $path -PathType Leaf) -or
                (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -cne [string]$file.Sha256) {
                throw "Refusing to replace a modified managed file: $path"
            }
        }
        throw 'Remove the existing owned catalog worker service before reinstalling.'
    }
}
$existingService = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction SilentlyContinue
if ($existingService) {
    throw 'Refusing to replace an existing Windows service; remove the owned installation first.'
}
if ([Diagnostics.EventLog]::SourceExists($serviceName)) {
    throw 'Refusing to replace an existing Event Log source.'
}

$logs = Join-Path $dataRoot 'logs'
$backups = Join-Path $dataRoot 'backups'
$secrets = Join-Path $dataRoot 'secrets'
$sid = Get-ServiceSecurityIdentifier $serviceName
$preflightAclTargets = @($installedExe, $config, $ffprobe, $logs, $backups, $secrets) + @($roots)
foreach ($target in $preflightAclTargets) {
    $targetExists = Test-Path -LiteralPath $target
    $aclTarget = Get-NearestExistingPath $target
    $existingRules = @((Get-Acl -LiteralPath $aclTarget).Access | Where-Object {
        $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -ceq $sid.Value -and
        ($targetExists -or [int]$_.InheritanceFlags -ne
            [int][Security.AccessControl.InheritanceFlags]::None)
    })
    if ($existingRules.Count -ne 0) {
        throw "Refusing pre-existing effective service ACL access for $target from $aclTarget"
    }
}

# Complete ownership preflight before Copy-Item, sc.exe create, New-EventLog, or AddAccessRule.
. (Join-Path $PSScriptRoot 'install-transaction.ps1')
$transaction = [Guid]::NewGuid().ToString('N')
$parent = [IO.Path]::GetDirectoryName($installRoot)
$stage = Join-Path $parent ".BookBox-worker-stage-$transaction"
$backup = Join-Path $parent ".BookBox-worker-backup-$transaction"

Invoke-BookBoxInstallTransaction -InstallDirectory $installRoot -StageDirectory $stage `
    -BackupDirectory $backup -PrepareStage {
        $null = New-Item -ItemType Directory -Path $stage -Force
        Copy-Item -LiteralPath $source -Destination (Join-Path $stage 'bookbox-catalog-worker.exe')
        [IO.File]::WriteAllText(
            (Join-Path $stage 'bookbox-catalog-worker-install.json'),
            '{}', [Text.UTF8Encoding]::new($false)
        )
    } -CreateExternalResources {
        param($rollbackActions)
        foreach ($directory in @($dataRoot, $logs, $backups, $secrets)) {
            if (-not (Test-Path -LiteralPath $directory)) {
                $null = New-Item -ItemType Directory -Path $directory
                $rollbackActions.Add({
                    if ((Test-Path -LiteralPath $directory -PathType Container) -and
                        @(Get-ChildItem -LiteralPath $directory -Force).Count -eq 0) {
                        Remove-Item -LiteralPath $directory -Force
                    }
                }.GetNewClosure())
            }
        }
        Invoke-SC @(
            'create', $serviceName,
            'binPath=', $binPath,
            'start=', 'delayed-auto',
            'obj=', $serviceAccount,
            'password=', '""',
            'DisplayName=', 'BookBox Catalog Worker'
        )
        $rollbackActions.Add({ Remove-OwnedService -Name $serviceName })
        Invoke-SC @('sidtype', $serviceName, 'unrestricted')
        $authoritativeSid = ([Security.Principal.NTAccount]::new($serviceAccount)).Translate(
            [Security.Principal.SecurityIdentifier]
        )
        if ($authoritativeSid.Value -cne $sid.Value) {
            throw 'The authoritative service SID does not match the deterministic preflight SID.'
        }
        Invoke-SC @(
            'failure', $serviceName,
            'reset=', '86400',
            'actions=', 'restart/5000/restart/30000/restart/60000'
        )
        Invoke-SC @('failureflag', $serviceName, '1')
        if (-not [Diagnostics.EventLog]::SourceExists($serviceName)) {
            New-EventLog -LogName Application -Source BookBoxCatalogWorker
            $rollbackActions.Add({ Remove-EventLog -Source $serviceName })
        }
        $serviceRegistryPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName"
        $serviceRegistry = Get-ItemProperty -LiteralPath $serviceRegistryPath
        $failureActionsBase64 = [Convert]::ToBase64String([byte[]]$serviceRegistry.FailureActions)
        if ([int]$serviceRegistry.Start -ne 2 -or
            [int]$serviceRegistry.DelayedAutoStart -ne 1 -or
            [int]$serviceRegistry.ServiceSidType -ne 1 -or
            [int]$serviceRegistry.FailureActionsOnNonCrashFailures -ne 1 -or
            [string]::IsNullOrWhiteSpace($failureActionsBase64)) {
            throw 'Windows did not apply the required delayed-start, SID, and recovery policy.'
        }
        $eventRegistryPath = "HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\$serviceName"
        $records = [Collections.Generic.List[object]]::new()
        $none = [Security.AccessControl.InheritanceFlags]::None
        $inherit = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor
            [Security.AccessControl.InheritanceFlags]::ObjectInherit
        $definitions = @(
            @{ Path = $installedExe; Rights = [Security.AccessControl.FileSystemRights]::ReadAndExecute; Inheritance = $none },
            @{ Path = $ffprobe; Rights = [Security.AccessControl.FileSystemRights]::ReadAndExecute; Inheritance = $none },
            @{ Path = $config; Rights = [Security.AccessControl.FileSystemRights]::Read; Inheritance = $none },
            @{ Path = $secrets; Rights = [Security.AccessControl.FileSystemRights]::ReadAndExecute; Inheritance = $inherit },
            @{ Path = $logs; Rights = [Security.AccessControl.FileSystemRights]::Modify; Inheritance = $inherit },
            @{ Path = $backups; Rights = [Security.AccessControl.FileSystemRights]::Modify; Inheritance = $inherit }
        ) + @($roots | ForEach-Object {
            @{ Path = $_; Rights = [Security.AccessControl.FileSystemRights]::Modify; Inheritance = $inherit }
        })
        foreach ($definition in $definitions) {
            $record = New-RecordedAccessRule -Path $definition.Path -Sid $sid `
                -Rights $definition.Rights -Inheritance $definition.Inheritance
            $records.Add($record)
            $rollbackActions.Add({
                $target = Get-Acl -LiteralPath $record.Path
                $rule = [Security.AccessControl.FileSystemAccessRule]::new(
                    [Security.Principal.SecurityIdentifier]::new($record.Sid),
                    [Security.AccessControl.FileSystemRights]$record.Rights,
                    [Security.AccessControl.InheritanceFlags]$record.InheritanceFlags,
                    [Security.AccessControl.PropagationFlags]$record.PropagationFlags,
                    [Security.AccessControl.AccessControlType]$record.AccessControlType
                )
                $target.RemoveAccessRuleSpecific($rule)
                Set-Acl -LiteralPath $record.Path -AclObject $target
            }.GetNewClosure())
        }
        $manifest = [ordered]@{
            SchemaVersion = 1
            Owner = 'BookBox'
            InstallDirectory = $installRoot
            DataRoot = $dataRoot
            Service = [ordered]@{
                Name = $serviceName; Account = $serviceAccount; Sid = $sid.Value
                BinPath = $binPath; ConfigPath = $config
                Registry = [ordered]@{
                    Start = [int]$serviceRegistry.Start
                    DelayedAutoStart = [int]$serviceRegistry.DelayedAutoStart
                    ServiceSidType = [int]$serviceRegistry.ServiceSidType
                    FailureActionsOnNonCrashFailures = [int]$serviceRegistry.FailureActionsOnNonCrashFailures
                    FailureActionsBase64 = $failureActionsBase64
                }
            }
            ManagedFiles = @([ordered]@{ Path = 'bookbox-catalog-worker.exe'; Sha256 = (Get-FileHash $installedExe -Algorithm SHA256).Hash })
            ExternalInputs = @(
                [ordered]@{ Role = 'config'; Path = $config; Sha256 = (Get-FileHash $config -Algorithm SHA256).Hash },
                [ordered]@{ Role = 'ffprobe'; Path = $ffprobe; Sha256 = (Get-FileHash $ffprobe -Algorithm SHA256).Hash }
            )
            LibraryRoots = $roots
            ACLRules = @($records)
            EventLog = [ordered]@{ Name = 'Application'; Source = $serviceName; RegistryPath = $eventRegistryPath }
        }
        [IO.File]::WriteAllText($manifestPath, ($manifest | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    }

Write-Output "Installed $serviceName at $installRoot."

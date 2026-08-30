[CmdletBinding()]
param(
    [string] $InstallDirectory = (Join-Path $env:ProgramFiles 'BookBox\CatalogWorker'),
    [switch] $RemoveData
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$serviceName = 'BookBoxCatalogWorker'
$serviceAccount = 'NT SERVICE\BookBoxCatalogWorker'
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this removal script from an elevated PowerShell window.'
}

function Assert-SafeRemovalDirectory {
    param([string] $Path, [string] $ExpectedParent, [string] $ExpectedLeaf)
    $full = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    if (-not [string]::Equals([IO.Path]::GetDirectoryName($full), $ExpectedParent,
            [StringComparison]::OrdinalIgnoreCase) -or
        [IO.Path]::GetFileName($full) -cne $ExpectedLeaf) {
        throw "Refusing unsafe data removal target: $full"
    }
    $item = Get-Item -LiteralPath $full -Force -ErrorAction SilentlyContinue
    if ($item -and ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing reparse-point data removal target: $full"
    }
    $full
}

function Assert-SafeExistingPathChain {
    param([Parameter(Mandatory)][string] $Path)
    $current = [IO.Path]::GetFullPath($Path)
    while ($true) {
        $item = Get-Item -LiteralPath $current -Force -ErrorAction Stop
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Refusing reparse-point ownership path: $current"
        }
        $parent = [IO.Path]::GetDirectoryName($current)
        if ([string]::IsNullOrWhiteSpace($parent) -or $parent -eq $current) { break }
        $current = $parent
    }
}

function Assert-SafeRemovalTree {
    param([Parameter(Mandatory)][string] $Path)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    Assert-SafeExistingPathChain $Path
    $pending = [Collections.Generic.Queue[string]]::new()
    $pending.Enqueue([IO.Path]::GetFullPath($Path))
    while ($pending.Count -gt 0) {
        $directory = $pending.Dequeue()
        foreach ($item in @(Get-ChildItem -LiteralPath $directory -Force)) {
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "Refusing recursive removal containing a reparse point: $($item.FullName)"
            }
            if ($item.PSIsContainer) { $pending.Enqueue($item.FullName) }
        }
    }
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
    throw "Timed out waiting for exact service deletion: $Name"
}

function Restore-OwnedService {
    param([Parameter(Mandatory)][object] $Manifest)
    Invoke-SC @(
        'create', $serviceName,
        'binPath=', [string]$Manifest.Service.BinPath,
        'start=', 'delayed-auto',
        'obj=', $serviceAccount,
        'DisplayName=', 'BookBox Catalog Worker'
    )
    Invoke-SC @('sidtype', $serviceName, 'unrestricted')
    Invoke-SC @(
        'failure', $serviceName,
        'reset=', '86400',
        'actions=', 'restart/5000/restart/30000/restart/60000'
    )
    Invoke-SC @('failureflag', $serviceName, '1')
    $registry = Get-ItemProperty -LiteralPath "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName"
    if ([Convert]::ToBase64String([byte[]]$registry.FailureActions) -cne
        [string]$Manifest.Service.Registry.FailureActionsBase64) {
        throw 'Restored service recovery configuration does not match its manifest.'
    }
}

function Restore-FileSnapshots {
    param([Parameter(Mandatory)][object[]] $Snapshots)
    foreach ($snapshot in $Snapshots) {
        [IO.File]::WriteAllBytes([string]$snapshot.Path, [byte[]]$snapshot.Bytes)
        $security = [Security.AccessControl.FileSecurity]::new()
        $security.SetSecurityDescriptorBinaryForm([byte[]]$snapshot.SecurityDescriptor)
        Set-Acl -LiteralPath ([string]$snapshot.Path) -AclObject $security
        [IO.File]::SetCreationTimeUtc([string]$snapshot.Path, [DateTime]$snapshot.CreationTimeUtc)
        [IO.File]::SetLastWriteTimeUtc([string]$snapshot.Path, [DateTime]$snapshot.LastWriteTimeUtc)
        [IO.File]::SetAttributes([string]$snapshot.Path, [IO.FileAttributes][int]$snapshot.Attributes)
    }
}

function New-ManifestAccessRule {
    param([Parameter(Mandatory)][object] $Record)
    [Security.AccessControl.FileSystemAccessRule]::new(
        [Security.Principal.SecurityIdentifier]::new([string]$Record.Sid),
        [Security.AccessControl.FileSystemRights][int]$Record.Rights,
        [Security.AccessControl.InheritanceFlags][int]$Record.InheritanceFlags,
        [Security.AccessControl.PropagationFlags][int]$Record.PropagationFlags,
        [Security.AccessControl.AccessControlType][int]$Record.AccessControlType
    )
}

$installRoot = [IO.Path]::GetFullPath($InstallDirectory).TrimEnd('\')
$manifestPath = Join-Path $installRoot 'bookbox-catalog-worker-install.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "Refusing to remove an installation without its manifest: $manifestPath"
}
Assert-SafeExistingPathChain $installRoot
Assert-SafeExistingPathChain $manifestPath
$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
if ([int]$manifest.SchemaVersion -ne 1 -or [string]$manifest.Owner -cne 'BookBox' -or
    [string]$manifest.Service.Name -cne $serviceName -or
    [string]$manifest.Service.Account -cne $serviceAccount -or
    -not [string]::Equals([IO.Path]::GetFullPath([string]$manifest.InstallDirectory), $installRoot,
        [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Refusing to remove a foreign catalog worker installation.'
}

foreach ($file in @($manifest.ManagedFiles)) {
    $path = [IO.Path]::GetFullPath((Join-Path $installRoot ([string]$file.Path)))
    if (-not $path.StartsWith($installRoot + '\', [StringComparison]::OrdinalIgnoreCase) -or
        -not (Test-Path -LiteralPath $path -PathType Leaf) -or
        (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -cne [string]$file.Sha256) {
        throw "Refusing to remove a missing, external, or modified managed file: $path"
    }
    Assert-SafeExistingPathChain $path
}
$managedFilePaths = @(@($manifest.ManagedFiles) | ForEach-Object {
    [IO.Path]::GetFullPath((Join-Path $installRoot ([string]$_.Path)))
})
$ownedFilePaths = @($managedFilePaths) + @($manifestPath)
if (@($ownedFilePaths | Sort-Object -Unique).Count -ne $ownedFilePaths.Count) {
    throw 'Refusing removal with duplicate managed-file paths.'
}
$fileSnapshots = @($ownedFilePaths | ForEach-Object {
    $item = Get-Item -LiteralPath $_ -Force -ErrorAction Stop
    $acl = Get-Acl -LiteralPath $_
    [pscustomobject]@{
        Path = [string]$item.FullName
        Bytes = [IO.File]::ReadAllBytes($item.FullName)
        SecurityDescriptor = $acl.GetSecurityDescriptorBinaryForm()
        CreationTimeUtc = $item.CreationTimeUtc
        LastWriteTimeUtc = $item.LastWriteTimeUtc
        Attributes = [int]$item.Attributes
    }
})
foreach ($input in @($manifest.ExternalInputs)) {
    $path = [IO.Path]::GetFullPath([string]$input.Path)
    if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or
        (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -cne [string]$input.Sha256) {
        throw "Refusing removal because an external input changed: $path"
    }
    Assert-SafeExistingPathChain $path
}

$serviceRegistryPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$serviceName"
$service = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction SilentlyContinue
$serviceInitiallyPresent = $null -ne $service
$serviceRegistryPresent = Test-Path -LiteralPath $serviceRegistryPath
$deletionCompleted = -not $serviceInitiallyPresent -and -not (Test-Path -LiteralPath $serviceRegistryPath)
$normalService = $false
$pendingDelete = $false
if ($serviceInitiallyPresent) {
    if (-not $serviceRegistryPresent -or
        -not [string]::Equals([string]$service.StartName, $serviceAccount, [StringComparison]::OrdinalIgnoreCase) -or
        [string]$service.PathName -cne [string]$manifest.Service.BinPath) {
        throw 'Refusing to remove a differently configured service.'
    }
    $serviceRegistry = Get-ItemProperty -LiteralPath $serviceRegistryPath -ErrorAction Stop
    $deleteFlagProperty = $serviceRegistry.PSObject.Properties['DeleteFlag']
    $deleteFlag = if ($deleteFlagProperty) { [int]$deleteFlagProperty.Value } else { 0 }
    $normalService = [string]$service.StartMode -ceq 'Auto' -and
        [int]$serviceRegistry.Start -eq [int]$manifest.Service.Registry.Start -and $deleteFlag -eq 0
    $pendingDelete = [string]$service.StartMode -ceq 'Disabled' -and
        [int]$manifest.Service.Registry.Start -eq 2 -and [int]$serviceRegistry.Start -eq 4 -and
        $deleteFlag -eq 1
    if ((-not $normalService -and -not $pendingDelete) -or
        [int]$serviceRegistry.DelayedAutoStart -ne [int]$manifest.Service.Registry.DelayedAutoStart -or
        [int]$serviceRegistry.ServiceSidType -ne [int]$manifest.Service.Registry.ServiceSidType -or
        [int]$serviceRegistry.FailureActionsOnNonCrashFailures -ne
            [int]$manifest.Service.Registry.FailureActionsOnNonCrashFailures -or
        [Convert]::ToBase64String([byte[]]$serviceRegistry.FailureActions) -cne
            [string]$manifest.Service.Registry.FailureActionsBase64) {
        throw 'Refusing to remove a service whose delayed-start, SID, or recovery policy changed.'
    }
} elseif (-not $deletionCompleted -or $serviceRegistryPresent) {
    throw 'Refusing an inconsistent missing-service installation.'
}
$eventRegistryPath = "HKLM:\SYSTEM\CurrentControlSet\Services\EventLog\Application\$serviceName"
if (-not [Diagnostics.EventLog]::SourceExists($serviceName) -or
    [string]$manifest.EventLog.Name -cne 'Application' -or
    [string]$manifest.EventLog.Source -cne $serviceName -or
    [string]$manifest.EventLog.RegistryPath -cne $eventRegistryPath -or
    -not (Test-Path -LiteralPath $eventRegistryPath)) {
    throw 'Refusing to remove a missing or foreign Event Log source.'
}

$verifiedRules = [Collections.Generic.List[object]]::new()
$rulesToRepair = [Collections.Generic.List[object]]::new()
foreach ($record in @($manifest.ACLRules)) {
    $path = [IO.Path]::GetFullPath([string]$record.Path)
    if (-not (Test-Path -LiteralPath $path)) {
        throw "Refusing removal because an ACL target is missing: $path"
    }
    Assert-SafeExistingPathChain $path
    $acl = Get-Acl -LiteralPath $path
    $expectedRule = New-ManifestAccessRule -Record $record
    $sidRules = @($acl.Access | Where-Object {
        -not $_.IsInherited -and
        $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -ceq [string]$record.Sid
    })
    $matches = @($acl.Access | Where-Object {
        -not $_.IsInherited -and
        $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -ceq [string]$record.Sid -and
        [int]$_.FileSystemRights -eq [int]$expectedRule.FileSystemRights -and
        [int]$_.InheritanceFlags -eq [int]$expectedRule.InheritanceFlags -and
        [int]$_.PropagationFlags -eq [int]$expectedRule.PropagationFlags -and
        [int]$_.AccessControlType -eq [int]$expectedRule.AccessControlType
    })
    if ($matches.Count -ne 1) {
        $repairableManagedRule = $pendingDelete -and $matches.Count -eq 0 -and
            $sidRules.Count -eq 0 -and @($managedFilePaths | Where-Object {
                [string]::Equals($_, $path, [StringComparison]::OrdinalIgnoreCase)
            }).Count -eq 1
        if (-not $repairableManagedRule) {
            throw "Refusing removal because the exact owned ACL rule is absent or ambiguous: $path"
        }
        $snapshot = @($fileSnapshots | Where-Object {
            [string]::Equals([string]$_.Path, $path, [StringComparison]::OrdinalIgnoreCase)
        })
        if ($snapshot.Count -ne 1) {
            throw "Refusing ambiguous managed-file ACL recovery: $path"
        }
        $repairSecurity = [Security.AccessControl.FileSecurity]::new()
        $repairSecurity.SetSecurityDescriptorBinaryForm([byte[]]$snapshot[0].SecurityDescriptor)
        $repairSecurity.AddAccessRule($expectedRule)
        $snapshot[0].SecurityDescriptor = $repairSecurity.GetSecurityDescriptorBinaryForm()
        $rulesToRepair.Add([pscustomobject]@{ Path = $path; Rule = $expectedRule })
    }
    $verifiedRules.Add([pscustomobject]@{ Path = $path; Rule = $expectedRule })
}

$dataRemovalTargets = @()
$dataConfigPath = $null
if ($RemoveData) {
    $dataRoot = [IO.Path]::GetFullPath([string]$manifest.DataRoot).TrimEnd('\')
    $expectedDataRoot = [IO.Path]::GetFullPath((Join-Path $env:ProgramData 'BookBox')).TrimEnd('\')
    if (-not [string]::Equals($dataRoot, $expectedDataRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing data removal outside the owned ProgramData root.'
    }
    Assert-SafeExistingPathChain $dataRoot
    $dataRemovalTargets = @(@('logs', 'backups', 'secrets') | ForEach-Object {
        Assert-SafeRemovalDirectory -Path (Join-Path $dataRoot $_) `
            -ExpectedParent $dataRoot -ExpectedLeaf $_
    })
    foreach ($target in $dataRemovalTargets) { Assert-SafeRemovalTree $target }
    $configInputs = @($manifest.ExternalInputs | Where-Object { [string]$_.Role -ceq 'config' })
    if ($configInputs.Count -ne 1) { throw 'Refusing ambiguous config data removal.' }
    $dataConfigPath = [IO.Path]::GetFullPath([string]$configInputs[0].Path)
    if (-not $dataConfigPath.StartsWith($dataRoot + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Refusing config removal outside the owned data root.'
    }
    Assert-SafeExistingPathChain $dataConfigPath
}

# Complete removal preflight before Stop-Service -Name $serviceName or any mutation.
$serviceObject = $null
$wasRunning = $false
if ($serviceInitiallyPresent) {
    $serviceObject = Get-Service -Name $serviceName -ErrorAction Stop
    $wasRunning = $serviceObject.Status -ne [ServiceProcess.ServiceControllerStatus]::Stopped
}
$removedRules = [Collections.Generic.List[object]]::new()
$eventRemoved = $false
$deleteRequested = $pendingDelete -or $deletionCompleted
$recoveryPolicyDisabled = $false
$serviceObjectDisposed = $false
try {
    foreach ($repair in $rulesToRepair) {
        $acl = Get-Acl -LiteralPath $repair.Path
        $acl.AddAccessRule($repair.Rule)
        Set-Acl -LiteralPath $repair.Path -AclObject $acl
    }
    if ($serviceInitiallyPresent) {
        if ($pendingDelete) {
            Set-ItemProperty -LiteralPath $serviceRegistryPath -Name FailureActionsOnNonCrashFailures -Value 0
        } else {
            Invoke-SC @('failureflag', $serviceName, '0')
        }
        $recoveryPolicyDisabled = $true
    }
    if ($pendingDelete) {
        if ($wasRunning) {
            Stop-Service -Name $serviceName -ErrorAction Stop
        }
        $serviceObject.Dispose()
        $serviceObjectDisposed = $true
        Wait-ForServiceAbsent -Name $serviceName -TimeoutSeconds 30
    } elseif ($wasRunning) {
        Stop-Service -Name $serviceName -ErrorAction Stop
        $serviceObject.WaitForStatus(
            [ServiceProcess.ServiceControllerStatus]::Stopped,
            [TimeSpan]::FromSeconds(30)
        )
    }
    foreach ($record in $verifiedRules) {
        $path = [IO.Path]::GetFullPath([string]$record.Path)
        $acl = Get-Acl -LiteralPath $path
        $acl.RemoveAccessRuleSpecific($record.Rule)
        Set-Acl -LiteralPath $path -AclObject $acl
        $removedRules.Add([pscustomobject]@{ Path = $path; Rule = $record.Rule })
    }
    Remove-EventLog -Source $serviceName
    $eventRemoved = $true
    if ($serviceObject -and -not $serviceObjectDisposed) {
        $serviceObject.Dispose()
        $serviceObjectDisposed = $true
    }
    if ($normalService) {
        Invoke-SC @('delete', $serviceName)
        $deleteRequested = $true
    }
    Wait-ForServiceAbsent -Name $serviceName -TimeoutSeconds 30
    foreach ($path in $ownedFilePaths) {
        Remove-Item -LiteralPath $path -Force
    }
} catch {
    $failure = $_
    $rollbackErrors = [Collections.Generic.List[string]]::new()
    if ($serviceObject -and -not $serviceObjectDisposed) {
        $serviceObject.Dispose()
        $serviceObjectDisposed = $true
    }
    try {
        Restore-FileSnapshots -Snapshots $fileSnapshots
    } catch {
        $rollbackErrors.Add("file restore: $($_.Exception.Message)")
    }
    try {
        $currentService = Get-CimInstance Win32_Service -Filter "Name='$serviceName'" -ErrorAction SilentlyContinue
        if ($serviceInitiallyPresent -and -not $currentService) {
            Restore-OwnedService -Manifest $manifest
        } elseif ($serviceInitiallyPresent -and $deleteRequested) {
            throw 'The service still exists after deletion was requested and cannot be recreated safely.'
        }
    } catch {
        $rollbackErrors.Add("service restore: $($_.Exception.Message)")
    }
    try {
        if ($eventRemoved -and -not [Diagnostics.EventLog]::SourceExists($serviceName)) {
            New-EventLog -LogName Application -Source BookBoxCatalogWorker
        }
    } catch {
        $rollbackErrors.Add("Event Log restore: $($_.Exception.Message)")
    }
    foreach ($removed in $removedRules) {
        $snapshotOwnsAcl = @($fileSnapshots | Where-Object {
            [string]::Equals([string]$_.Path, [string]$removed.Path,
                [StringComparison]::OrdinalIgnoreCase)
        }).Count -ne 0
        if ($snapshotOwnsAcl) { continue }
        try {
            $acl = Get-Acl -LiteralPath $removed.Path
            $acl.AddAccessRule($removed.Rule)
            Set-Acl -LiteralPath $removed.Path -AclObject $acl
        } catch {
            $rollbackErrors.Add("ACL restore for $($removed.Path): $($_.Exception.Message)")
        }
    }
    if ($serviceInitiallyPresent -and $recoveryPolicyDisabled) {
        try {
            Invoke-SC @('failureflag', $serviceName, '1')
        } catch {
            $rollbackErrors.Add("recovery-policy restore: $($_.Exception.Message)")
        }
    }
    if ($wasRunning) {
        try {
            Start-Service -Name $serviceName -ErrorAction Stop
        } catch {
            $rollbackErrors.Add("running-state restore: $($_.Exception.Message)")
        }
    }
    if ($rollbackErrors.Count -ne 0) {
        throw "Removal failed: $($failure.Exception.Message). Rollback failed: $($rollbackErrors -join '; ')"
    }
    throw $failure
}

if ($RemoveData) {
    foreach ($target in $dataRemovalTargets) {
        if (Test-Path -LiteralPath $target) {
            Remove-Item -LiteralPath $target -Recurse -Force
        }
    }
    Remove-Item -LiteralPath $dataConfigPath -Force -ErrorAction SilentlyContinue
    if ((Test-Path -LiteralPath $dataRoot -PathType Container) -and
        @(Get-ChildItem -LiteralPath $dataRoot -Force).Count -eq 0) {
        Remove-Item -LiteralPath $dataRoot -Force
    }
}

if ((Test-Path -LiteralPath $installRoot -PathType Container) -and
    @(Get-ChildItem -LiteralPath $installRoot -Force).Count -eq 0) {
    Remove-Item -LiteralPath $installRoot -Force
}
Write-Output "Removed the owned $serviceName service; external ffprobe and data were preserved unless -RemoveData was supplied."

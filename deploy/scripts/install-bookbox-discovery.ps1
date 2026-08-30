[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $SourceExecutable,

    [string] $InstallDirectory = (Join-Path $env:LOCALAPPDATA 'BookBox'),
    [string] $ComposeFile = (Join-Path $PSScriptRoot '..\compose.yml'),
    [string] $HybridComposeFile = (Join-Path $PSScriptRoot '..\compose.hybrid.yml'),
    [ValidateSet('Container', 'Hybrid')]
    [string] $DeploymentMode = 'Container',
    [string] $TaskName = 'BookBox Discovery',
    [string] $TaskPath = '\BookBox\',
    [string] $Address = $env:BOOKBOX_BIND_ADDRESS,
    [int] $Port = $(if ($env:BOOKBOX_HOST_PORT) { [int]$env:BOOKBOX_HOST_PORT } else { 8787 })
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principalContext = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principalContext.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this installer from an elevated PowerShell window.'
}
$parsedAddress = $null
if (-not [Net.IPAddress]::TryParse($Address, [ref]$parsedAddress)) {
    throw 'BOOKBOX_BIND_ADDRESS must be a valid private IPv4 address.'
}
$bytes = $parsedAddress.GetAddressBytes()
$private = $bytes.Count -eq 4 -and (
    $bytes[0] -eq 10 -or
    ($bytes[0] -eq 172 -and $bytes[1] -ge 16 -and $bytes[1] -le 31) -or
    ($bytes[0] -eq 192 -and $bytes[1] -eq 168)
)
if (-not $private) {
    throw 'BOOKBOX_BIND_ADDRESS must be an RFC1918 private IPv4 address.'
}
$assigned = Get-NetIPAddress -AddressFamily IPv4 -IPAddress $Address -ErrorAction SilentlyContinue |
    Where-Object { $_.AddressState -eq 'Preferred' } |
    Select-Object -First 1
if (-not $assigned) {
    throw "BOOKBOX_BIND_ADDRESS is not assigned to this Windows machine: $Address"
}
if ((Get-NetConnectionProfile -InterfaceIndex $assigned.InterfaceIndex).NetworkCategory -ne 'Private') {
    throw 'The selected BookBox address must use a Windows Private network profile.'
}

$source = (Resolve-Path -LiteralPath $SourceExecutable -ErrorAction Stop).Path
$compose = (Resolve-Path -LiteralPath $ComposeFile -ErrorAction Stop).Path
$hybridCompose = [IO.Path]::GetFullPath($HybridComposeFile)
if ($DeploymentMode -eq 'Hybrid') {
    $hybridCompose = (Resolve-Path -LiteralPath $hybridCompose -ErrorAction Stop).Path
}
$installRoot = [IO.Path]::GetFullPath($InstallDirectory)
$binDirectory = Join-Path $installRoot 'bin'
$scriptsDirectory = Join-Path $installRoot 'scripts'
$stateDirectory = Join-Path $installRoot 'state'
$destination = Join-Path $binDirectory 'bookbox-discovery.exe'
$installedStartScript = Join-Path $scriptsDirectory 'start-bookbox.ps1'
$manifestPath = Join-Path $installRoot 'bookbox-install.json'
$managedScripts = @(
    'start-bookbox.ps1',
    'startup-environment.ps1',
    'compose-lifecycle.ps1',
    'stop-bookbox.ps1',
    'discovery-process.ps1',
    'discovery-supervisor.ps1',
    'remove-bookbox-discovery.ps1'
)
foreach ($actionValue in @(
        $installedStartScript,
        $destination,
        $Address,
        $stateDirectory,
        $compose,
        $DeploymentMode,
        $hybridCompose
    )) {
    if ([string]$actionValue -match '["\r\n]') {
        throw 'Scheduled-task arguments must not contain quotes or line breaks.'
    }
}
$actionArguments = (
    '-NoProfile -ExecutionPolicy Bypass -File "{0}" ' +
    '-DiscoveryExecutable "{1}" -Address "{2}" -Port {3} ' +
    '-StateDirectory "{4}" -ComposeFile "{5}" ' +
    '-DeploymentMode "{6}" -HybridComposeFile "{7}"'
) -f $installedStartScript, $destination, $Address, $Port, $stateDirectory, $compose,
    $DeploymentMode, $hybridCompose

function Assert-OwnedFirewallRule {
    param(
        [object] $Rule,
        [string] $DisplayName,
        [string] $Protocol,
        [int] $LocalPort
    )
    if (-not $Rule) { return }
    $portFilter = $Rule | Get-NetFirewallPortFilter
    $addressFilter = $Rule | Get-NetFirewallAddressFilter
    if (
        $Rule.Group -ne 'BookBox' -or
        $Rule.Direction -ne 'Inbound' -or
        $Rule.Action -ne 'Allow' -or
        $Rule.Enabled -ne 'True' -or
        $Rule.Profile -ne 'Private' -or
        [string]$portFilter.Protocol -cne $Protocol -or
        [string]$portFilter.LocalPort -cne [string]$LocalPort -or
        [string]$addressFilter.LocalAddress -cne $Address -or
        [string]$addressFilter.RemoteAddress -cne 'LocalSubnet'
    ) {
        throw "Refusing to replace an unrelated firewall rule: $DisplayName"
    }
}

# Complete ownership preflight before Copy-Item, firewall mutation, or task mutation.
$existingTask = Get-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath -ErrorAction SilentlyContinue
if ($existingTask) {
    $existingActions = @($existingTask.Actions)
    $existingTriggers = @($existingTask.Triggers)
    if (
        $existingTask.Description -cne 'BookBox managed discovery version 2' -or
        $existingActions.Count -ne 1 -or
        [string]$existingActions[0].Execute -notmatch '(?i)(^|\\)powershell\.exe$' -or
        [string]$existingActions[0].Arguments -cne $actionArguments -or
        [string]$existingTask.Principal.UserId -cne $identity.Name -or
        [string]$existingTask.Principal.RunLevel -cne 'Limited' -or
        [string]$existingTask.Principal.LogonType -notmatch '^Interactive' -or
        [string]$existingTask.Settings.MultipleInstances -cne 'IgnoreNew' -or
        $existingTriggers.Count -ne 1 -or
        $existingTriggers[0].CimClass.CimClassName -cne 'MSFT_TaskLogonTrigger' -or
        [string]$existingTriggers[0].UserId -cne $identity.Name
    ) {
        throw "Refusing to overwrite an unrelated scheduled task: $TaskPath$TaskName"
    }
}

$firewallDefinitions = @(
    [pscustomobject]@{ Name = "BookBox-HTTP-$Port"; DisplayName = "BookBox HTTP $Port"; Protocol = 'TCP'; Port = $Port },
    [pscustomobject]@{ Name = 'BookBox-mDNS-5353'; DisplayName = 'BookBox mDNS'; Protocol = 'UDP'; Port = 5353 }
)
$existingRules = @{}
foreach ($definition in $firewallDefinitions) {
    $rules = @(Get-NetFirewallRule -DisplayName $definition.DisplayName -ErrorAction SilentlyContinue)
    if ($rules.Count -gt 1) {
        throw "Refusing to replace an unrelated firewall rule: $($definition.DisplayName)"
    }
    $rule = if ($rules.Count -eq 1) { $rules[0] } else { $null }
    Assert-OwnedFirewallRule `
        -Rule $rule `
        -DisplayName $definition.DisplayName `
        -Protocol $definition.Protocol `
        -LocalPort $definition.Port
    $existingRules[$definition.DisplayName] = $rule
}

if (Test-Path -LiteralPath $installRoot -PathType Container) {
    if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        if (@(Get-ChildItem -LiteralPath $installRoot -Force).Count -gt 0) {
            throw "Refusing to overwrite an unowned install directory: $installRoot"
        }
    } else {
        $oldManifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
        if (
            [int]$oldManifest.SchemaVersion -ne 2 -or
            [string]$oldManifest.TaskName -cne $TaskName -or
            [string]$oldManifest.TaskPath -cne $TaskPath -or
            [string]$oldManifest.Owner -cne $identity.Name -or
            [string]$oldManifest.DeploymentMode -cne $DeploymentMode -or
            -not [string]::Equals(
                [string]$oldManifest.ComposeFile,
                $compose,
                [StringComparison]::OrdinalIgnoreCase
            ) -or
            -not [string]::Equals(
                [string]$oldManifest.HybridComposeFile,
                $hybridCompose,
                [StringComparison]::OrdinalIgnoreCase
            ) -or
            [string]$oldManifest.ExpectedActionArguments -cne $actionArguments
        ) {
            throw "Refusing to overwrite an unowned BookBox installation: $installRoot"
        }
        foreach ($file in @($oldManifest.Files)) {
            $oldPath = Join-Path $installRoot ([string]$file.Path)
            if (
                -not (Test-Path -LiteralPath $oldPath -PathType Leaf) -or
                (Get-FileHash -LiteralPath $oldPath -Algorithm SHA256).Hash -cne
                    [string]$file.Sha256
            ) {
                throw "Refusing to overwrite a modified installed file: $oldPath"
            }
        }
    }
}

if (Test-Path -LiteralPath (Join-Path $stateDirectory 'discovery-process.json')) {
    throw 'Stop the installed BookBox discovery process before reinstalling.'
}

. (Join-Path $PSScriptRoot 'install-transaction.ps1')
$transactionID = [Guid]::NewGuid().ToString('N')
$installParent = [IO.Path]::GetDirectoryName($installRoot)
$stageRoot = Join-Path $installParent ".BookBox-stage-$transactionID"
$backupRoot = Join-Path $installParent ".BookBox-backup-$transactionID"
Invoke-BookBoxInstallTransaction `
    -InstallDirectory $installRoot `
    -StageDirectory $stageRoot `
    -BackupDirectory $backupRoot `
    -PrepareStage {
        $stageBin = Join-Path $stageRoot 'bin'
        $stageScripts = Join-Path $stageRoot 'scripts'
        $stageState = Join-Path $stageRoot 'state'
        $null = New-Item -ItemType Directory -Path $stageBin, $stageScripts, $stageState -Force
        Copy-Item -LiteralPath $source `
            -Destination (Join-Path $stageBin 'bookbox-discovery.exe')
        foreach ($leaf in $managedScripts) {
            Copy-Item -LiteralPath (Join-Path $PSScriptRoot $leaf) `
                -Destination (Join-Path $stageScripts $leaf)
        }
        $stageFiles = @(
            Join-Path $stageBin 'bookbox-discovery.exe'
            foreach ($leaf in $managedScripts) {
                Join-Path $stageScripts $leaf
            }
        )
        $manifest = [ordered]@{
            SchemaVersion = 2
            Owner = $identity.Name
            TaskName = $TaskName
            TaskPath = $TaskPath
            Address = $Address
            Port = $Port
            StateDirectory = $stateDirectory
            DeploymentMode = $DeploymentMode
            ComposeFile = $compose
            HybridComposeFile = $hybridCompose
            ExpectedActionArguments = $actionArguments
            Files = @($stageFiles | ForEach-Object {
                [ordered]@{
                    Path = $_.Substring($stageRoot.TrimEnd('\').Length + 1)
                    Sha256 = (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash
                }
            })
        }
        [IO.File]::WriteAllText(
            (Join-Path $stageRoot 'bookbox-install.json'),
            ($manifest | ConvertTo-Json -Depth 6),
            [Text.UTF8Encoding]::new($false)
        )
    } `
    -CreateExternalResources {
        param($rollbackActions)
        foreach ($definition in $firewallDefinitions) {
            if (-not $existingRules[$definition.DisplayName]) {
                $createdRuleName = $definition.Name
                New-NetFirewallRule `
                    -Name $definition.Name `
                    -DisplayName $definition.DisplayName `
                    -Group 'BookBox' `
                    -Direction Inbound `
                    -Action Allow `
                    -Enabled True `
                    -Profile Private `
                    -Protocol $definition.Protocol `
                    -LocalAddress $Address `
                    -LocalPort $definition.Port `
                    -RemoteAddress LocalSubnet | Out-Null
                $rollbackActions.Add({
                    Remove-NetFirewallRule -Name $createdRuleName
                }.GetNewClosure())
            }
        }

        if (-not $existingTask) {
            $action = New-ScheduledTaskAction `
                -Execute 'powershell.exe' `
                -Argument $actionArguments `
                -WorkingDirectory $scriptsDirectory
            $trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity.Name
            $taskPrincipal = New-ScheduledTaskPrincipal `
                -UserId $identity.Name `
                -LogonType Interactive `
                -RunLevel Limited
            $settings = New-ScheduledTaskSettingsSet `
                -AllowStartIfOnBatteries `
                -DontStopIfGoingOnBatteries `
                -ExecutionTimeLimit ([TimeSpan]::Zero) `
                -MultipleInstances IgnoreNew
            $task = New-ScheduledTask `
                -Action $action `
                -Trigger $trigger `
                -Principal $taskPrincipal `
                -Settings $settings `
                -Description 'BookBox managed discovery version 2'
            Register-ScheduledTask `
                -TaskName $TaskName `
                -TaskPath $TaskPath `
                -InputObject $task | Out-Null
            $rollbackTaskName = $TaskName
            $rollbackTaskPath = $TaskPath
            $rollbackActions.Add({
                Unregister-ScheduledTask `
                    -TaskName $rollbackTaskName `
                    -TaskPath $rollbackTaskPath `
                    -Confirm:$false
            }.GetNewClosure())
        }
    }

Write-Output "Installed headless BookBox discovery in $installRoot and registered '$TaskPath$TaskName'."

[CmdletBinding()]
param(
    [string] $InstallDirectory = (Join-Path $env:LOCALAPPDATA 'BookBox'),
    [string] $TaskName = 'BookBox Discovery',
    [string] $TaskPath = '\BookBox\'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principalContext = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principalContext.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this removal script from an elevated PowerShell window.'
}
function Test-TaskUserIdentity {
    param(
        [Parameter(Mandatory)][string] $Account,
        [Parameter(Mandatory)][Security.Principal.WindowsIdentity] $Identity
    )
    try {
        $sid = ([Security.Principal.NTAccount]::new($Account)).Translate(
            [Security.Principal.SecurityIdentifier]
        )
        return [string]$sid.Value -ceq [string]$Identity.User.Value
    } catch {
        return $false
    }
}
$installRoot = [IO.Path]::GetFullPath($InstallDirectory)
$manifestPath = Join-Path $installRoot 'bookbox-install.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "Refusing to remove an installation without bookbox-install.json: $installRoot"
}
$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
if (
    [int]$manifest.SchemaVersion -ne 2 -or
    [string]$manifest.Owner -cne $identity.Name -or
    [string]$manifest.TaskName -cne $TaskName -or
    [string]$manifest.TaskPath -cne $TaskPath
) {
    throw 'Refusing to remove an installation whose ownership manifest does not match.'
}
$deploymentMode = [string]$manifest.DeploymentMode
$composeFile = [IO.Path]::GetFullPath([string]$manifest.ComposeFile)
$hybridComposeFile = [IO.Path]::GetFullPath([string]$manifest.HybridComposeFile)
if ($deploymentMode -notin @('Container', 'Hybrid') -or
    -not (Test-Path -LiteralPath $composeFile -PathType Leaf) -or
    ($deploymentMode -eq 'Hybrid' -and
        -not (Test-Path -LiteralPath $hybridComposeFile -PathType Leaf))) {
    throw 'Refusing to remove an installation with invalid deployment scope.'
}
$stateDirectory = [IO.Path]::GetFullPath([string]$manifest.StateDirectory)
$expectedStateDirectory = Join-Path $installRoot 'state'
if (
    -not [string]::Equals(
        $stateDirectory,
        $expectedStateDirectory,
        [StringComparison]::OrdinalIgnoreCase
    ) -or
    [string]$manifest.ExpectedActionArguments -notlike
        "*$(Join-Path $installRoot 'scripts\start-bookbox.ps1')*"
) {
    throw 'Refusing to remove an installation with external state or action paths.'
}
foreach ($file in @($manifest.Files)) {
    $path = [IO.Path]::GetFullPath((Join-Path $installRoot ([string]$file.Path)))
    if (
        -not $path.StartsWith(
            $installRoot.TrimEnd('\') + '\',
            [StringComparison]::OrdinalIgnoreCase
        ) -or
        -not (Test-Path -LiteralPath $path -PathType Leaf) -or
        (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -cne [string]$file.Sha256
    ) {
        throw "Refusing to remove a missing, external, or modified installed file: $path"
    }
}

$existingTask = Get-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath -ErrorAction SilentlyContinue
if ($existingTask) {
    $actions = @($existingTask.Actions)
    $triggers = @($existingTask.Triggers)
    $managedWorkingDirectory = Join-Path $installRoot 'scripts'
    $workingDirectoryOwned = $actions.Count -eq 1 -and (
        [string]::IsNullOrWhiteSpace([string]$actions[0].WorkingDirectory) -or
            [string]::Equals(
                [string]$actions[0].WorkingDirectory,
                $managedWorkingDirectory,
                [StringComparison]::OrdinalIgnoreCase
            )
    )
    if (
        $existingTask.Description -cne 'BookBox managed discovery version 2' -or
        $actions.Count -ne 1 -or
        [string]$actions[0].Execute -notmatch '(?i)(^|\\)powershell\.exe$' -or
        -not $workingDirectoryOwned -or
        [string]$actions[0].Arguments -cne [string]$manifest.ExpectedActionArguments -or
        -not (Test-TaskUserIdentity -Account ([string]$existingTask.Principal.UserId) -Identity $identity) -or
        [string]$existingTask.Principal.RunLevel -cne 'Limited' -or
        [string]$existingTask.Principal.LogonType -notmatch '^Interactive' -or
        [string]$existingTask.Settings.MultipleInstances -cne 'IgnoreNew' -or
        $triggers.Count -ne 1 -or
        $triggers[0].CimClass.CimClassName -cne 'MSFT_TaskLogonTrigger' -or
        -not (Test-TaskUserIdentity -Account ([string]$triggers[0].UserId) -Identity $identity)
    ) {
        throw "Refusing to remove an unrelated scheduled task: $TaskPath$TaskName"
    }
}

$firewallDefinitions = @(
    [pscustomobject]@{ DisplayName = "BookBox HTTP $($manifest.Port)"; Protocol = 'TCP'; Port = [int]$manifest.Port },
    [pscustomobject]@{ DisplayName = 'BookBox mDNS'; Protocol = 'UDP'; Port = 5353 }
)
$ownedRules = [Collections.Generic.List[object]]::new()
foreach ($definition in $firewallDefinitions) {
    $rules = @(Get-NetFirewallRule -DisplayName $definition.DisplayName -ErrorAction SilentlyContinue)
    if ($rules.Count -gt 1) {
        throw "Refusing to remove ambiguous firewall rules: $($definition.DisplayName)"
    }
    if ($rules.Count -eq 1) {
        $rule = $rules[0]
        $portFilter = $rule | Get-NetFirewallPortFilter
        $addressFilter = $rule | Get-NetFirewallAddressFilter
        if (
            $rule.Group -ne 'BookBox' -or
            $rule.Profile -ne 'Private' -or
            $rule.Direction -ne 'Inbound' -or
            $rule.Action -ne 'Allow' -or
            [string]$portFilter.Protocol -cne $definition.Protocol -or
            [string]$portFilter.LocalPort -cne [string]$definition.Port -or
            [string]$addressFilter.LocalAddress -cne [string]$manifest.Address -or
            [string]$addressFilter.RemoteAddress -cne 'LocalSubnet'
        ) {
            throw "Refusing to remove an unrelated firewall rule: $($definition.DisplayName)"
        }
        $ownedRules.Add($rule)
    }
}

$processScript = Join-Path $installRoot 'scripts\discovery-process.ps1'
$stopScript = Join-Path $installRoot 'scripts\stop-bookbox.ps1'
$taskWasEnabled = [bool]($existingTask -and [bool]$existingTask.Settings.Enabled)
try {
    if ($existingTask) {
        Disable-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath | Out-Null
        $taskAfterDisable = Get-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath
        if ([string]$taskAfterDisable.State -eq 'Running') {
            Stop-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath
            $taskDeadline = [DateTime]::UtcNow.AddSeconds(30)
            do {
                Start-Sleep -Milliseconds 200
                $taskState = (Get-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath).State
            } while ([string]$taskState -eq 'Running' -and [DateTime]::UtcNow -lt $taskDeadline)
            if ([string]$taskState -eq 'Running') {
                throw 'BookBox scheduled startup task did not stop within 30 seconds.'
            }
        }
    }
    $composeStatePath = Join-Path $stateDirectory 'compose-startup.json'
    if (Test-Path -LiteralPath $composeStatePath -PathType Leaf) {
        & $stopScript `
            -StateDirectory $stateDirectory `
            -ComposeFile $composeFile `
            -HybridComposeFile ([string]$manifest.HybridComposeFile) `
            -DeploymentMode ([string]$manifest.DeploymentMode)
    } elseif (Test-Path -LiteralPath $processScript -PathType Leaf) {
        . $processScript
        Stop-BookBoxDiscovery -StateDirectory $stateDirectory
    }
} catch {
    if ($taskWasEnabled) {
        Enable-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath | Out-Null
    }
    throw
}
if ($existingTask) {
    Unregister-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath -Confirm:$false
}
foreach ($rule in $ownedRules) {
    Remove-NetFirewallRule -Name $rule.Name
}
foreach ($file in @($manifest.Files)) {
    Remove-Item -LiteralPath (Join-Path $installRoot ([string]$file.Path)) -Force
}
Remove-Item -LiteralPath $manifestPath -Force
foreach ($leaf in @(
    'discovery-process.json',
    'discovery.stdout.log',
    'discovery.stderr.log',
    'discovery.arguments.json',
    'discovery.bootstrap.json',
    'discovery.stop',
    'discovery.accepted'
)) {
    Remove-Item -LiteralPath (Join-Path $stateDirectory $leaf) `
        -Force -ErrorAction SilentlyContinue
}
foreach ($directory in @(
    $stateDirectory,
    (Join-Path $installRoot 'bin'),
    (Join-Path $installRoot 'scripts'),
    $installRoot
)) {
    if (
        (Test-Path -LiteralPath $directory -PathType Container) -and
        @(Get-ChildItem -LiteralPath $directory -Force).Count -eq 0
    ) {
        Remove-Item -LiteralPath $directory -Force
    }
}

Write-Output "Removed owned BookBox discovery task, firewall rules, executable, scripts, and manifest from $installRoot."

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
$installRoot = [IO.Path]::GetFullPath($InstallDirectory)
$manifestPath = Join-Path $installRoot 'bookbox-install.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "Refusing to remove an installation without bookbox-install.json: $installRoot"
}
$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
if (
    [int]$manifest.SchemaVersion -ne 1 -or
    [string]$manifest.Owner -cne $identity.Name -or
    [string]$manifest.TaskName -cne $TaskName -or
    [string]$manifest.TaskPath -cne $TaskPath
) {
    throw 'Refusing to remove an installation whose ownership manifest does not match.'
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
    if (
        $existingTask.Description -cne 'BookBox managed discovery version 1' -or
        $actions.Count -ne 1 -or
        [string]$actions[0].Execute -notmatch '(?i)(^|\\)powershell\.exe$' -or
        [string]$actions[0].WorkingDirectory -cne (Join-Path $installRoot 'scripts') -or
        [string]$actions[0].Arguments -cne [string]$manifest.ExpectedActionArguments -or
        [string]$existingTask.Principal.UserId -cne $identity.Name -or
        [string]$existingTask.Principal.RunLevel -cne 'Limited' -or
        [string]$existingTask.Principal.LogonType -notmatch '^Interactive' -or
        $triggers.Count -ne 1 -or
        $triggers[0].CimClass.CimClassName -cne 'MSFT_TaskLogonTrigger' -or
        [string]$triggers[0].UserId -cne $identity.Name
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
if (Test-Path -LiteralPath $processScript -PathType Leaf) {
    . $processScript
    Stop-BookBoxDiscovery -StateDirectory $stateDirectory
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

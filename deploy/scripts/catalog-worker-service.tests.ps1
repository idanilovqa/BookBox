[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$installPath = Join-Path $PSScriptRoot 'install-catalog-worker-service.ps1'
$removePath = Join-Path $PSScriptRoot 'remove-catalog-worker-service.ps1'
foreach ($path in @($installPath, $removePath)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Catalog worker service script is missing: $path"
    }
    $tokens = $null
    $parseErrors = $null
    $null = [Management.Automation.Language.Parser]::ParseFile(
        $path, [ref]$tokens, [ref]$parseErrors
    )
    if (@($parseErrors).Count -ne 0) {
        throw "Catalog worker service script has syntax errors: $($parseErrors -join '; ')"
    }
}

function Get-ScriptAst {
    param([string] $Path)
    $tokens = $null
    $errors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$errors)
    if (@($errors).Count -ne 0) { throw "PowerShell parse failed: $($errors -join '; ')" }
    $ast
}

function Get-FirstCommandOffset {
    param([object] $Ast, [string[]] $Names)
    $commands = @($Ast.FindAll({
        param($node)
        if ($node -isnot [Management.Automation.Language.CommandAst] -or
            $Names -notcontains $node.GetCommandName()) { return $false }
        $parent = $node.Parent
        while ($null -ne $parent) {
            if ($parent -is [Management.Automation.Language.FunctionDefinitionAst]) {
                return $false
            }
            $parent = $parent.Parent
        }
        return $true
    }, $true) | Sort-Object { $_.Extent.StartOffset })
    if ($commands.Count -eq 0) { return -1 }
    [int]$commands[0].Extent.StartOffset
}

$install = Get-Content -Raw -LiteralPath $installPath
$remove = Get-Content -Raw -LiteralPath $removePath
$installAst = Get-ScriptAst $installPath
$removeAst = Get-ScriptAst $removePath
$both = $install + [Environment]::NewLine + $remove

foreach ($required in @(
    'BookBoxCatalogWorker',
    [regex]::Escape('NT SERVICE\BookBoxCatalogWorker'),
    'WindowsBuiltInRole.*Administrator',
    'SourceExecutable',
    'ConfigFile',
    'FFProbeExecutable',
    'LibraryRoot',
    'InstallDirectory',
    'FileAttributes.*ReparsePoint|ReparsePoint',
    'DriveType.*[23]|Win32_LogicalDisk',
    'SchemaVersion',
    'ManagedFiles',
    'ExternalInputs',
    'ACLRules',
    'Sha256|SHA256'
)) {
    if ($install -notmatch $required) {
        throw "Installer contract is missing: $required"
    }
}

$preflight = $install.IndexOf('# Complete ownership preflight')
$elevation = $install.IndexOf('IsInRole')
$aclCollisionPreflight = $install.IndexOf('$existingRules = @((Get-Acl')
$firstMutation = Get-FirstCommandOffset $installAst @(
    'Copy-Item', 'New-Item', 'Set-Acl', 'New-EventLog', 'sc.exe'
)
if ($elevation -lt 0 -or $preflight -lt 0 -or $aclCollisionPreflight -lt 0 -or
    $firstMutation -lt 0 -or
    $elevation -gt $preflight -or $preflight -gt $firstMutation) {
    throw 'Installer must complete all ownership preflight before any mutation.'
}
if ($aclCollisionPreflight -gt $preflight) {
    throw 'Installer must reject pre-existing exact service-SID ACLs before the mutation boundary.'
}
$aclCoverageStart = $install.IndexOf('$preflightAclTargets')
if ($aclCoverageStart -lt 0 -or $aclCoverageStart -gt $aclCollisionPreflight) {
    throw 'Installer ACL preflight target set is missing or occurs too late.'
}
$aclPreflightText = $install.Substring($aclCoverageStart, $preflight - $aclCoverageStart)
foreach ($required in @('Get-NearestExistingPath', 'InheritanceFlags', '\$installedExe')) {
    if ($aclPreflightText -notmatch $required) {
        throw "Installer ACL preflight does not cover inherited/new-target access: $required"
    }
}
if ($aclPreflightText -match '-not\s+\$_\.IsInherited') {
    throw 'Installer ACL preflight ignores effective inherited service-SID rules.'
}
foreach ($preflightEvidence in @(
    'LibraryRoot\)\.Count -lt 1',
    'Assert-SafeNativeArgument \$SourceExecutable',
    'Assert-SafeNativeArgument \$ConfigFile',
    'Assert-SafeNativeArgument \$FFProbeExecutable',
    'Assert-SafeNativeArgument \$InstallDirectory',
    'Assert-SafeNativeArgument \$candidateRoot',
    'Resolve-SafeFile \$SourceExecutable',
    'Resolve-SafeFile \$ConfigFile',
    'Resolve-SafeFile \$FFProbeExecutable',
    'Resolve-LocalLibraryRoot',
    'Get-FileHash',
    'Get-CimInstance Win32_Service',
    'EventLog\]::SourceExists'
)) {
    $match = [regex]::Match($install, $preflightEvidence)
    if (-not $match.Success -or $match.Index -gt $preflight) {
        throw "Installer ownership validation is absent or occurs after mutation boundary: $preflightEvidence"
    }
}

foreach ($required in @(
    "Invoke-SC @\((?s:.*?)'create',\s*\`$serviceName(?s:.*?)'binPath=',\s*\`$binPath(?s:.*?)'start=',\s*'delayed-auto'(?s:.*?)'obj=',\s*\`$serviceAccount(?s:.*?)'password=',\s*'`"`"'",
    "Invoke-SC @\('sidtype',\s*\`$serviceName,\s*'unrestricted'\)",
    "Invoke-SC @\((?s:.*?)'failure',\s*\`$serviceName(?s:.*?)'reset=',\s*'86400'(?s:.*?)'actions=',\s*'restart/5000/restart/30000/restart/60000'",
    "Invoke-SC @\('failureflag',\s*\`$serviceName,\s*'1'\)",
    'New-EventLog.*Application.*BookBoxCatalogWorker',
    'ReadAndExecute',
    'FileSystemRights.*Read',
    'FileSystemRights.*Modify',
    'logs',
    'backups',
    'AddAccessRule',
    'rollbackActions'
)) {
    if ($install -notmatch $required) {
        throw "Installer service/ACL contract is missing: $required"
    }
}
if ($install -match 'Path\s*=\s*\$dataRoot\s*;\s*Rights\s*=\s*\[Security\.AccessControl\.FileSystemRights\]::Modify') {
    throw 'Installer grants broad ProgramData Modify instead of scoped mutable directories.'
}
foreach ($aclDefinition in @(
    '\$installedExe; Rights = \[Security\.AccessControl\.FileSystemRights\]::ReadAndExecute',
    '\$ffprobe; Rights = \[Security\.AccessControl\.FileSystemRights\]::ReadAndExecute',
    '\$config; Rights = \[Security\.AccessControl\.FileSystemRights\]::Read',
    '\$secrets; Rights = \[Security\.AccessControl\.FileSystemRights\]::ReadAndExecute',
    '\$logs; Rights = \[Security\.AccessControl\.FileSystemRights\]::Modify',
    '\$backups; Rights = \[Security\.AccessControl\.FileSystemRights\]::Modify',
    'Path = \$_; Rights = \[Security\.AccessControl\.FileSystemRights\]::Modify'
)) {
    if ($install -notmatch $aclDefinition) {
        throw "Installer lacks an exact scoped ACL definition: $aclDefinition"
    }
}
if ($install -match 'Path\s*=\s*\$dataRoot;\s*Rights\s*=.*::Modify') {
    throw 'Installer grants Modify to the broad data root.'
}
foreach ($rollback in @('RemoveAccessRuleSpecific', 'Remove-EventLog -Source \$serviceName', 'Remove-OwnedService\s+-Name\s+\$serviceName')) {
    if ($install -notmatch $rollback) { throw "Installer rollback is incomplete: $rollback" }
}
foreach ($rollbackSafety in @(
    'function\s+Remove-OwnedService',
    'Wait-ForServiceAbsent',
    'Remove-OwnedService\s+-Name\s+\$serviceName'
)) {
    if ($install -notmatch $rollbackSafety) {
        throw "Installer service rollback is not bounded/state-aware: $rollbackSafety"
    }
}
$sidTypeApplied = $install.IndexOf("Invoke-SC @('sidtype', `$serviceName, 'unrestricted')")
$authoritativeSidCheck = $install.IndexOf('$authoritativeSid = ([Security.Principal.NTAccount]::new($serviceAccount)).Translate(')
$aclApplication = $install.IndexOf('$record = New-RecordedAccessRule')
$manifestCreation = $install.IndexOf('$manifest = [ordered]@{')
if ($sidTypeApplied -lt 0 -or $authoritativeSidCheck -lt 0 -or $aclApplication -lt 0 -or
    $manifestCreation -lt 0 -or $authoritativeSidCheck -lt $sidTypeApplied -or
    $authoritativeSidCheck -gt $aclApplication -or $authoritativeSidCheck -gt $manifestCreation -or
    $install -notmatch '\$authoritativeSid\.Value\s+-cne\s+\$sid\.Value') {
    throw 'Installer must verify the authoritative SCM account SID equals the deterministic preflight SID before ACL/manifest use.'
}
if ($install -match 'Copy-Item[^\r\n]*FFProbeExecutable') {
    throw 'Installer must require external ffprobe without bundling it.'
}

foreach ($required in @(
    'bookbox-catalog-worker-install\.json',
    'Get-FileHash',
    'Stop-Service\s+-Name\s+\$serviceName',
    'WaitForStatus|WaitForExit|StopPending',
    "Invoke-SC @\('delete',\s*\`$serviceName\)",
    'Remove-EventLog\s+-Source\s+\$serviceName',
    'RemoveAccessRuleSpecific',
    'RemoveData',
    'ManagedFiles',
    'ACLRules'
)) {
    if ($remove -notmatch $required) {
        throw "Removal ownership contract is missing: $required"
    }
}

foreach ($forbidden in @(
    '\btaskkill\b',
    'Stop-Process\s+-Name',
    'Get-Service[^\r\n]*\*',
    'Remove-Service[^\r\n]*\*',
    'Remove-Item[^\r\n]*-Path[^\r\n]*\*',
    'Remove-Item[^\r\n]*ProgramData[^\r\n]*-Recurse',
    'icacls[^\r\n]*\*'
)) {
    if ($both -match $forbidden) {
        throw "Catalog worker service scripts contain a broad action: $forbidden"
    }
}

$removePreflight = $remove.IndexOf('# Complete removal preflight')
$removeElevation = $remove.IndexOf('IsInRole')
$firstRemovalMutation = Get-FirstCommandOffset $removeAst @(
    'Stop-Service', 'Set-Acl', 'Remove-EventLog', 'Remove-Item', 'sc.exe'
)
if ($removeElevation -lt 0 -or $removePreflight -lt 0 -or $firstRemovalMutation -lt 0 -or
    $removeElevation -gt $removePreflight -or $removePreflight -gt $firstRemovalMutation) {
    throw 'Removal must validate every owned resource before stopping the service.'
}
foreach ($evidence in @(
    'ManagedFiles', 'ExternalInputs', 'FailureActionsBase64', 'ServiceSidType',
    'DelayedAutoStart', 'EventLog\\Application', 'ACLRules', 'Assert-SafeExistingPathChain',
    'Assert-SafeRemovalTree'
)) {
    $match = [regex]::Match($remove, $evidence)
    if (-not $match.Success -or $match.Index -gt $removePreflight) {
        throw "Removal ownership validation is absent or late: $evidence"
    }
}
if ($remove -notmatch 'catch\s*\{(?s:.*?)Start-Service\s+-Name\s+\$serviceName') {
    throw 'Failed service deletion must restore the exact previously-running service.'
}
if ($remove -notmatch '(?s)try\s*\{.*?Stop-Service\s+-Name\s+\$serviceName.*?WaitForStatus.*?Invoke-SC\s+@\(''delete'',\s*\$serviceName\).*?Remove-Item\s+-LiteralPath.*?\}\s*catch\s*\{') {
    throw 'Service stop/wait, SCM deletion, and managed-file removal must share one rollback boundary.'
}
foreach ($transactionEvidence in @(
    'Wait-ForServiceAbsent',
    'Restore-OwnedService',
    'Restore-FileSnapshots',
    'ReadAllBytes',
    'GetSecurityDescriptorBinaryForm',
    'snapshotOwnsAcl',
    'FromSeconds\(30\)',
    'Start-Service\s+-Name\s+\$serviceName'
)) {
    if ($remove -notmatch $transactionEvidence) {
        throw "Removal transaction is missing bounded recovery evidence: $transactionEvidence"
    }
}

Write-Output 'PASS: Windows catalog worker service install/removal contracts are scoped and ownership-safe.'

[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateSet('backup', 'restore', 'rotate')]
    [string] $Operation,

    [Parameter(Mandatory)]
    [string] $ArgumentsFile
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$argumentPath = [IO.Path]::GetFullPath($ArgumentsFile)
$runRoot = [IO.Path]::GetDirectoryName($argumentPath)
$expectedParent = [IO.Path]::GetFullPath(
    (Join-Path ([IO.Path]::GetTempPath()) 'bookbox-recovery')
).TrimEnd('\')
$runID = [IO.Path]::GetFileName($runRoot)
if (
    [IO.Path]::GetFileName($argumentPath) -cne 'recovery-operation.json' -or
    $runID -notmatch '^[0-9a-f]{32}$' -or
    -not [string]::Equals(
        [IO.Path]::GetDirectoryName($runRoot),
        $expectedParent,
        [StringComparison]::OrdinalIgnoreCase
    ) -or
    -not (Test-Path -LiteralPath $argumentPath -PathType Leaf)
) {
    throw 'Synthetic recovery arguments are outside the task-scoped temp root.'
}

$document = Get-Content -LiteralPath $argumentPath -Raw | ConvertFrom-Json
if ([string] $document.ProjectName -cne "bookbox-recovery-$runID") {
    throw 'Synthetic recovery project does not match its task-scoped temp root.'
}
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$expectedCompose = @(
    [IO.Path]::GetFullPath((Join-Path $repo 'deploy\compose.yml')),
    [IO.Path]::GetFullPath((Join-Path $repo 'deploy\compose.recovery.test.yml'))
)
$composeFiles = @($document.ComposeFile | ForEach-Object {
    [IO.Path]::GetFullPath([string] $_)
})
if (
    $composeFiles.Count -ne 2 -or
    -not [string]::Equals(
        $composeFiles[0], $expectedCompose[0],
        [StringComparison]::OrdinalIgnoreCase
    ) -or
    -not [string]::Equals(
        $composeFiles[1], $expectedCompose[1],
        [StringComparison]::OrdinalIgnoreCase
    )
) {
    throw 'Synthetic recovery must use the base and recovery-test Compose files.'
}

$allowed = switch ($Operation) {
    backup {
        @(
            'Destination', 'ProjectName', 'ComposeFile', 'EnvironmentFile',
            'ConfigFile', 'AppVersion'
        )
    }
    restore {
        @(
            'BackupDirectory', 'ProjectName', 'ComposeFile', 'EnvironmentFile',
            'BaseUrl', 'TimeoutSeconds'
        )
    }
    rotate {
        @(
            'ProjectName', 'ComposeFile', 'EnvironmentFile',
            'CurrentPasswordFile', 'NewPasswordFile', 'BaseUrl', 'Username',
            'TimeoutSeconds'
        )
    }
}
$properties = @($document.PSObject.Properties)
if (@($properties | Where-Object { $allowed -notcontains $_.Name }).Count -ne 0) {
    throw 'Synthetic recovery operation contains an unapproved argument.'
}
foreach ($required in $allowed) {
    if ($properties.Name -notcontains $required) {
        throw "Synthetic recovery operation is missing argument: $required"
    }
}
foreach ($pathProperty in @(
    'Destination', 'BackupDirectory', 'EnvironmentFile', 'ConfigFile',
    'CurrentPasswordFile', 'NewPasswordFile'
)) {
    if ($properties.Name -contains $pathProperty) {
        $value = [IO.Path]::GetFullPath([string] $document.$pathProperty)
        if (-not $value.StartsWith(
            $runRoot.TrimEnd('\') + '\',
            [StringComparison]::OrdinalIgnoreCase
        )) {
            throw "Synthetic recovery path escapes its task root: $pathProperty"
        }
    }
}
$exactPaths = [ordered]@{
    Destination = 'backup'
    BackupDirectory = 'backup'
    EnvironmentFile = 'compose.env'
    ConfigFile = 'config.yml'
    CurrentPasswordFile = 'current-password.txt'
    NewPasswordFile = 'new-password.txt'
}
foreach ($entry in $exactPaths.GetEnumerator()) {
    if ($properties.Name -contains $entry.Key) {
        $actual = [IO.Path]::GetFullPath([string] $document.($entry.Key))
        $expected = [IO.Path]::GetFullPath((Join-Path $runRoot $entry.Value))
        if (-not [string]::Equals(
            $actual,
            $expected,
            [StringComparison]::OrdinalIgnoreCase
        )) {
            throw "Synthetic recovery path is not the exact task path: $($entry.Key)"
        }
    }
}
if (
    ($properties.Name -contains 'BaseUrl' -and
        [string] $document.BaseUrl -cne 'https://127.0.0.1:18443') -or
    ($properties.Name -contains 'Username' -and
        [string] $document.Username -cne 'owner') -or
    ($properties.Name -contains 'AppVersion' -and
        [string] $document.AppVersion -cne '1.0.0')
) {
    throw 'Synthetic recovery scalar identity is not approved.'
}

$environmentFile = [IO.Path]::GetFullPath([string] $document.EnvironmentFile)
$composeArguments = @(
    'compose',
    '--project-name', [string] $document.ProjectName,
    '--env-file', $environmentFile,
    '-f', $composeFiles[0],
    '-f', $composeFiles[1],
    'config', '--format', 'json'
)
$previousPreference = $ErrorActionPreference
try {
    $ErrorActionPreference = 'Continue'
    $resolvedOutput = @(& docker @composeArguments 2>&1)
    $resolvedExitCode = $LASTEXITCODE
} finally {
    $ErrorActionPreference = $previousPreference
}
if ($resolvedExitCode -ne 0) {
    throw "Synthetic recovery Compose scope could not be resolved:`n$(
        $resolvedOutput -join [Environment]::NewLine
    )"
}
$resolved = ($resolvedOutput -join [Environment]::NewLine) | ConvertFrom-Json
. (Join-Path $PSScriptRoot 'synthetic-recovery-scope.ps1')
Test-SyntheticRecoveryResolvedScope `
    -Resolved $resolved `
    -RunRoot $runRoot `
    -ProjectName ([string] $document.ProjectName)

$parameters = @{}
foreach ($property in $properties) {
    if ($property.Name -eq 'ComposeFile') {
        $parameters.ComposeFile = $composeFiles
    } elseif ($property.Name -eq 'TimeoutSeconds') {
        $parameters.TimeoutSeconds = [int] $property.Value
    } else {
        $parameters[$property.Name] = [string] $property.Value
    }
}
$script = switch ($Operation) {
    backup { Join-Path $PSScriptRoot 'backup.ps1' }
    restore { Join-Path $PSScriptRoot 'restore.ps1' }
    rotate { Join-Path $PSScriptRoot 'rotate-password.ps1' }
}
& $script @parameters

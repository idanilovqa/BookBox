[CmdletBinding()]
param()

Set-StrictMode -Version Latest

function Resolve-BookBoxComposeScope {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [ValidateSet('Container', 'Hybrid')]
        [string] $DeploymentMode,

        [Parameter(Mandatory)]
        [string] $ComposeFile,

        [Parameter(Mandatory)]
        [string] $HybridComposeFile
    )

    $base = [IO.Path]::GetFullPath($ComposeFile)
    if (-not (Test-Path -LiteralPath $base -PathType Leaf)) {
        throw "Compose file not found: $base"
    }
    $hybrid = [IO.Path]::GetFullPath($HybridComposeFile)
    if ($DeploymentMode -eq 'Hybrid' -and
        -not (Test-Path -LiteralPath $hybrid -PathType Leaf)) {
        throw "Hybrid Compose file not found: $hybrid"
    }

    if ($DeploymentMode -eq 'Container') {
        return [pscustomobject]@{
            DeploymentMode = 'Container'
            ComposeFiles = @($base)
            Profiles = @('container-worker')
        }
    }
    [pscustomobject]@{
        DeploymentMode = 'Hybrid'
        ComposeFiles = @($base, $hybrid)
        Profiles = @()
    }
}

function Get-BookBoxComposeArguments {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [object] $Scope,

        [string] $EnvironmentFile,

        [string] $ProjectName,

        [Parameter(Mandatory)]
        [string[]] $Command
    )

    $arguments = @('compose')
    if ($EnvironmentFile) {
        $arguments += @('--env-file', [IO.Path]::GetFullPath($EnvironmentFile))
    }
    if ($ProjectName) {
        $arguments += @('--project-name', $ProjectName)
    }
    foreach ($profile in @($Scope.Profiles)) {
        $arguments += @('--profile', [string]$profile)
    }
    foreach ($file in @($Scope.ComposeFiles)) {
        $arguments += @('-f', [string]$file)
    }
    $arguments += @($Command)
    $arguments
}

function Get-BookBoxComposeStatePath {
    param([Parameter(Mandatory)][string] $StateDirectory)
    Join-Path ([IO.Path]::GetFullPath($StateDirectory)) 'compose-startup.json'
}

function Write-BookBoxComposeState {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string] $StateDirectory,
        [Parameter(Mandatory)][object] $Scope,
        [Parameter(Mandatory)][string] $Address,
        [Parameter(Mandatory)][ValidateRange(1, 65535)][int] $Port,
        [AllowNull()][string] $EnvironmentFile
    )

    $directory = [IO.Path]::GetFullPath($StateDirectory)
    $statePath = Get-BookBoxComposeStatePath -StateDirectory $directory
    if (Test-Path -LiteralPath $statePath) {
        throw "An active deployment Compose state already exists: $statePath"
    }
    $null = New-Item -ItemType Directory -Path $directory -Force
    $temporary = Join-Path $directory ".compose-startup-$([Guid]::NewGuid().ToString('N')).tmp"
    $record = [ordered]@{
        SchemaVersion = 1
        DeploymentMode = [string]$Scope.DeploymentMode
        ComposeFiles = @($Scope.ComposeFiles | ForEach-Object { [IO.Path]::GetFullPath([string]$_) })
        Profiles = @($Scope.Profiles | ForEach-Object { [string]$_ })
        EnvironmentFile = if ([string]::IsNullOrWhiteSpace($EnvironmentFile)) {
            $null
        } else {
            [IO.Path]::GetFullPath($EnvironmentFile)
        }
        Address = $Address
        Port = $Port
    }
    try {
        $record | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $temporary -Encoding UTF8
        Move-Item -LiteralPath $temporary -Destination $statePath
    } finally {
        Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue
    }
}

function Read-BookBoxComposeState {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string] $StateDirectory,
        [Parameter(Mandatory)][string] $ComposeFile,
        [Parameter(Mandatory)][string] $HybridComposeFile,
        [AllowNull()][string] $AllowedEnvironmentFile
    )

    $statePath = Get-BookBoxComposeStatePath -StateDirectory $StateDirectory
    if (-not (Test-Path -LiteralPath $statePath -PathType Leaf)) {
        throw "Owned Compose startup state is missing: $statePath"
    }
    try {
        $record = Get-Content -Raw -LiteralPath $statePath | ConvertFrom-Json
    } catch {
        throw "Refusing malformed Compose startup state: $statePath"
    }
    if ([int]$record.SchemaVersion -ne 1 -or
        [string]$record.DeploymentMode -notin @('Container', 'Hybrid')) {
        throw 'Refusing unsupported Compose startup state.'
    }
    $expected = Resolve-BookBoxComposeScope `
        -DeploymentMode ([string]$record.DeploymentMode) `
        -ComposeFile $ComposeFile `
        -HybridComposeFile $HybridComposeFile
    $actualFiles = @($record.ComposeFiles | ForEach-Object { [IO.Path]::GetFullPath([string]$_) })
    $expectedFiles = @($expected.ComposeFiles)
    $actualProfiles = @($record.Profiles | ForEach-Object { [string]$_ })
    $expectedProfiles = @($expected.Profiles)
    if ($actualFiles.Count -ne $expectedFiles.Count -or
        $actualProfiles.Count -ne $expectedProfiles.Count) {
        throw 'Refusing Compose startup state with an unexpected scope.'
    }
    for ($index = 0; $index -lt $expectedFiles.Count; $index++) {
        if (-not [string]::Equals($actualFiles[$index], $expectedFiles[$index],
                [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Refusing Compose startup state with an unexpected file scope.'
        }
    }
    for ($index = 0; $index -lt $expectedProfiles.Count; $index++) {
        if ($actualProfiles[$index] -cne $expectedProfiles[$index]) {
            throw 'Refusing Compose startup state with an unexpected profile scope.'
        }
    }
    if ([int]$record.Port -lt 1 -or [int]$record.Port -gt 65535 -or
        [string]::IsNullOrWhiteSpace([string]$record.Address)) {
        throw 'Refusing Compose startup state with invalid endpoint data.'
    }
    if (-not $record.PSObject.Properties['EnvironmentFile']) {
        throw 'Refusing Compose startup state without an env-file choice.'
    }
    if (-not [string]::IsNullOrWhiteSpace([string]$record.EnvironmentFile)) {
        $recordedEnvironment = [IO.Path]::GetFullPath([string]$record.EnvironmentFile)
        if ([string]::IsNullOrWhiteSpace($AllowedEnvironmentFile) -or
            -not [string]::Equals(
                $recordedEnvironment,
                [IO.Path]::GetFullPath($AllowedEnvironmentFile),
                [StringComparison]::OrdinalIgnoreCase
            )) {
            throw 'Refusing Compose startup state with an unexpected env-file scope.'
        }
        $record.EnvironmentFile = $recordedEnvironment
    }
    $record
}

function Remove-BookBoxComposeState {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string] $StateDirectory)
    $statePath = Get-BookBoxComposeStatePath -StateDirectory $StateDirectory
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
}

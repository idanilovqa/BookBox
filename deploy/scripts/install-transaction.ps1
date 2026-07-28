[CmdletBinding()]
param()

Set-StrictMode -Version Latest

function Invoke-BookBoxInstallTransaction {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string] $InstallDirectory,

        [Parameter(Mandatory)]
        [string] $StageDirectory,

        [Parameter(Mandatory)]
        [string] $BackupDirectory,

        [Parameter(Mandatory)]
        [scriptblock] $PrepareStage,

        [Parameter(Mandatory)]
        [scriptblock] $CreateExternalResources
    )

    $install = [IO.Path]::GetFullPath($InstallDirectory).TrimEnd('\')
    $stage = [IO.Path]::GetFullPath($StageDirectory).TrimEnd('\')
    $backup = [IO.Path]::GetFullPath($BackupDirectory).TrimEnd('\')
    $parent = [IO.Path]::GetDirectoryName($install)
    if (
        [string]::IsNullOrWhiteSpace($parent) -or
        [string]::Equals(
            $install,
            [IO.Path]::GetPathRoot($install).TrimEnd('\'),
            [StringComparison]::OrdinalIgnoreCase
        )
    ) {
        throw "Refusing a broad install transaction target: $install"
    }
    foreach ($candidate in @($stage, $backup)) {
        if (
            -not [string]::Equals(
                [IO.Path]::GetDirectoryName($candidate),
                $parent,
                [StringComparison]::OrdinalIgnoreCase
            ) -or
            [string]::Equals($candidate, $install, [StringComparison]::OrdinalIgnoreCase)
        ) {
            throw "Install transaction path is outside the exact install parent: $candidate"
        }
    }
    if (
        (Test-Path -LiteralPath $stage) -or
        (Test-Path -LiteralPath $backup)
    ) {
        throw 'Install transaction staging or backup path already exists.'
    }

    $rollbackActions = [Collections.Generic.List[scriptblock]]::new()
    $movedExisting = $false
    $installedStage = $false
    try {
        & $PrepareStage
        if (-not (Test-Path -LiteralPath $stage -PathType Container)) {
            throw 'Install transaction did not produce its exact staging directory.'
        }
        if (Test-Path -LiteralPath $install -PathType Container) {
            Move-Item -LiteralPath $install -Destination $backup
            $movedExisting = $true
        }
        Move-Item -LiteralPath $stage -Destination $install
        $installedStage = $true
        & $CreateExternalResources $rollbackActions
        if ($movedExisting -and (Test-Path -LiteralPath $backup)) {
            Remove-Item -LiteralPath $backup -Recurse -Force
        }
    } catch {
        $primaryFailure = $_
        $rollbackErrors = [Collections.Generic.List[string]]::new()
        for ($index = $rollbackActions.Count - 1; $index -ge 0; $index--) {
            try {
                & $rollbackActions[$index]
            } catch {
                $rollbackErrors.Add($_.Exception.Message)
            }
        }
        try {
            if ($installedStage -and (Test-Path -LiteralPath $install)) {
                Remove-Item -LiteralPath $install -Recurse -Force
            }
            if ($movedExisting -and (Test-Path -LiteralPath $backup)) {
                Move-Item -LiteralPath $backup -Destination $install
            }
            if (Test-Path -LiteralPath $stage) {
                Remove-Item -LiteralPath $stage -Recurse -Force
            }
        } catch {
            $rollbackErrors.Add($_.Exception.Message)
        }
        if ($rollbackErrors.Count -gt 0) {
            throw "Install failed: $($primaryFailure.Exception.Message). Rollback failed: $($rollbackErrors -join '; ')"
        }
        throw $primaryFailure
    }
}

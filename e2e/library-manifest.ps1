[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string] $Path,

    [Parameter(Mandatory)]
    [string] $OutputPath
)

$ErrorActionPreference = 'Stop'

function Get-NormalizedFullPath {
    param(
        [Parameter(Mandatory)]
        [string] $Candidate
    )

    [System.IO.Path]::GetFullPath($Candidate).TrimEnd(
        [System.IO.Path]::DirectorySeparatorChar,
        [System.IO.Path]::AltDirectorySeparatorChar
    )
}

function Test-SamePath {
    param(
        [Parameter(Mandatory)]
        [string] $Left,

        [Parameter(Mandatory)]
        [string] $Right
    )

    [string]::Equals(
        $Left,
        $Right,
        [System.StringComparison]::OrdinalIgnoreCase
    )
}

$candidatePath = Get-NormalizedFullPath -Candidate $Path
$protectedLibrary = Get-NormalizedFullPath -Candidate 'H:\Audiobooks'
$protectedPrefix = $protectedLibrary + [System.IO.Path]::DirectorySeparatorChar
$forbiddenPaths = @(
    (Get-NormalizedFullPath -Candidate 'H:\'),
    $protectedLibrary,
    (Get-NormalizedFullPath -Candidate 'C:\')
)
if (
    ($forbiddenPaths | Where-Object { Test-SamePath $_ $candidatePath }) -or
    $candidatePath.StartsWith(
        $protectedPrefix,
        [System.StringComparison]::OrdinalIgnoreCase
    )
) {
    throw 'Refusing to inventory a protected library path or filesystem root.'
}

$resolvedItem = Get-Item -LiteralPath $Path -Force
if (-not $resolvedItem.PSIsContainer) {
    throw 'Library manifest path must resolve to a directory.'
}
if (
    ($resolvedItem.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0
) {
    throw 'Refusing to inventory a source root that is a reparse point.'
}
$libraryPath = Get-NormalizedFullPath -Candidate $resolvedItem.FullName
$filesystemRoot = Get-NormalizedFullPath -Candidate (
    [System.IO.Path]::GetPathRoot($libraryPath)
)
if (Test-SamePath $libraryPath $filesystemRoot) {
    throw 'Refusing to inventory a filesystem root.'
}

$outputFullPath = [System.IO.Path]::GetFullPath($OutputPath)
$sourcePrefix = $libraryPath + [System.IO.Path]::DirectorySeparatorChar
if (
    (Test-SamePath $outputFullPath $libraryPath) -or
    $outputFullPath.StartsWith(
        $sourcePrefix,
        [System.StringComparison]::OrdinalIgnoreCase
    )
) {
    throw 'Manifest output must be outside the source tree.'
}

$records = foreach ($entry in Get-ChildItem -LiteralPath $libraryPath -Force -Recurse) {
    if (($entry.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "Refusing to traverse a reparse point: $($entry.FullName)"
    }
    $entryPath = Get-NormalizedFullPath -Candidate $entry.FullName
    if (-not $entryPath.StartsWith(
        $sourcePrefix,
        [System.StringComparison]::OrdinalIgnoreCase
    )) {
        throw 'Source entry escaped the selected library tree.'
    }
    $relativePath = $entryPath.Substring($sourcePrefix.Length).Replace('\', '/')
    if ($entry.PSIsContainer) {
        [pscustomobject][ordered]@{
            path = $relativePath
            type = 'directory'
            size = 0
            lastWriteUtc = $null
            sha256 = $null
        }
    } else {
        [pscustomobject][ordered]@{
            path = $relativePath
            type = 'file'
            size = [long] $entry.Length
            lastWriteUtc = $entry.LastWriteTimeUtc.ToString(
                'yyyy-MM-ddTHH:mm:ss.fffffffZ',
                [System.Globalization.CultureInfo]::InvariantCulture
            )
            sha256 = (
                Get-FileHash -Algorithm SHA256 -LiteralPath $entry.FullName
            ).Hash.ToUpperInvariant()
        }
    }
}

$json = @($records | Sort-Object -Property path) |
    ConvertTo-Json -Depth 4
$outputDirectory = Split-Path -Parent $outputFullPath
if (-not (Test-Path -LiteralPath $outputDirectory -PathType Container)) {
    throw 'Manifest output directory does not exist.'
}
[System.IO.File]::WriteAllText(
    $outputFullPath,
    $json + [Environment]::NewLine,
    [System.Text.UTF8Encoding]::new($false)
)

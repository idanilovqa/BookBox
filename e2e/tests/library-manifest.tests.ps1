[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$scriptPath = Join-Path (Split-Path -Parent $PSScriptRoot) 'library-manifest.ps1'
$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) (
    'netstorage-manifest-test-' + [Guid]::NewGuid().ToString('N')
)

function Assert-True {
    param(
        [Parameter(Mandatory)]
        [bool] $Condition,

        [Parameter(Mandatory)]
        [string] $Message
    )

    if (-not $Condition) {
        throw $Message
    }
}

function Invoke-Manifest {
    param(
        [Parameter(Mandatory)]
        [string] $LibraryPath,

        [Parameter(Mandatory)]
        [string] $OutputPath
    )

    & $scriptPath -Path $LibraryPath -OutputPath $OutputPath
}

try {
    $library = Join-Path $testRoot 'library'
    $nested = Join-Path $library 'Author One\Normal Book'
    $null = New-Item -ItemType Directory -Path $nested -Force
    [System.IO.File]::WriteAllBytes(
        (Join-Path $nested 'chapter01.mp3'),
        [byte[]](0, 1, 2, 3, 254, 255)
    )
    $output = Join-Path $testRoot 'manifest.json'

    Invoke-Manifest -LibraryPath $library -OutputPath $output

    $records = Get-Content -Raw -LiteralPath $output | ConvertFrom-Json
    Assert-True ($records.Count -eq 3) 'Expected two directories and one file.'
    Assert-True (
        ($records | ForEach-Object { $_.path }) -join '|' -eq
        'Author One|Author One/Normal Book|Author One/Normal Book/chapter01.mp3'
    ) 'Manifest records must use sorted slash-separated relative paths.'
    $file = $records | Where-Object { $_.type -eq 'file' }
    Assert-True ($file.size -eq 6) 'File size was not recorded.'
    Assert-True (
        $file.sha256 -eq
        '7EA646958715ED687AA9AC2F5D785FEB1A93411F4F25FDD6C7FCC6AB07FDF0E3'
    ) 'File SHA-256 was not recorded.'
    Assert-True (
        $file.lastWriteUtc -match 'Z$'
    ) 'Last-write time must be serialized as UTC.'
    $directory = $records | Where-Object { $_.path -eq 'Author One/Normal Book' }
    Assert-True (
        $null -eq $directory.lastWriteUtc
    ) 'Directory timestamps must not affect source-integrity manifests.'

    $beforeDirectoryTimestampChange = Get-Content -Raw -LiteralPath $output
    [System.IO.Directory]::SetLastWriteTimeUtc(
        $nested,
        [DateTime]::UtcNow.AddDays(-2)
    )
    $afterTimestampOutput = Join-Path $testRoot 'manifest-after-directory-time.json'
    Invoke-Manifest -LibraryPath $library -OutputPath $afterTimestampOutput
    $afterDirectoryTimestampChange = Get-Content -Raw -LiteralPath (
        $afterTimestampOutput
    )
    Assert-True (
        $beforeDirectoryTimestampChange -ceq $afterDirectoryTimestampChange
    ) 'Changing only a directory timestamp must not change the manifest.'

    $rootOutput = Join-Path $testRoot 'root.json'
    $rootRejected = $false
    try {
        Invoke-Manifest -LibraryPath (
            [System.IO.Path]::GetPathRoot($testRoot)
        ) -OutputPath $rootOutput
    } catch {
        $rootRejected = $_.Exception.Message -match 'filesystem root'
    }
    Assert-True $rootRejected 'Filesystem roots must be rejected explicitly.'

    $protectedRejected = $false
    try {
        Invoke-Manifest -LibraryPath 'H:\Audiobooks\NeverUseForE2E' `
            -OutputPath $rootOutput
    } catch {
        $protectedRejected = $_.Exception.Message -match 'protected library'
    }
    Assert-True $protectedRejected (
        'Every descendant of H:\Audiobooks must be rejected before resolution.'
    )

    $junctionTarget = Join-Path $testRoot 'junction-target'
    $junctionRoot = Join-Path $testRoot 'junction-root'
    $null = New-Item -ItemType Directory -Path $junctionTarget
    $junctionCreated = $false
    try {
        $null = New-Item -ItemType Junction -Path $junctionRoot `
            -Target $junctionTarget
        $junctionCreated = $true
    } catch {
        Write-Verbose 'Junction creation is unavailable; root-reparse runtime case skipped.'
    }
    if ($junctionCreated) {
        $junctionRejected = $false
        try {
            Invoke-Manifest -LibraryPath $junctionRoot -OutputPath $rootOutput
        } catch {
            $junctionRejected = $_.Exception.Message -match 'reparse point'
        }
        Assert-True $junctionRejected (
            'A source root that is itself a reparse point must be rejected.'
        )
    }

    $insideOutput = Join-Path $library 'manifest.json'
    $insideRejected = $false
    try {
        Invoke-Manifest -LibraryPath $library -OutputPath $insideOutput
    } catch {
        $insideRejected = $_.Exception.Message -match 'outside'
    }
    Assert-True $insideRejected 'Manifest output must stay outside the source tree.'

    Write-Output 'PASS: library manifest is deterministic and refuses unsafe roots.'
} finally {
    if (Test-Path -LiteralPath $testRoot) {
        $tempRoot = [System.IO.Path]::GetFullPath(
            [System.IO.Path]::GetTempPath()
        ).TrimEnd('\')
        $deleteTarget = [System.IO.Path]::GetFullPath($testRoot).TrimEnd('\')
        $deleteParent = [System.IO.Path]::GetDirectoryName($deleteTarget)
        $deleteName = [System.IO.Path]::GetFileName($deleteTarget)
        if (
            -not [string]::Equals(
                $deleteParent,
                $tempRoot,
                [System.StringComparison]::OrdinalIgnoreCase
            ) -or
            $deleteName -notmatch '^netstorage-manifest-test-[0-9a-f]{32}$'
        ) {
            throw "Refusing unsafe test cleanup target: $deleteTarget"
        }
        Remove-Item -LiteralPath $testRoot -Recurse -Force
    }
}

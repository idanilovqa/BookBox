[CmdletBinding()]
param(
    [string] $BaseUrl,
    [string] $Username = 'owner',
    [string] $Password,
    [string[]] $RootIds = @('audiobooks'),
    [string] $RawRootId = 'audiobooks',
    [string] $PreviousCompletedAt,
    [string] $ExpectedRawRelativePath,
    [ValidateRange(1, 300)]
    [int] $TimeoutSeconds = 120,
    [switch] $HarnessInvocation
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

if (-not $HarnessInvocation) {
    if (-not [string]::IsNullOrWhiteSpace($BaseUrl) -or
        -not [string]::IsNullOrWhiteSpace($Password)) {
        throw 'BaseUrl and Password are supplied only by the task-scoped E2E harness.'
    }
    $run = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\run.ps1'))
    & $run -CatalogSearchOnly -TimeoutSeconds $TimeoutSeconds
    if (-not $?) {
        throw 'The focused catalog-search E2E harness failed.'
    }
    return
}

if ([string]::IsNullOrWhiteSpace($BaseUrl) -or
    [string]::IsNullOrWhiteSpace($Password)) {
    throw 'HarnessInvocation requires BaseUrl and Password.'
}
if ($RootIds.Count -lt 1 -or $RootIds -contains '') {
    throw 'At least one nonempty audiobook root ID is required.'
}
if ($RootIds -notcontains $RawRootId) {
    throw 'RawRootId must be one of RootIds.'
}

function Invoke-BookBoxJson {
    param(
        [Parameter(Mandatory)][string] $Method,
        [Parameter(Mandatory)][string] $Uri,
        [string] $AccessToken,
        [object] $Body
    )
    $headers = @{}
    if (-not [string]::IsNullOrWhiteSpace($AccessToken)) {
        $headers.Authorization = "Bearer $AccessToken"
    }
    $parameters = @{
        Method = $Method
        Uri = $Uri
        Headers = $headers
        UseBasicParsing = $true
        TimeoutSec = 15
    }
    if ($null -ne $Body) {
        $parameters.ContentType = 'application/json'
        $parameters.Body = ($Body | ConvertTo-Json -Compress)
    }
    $response = Invoke-WebRequest @parameters
    if ([string]::IsNullOrWhiteSpace($response.Content)) {
        return $null
    }
    $response.Content | ConvertFrom-Json
}

function Assert-NoPrivatePath {
    param([Parameter(Mandatory)][object] $Value)
    $json = $Value | ConvertTo-Json -Depth 20 -Compress
    if ($json -match '(?i)([a-z]:[\\/]|/library/|\\\\[^\\]+\\)') {
        throw 'Search response failed the private-path confidentiality assertion.'
    }
}

function Get-OptionalJsonString {
    param(
        [Parameter(Mandatory)][object] $Value,
        [Parameter(Mandatory)][string] $Name
    )
    $property = $Value.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return ''
    }
    return [string] $property.Value
}

$base = $BaseUrl.TrimEnd('/')
$session = Invoke-BookBoxJson -Method POST -Uri "$base/v1/session" -Body @{
    username = $Username
    password = $Password
}
$accessToken = "$($session.accessToken)"
if ([string]::IsNullOrWhiteSpace($accessToken)) {
    throw 'Catalog-search E2E login returned no access token.'
}

$rootQuery = @($RootIds | ForEach-Object {
    'rootId=' + [Uri]::EscapeDataString($_)
}) -join '&'
$booksUri = "$base/v1/books?q=needlebox&limit=50&$rootQuery"
$rawUri = "$base/v1/browse/search?root=$([Uri]::EscapeDataString($RawRootId))&q=needlebox&limit=50"
try {
    $null = Invoke-BookBoxJson -Method GET -Uri $booksUri
    throw 'Unauthenticated audiobook search unexpectedly succeeded.'
} catch {
    if ($_.Exception.Message -eq 'Unauthenticated audiobook search unexpectedly succeeded.') {
        throw
    }
    $status = $_.Exception.Response.StatusCode.value__
    if ($status -notin @(401, 403)) {
        throw 'Unauthenticated audiobook search did not return an authentication error.'
    }
}
try {
    $null = Invoke-BookBoxJson -Method GET -Uri $rawUri
    throw 'Unauthenticated raw search unexpectedly succeeded.'
} catch {
    if ($_.Exception.Message -eq 'Unauthenticated raw search unexpectedly succeeded.') {
        throw
    }
    $status = $_.Exception.Response.StatusCode.value__
    if ($status -notin @(401, 403)) {
        throw 'Unauthenticated raw search did not return an authentication error.'
    }
}
$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
$nextProgress = [DateTime]::UtcNow
$books = $null
$raw = $null
do {
    $books = $null
    $raw = $null
    try {
        $books = Invoke-BookBoxJson -Method GET -Uri $booksUri -AccessToken $accessToken
        $raw = Invoke-BookBoxJson -Method GET -Uri $rawUri -AccessToken $accessToken
    } catch {
        if ($null -eq $_.Exception.Response -or
            $_.Exception.Response.StatusCode.value__ -ne 503) {
            throw
        }
    }
    if ($null -eq $books -or $null -eq $raw) {
        if ([DateTime]::UtcNow -ge $nextProgress) {
            Write-Host 'Waiting for bounded catalog-search projection assertions.'
            $nextProgress = [DateTime]::UtcNow.AddSeconds(30)
        }
        Start-Sleep -Seconds 1
        continue
    }
    $snapshotRoots = @($books.snapshots | ForEach-Object { $_.rootId })
    $duplicateGroups = @($books.groups | Where-Object {
        $_.duplicate -eq $true -and @($_.books).Count -eq 2
    })
    $duneFile = @($raw.entries | Where-Object {
        $_.kind -eq 'file' -and $_.name -eq 'Needlebox.m4b' -and
        $_.duplicateBook -eq $true
    })
    $expectedRaw = [string]::IsNullOrWhiteSpace($ExpectedRawRelativePath) -or
        @($raw.entries | Where-Object {
            $_.kind -eq 'file' -and $_.relativePath -eq $ExpectedRawRelativePath
        }).Count -eq 1
    $changed = [string]::IsNullOrWhiteSpace($PreviousCompletedAt) -or
        "$($raw.snapshot.completedAt)" -ne $PreviousCompletedAt
    if (
        $snapshotRoots.Count -eq $RootIds.Count -and
        @($RootIds | Where-Object { $snapshotRoots -notcontains $_ }).Count -eq 0 -and
        $duplicateGroups.Count -eq 1 -and
        $duneFile.Count -ge 1 -and
        $expectedRaw -and
        $changed
    ) {
        break
    }
    if ([DateTime]::UtcNow -ge $nextProgress) {
        Write-Host 'Waiting for bounded catalog-search projection assertions.'
        $nextProgress = [DateTime]::UtcNow.AddSeconds(30)
    }
    Start-Sleep -Seconds 1
} while ([DateTime]::UtcNow -lt $deadline)

if ($null -eq $books -or $null -eq $raw) {
    throw 'Catalog search did not become available before the bounded deadline.'
}
$snapshotRoots = @($books.snapshots | ForEach-Object { $_.rootId })
if ($snapshotRoots.Count -ne $RootIds.Count -or
    @($RootIds | Where-Object { $snapshotRoots -notcontains $_ }).Count -ne 0) {
    throw "Audiobook search did not return every requested root snapshot: $($snapshotRoots -join ', ')"
}
$sortedRoots = @($snapshotRoots | Sort-Object -Unique)
if (($snapshotRoots -join "`n") -cne ($sortedRoots -join "`n")) {
    throw 'Audiobook snapshots were not unique and sorted by root ID.'
}
$duplicateGroups = @($books.groups | Where-Object {
    $_.duplicate -eq $true -and @($_.books).Count -eq 2
})
if ($duplicateGroups.Count -ne 1) {
        throw "Expected exactly one two-copy duplicate group; got $($duplicateGroups.Count)."
}
$otherNarrator = @($books.groups | Where-Object {
    $_.duplicate -eq $false -and
        @($_.books | Where-Object { $_.title -eq 'Needlebox' -and $_.narrators -contains 'Other Narrator' }).Count -eq 1
})
if ($otherNarrator.Count -ne 1) {
    throw 'The different-narrator recording was not preserved as a singleton group.'
}
$outsideTolerance = @($books.groups | Where-Object {
    $_.duplicate -eq $false -and
    @($_.books | Where-Object {
        $_.title -eq 'Needlebox' -and $_.narrators -contains 'Shared Narrator' -and
        $_.sourceFolder -eq 'Frank Herbert/Needlebox Long Recording'
    }).Count -eq 1
})
if ($outsideTolerance.Count -ne 1) {
    throw 'The outside-duration-tolerance recording was not preserved as a singleton group.'
}
foreach ($group in @($books.groups)) {
    try {
        $null = [Guid]$group.groupId
        foreach ($book in @($group.books)) {
            $null = [Guid]$book.id
        }
    } catch {
        throw 'Audiobook search returned a malformed group or book ID.'
    }
    $groupRoots = @($group.books | ForEach-Object { $_.rootId } | Sort-Object -Unique)
    if ($groupRoots.Count -ne 1) {
        throw 'An audiobook duplicate group crossed configured roots.'
    }
}
$duneFiles = @($raw.entries | Where-Object {
    $_.kind -eq 'file' -and $_.name -eq 'Needlebox.m4b' -and
    $_.duplicateBook -eq $true
})
if ($duneFiles.Count -lt 1) {
    throw 'Raw search did not return a duplicate-marked Needlebox.m4b file.'
}
$nestedRawExpected = [ordered]@{
    'Needlebox Nested' = 'folder'
    'Needlebox Nested/Needlebox Assets' = 'folder'
    'Needlebox Nested/Needlebox Assets/cover.jpg' = 'file'
}
foreach ($expected in $nestedRawExpected.GetEnumerator()) {
    $matches = @($raw.entries | Where-Object {
        $_.relativePath -eq $expected.Key -and $_.kind -eq $expected.Value
    })
    if ($matches.Count -ne 1) {
        throw 'Raw search did not return the nested cover and every nonempty ancestor folder.'
    }
}
$rawIDs = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::Ordinal
)
foreach ($entry in @($raw.entries)) {
    try {
        $null = [Guid]$entry.id
    } catch {
        throw 'Raw search returned a malformed result ID.'
    }
    if (-not $rawIDs.Add("$($entry.id)")) {
        throw 'Raw search returned a duplicate result ID.'
    }
}
if (-not [string]::IsNullOrWhiteSpace($ExpectedRawRelativePath) -and
    @($raw.entries | Where-Object {
        $_.kind -eq 'file' -and $_.relativePath -eq $ExpectedRawRelativePath
    }).Count -ne 1) {
    throw 'Raw search did not return the expected monitored file.'
}
foreach ($excluded in @('notes.txt', 'Empty Folder')) {
    if (($raw | ConvertTo-Json -Depth 20 -Compress).Contains($excluded)) {
        throw "Raw search exposed excluded content: $excluded"
    }
}
if (-not [string]::IsNullOrWhiteSpace($PreviousCompletedAt) -and
    "$($raw.snapshot.completedAt)" -eq $PreviousCompletedAt) {
    throw 'The monitored refresh did not publish a changed projection timestamp.'
}
Assert-NoPrivatePath -Value $books
Assert-NoPrivatePath -Value $raw

$repeatBooks = Invoke-BookBoxJson -Method GET -Uri $booksUri -AccessToken $accessToken
$repeatRaw = Invoke-BookBoxJson -Method GET -Uri $rawUri -AccessToken $accessToken
$bookOrder = @($books.groups | ForEach-Object {
    "$($_.groupId):$(@($_.books | ForEach-Object { $_.id }) -join ',')"
}) -join "`n"
$repeatBookOrder = @($repeatBooks.groups | ForEach-Object {
    "$($_.groupId):$(@($_.books | ForEach-Object { $_.id }) -join ',')"
}) -join "`n"
$rawOrder = @($raw.entries | ForEach-Object { $_.id }) -join "`n"
$repeatRawOrder = @($repeatRaw.entries | ForEach-Object { $_.id }) -join "`n"
$booksCursor = Get-OptionalJsonString -Value $books -Name 'nextCursor'
$repeatBooksCursor = Get-OptionalJsonString -Value $repeatBooks -Name 'nextCursor'
$rawCursor = Get-OptionalJsonString -Value $raw -Name 'nextCursor'
$repeatRawCursor = Get-OptionalJsonString -Value $repeatRaw -Name 'nextCursor'
if ($bookOrder -cne $repeatBookOrder -or $rawOrder -cne $repeatRawOrder -or
    $booksCursor -cne $repeatBooksCursor -or
    $rawCursor -cne $repeatRawCursor) {
    throw 'Repeated catalog search changed group membership, result order, or cursor.'
}
Assert-NoPrivatePath -Value $repeatBooks
Assert-NoPrivatePath -Value $repeatRaw

$cursorBooksUri = "$base/v1/books?q=needlebox&limit=2&$rootQuery"
$cursorRawUri = "$base/v1/browse/search?root=$([Uri]::EscapeDataString($RawRootId))&q=needlebox&limit=2"
$cursorBooks = Invoke-BookBoxJson -Method GET -Uri $cursorBooksUri -AccessToken $accessToken
$cursorBooksRepeat = Invoke-BookBoxJson -Method GET -Uri $cursorBooksUri -AccessToken $accessToken
$cursorRaw = Invoke-BookBoxJson -Method GET -Uri $cursorRawUri -AccessToken $accessToken
$cursorRawRepeat = Invoke-BookBoxJson -Method GET -Uri $cursorRawUri -AccessToken $accessToken
$cursorBooksValue = Get-OptionalJsonString -Value $cursorBooks -Name 'nextCursor'
$cursorBooksRepeatValue = Get-OptionalJsonString -Value $cursorBooksRepeat -Name 'nextCursor'
$cursorRawValue = Get-OptionalJsonString -Value $cursorRaw -Name 'nextCursor'
$cursorRawRepeatValue = Get-OptionalJsonString -Value $cursorRawRepeat -Name 'nextCursor'
if ([string]::IsNullOrWhiteSpace($cursorBooksValue) -or
    [string]::IsNullOrWhiteSpace($cursorRawValue) -or
    $cursorBooksValue -cne $cursorBooksRepeatValue -or
    $cursorRawValue -cne $cursorRawRepeatValue) {
    throw 'Bounded catalog search did not return stable nonempty cursors.'
}
Assert-NoPrivatePath -Value $cursorBooks
Assert-NoPrivatePath -Value $cursorRaw

[pscustomobject]@{
    RawCompletedAt = "$($raw.snapshot.completedAt)"
    AudiobookSnapshotCount = $snapshotRoots.Count
    DuplicateGroupId = "$($duplicateGroups[0].groupId)"
}

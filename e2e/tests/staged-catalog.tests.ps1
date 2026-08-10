[CmdletBinding()]
param(
    [string] $BaseUrl,
    [string] $Username = 'owner',
    [string] $Password,
    [string] $ProjectName,
    [string] $EnvironmentFile,
    [string[]] $ComposeFiles,
    [string] $LibraryPath,
    [ValidateRange(1, 1800)]
    [int] $TimeoutSeconds = 180,
    [switch] $ResumeRefinement,
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
    & $run -StagedCatalogOnly -DeploymentMode Hybrid -TimeoutSeconds $TimeoutSeconds
    if (-not $?) { throw 'The focused staged-catalog E2E harness failed.' }
    return
}

if ([string]::IsNullOrWhiteSpace($BaseUrl) -or
    [string]::IsNullOrWhiteSpace($Password) -or
    [string]::IsNullOrWhiteSpace($ProjectName) -or
    [string]::IsNullOrWhiteSpace($EnvironmentFile) -or
    [string]::IsNullOrWhiteSpace($LibraryPath) -or
    $ComposeFiles.Count -lt 2) {
    throw 'HarnessInvocation requires the task-scoped API and Compose inputs.'
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$processRunner = Join-Path $repoRoot 'e2e\process-runner.ps1'
$manifestScript = Join-Path $repoRoot 'e2e\library-manifest.ps1'
foreach ($requiredPath in @($processRunner, $manifestScript)) {
    if (-not (Test-Path -LiteralPath $requiredPath -PathType Leaf)) {
        throw "Required staged-catalog helper is missing: $requiredPath"
    }
}
. $processRunner
$taskRoot = [IO.Path]::GetFullPath((Split-Path -Parent ([IO.Path]::GetFullPath($EnvironmentFile))))
$expectedTaskParent = [IO.Path]::GetFullPath((Join-Path ([IO.Path]::GetTempPath()) 'bookbox-e2e'))
$taskLeaf = Split-Path -Leaf $taskRoot
$taskParent = [IO.Path]::GetFullPath((Split-Path -Parent $taskRoot))
$expectedProjectName = "bookbox-e2e-$taskLeaf"
$expectedEnvironmentFile = [IO.Path]::GetFullPath((Join-Path $taskRoot 'compose.env'))
$libraryFullPath = [IO.Path]::GetFullPath($LibraryPath)
$expectedLibraryPath = [IO.Path]::GetFullPath((Join-Path $taskRoot 'library'))
$expectedComposeFiles = @(
    [IO.Path]::GetFullPath((Join-Path $repoRoot 'deploy\compose.yml'))
    [IO.Path]::GetFullPath((Join-Path $repoRoot 'deploy\compose.test.yml'))
)
$expectedHybridCompose = [IO.Path]::GetFullPath((Join-Path $repoRoot 'deploy\compose.hybrid.yml'))
$actualComposeFiles = @($ComposeFiles | ForEach-Object { [IO.Path]::GetFullPath($_) })
$safeBaseUrl = [string]::Equals(
    $BaseUrl.TrimEnd('/'),
    'http://127.0.0.1:18787',
    [StringComparison]::Ordinal
)
$safeTaskRoot = [string]::Equals($taskParent, $expectedTaskParent, [StringComparison]::OrdinalIgnoreCase) -and
    $taskLeaf -match '^[0-9a-f]{32}$'
$safeProject = [string]::Equals($ProjectName, $expectedProjectName, [StringComparison]::Ordinal)
$safeEnvironment = [string]::Equals(
    [IO.Path]::GetFullPath($EnvironmentFile),
    $expectedEnvironmentFile,
    [StringComparison]::OrdinalIgnoreCase
)
$safeLibrary = [string]::Equals(
    $libraryFullPath, $expectedLibraryPath, [StringComparison]::OrdinalIgnoreCase
)
$safeCompose = $actualComposeFiles.Count -in @(2, 3)
if ($safeCompose) {
    for ($index = 0; $index -lt $expectedComposeFiles.Count; $index++) {
        if (-not [string]::Equals(
            $actualComposeFiles[$index],
            $expectedComposeFiles[$index],
            [StringComparison]::OrdinalIgnoreCase
        )) {
            $safeCompose = $false
            break
        }
    }
}
if ($safeCompose -and $actualComposeFiles.Count -eq 3) {
    $safeCompose = [string]::Equals(
        $actualComposeFiles[2], $expectedHybridCompose, [StringComparison]::OrdinalIgnoreCase
    )
}
if (-not ($safeBaseUrl -and $safeTaskRoot -and $safeProject -and
    $safeEnvironment -and $safeLibrary -and $safeCompose)) {
    throw 'HarnessInvocation refused inputs outside the generated task-scoped E2E project.'
}
$primaryLibraryPath = Join-Path $libraryFullPath 'primary'
if (-not (Test-Path -LiteralPath $primaryLibraryPath -PathType Container)) {
    throw 'HarnessInvocation requires the disposable primary library root.'
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
        Method = $Method; Uri = $Uri; Headers = $headers
        UseBasicParsing = $true; TimeoutSec = 15
    }
    if ($null -ne $Body) {
        $parameters.ContentType = 'application/json'
        $parameters.Body = ($Body | ConvertTo-Json -Depth 20 -Compress)
    }
    $response = Invoke-WebRequest @parameters
    if ([string]::IsNullOrWhiteSpace($response.Content)) { return $null }
    $response.Content | ConvertFrom-Json
}

function Invoke-CatalogSql {
    param([Parameter(Mandatory)][string] $Sql)
    $arguments = @('compose', '--project-name', $ProjectName, '--env-file', $EnvironmentFile)
    foreach ($composeFile in $ComposeFiles) { $arguments += @('-f', $composeFile) }
    $arguments += @(
        'exec', '-T', '-e', 'PGOPTIONS=-c statement_timeout=25000',
        'postgres', 'psql', '-U', 'bookbox', '-d', 'bookbox',
        '-v', 'ON_ERROR_STOP=1', '-At', '-c', $Sql
    )
    $docker = (Get-Command docker -ErrorAction Stop).Source
    $result = Invoke-BookBoxCapturedProcess `
        -FilePath $docker `
        -Arguments $arguments `
        -WorkingDirectory $repoRoot `
        -TimeoutMilliseconds 30000
    if ($result.TimedOut -or $result.ExitCode -ne 0) {
        $detail = @($result.StandardOutput, $result.StandardError) |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
        throw "Task-scoped catalog query failed or timed out:`n$($detail -join [Environment]::NewLine)"
    }
    @($result.StandardOutput -split '\r?\n' | Where-Object {
        -not [string]::IsNullOrWhiteSpace($_)
    })
}

function Read-ManifestFiles {
    param([Parameter(Mandatory)][string] $Path)
    $records = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json
    $result = @{}
    foreach ($record in $records) {
        if ($record.type -ne 'file') { continue }
        if ($result.ContainsKey("$($record.path)")) {
            throw "Duplicate manifest path: $($record.path)"
        }
        $result["$($record.path)"] = $record
    }
    $result
}

function Assert-MovedFileManifest {
    param(
        [Parameter(Mandatory)][string] $BeforePath,
        [Parameter(Mandatory)][string] $AfterPath,
        [Parameter(Mandatory)][object[]] $Moves
    )
    $before = Read-ManifestFiles -Path $BeforePath
    $after = Read-ManifestFiles -Path $AfterPath
    $sources = @{}
    $destinations = @{}
    foreach ($move in $Moves) {
        $source = "$($move.sourcePath)"
        $destination = "$($move.destinationPath)"
        if (-not $before.ContainsKey($source) -or -not $after.ContainsKey($destination)) {
            throw "Planned move is absent from a physical manifest: $source -> $destination " +
                "(sourceBefore=$($before.ContainsKey($source)) destinationAfter=$($after.ContainsKey($destination)))"
        }
        if ($after.ContainsKey($source)) {
            throw "Planned source still exists after reorganization: $source"
        }
        $old = $before[$source]
        $new = $after[$destination]
        $expectedModified = ([DateTime]$move.expectedModifiedAt).ToUniversalTime()
        $oldModified = ([DateTime]$old.lastWriteUtc).ToUniversalTime()
        $newModified = ([DateTime]$new.lastWriteUtc).ToUniversalTime()
        $expectedMicros = $expectedModified.Ticks - ($expectedModified.Ticks % 10)
        $oldMicros = $oldModified.Ticks - ($oldModified.Ticks % 10)
        if ([int64]$old.size -ne [int64]$new.size -or
            [int64]$old.size -ne [int64]$move.expectedSize -or
            "$($old.sha256)" -cne "$($new.sha256)" -or
            $oldModified -ne $newModified -or $oldMicros -ne $expectedMicros) {
            throw "Moved bytes differ for $source -> $destination"
        }
        $sources[$source] = $true
        $destinations[$destination] = $true
    }
    foreach ($path in $before.Keys) {
        if ($sources.ContainsKey($path)) { continue }
        if (-not $after.ContainsKey($path) -or
            [int64]$before[$path].size -ne [int64]$after[$path].size -or
            "$($before[$path].sha256)" -cne "$($after[$path].sha256)" -or
            "$($before[$path].lastWriteUtc)" -cne "$($after[$path].lastWriteUtc)") {
            throw "Unplanned source file changed during reorganization: $path"
        }
    }
    foreach ($path in $after.Keys) {
        if (-not $destinations.ContainsKey($path) -and -not $before.ContainsKey($path)) {
            throw "Unplanned source file appeared during reorganization: $path"
        }
    }
}

function Assert-FileManifestUnchanged {
    param(
        [Parameter(Mandatory)][string] $BeforePath,
        [Parameter(Mandatory)][string] $AfterPath
    )
    $before = Read-ManifestFiles -Path $BeforePath
    $after = Read-ManifestFiles -Path $AfterPath
    if ($before.Count -ne $after.Count) {
        throw 'Rollback changed the number of physical source files.'
    }
    foreach ($path in $before.Keys) {
        if (-not $after.ContainsKey($path) -or
            [int64]$before[$path].size -ne [int64]$after[$path].size -or
            "$($before[$path].sha256)" -cne "$($after[$path].sha256)" -or
            "$($before[$path].lastWriteUtc)" -cne "$($after[$path].lastWriteUtc)") {
            throw "Rollback did not restore the original bytes and path: $path"
        }
    }
}

function Assert-Eventually {
    param(
        [Parameter(Mandatory)][string] $Description,
        [Parameter(Mandatory)][scriptblock] $Script
    )
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $nextProgress = [DateTime]::UtcNow
    do {
        try { if (& $Script) { return } } catch { if ([DateTime]::UtcNow -ge $deadline) { throw } }
        if ([DateTime]::UtcNow -ge $nextProgress) {
            Write-Host "Waiting for $Description."
            $nextProgress = [DateTime]::UtcNow.AddSeconds(30)
        }
        Start-Sleep -Seconds 1
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Timed out waiting for $Description."
}

function Assert-DatabaseCount {
    param(
        [Parameter(Mandatory)][ValidateSet(
            'source_files', 'books', 'book_grouping_evidence',
            'library_search_documents', 'worker_jobs'
        )][string] $Table,
        [Parameter(Mandatory)][int] $Minimum
    )
    $count = [int64] @(Invoke-CatalogSql "select count(*) from $Table;")[0]
    if ($count -lt $Minimum) { throw "$Table count $count is below $Minimum." }
}

function Assert-NoPrivatePath {
    param([Parameter(Mandatory)][object] $Value)
    $json = $Value | ConvertTo-Json -Depth 30 -Compress
    if ($json -match '(?i)([a-z]:[\\/]|/library/|\\\\[^\\]+\\)') {
        throw 'Catalog response exposed a private absolute source path.'
    }
}

$base = $BaseUrl.TrimEnd('/')
$session = Invoke-BookBoxJson -Method POST -Uri "$base/v1/session" -Body @{
    username = $Username; password = $Password
}
$accessToken = "$($session.accessToken)"
if ([string]::IsNullOrWhiteSpace($accessToken)) {
    throw 'Staged-catalog E2E login returned no access token.'
}

$provisionalGeneration = 0
if ($ResumeRefinement) {
    $provisionalBooks = $null
    $global:BookBoxProvisionalSummary = 'not observed'
    try {
        Assert-Eventually -Description 'provisional books while refinement claims are paused' -Script {
            $script:provisionalBooks = Invoke-BookBoxJson -Method GET `
                -Uri "$base/v1/books?rootId=audiobooks&limit=100" -AccessToken $accessToken
            $cards = @($script:provisionalBooks.groups | ForEach-Object { @($_.books) })
            $provisionalProof = @(Invoke-CatalogSql @'
select concat_ws('|',
  (select count(*) from worker_jobs
     where kind in ('probe_file','inspect_companion','regroup_scope')
       and payload->>'rootId'='audiobooks'
       and status='queued' and run_after > now() + interval '20 minutes'),
  (select count(*) from source_files
     where root_id='audiobooks' and missing_at is null
       and file_kind='audio' and probed_at is not null));
'@)[0] -split '\|'
            $provisionalPendingJobs = [int64]$provisionalProof[0]
            $provisionalCompletedAudioFiles = [int64]$provisionalProof[1]
            $databaseReview = @(Invoke-CatalogSql @'
select concat_ws('|',
  (select count(*) from books where root_id='audiobooks' and missing_at is null and review_state='needs_review'),
  (select count(*) from library_search_documents where root_id='audiobooks' and entity_kind='audiobook' and review_state='needs_review'));
'@)[0]
            $states = @($cards | Group-Object reviewState | ForEach-Object { "$($_.Name)=$($_.Count)" }) -join ','
            $summary = "v2 cards=$($cards.Count) states=[$states] paused=$provisionalPendingJobs completedProbes=$provisionalCompletedAudioFiles databaseReview=$databaseReview"
            if ($global:BookBoxProvisionalSummary -ne $summary) {
                $global:BookBoxProvisionalSummary = $summary
                Write-Host "Provisional state: $summary"
            }
            $cards.Count -gt 0 -and
                @($cards | Where-Object { $_.reviewState -eq 'needs_review' }).Count -gt 0 -and
                $provisionalPendingJobs -gt 0 -and $provisionalCompletedAudioFiles -eq 0
        }
    } catch {
        throw "$($_.Exception.Message) Last provisional state: $global:BookBoxProvisionalSummary"
    }
    Remove-Variable BookBoxProvisionalSummary -Scope Global -ErrorAction SilentlyContinue
    Assert-NoPrivatePath -Value $provisionalBooks
    Assert-DatabaseCount -Table source_files -Minimum 1
    Assert-DatabaseCount -Table books -Minimum 1
    $provisionalGeneration = [int64] @($provisionalBooks.snapshots | Where-Object {
        $_.rootId -eq 'audiobooks'
    })[0].catalogGeneration
    if ($provisionalGeneration -lt 1) {
        throw 'Provisional catalog did not publish a positive generation.'
    }
    $null = Invoke-CatalogSql @'
drop trigger if exists e2e_pause_catalog_refinement on worker_jobs;
drop function if exists e2e_pause_catalog_refinement();
update worker_jobs
set run_after=now(), updated_at=now()
where kind in ('probe_file','inspect_companion','regroup_scope') and status='queued';
'@
}

$books = $null
Assert-Eventually -Description 'refined audiobook cards and completed staged jobs' -Script {
    $script:books = Invoke-BookBoxJson -Method GET `
        -Uri "$base/v1/books?rootId=audiobooks&limit=100" -AccessToken $accessToken
    $cards = @($script:books.groups | ForEach-Object { @($_.books) })
    $remaining = [int64] @(Invoke-CatalogSql @'
select count(*) from worker_jobs
where kind in ('probe_file','inspect_companion','regroup_scope','publish_search')
  and payload->>'rootId'='audiobooks'
  and status in ('queued','running');
'@)[0]
    $refinedGeneration = [int64] @($script:books.snapshots | Where-Object {
        $_.rootId -eq 'audiobooks'
    })[0].catalogGeneration
    $cards.Count -gt 0 -and
        @($cards | Where-Object { $_.reviewState -eq 'needs_review' }).Count -gt 0 -and
        $remaining -eq 0 -and
        (-not $ResumeRefinement -or $refinedGeneration -gt $provisionalGeneration)
}
Assert-NoPrivatePath -Value $books
Assert-DatabaseCount -Table source_files -Minimum 1
Assert-DatabaseCount -Table books -Minimum 1
Assert-DatabaseCount -Table book_grouping_evidence -Minimum 1
Assert-DatabaseCount -Table library_search_documents -Minimum 1

$stageProof = @(Invoke-CatalogSql @'
select concat_ws('|',
    (select count(*) from source_files where root_id='audiobooks' and missing_at is null),
    (select count(*) from source_files where root_id='audiobooks' and missing_at is null and file_kind='audio'),
    (select count(*) from source_files where root_id='audiobooks' and missing_at is null and file_kind='cover'),
    (select count(*) from source_files where root_id='audiobooks' and missing_at is null and file_kind='companion'),
    (select count(*) from source_files where root_id='audiobooks' and missing_at is null and companion_kind in ('cue','nfo','opf','playlist')),
    (select count(*) from source_files where root_id='audiobooks' and missing_at is null and file_kind='audio' and probe_status='failed'),
    (select count(*) from source_files where root_id='audiobooks' and missing_at is null and file_kind='audio' and probe_version <> 1),
    (select generation from catalog_generations where root_id='audiobooks'),
    (select catalog_generation from library_search_snapshots where root_id='audiobooks'),
    (select count(*) from source_files sf where sf.root_id='audiobooks' and sf.missing_at is null and sf.file_kind='audio'
       and (select count(*) from book_files bf where bf.root_id=sf.root_id and bf.file_id=sf.id and bf.role='audio') <> 1),
    (select count(distinct bf.book_id) from source_files sf join book_files bf on bf.file_id=sf.id and bf.root_id=sf.root_id
       where sf.root_id='audiobooks' and sf.relative_path like 'Disc Saga/%' and sf.file_kind='audio'),
    (select count(distinct bf.book_id) from source_files sf join book_files bf on bf.file_id=sf.id and bf.root_id=sf.root_id
       where sf.root_id='audiobooks' and sf.relative_path like 'Mixed Shelf/%' and sf.file_kind='audio'),
    (select count(*) from books b join book_grouping_evidence e on e.book_id=b.id
       where b.root_id='audiobooks' and b.missing_at is null and e.reason_code='probe_failed'),
    (select count(distinct payload->>'fileId') from worker_jobs
       where kind='probe_file' and payload->>'rootId'='audiobooks' and status='succeeded'),
    (select count(distinct payload->>'fileId') from worker_jobs
       where kind='inspect_companion' and payload->>'rootId'='audiobooks' and status='succeeded'),
    (select count(*) from scan_runs where root_id='audiobooks' and status='succeeded')
);
'@)[0] -split '\|'

$activeFiles, $audioFiles, $covers, $companions, $parseableCompanions, $probeFailures,
    $wrongProbeVersions, $generation, $projectionGeneration,
    $unassignedAudio, $discBooks, $mixedBooks, $failedBooks,
    $probeJobs, $companionJobs, $completedInventories = @($stageProof | ForEach-Object { [int64] $_ })
if ($activeFiles -lt 12 -or $audioFiles -lt 7 -or $covers -lt 2 -or $companions -lt 5) {
    throw "Incomplete staged fixture: files=$activeFiles audio=$audioFiles covers=$covers companions=$companions."
}
if ($probeFailures -lt 1 -or $wrongProbeVersions -ne 0 -or $unassignedAudio -ne 0) {
    throw "Probe/assignment invariant failed: failures=$probeFailures wrongVersions=$wrongProbeVersions unassigned=$unassignedAudio."
}
if ($generation -lt 2 -or $projectionGeneration -ne $generation) {
    throw "Projection generation $projectionGeneration does not match catalog generation $generation."
}
if ($discBooks -ne 1 -or $mixedBooks -ne 2 -or $failedBooks -lt 1) {
    throw "Grouping refinement failed: discBooks=$discBooks mixedBooks=$mixedBooks failedBooks=$failedBooks."
}
if ($probeJobs -ne $audioFiles -or $companionJobs -ne $parseableCompanions -or $completedInventories -lt 1) {
    throw 'Staged job history does not prove inventory, probe, and companion inspection completion.'
}

$metadataProof = @(Invoke-CatalogSql @'
select concat_ws('|',
    (select count(*) from source_files
       where root_id='audiobooks'
         and relative_path in ('Disc Saga/Disc 1/chapter01.mp3','Disc Saga/Disc 2/chapter02.mp3')
         and media->>'title'='Disc Saga'
         and coalesce(nullif(media->>'author',''),media->>'artist')='Saga Author'
         and media->>'narrator'='Saga Narrator'
         and (media->>'durationMs')::bigint between 11500 and 12500),
    (select count(*) from books
       where root_id='audiobooks' and source_folder='Disc Saga' and missing_at is null
         and title='Disc Saga' and authors @> array['Saga Author']::text[]
         and narrators @> array['Saga Narrator']::text[]),
    (select count(*) from book_grouping_evidence
       where root_id='audiobooks' and reason_code='weak_loose_files')
);
'@)[0] -split '\|'
$refinedFiles, $refinedBooks, $weakLooseBooks = @($metadataProof | ForEach-Object { [int64] $_ })
if ($refinedFiles -ne 2 -or $refinedBooks -ne 1) {
    throw "Representative refined metadata is incomplete: files=$refinedFiles books=$refinedBooks."
}
if ($weakLooseBooks -lt 1) {
    throw 'The loose weak-audio fixture did not retain the weak_loose_files review reason.'
}

$provisionalOrdering = [int64] @(Invoke-CatalogSql @'
select count(*) from books b
where b.root_id='audiobooks' and exists (
    select 1 from book_files bf
    join source_files sf on sf.id=bf.file_id and sf.root_id=bf.root_id
    where bf.book_id=b.id and sf.file_kind='audio'
      and sf.probed_at is not null and b.created_at <= sf.probed_at
);
'@)[0]
if ($provisionalOrdering -lt 1) {
    throw 'No persisted provisional book predates completion of its media probe.'
}

foreach ($route in @(
    '/v1/books?q=saga&rootId=audiobooks',
    '/v1/browse/search?root=audiobooks&q=saga'
)) {
    Assert-NoPrivatePath -Value (Invoke-BookBoxJson -Method GET `
        -Uri ($base + $route) -AccessToken $accessToken)
}

# A second complete inventory over unchanged fingerprints must not enqueue
# another audio probe or companion inspection.
$beforeUnchanged = @(Invoke-CatalogSql @'
select concat_ws('|',
  (select count(*) from scan_runs where root_id='audiobooks' and status='succeeded'),
  (select count(*) from worker_jobs where kind='probe_file' and payload->>'rootId'='audiobooks'),
  (select count(*) from worker_jobs where kind='inspect_companion' and payload->>'rootId'='audiobooks')
);
'@)[0] -split '\|'
$scanCountBefore, $probeCountBefore, $companionCountBefore = @(
    $beforeUnchanged | ForEach-Object { [int64] $_ }
)
$null = Invoke-BookBoxJson -Method POST -Uri "$base/v1/catalog/rescan" `
    -AccessToken $accessToken -Body @{ rootId = 'audiobooks' }
Assert-Eventually -Description 'unchanged fingerprint inventory' -Script {
    $proof = @(Invoke-CatalogSql @'
select concat_ws('|',
  (select count(*) from scan_runs where root_id='audiobooks' and status='succeeded'),
  (select count(*) from worker_jobs
     where payload->>'rootId'='audiobooks'
       and kind in ('scan_root','probe_file','inspect_companion','regroup_scope','publish_search')
       and status in ('queued','running'))
);
'@)[0] -split '\|'
    [int64]$proof[0] -gt $scanCountBefore -and [int64]$proof[1] -eq 0
}
$afterUnchanged = @(Invoke-CatalogSql @'
select concat_ws('|',
  (select count(*) from worker_jobs where kind='probe_file' and payload->>'rootId'='audiobooks'),
  (select count(*) from worker_jobs where kind='inspect_companion' and payload->>'rootId'='audiobooks')
);
'@)[0] -split '\|'
if ([int64]$afterUnchanged[0] -ne $probeCountBefore -or
    [int64]$afterUnchanged[1] -ne $companionCountBefore) {
    throw 'An unchanged inventory queued duplicate probe or companion-inspection work.'
}

# Physical changes are confined to the generated task root. The checked-in
# and configured live libraries are never passed to this script. Split the
# merged disc set into two persisted groups and move all associated companions.
$discBook = @($books.groups | ForEach-Object { @($_.books) } | Where-Object {
    $_.sourceFolder -eq 'Disc Saga'
}) | Select-Object -First 1
if ($null -eq $discBook) {
    throw 'The disposable disc-set book is unavailable for reorganization.'
}
$context = Invoke-BookBoxJson -Method GET `
    -Uri "$base/v1/books/$($discBook.id)/grouping" -AccessToken $accessToken
Assert-NoPrivatePath -Value $context
if (-not $context.canReorganize) {
    throw 'The disposable root did not grant the explicit reorganization capability.'
}
$discAudio = @($context.files | Where-Object { $_.role -eq 'audio' } | Sort-Object relativePath)
$discCompanions = @($context.files | Where-Object { $_.role -ne 'audio' })
if ($discAudio.Count -ne 2 -or $discCompanions.Count -lt 1) {
    throw 'The disc-set context does not contain two audio files and associated companions.'
}
$firstGroupFileIDs = @("$($discAudio[0].id)") + @($discCompanions | ForEach-Object { "$($_.id)" })
$splitGroups = @(
    @{
        bookId = "$($discBook.id)"
        groupingKey = 'e2e-disc-one'
        fileIds = $firstGroupFileIDs
    },
    @{
        groupingKey = 'e2e-disc-two'
        fileIds = @("$($discAudio[1].id)")
    }
)
$splitTargets = [ordered]@{
    'e2e-disc-one' = 'E2E Reorganized/Disc One'
    'e2e-disc-two' = 'E2E Reorganized/Disc Two'
}
$manifestRoot = Split-Path -Parent $libraryFullPath
$successBefore = Join-Path $manifestRoot 'reorganization-success-before.json'
$successAfter = Join-Path $manifestRoot 'reorganization-success-after.json'
& $manifestScript -Path $primaryLibraryPath -OutputPath $successBefore
$currentGeneration = [int64]$context.catalogGeneration
$preview = Invoke-BookBoxJson -Method POST `
    -Uri "$base/v1/books/$($discBook.id)/reorganization/preview" `
    -AccessToken $accessToken `
    -Body @{
        catalogGeneration = $currentGeneration
        groups = $splitGroups
        targetFolders = $splitTargets
    }
Assert-NoPrivatePath -Value $preview
if ([string]::IsNullOrWhiteSpace("$($preview.operationId)") -or
    @($preview.moves).Count -ne @($context.files).Count -or
    @($preview.moves | Where-Object { $_.role -eq 'audio' }).Count -ne 2 -or
    @($preview.moves | Where-Object { $_.role -in @('cover', 'sidecar') }).Count -lt 1) {
    throw 'Split reorganization preview omitted disposable audio or associated companions.'
}
$null = Invoke-BookBoxJson -Method POST `
    -Uri "$base/v1/books/$($discBook.id)/reorganization/confirm" `
    -AccessToken $accessToken `
    -Body @{ operationId = "$($preview.operationId)" }
$operationID = ([Guid]$preview.operationId).ToString()
Assert-Eventually -Description 'journaled disposable split reorganization' -Script {
    $status = @(Invoke-CatalogSql "select status from reorganization_operations where id='$operationID';")
    $status.Count -eq 1 -and $status[0] -eq 'succeeded'
}
& $manifestScript -Path $primaryLibraryPath -OutputPath $successAfter
Assert-MovedFileManifest -BeforePath $successBefore -AfterPath $successAfter -Moves @($preview.moves)
$successProof = @(Invoke-CatalogSql @"
select concat_ws('|',
  (select count(*) from reorganization_moves m join source_files sf
     on sf.id=m.file_id and sf.root_id=m.root_id and sf.relative_path=m.destination_path
     where m.operation_id='$operationID' and m.state='moved' and sf.missing_at is null),
  (select count(distinct grouping_key) from books
     where root_id='audiobooks'
       and source_folder in ('E2E Reorganized/Disc One','E2E Reorganized/Disc Two')
       and missing_at is null
       and grouping_key in ('e2e-disc-one','e2e-disc-two')),
  (select count(*) from grouping_corrections
     where root_id='audiobooks'
       and source_folder in ('E2E Reorganized/Disc One','E2E Reorganized/Disc Two')
       and jsonb_array_length(groups)=1)
);
"@)[0] -split '\|'
if ([int64]$successProof[0] -ne @($preview.moves).Count -or
    [int64]$successProof[1] -ne 2 -or [int64]$successProof[2] -ne 2) {
    throw 'Successful reorganization did not persist every path and split grouping.'
}
Assert-Eventually -Description 'post-reorganization search publication' -Script {
    $updated = Invoke-BookBoxJson -Method GET `
        -Uri "$base/v1/books?rootId=audiobooks&limit=100" -AccessToken $accessToken
    $updatedCards = @($updated.groups | ForEach-Object { @($_.books) })
    @($updated.snapshots | Where-Object {
        $_.rootId -eq 'audiobooks' -and [int64]$_.catalogGeneration -gt $currentGeneration
    }).Count -eq 1 -and
        @($updatedCards | Where-Object {
            $_.sourceFolder -in @(
                'E2E Reorganized/Disc One',
                'E2E Reorganized/Disc Two'
            )
        }).Count -eq 2
}

# Force the journal write for the second completed move to fail. The mover has
# changed two disposable paths at that point, so both must be physically undone.
$normalBook = @($books.groups | ForEach-Object { @($_.books) } | Where-Object {
    $_.sourceFolder -eq 'Author One/Normal Book'
}) | Select-Object -First 1
if ($null -eq $normalBook) { throw 'Normal-book rollback fixture is unavailable.' }
$rollbackContext = Invoke-BookBoxJson -Method GET `
    -Uri "$base/v1/books/$($normalBook.id)/grouping" -AccessToken $accessToken
Assert-NoPrivatePath -Value $rollbackContext
$rollbackBook = @($rollbackContext.books | Where-Object {
    $_.bookId -eq $normalBook.id
}) | Select-Object -First 1
if ($null -eq $rollbackBook -or @($rollbackBook.fileIds).Count -lt 2) {
    throw 'Rollback fixture does not contain at least two files.'
}
$rollbackPreview = Invoke-BookBoxJson -Method POST `
    -Uri "$base/v1/books/$($normalBook.id)/reorganization/preview" `
    -AccessToken $accessToken `
    -Body @{
        catalogGeneration = [int64]$rollbackContext.catalogGeneration
        groups = @(@{
            bookId = "$($normalBook.id)"
            groupingKey = 'e2e-rollback-normal'
            fileIds = @($rollbackBook.fileIds)
        })
        targetFolders = @{ 'e2e-rollback-normal' = 'E2E Rollback/Normal Book' }
    }
Assert-NoPrivatePath -Value $rollbackPreview
if (@($rollbackPreview.moves).Count -lt 2) {
    throw 'Rollback preview did not contain at least two disposable moves.'
}
$rollbackBefore = Join-Path $manifestRoot 'reorganization-rollback-before.json'
$rollbackAfter = Join-Path $manifestRoot 'reorganization-rollback-after.json'
& $manifestScript -Path $primaryLibraryPath -OutputPath $rollbackBefore
$null = Invoke-CatalogSql @'
create or replace function e2e_fail_second_move_journal()
returns trigger language plpgsql as $$
begin
  if new.move_order=2 and new.state='moved' then
    raise exception 'e2e forced mid-move journal failure';
  end if;
  return new;
end
$$;
drop trigger if exists e2e_fail_second_move_journal on reorganization_moves;
create trigger e2e_fail_second_move_journal
before update on reorganization_moves
for each row execute function e2e_fail_second_move_journal();
'@
$rollbackOperationID = ([Guid]$rollbackPreview.operationId).ToString()
try {
    $null = Invoke-BookBoxJson -Method POST `
        -Uri "$base/v1/books/$($normalBook.id)/reorganization/confirm" `
        -AccessToken $accessToken `
        -Body @{ operationId = $rollbackOperationID }
    Assert-Eventually -Description 'forced mid-move rollback' -Script {
        $status = @(Invoke-CatalogSql "select status from reorganization_operations where id='$rollbackOperationID';")
        $status.Count -eq 1 -and $status[0] -eq 'rolled_back'
    }
} finally {
    $null = Invoke-CatalogSql @'
drop trigger if exists e2e_fail_second_move_journal on reorganization_moves;
drop function if exists e2e_fail_second_move_journal();
'@
}
& $manifestScript -Path $primaryLibraryPath -OutputPath $rollbackAfter
Assert-FileManifestUnchanged -BeforePath $rollbackBefore -AfterPath $rollbackAfter
$rolledBack = [int64] @(Invoke-CatalogSql (
    "select count(*) from reorganization_moves where operation_id='$rollbackOperationID' and state='rolled_back';"
))[0]
if ($rolledBack -lt 2) {
    throw "Forced mid-move failure rolled back only $rolledBack moves."
}
$missingRollbackSources = [int64] @(Invoke-CatalogSql (
    "select count(*) from reorganization_moves m where m.operation_id='$rollbackOperationID' " +
    "and not exists (select 1 from source_files sf where sf.id=m.file_id and sf.relative_path=m.source_path and sf.missing_at is null);"
))[0]
if ($missingRollbackSources -ne 0) {
    throw 'Rollback did not preserve every original catalog source path.'
}

$finalGenerations = @(Invoke-CatalogSql @'
select concat_ws('|',
  (select generation from catalog_generations where root_id='audiobooks'),
  (select catalog_generation from library_search_snapshots where root_id='audiobooks')
);
'@)[0] -split '\|'
[pscustomobject]@{
    ActiveFiles = $activeFiles
    ActiveBooks = [int64] @(Invoke-CatalogSql "select count(*) from books where root_id='audiobooks' and missing_at is null;")[0]
    CatalogGeneration = [int64]$finalGenerations[0]
    ProjectionGeneration = [int64]$finalGenerations[1]
    ReorganizationOperationId = $operationID
    RollbackOperationId = $rollbackOperationID
}

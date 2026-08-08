# Staged Audiobook Catalog and Shared Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Populate the Audiobooks screen from a fast PostgreSQL inventory, refine metadata and grouping through resumable background jobs, serve both search modes from one indexed projection, and let users review or explicitly reorganize uncertain groups.

**Architecture:** A root inventory records file fingerprints without probing media and publishes provisional books plus a complete search projection. File-probe and grouping jobs update PostgreSQL, advance a catalog generation, and coalesce later projection publication. Android reads PostgreSQL-backed APIs, shows uncertain books with a red review action, and offers audited source reorganization only when the server grants that capability.

**Tech Stack:** Go 1.26.5, PostgreSQL 18 with `pg_trgm`, pgx v5, ffprobe, fsnotify, Kotlin, coroutines/Flow, Jetpack ViewModel, Jetpack Compose, OkHttp, kotlinx.serialization, JUnit.

## Global Constraints

- Search and audiobook-list requests must not read configured source roots.
- A completed inventory must make provisional cards visible before file-probe jobs finish.
- The regression suite must cover the observed 22,824-audio-file scale with generated records or the configured read-only library.
- PostgreSQL stores file fingerprints, probe state, evidence, assignments, corrections, catalog generations, and reorganization journals.
- A failed inventory must not mark unseen files or books missing.
- Unchanged fingerprints must not queue a new probe.
- Every active recognized audio file must belong to one visible provisional or confirmed book.
- Duplicate-recording identity remains equal normalized title, nonempty author identity, nonempty narrator identity, and duration within `min(5 minutes, max(2 minutes, 0.5 percent))`.
- Search uses the latest completed projection and retains the previous projection after failed inventory, refinement, or publication.
- Android submits text search after at least two Unicode letters or digits and uses a 150-millisecond debounce.
- Warm server search p95 must stay below 50 milliseconds for 100,000 documents; cold search must stay below 150 milliseconds.
- Physical reorganization requires `allow_library_reorganization: true`, an authenticated capability, a current preview, and writable source access.
- Reorganization must not overwrite, cross roots, traverse symlinks, or expose absolute source paths.
- Run one test command at a time. Do not overlap Go, Gradle, Compose, PostgreSQL, or E2E test processes.
- Preserve the user's existing changes under `deploy/scripts/` unless a task names the same file.

## File Map

- `server/migrations/0003_library_search.sql`: shared search projection, normalization helpers, and indexes.
- `server/migrations/0004_staged_catalog.sql`: fingerprints, probe state, evidence, generations, job kinds, and reorganization journals.
- `server/internal/catalog/inventory.go`: inventory domain types and fingerprint comparisons.
- `server/internal/catalog/search_projection.go`: search documents and duplicate classification.
- `server/internal/catalog/search_postgres.go`: atomic projection publication and indexed queries.
- `server/internal/catalog/reorganization.go`: preview and durable move-plan domain types.
- `server/internal/scanner/inventory.go`: fast filesystem inventory with no media probe.
- `server/internal/grouping/evidence.go`: normalized grouping evidence and subtree inference.
- `server/internal/companion/parser.go`: bounded CUE, NFO, OPF, and playlist evidence extraction.
- `server/internal/worker/lease.go`: lease renewal around long-running handlers.
- `server/internal/worker/worker.go`: inventory, probe, regroup, projection, cover, and move orchestration.
- `server/internal/monitor/root_monitor.go`: debounced root-relative reconciliation events.
- `server/internal/httpapi/library_search.go`: shared search response validation and shaping.
- `server/internal/httpapi/reorganization.go`: capability, preview, and confirmation routes.
- `android/app/src/main/java/com/rendref/bookbox/core/search/DebouncedSearch.kt`: shared search behavior.
- `android/app/src/main/java/com/rendref/bookbox/feature/library/DuplicateBookGroupCard.kt`: duplicate presentation.
- `android/app/src/main/java/com/rendref/bookbox/feature/book/GroupingScreen.kt`: evidence, companion assignment, confirmation, and move preview.

---

### Task 1: Create Search and Staged-Catalog Migrations

**Files:**
- Create: `server/migrations/0003_library_search.sql`
- Create: `server/migrations/0004_staged_catalog.sql`
- Modify: `server/internal/database/migrate_test.go`

**Interfaces:**
- Produces: `catalog_search_compact(text) returns text` and `catalog_search_grams(text) returns text[]`.
- Produces: `library_search_snapshots`, `library_search_documents`, `catalog_generations`, `book_grouping_evidence`, `reorganization_operations`, and `reorganization_moves`.
- Extends: `source_files` with classification and probe state; `worker_jobs.kind` with staged job kinds.

- [ ] **Step 1: Write the failing migration tests**

Add migrations 0003 and 0004 to the expected migration list and add:

```go
func TestMigrateCreatesStagedCatalogAndSearchSchema(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil { t.Fatal(err) }
	for _, relation := range []string{
		"library_search_snapshots", "library_search_documents",
		"catalog_generations", "book_grouping_evidence",
		"reorganization_operations", "reorganization_moves",
		"library_search_documents_trgm", "library_search_documents_grams",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`select to_regclass('public.' || $1) is not null`, relation,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("relation %s exists=%v err=%v", relation, exists, err)
		}
	}
	var columns int
	if err := pool.QueryRow(ctx, `
		select count(*) from information_schema.columns
		where table_name='source_files'
		  and column_name in (
		    'file_kind','companion_kind','probe_status','probe_version',
		    'probe_error','probed_at'
		  )
	`).Scan(&columns); err != nil || columns != 6 {
		t.Fatalf("source file staged columns=%d err=%v", columns, err)
	}
}
```

- [ ] **Step 2: Run the database test and confirm RED**

Run: `cd server; go test ./internal/database -run TestMigrateCreatesStagedCatalogAndSearchSchema -v`

Expected: FAIL because migrations 0003 and 0004 do not exist.

- [ ] **Step 3: Implement migration 0003**

Create `pg_trgm`, immutable compact/gram functions, and these projection tables:

```sql
create table library_search_snapshots (
    root_id text primary key references library_roots(id),
    scan_run_id uuid not null,
    catalog_generation bigint not null check (catalog_generation > 0),
    completed_at timestamptz not null,
    document_count integer not null check (document_count >= 0),
    foreign key (scan_run_id, root_id) references scan_runs(id, root_id)
);

create table library_search_documents (
    root_id text not null references library_roots(id),
    document_id uuid not null,
    entity_kind text not null check (entity_kind in ('audiobook','folder','file')),
    book_id uuid references books(id),
    relative_path text not null,
    display_name text not null,
    size bigint check (size is null or size >= 0),
    modified_at timestamptz,
    title text not null default '',
    authors text[] not null default '{}',
    series text not null default '',
    narrators text[] not null default '{}',
    filenames text[] not null default '{}',
    duration_ms bigint check (duration_ms is null or duration_ms >= 0),
    review_state text not null,
    review_reason text,
    duplicate_group_id uuid,
    duplicate_count integer not null default 1 check (duplicate_count >= 1),
    compact_name text not null,
    compact_path text not null,
    compact_metadata text not null,
    search_compact text not null,
    search_terms text not null,
    search_grams text[] not null,
    primary key (root_id, document_id),
    check (relative_path = '' or catalog_valid_relative_path(relative_path)),
    check ((entity_kind='audiobook' and book_id is not null) or entity_kind<>'audiobook'),
    check ((review_state='needs_review') = (review_reason is not null))
);
```

`catalog_search_compact` lowercases Unicode letters/digits and removes other characters. `catalog_search_grams` returns sorted unique two- and three-character windows. Include these indexes:

```sql
create index library_search_documents_trgm
    on library_search_documents using gin (search_compact gin_trgm_ops);
create index library_search_documents_grams
    on library_search_documents using gin (search_grams);
create index library_search_documents_page
    on library_search_documents(
        root_id, entity_kind, display_name, relative_path, document_id
    );
```

`catalog_generation` replaces the search-only plan's assumption that one scan run owns every publication.

- [ ] **Step 4: Implement migration 0004**

Use explicit constraints so future migrations can address them by name:

```sql
alter table source_files
    add column file_kind text not null default 'audio'
        constraint source_files_file_kind_check
        check (file_kind in ('audio','cover','companion')),
    add column companion_kind text
        constraint source_files_companion_kind_check
        check (companion_kind is null or companion_kind in (
            'cue','nfo','opf','playlist','ebook','other'
        )),
    add column probe_status text not null default 'pending'
        constraint source_files_probe_status_check
        check (probe_status in ('not_applicable','pending','succeeded','failed')),
    add column probe_version integer not null default 0 check (probe_version >= 0),
    add column probe_error text,
    add column probed_at timestamptz;

create table catalog_generations (
    root_id text primary key references library_roots(id),
    generation bigint not null default 1 check (generation > 0),
    inventory_scan_run_id uuid,
    updated_at timestamptz not null default now(),
    foreign key (inventory_scan_run_id, root_id) references scan_runs(id, root_id)
);

create table book_grouping_evidence (
    book_id uuid primary key references books(id) on delete cascade,
    root_id text not null,
    reason_code text not null check (reason_code in (
        'folder_default','consistent_subtree','distinct_identity',
        'conflicting_title','conflicting_author','weak_loose_files',
        'probe_failed','manual','reorganization_incomplete'
    )),
    evidence jsonb not null check (jsonb_typeof(evidence)='object'),
    updated_at timestamptz not null default now(),
    foreign key (book_id, root_id) references books(id, root_id) on delete cascade
);

alter table worker_jobs drop constraint worker_jobs_kind_check;
alter table worker_jobs add constraint worker_jobs_kind_check check (kind in (
    'scan_root','probe_file','inspect_companion','regroup_scope','publish_search',
    'enrich_book','publish_cover','reorganize_files'
));
```

Create the durable move journal:

```sql
create table reorganization_operations (
    id uuid primary key,
    root_id text not null references library_roots(id),
    book_id uuid not null,
    catalog_generation bigint not null check (catalog_generation > 0),
    actor text not null check (btrim(actor) <> ''),
    status text not null check (status in (
        'previewed','queued','running','succeeded','rolled_back','needs_attention'
    )),
    last_error text,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    foreign key (book_id, root_id) references books(id, root_id)
);

create table reorganization_moves (
    operation_id uuid not null references reorganization_operations(id) on delete cascade,
    move_order integer not null check (move_order >= 0),
    file_id uuid not null,
    root_id text not null,
    source_path text not null check (catalog_valid_relative_path(source_path)),
    destination_path text not null check (catalog_valid_relative_path(destination_path)),
    role text not null check (role in ('audio','cover','sidecar')),
    expected_size bigint not null check (expected_size >= 0),
    expected_modified_at timestamptz not null,
    state text not null check (state in ('planned','moved','rolled_back','failed')),
    last_error text,
    primary key (operation_id, move_order),
    unique (operation_id, source_path),
    unique (operation_id, destination_path),
    foreign key (file_id, root_id) references source_files(id, root_id)
);
```

- [ ] **Step 5: Run migration tests**

Run: `cd server; go test ./internal/database -v`

Expected: PASS with four migrations and every required relation, column, function, and index present.

- [ ] **Step 6: Commit**

```powershell
git add server/migrations/0003_library_search.sql server/migrations/0004_staged_catalog.sql server/internal/database/migrate_test.go
git commit -m "feat(catalog): add staged catalog and search schema"
```

---

### Task 2: Recover and Renew Worker Leases

**Files:**
- Modify: `server/internal/catalog/repository.go`
- Modify: `server/internal/catalog/postgres.go`
- Modify: `server/internal/catalog/postgres_test.go`
- Create: `server/internal/worker/lease.go`
- Create: `server/internal/worker/lease_test.go`
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/worker/worker_test.go`

**Interfaces:**
- Produces: `RecoverExpiredJobs(context.Context, time.Time) error`.
- Produces: `RenewJob(context.Context, uuid.UUID, uuid.UUID, time.Time) (time.Time, error)`.
- Produces: `runWithLease(context.Context, LeaseRepository, WorkerJob, time.Duration, func(context.Context) error) error`.

- [ ] **Step 1: Write failing repository tests for the live stuck-job state**

```go
func TestRecoverExpiredJobsRunsBeforeStartupDeduplication(t *testing.T) {
	repository, pool := workerJobFixture(t)
	job := expiredRunningJob(t, pool, JobScanRoot, 4, 5)
	now := time.Now().UTC()
	if err := repository.RecoverExpiredJobs(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.QueryRow(context.Background(),
		`select status from worker_jobs where id=$1`, job.ID,
	).Scan(&status); err != nil { t.Fatal(err) }
	if status != JobDead { t.Fatalf("status=%q", status) }
	enqueued, err := repository.EnqueueDeduplicated(context.Background(), WorkerJob{
		ID: uuid.New(), Kind: JobScanRoot, Payload: map[string]any{"rootId":"audiobooks"},
	}, "scan_root:audiobooks")
	if err != nil || enqueued.ID == job.ID { t.Fatalf("job=%#v err=%v", enqueued, err) }
}

func TestRenewJobExtendsOnlyCurrentLease(t *testing.T) {
	repository, _ := workerJobFixture(t)
	claimed := enqueueAndClaim(t, repository)
	next, err := repository.RenewJob(context.Background(), claimed.ID,
		claimed.LeaseToken, time.Now().UTC())
	if err != nil || !next.After(*claimed.LeaseUntil) { t.Fatalf("next=%v err=%v", next, err) }
	if _, err := repository.RenewJob(context.Background(), claimed.ID,
		uuid.New(), time.Now().UTC()); !errors.Is(err, ErrConflict) { t.Fatal(err) }
}
```

- [ ] **Step 2: Run repository tests and confirm RED**

Run: `cd server; go test ./internal/catalog -run 'TestRecoverExpiredJobs|TestRenewJob' -v`

Expected: FAIL because both repository methods are missing.

- [ ] **Step 3: Implement recovery and renewal**

Move expired-running cleanup out of `ClaimJob` into `RecoverExpiredJobs`. Increment attempts once. Queue a retry when attempts remain; mark the job dead otherwise. `RenewJob` must update `lease_until` only when status is `running`, the lease token matches, and the old lease has not expired.

```go
type LeaseRepository interface {
	RenewJob(context.Context, uuid.UUID, uuid.UUID, time.Time) (time.Time, error)
}
```

Call `RecoverExpiredJobs` before `scheduleScans` in `Worker.Run`.

- [ ] **Step 4: Write failing lease-runner tests**

Use a fake clock/ticker. Assert renewal before expiry, cancellation after renewal conflict, no goroutine leak after handler completion, and unchanged handler errors.

- [ ] **Step 5: Run lease-runner tests and confirm RED**

Run: `cd server; go test ./internal/worker -run TestRunWithLease -v`

Expected: FAIL because `runWithLease` does not exist.

- [ ] **Step 6: Implement the lease runner and worker integration**

Renew every five minutes for the 15-minute lease. Cancel the handler context on renewal failure. Wait for the renewal goroutine before returning. Replace the direct `runWithGrace` handler call with `runWithLease` while retaining the shutdown grace period around the operation context.

- [ ] **Step 7: Run worker and catalog tests**

Run: `cd server; go test ./internal/catalog ./internal/worker -v`

Expected: PASS, including the expired fifth-attempt scan regression.

- [ ] **Step 8: Commit**

```powershell
git add server/internal/catalog server/internal/worker
git commit -m "fix(worker): recover and renew catalog jobs"
```

---

### Task 3: Replace Media-Probing Scan with Fast Inventory

**Files:**
- Create: `server/internal/scanner/inventory.go`
- Create: `server/internal/scanner/inventory_test.go`
- Modify: `server/internal/scanner/scanner.go`
- Modify: `server/internal/scanner/scanner_test.go`

**Interfaces:**
- Produces: `Inventory.Scan(context.Context, config.Root, Scope) (InventoryResult, error)`.
- Produces: `InventoryFile` with ID, root, path, size, modified time, file kind, and companion kind.
- Preserves: hidden/excluded filtering, symlink confinement, race handling, and stable file IDs.

- [ ] **Step 1: Write failing fast-inventory tests**

```go
func TestInventoryRecognizesAudioCoverAndGroupingCompanionsWithoutProbe(t *testing.T) {
	root := inventoryFixture(t, map[string]string{
		"Author/Book/01.mp3": "audio",
		"Author/Book/cover.jpg": "image",
		"Author/Book/book.cue": "cue",
		"Author/Book/book.nfo": "nfo",
		"Author/Book/book.opf": "opf",
		"Author/Book/book.m3u": "playlist",
		"Author/Book/book.epub": "ebook",
		"Author/Book/tool.exe": "ignored",
	})
	result, err := NewInventory(Options{}).Scan(context.Background(), root, FullScope())
	if err != nil { t.Fatal(err) }
	assertInventoryKinds(t, result.Files, map[string]string{
		"Author/Book/01.mp3":"audio", "Author/Book/cover.jpg":"cover",
		"Author/Book/book.cue":"companion:cue", "Author/Book/book.nfo":"companion:nfo",
		"Author/Book/book.opf":"companion:opf", "Author/Book/book.m3u":"companion:playlist",
		"Author/Book/book.epub":"companion:ebook",
	})
}
```

Add a `panicProbe` to the old scanner fixture and prove inventory never calls it. Cover partial traversal, path-scoped inventory, hidden paths, exclusions, changing files, and symlink escapes.

- [ ] **Step 2: Run inventory tests and confirm RED**

Run: `cd server; go test ./internal/scanner -run TestInventory -v`

Expected: FAIL because `Inventory`, `Scope`, and companion classification do not exist.

- [ ] **Step 3: Implement inventory types and classification**

```go
type Scope struct { RelativePath string; Full bool }
func FullScope() Scope { return Scope{Full: true} }
func PathScope(relative string) (Scope, error)

type InventoryFile struct {
	ID uuid.UUID
	RootID, RelativePath, Name, Folder string
	Kind FileKind
	CompanionKind string
	Size int64
	ModifiedAt time.Time
}

type InventoryResult struct {
	RootID string
	Scope Scope
	Files []InventoryFile
	Warnings []Warning
}
```

Recognize the existing audio/cover extensions plus `.cue`, `.nfo`, `.opf`, `.m3u`, `.m3u8`, `.epub`, `.fb2`, `.pdf`, `.mobi`, `.azw`, and `.azw3`. Do not recognize executables, DVD files, or generic text as grouping companions.

- [ ] **Step 4: Remove probing from root traversal**

Keep `scanner.New(probe, options)` temporarily for file-probe job compatibility, but route root scans through `NewInventory`. Move stable ID, exclusion, hidden-path, and classification helpers into shared package-private functions. Delete metadata/probe fields from the inventory result.

- [ ] **Step 5: Run scanner tests**

Run: `cd server; go test ./internal/scanner -v`

Expected: PASS. The fast inventory fixture must finish without invoking any media probe.

- [ ] **Step 6: Commit**

```powershell
git add server/internal/scanner
git commit -m "refactor(scanner): inventory library without media probes"
```

---

### Task 4: Persist Fingerprints and Queue Changed Files

**Files:**
- Create: `server/internal/catalog/inventory.go`
- Create: `server/internal/catalog/inventory_test.go`
- Modify: `server/internal/catalog/model.go`
- Modify: `server/internal/catalog/repository.go`
- Modify: `server/internal/catalog/postgres.go`
- Modify: `server/internal/catalog/postgres_test.go`

**Interfaces:**
- Produces: `ApplyInventory(context.Context, InventoryUpdate) (InventoryDelta, error)`.
- Produces: `IncrementCatalogGeneration(context.Context, string) (CatalogGeneration, error)`.

- [ ] **Step 1: Write failing delta tests**

```go
func TestApplyInventoryQueuesProbeOnlyForNewOrChangedAudio(t *testing.T) {
	repository := inventoryRepositoryFixture(t,
		storedAudio("same.mp3", 10, instant(1), ProbeSucceeded),
		storedAudio("changed.mp3", 10, instant(1), ProbeSucceeded),
	)
	delta, err := repository.ApplyInventory(context.Background(), InventoryUpdate{
		ScanRunID: uuid.New(), RootID: "audiobooks", Complete: true,
		Files: []InventoryFile{
			inventoryAudio("same.mp3", 10, instant(1)),
			inventoryAudio("changed.mp3", 11, instant(2)),
			inventoryAudio("new.mp3", 12, instant(3)),
		},
	})
	if err != nil { t.Fatal(err) }
	assertPaths(t, delta.ProbeFiles, "changed.mp3", "new.mp3")
	assertPaths(t, delta.UnchangedFiles, "same.mp3")
}
```

Add tests proving partial inventory does not mark unseen rows missing, full inventory does, restored IDs stay stable, changed companions queue bounded inspection, and changed audio clears stale probe metadata.

- [ ] **Step 2: Run inventory repository tests and confirm RED**

Run: `cd server; go test ./internal/catalog -run 'TestApplyInventory|TestIncrementCatalogGeneration' -v`

Expected: FAIL because the inventory repository API does not exist.

- [ ] **Step 3: Define catalog inventory types**

```go
type CatalogGeneration struct { RootID string; Value int64; UpdatedAt time.Time }
const (
	ProbeNotApplicable = "not_applicable"
	ProbePending = "pending"
	ProbeSucceeded = "succeeded"
	ProbeFailed = "failed"
	JobProbeFile = "probe_file"
	JobInspectCompanion = "inspect_companion"
	JobRegroupScope = "regroup_scope"
	JobPublishSearch = "publish_search"
	JobReorganizeFiles = "reorganize_files"
)
type GroupingEvidence struct {
	ReasonCode string
	Confidence float64
	ReviewState string
	Values map[string][]string
}
type InventoryFile struct {
	ID uuid.UUID
	RootID, RelativePath, FileKind, CompanionKind string
	Size int64
	ModifiedAt time.Time
}
type InventoryUpdate struct {
	ScanRunID uuid.UUID
	RootID string
	ScopePath string
	Complete bool
	Files []InventoryFile
}
type InventoryDelta struct {
	ProbeFiles []SourceFile
	InspectCompanions []SourceFile
	RegroupScopes []string
	UnchangedFiles []SourceFile
	MissingFileIDs []uuid.UUID
}
```

Extend `SourceFile` with `FileKind`, `CompanionKind`, `ProbeStatus`, `ProbeVersion`, `ProbeError`, and `ProbedAt`. Add the constants above to `catalog/model.go` and the new repository methods to `catalog.Repository`.

- [ ] **Step 4: Implement transactional fingerprint application**

Lock the scan run and root. Upsert recognized files. Preserve `media`, `probe_version`, and `probed_at` only when size and modified time match. Set changed/new audio and parseable companions to `pending`; set covers and filename-only ebook companions to `not_applicable`. Mark missing rows only for a complete full-root inventory or inside the exact completed scoped subtree.

Return deterministic sorted deltas. Do not enqueue jobs inside the repository transaction.

- [ ] **Step 5: Implement generation updates**

`IncrementCatalogGeneration` locks the root generation, increments it once, and returns the committed value. Keep failed/partial `FinishScan` behavior for inventories that do not reach atomic catalog finalization in Task 6.

- [ ] **Step 6: Run catalog inventory tests**

Run: `cd server; go test ./internal/catalog -run 'TestApplyInventory|TestIncrementCatalogGeneration' -v`

Expected: PASS for unchanged, changed, missing, restored, partial, and generation cases.

- [ ] **Step 7: Commit**

```powershell
git add server/internal/catalog
git commit -m "feat(catalog): persist incremental library inventory"
```

---

### Task 5: Infer Provisional Books Across Folder Subtrees

**Files:**
- Create: `server/internal/grouping/evidence.go`
- Create: `server/internal/grouping/evidence_test.go`
- Modify: `server/internal/grouping/group.go`
- Modify: `server/internal/grouping/group_test.go`

**Interfaces:**
- Produces: `InferTree(Tree) Result`.
- Produces: `catalog.GroupingEvidence` with reason code, confidence, review state, and source file roles.
- Consumes: persisted file classification and any available `media` metadata.

- [ ] **Step 1: Write failing domain tests for approved grouping rules**

Add table-driven tests for:

```go
func TestInferTreeApprovedCases(t *testing.T) {
	cases := []struct { name string; tree Tree; want []ExpectedBook }{
		{"dedicated chapter folder", tree("Author/Book", mp3("01 Book.mp3"), mp3("02 Book.mp3")),
			[]ExpectedBook{{Title:"Book", Review:catalog.ReviewUnreviewed}}},
		{"disc subfolders combine", tree("Author/Book", tagged("Disc 1/01.mp3","Book","Author"),
			tagged("Disc 2/01.mp3","Book","Author")), []ExpectedBook{{Title:"Book"}}},
		{"two tagged mp3 books split", tree("Mixed", tagged("A 01.mp3","A","Author"),
			tagged("B 01.mp3","B","Author")), []ExpectedBook{{Title:"A"},{Title:"B"}}},
		{"weak loose files remain visible", rootTree(mp3("Track 01.mp3"), mp3("Track 02.mp3")),
			[]ExpectedBook{{Review:catalog.ReviewNeedsReview, Reason:"weak_loose_files"}}},
		{"failed probe remains visible", tree("Book", failedProbe("01.mp3")),
			[]ExpectedBook{{Title:"Book", Review:catalog.ReviewNeedsReview, Reason:"probe_failed"}}},
	}
	for _, test := range cases { t.Run(test.name, func(t *testing.T) {
		assertExpectedBooks(t, InferTree(test.tree), test.want)
	}) }
}
```

Add cases for distinct M4B files, conflicting author/title, generic parent names, common filename stems, shared covers, CUE references, NFO/OPF identity, playlists, ebooks, and user-locked assignments.

- [ ] **Step 2: Run grouping tests and confirm RED**

Run: `cd server; go test ./internal/grouping -run TestInferTree -v`

Expected: FAIL because subtree inference types do not exist and MP3 splitting is unsupported.

- [ ] **Step 3: Implement evidence normalization**

```go
type Tree struct { RootID, ScopePath string; Files []catalog.SourceFile }
type Result struct {
	Books []catalog.InferredBook
	Evidence map[string]catalog.GroupingEvidence
}
func InferTree(tree Tree) Result
```

Normalize punctuation, case, whitespace, leading track/disc numbers, edition suffixes, and `Disc|Disk|CD` directory tokens. Parse CUE file references and use stored companion metadata; do not read companion files in the grouping function.

- [ ] **Step 4: Implement conservative subtree clustering**

Start with dedicated-folder clusters. Join child disc folders when normalized title/album and author agree or when filename stems plus disc structure agree. Split any supported audio format when distinct embedded identities or strong filename clusters identify separate books. Require stronger identity for loose/root-level files. Assign every active audio exactly once.

Apply grouping corrections before automatic candidates. Keep corrected books locked and create `Needs review` provisional books for new unassigned files in that scope.

- [ ] **Step 5: Run grouping tests**

Run: `cd server; go test ./internal/grouping -v`

Expected: PASS for direct folders, subtrees, mixed folders, loose files, companions, ambiguity, and locked corrections.

- [ ] **Step 6: Commit**

```powershell
git add server/internal/grouping
git commit -m "feat(grouping): infer audiobooks from subtree evidence"
```

---

### Task 6: Build, Publish, and Query the Shared Search Projection

**Files:**
- Create: `server/internal/catalog/search_model.go`
- Create: `server/internal/catalog/search_projection.go`
- Create: `server/internal/catalog/search_projection_test.go`
- Create: `server/internal/catalog/search_postgres.go`
- Create: `server/internal/catalog/search_postgres_test.go`
- Modify: `server/internal/catalog/repository.go`
- Modify: `server/internal/catalog/search.go`

**Interfaces:**
- Produces: `BuildSearchProjection(SearchProjectionInput) (SearchProjection, error)`.
- Produces: `FinalizeInventoryAndPublish(context.Context, InventoryFinalization) (CatalogGeneration, error)`.
- Produces: `ApplyGroupingRefinement(context.Context, GroupingRefinement) (CatalogGeneration, error)`.
- Produces: `PublishSearchProjection(context.Context, SearchProjection) error`.
- Produces: `SearchLibrary(context.Context, SearchQuery) (SearchPage, error)`.
- Consumes: `CatalogGeneration`, active books, active recognized files, assignments, and grouping evidence.

- [ ] **Step 1: Write failing projection tests**

Use the shared-search fixtures for audiobook, raw file, ancestor folder, metadata aggregation, duplicate identity, stable IDs, and unsupported-file exclusion. Add:

```go
func TestBuildSearchProjectionCarriesReviewReasonAndGeneration(t *testing.T) {
	input := projectionFixture(needsReviewBook("book-1", "probe_failed"))
	input.Generation = CatalogGeneration{RootID:"audiobooks", Value:7}
	projection, err := BuildSearchProjection(input)
	if err != nil { t.Fatal(err) }
	document := audiobookDocument(t, projection, "book-1")
	if document.ReviewState != ReviewNeedsReview ||
		document.ReviewReason != "probe_failed" || projection.Snapshot.CatalogGeneration != 7 {
		t.Fatalf("document=%#v snapshot=%#v", document, projection.Snapshot)
	}
}
```

- [ ] **Step 2: Run projection tests and confirm RED**

Run: `cd server; go test ./internal/catalog -run TestBuildSearchProjection -v`

Expected: FAIL because projection types do not exist.

- [ ] **Step 3: Define search types and build documents**

Define the complete boundary:

```go
type SearchMode string
const (
	SearchModeAudiobooks SearchMode = "audiobooks"
	SearchModeRaw SearchMode = "raw"
)
type SearchSnapshot struct {
	RootID string
	ScanRunID uuid.UUID
	CatalogGeneration int64
	CompletedAt time.Time
	DocumentCount int
}
type SearchDocument struct {
	RootID string
	DocumentID uuid.UUID
	EntityKind string
	BookID *uuid.UUID
	RelativePath, DisplayName, Title, Series string
	Size *int64
	ModifiedAt *time.Time
	Authors, Narrators, Filenames []string
	DurationMS *int64
	ReviewState, ReviewReason string
	DuplicateGroupID *uuid.UUID
	DuplicateCount int
}
type SearchProjectionInput struct {
	Snapshot SearchSnapshot
	Books []Book
	Files []SourceFile
	Assignments map[uuid.UUID][]uuid.UUID
	Evidence map[uuid.UUID]GroupingEvidence
}
type SearchProjection struct { Snapshot SearchSnapshot; Documents []SearchDocument }
type SearchQuery struct {
	RootID, Text, Cursor string
	Mode SearchMode
	Limit int
	BookQuery BookQuery
}
type SearchGroup struct { ID uuid.UUID; Duplicate bool; Documents []SearchDocument }
type SearchPage struct { Snapshot SearchSnapshot; Groups []SearchGroup; NextCursor string }
func BuildSearchProjection(SearchProjectionInput) (SearchProjection, error)
```

Apply the committed duplicate rules. Build audiobook documents, audio/cover raw-file documents, and their ancestor folders only from PostgreSQL input. Store companions for grouping and review but exclude them from raw search as required by the shared-search design.

- [ ] **Step 4: Write failing publication/query tests**

Assert atomic replacement, stale-generation rejection, prior snapshot preservation, indexed two-character/trigram queries, deterministic cursors, group-safe pagination, structured filters, and 100,000-document latency. Inject a failure after provisional book replacement and prove the scan, books, generation, and prior projection all roll back.

- [ ] **Step 5: Run publication/query tests and confirm RED**

Run: `cd server; go test ./internal/catalog -run 'TestPublishSearchProjection|TestSearchLibrary|TestLibrarySearchPlan|TestLibrarySearchPerformance' -v`

Expected: FAIL because publication and query methods do not exist.

- [ ] **Step 6: Implement atomic generation publication**

Lock `catalog_generations` for the root. Reject a projection whose generation differs from the current catalog generation. Replace root documents, upsert the snapshot, and commit. Do not delete the prior projection until the new rows validate.

Implement the initial-inventory boundary as:

```go
type InventoryFinalization struct {
	ScanRunID uuid.UUID
	RootID string
	Books []InferredBook
	Evidence map[string]GroupingEvidence
}
type GroupingRefinement struct {
	RootID, ScopePath string
	ExpectedGeneration int64
	Books []InferredBook
	Evidence map[string]GroupingEvidence
}
func (r *PostgresRepository) FinalizeInventoryAndPublish(
	ctx context.Context,
	input InventoryFinalization,
) (CatalogGeneration, error)
```

Within one transaction, lock the running scan and root generation, replace provisional books/evidence/assignments, compute the next generation, build and replace the complete projection, mark the scan succeeded, store the generation, and commit. This method creates the first visible audiobook/search snapshot atomically.

`ApplyGroupingRefinement` locks the expected generation, reapplies locked corrections, replaces automatic books/evidence/assignments inside the exact scope, increments the generation, and commits. It returns `ErrConflict` when newer work already advanced the root.

- [ ] **Step 7: Implement indexed search**

Retain the shared plan's activation, normalization, ranking, duplicate grouping, root isolation, opaque cursor, and performance rules. Return `ErrSearchIndexBuilding` only when a root has no published snapshot.

- [ ] **Step 8: Run catalog search tests**

Run: `cd server; go test ./internal/catalog -run 'TestBuildSearchProjection|TestPublishSearchProjection|TestSearchLibrary|TestLibrarySearchPlan|TestLibrarySearchPerformance' -v`

Expected: PASS with no sequential scan of `library_search_documents` in supported query plans.

- [ ] **Step 9: Commit**

```powershell
git add server/internal/catalog
git commit -m "feat(search): publish indexed catalog generations"
```

---

### Task 7: Orchestrate Inventory, Probe, Regroup, and Publication Jobs

**Files:**
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/worker/worker_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`
- Modify: `server/cmd/bookbox-catalog-worker/main_test.go`
- Modify: `server/internal/media/probe.go`
- Modify: `server/internal/media/ffprobe.go`
- Modify: `server/internal/media/ffprobe_test.go`
- Create: `server/internal/companion/parser.go`
- Create: `server/internal/companion/parser_test.go`

**Interfaces:**
- Produces handlers for `scan_root`, `probe_file`, `inspect_companion`, `regroup_scope`, and `publish_search`.
- Consumes: fast inventory, `ApplyInventory`, `InferTree`, catalog generations, and search publication.

- [ ] **Step 1: Write failing scan orchestration tests**

```go
func TestScanRootPublishesProvisionalCatalogBeforeProbeJobs(t *testing.T) {
	repository := &scanRepositoryFake{}
	handler := scanHandlerFixture(repository, inventoryWithAudio("Author/Book/01.mp3"))
	if err := handler.Handle(context.Background(), scanJob("audiobooks")); err != nil {
		t.Fatal(err)
	}
	if repository.finalizedInventories != 1 || repository.published != 1 {
		t.Fatalf("finalized=%d published=%d", repository.finalizedInventories, repository.published)
	}
	assertQueuedKinds(t, repository.jobs, catalog.JobProbeFile)
	if repository.probesRun != 0 { t.Fatalf("probes ran during inventory") }
}
```

Add tests for partial inventory, unchanged files, deterministic dedupe keys, probe failure, changed metadata regrouping, stale publication retry, and projection coalescing.

- [ ] **Step 2: Run worker tests and confirm RED**

Run: `cd server; go test ./internal/worker -run 'TestScanRoot|TestProbeFile|TestInspectCompanion|TestRegroupScope|TestPublishSearch' -v`

Expected: FAIL because staged handlers and job kinds do not exist.

- [ ] **Step 3: Implement root inventory handler**

The handler runs fast inventory, applies the delta, loads the affected root's persisted files and corrections, calls `grouping.InferTree`, and calls `FinalizeInventoryAndPublish`. It then enqueues one `probe_file:<file-id>:<size>:<modified-nanos>` job per delta audio file and one `inspect_companion:<file-id>:<size>:<modified-nanos>` job per parseable changed companion. It must not wait for those jobs.

- [ ] **Step 4: Implement descriptor-safe file probing**

Add `ProbeSourceFile(context.Context, rootID, relativePath)` through `library.Resolver.OpenRegular`. Pass the open descriptor to `FFProbe.ProbeOpenFile`. Extend `media.Metadata` and FFProbe parsing with series, track, disc, album artist, and identifiers. Store success or sanitized failure only when the expected fingerprint still matches. Queue `regroup_scope:<root>:<scope>:<generation>` after a material metadata change.

- [ ] **Step 5: Implement bounded companion inspection**

Create `companion.Parse(kind string, io.Reader, limit int64) (Evidence, error)`. Cap CUE, NFO, OPF, M3U, and M3U8 input at two MiB. Extract title, creator/author, referenced filenames, and playlist order. Use `encoding/xml` for OPF without external resolution. Treat invalid encodings, oversize input, and malformed XML as sanitized inspection failures. Store evidence in `source_files.media` and queue the same scoped regroup job used by audio probes. Ebook files contribute filename/folder evidence only and require no content parser.

- [ ] **Step 6: Implement regroup and projection handlers**

The regroup handler loads persisted subtree evidence, reapplies corrections, updates books/evidence/assignments, increments the generation, and enqueues `publish_search:<root>` through active-job deduplication. The publication handler reads one repeatable catalog snapshot, builds the projection, and publishes it. A stale generation queues one replacement publication and succeeds without overwriting newer state.

- [ ] **Step 7: Wire handlers in the worker command**

Construct `scanner.NewInventory`, descriptor-safe FFProbe, grouping services, and all handlers. Keep online metadata enrichment and cover publication after grouping refinement. Remove root-scan use of `scanner.New(media.NewFFProbe(...))`.

- [ ] **Step 8: Run worker, media, companion, scanner, grouping, and command tests**

Run: `cd server; go test ./internal/worker ./internal/media ./internal/companion ./internal/scanner ./internal/grouping ./cmd/bookbox-catalog-worker -v`

Expected: PASS. The scan-root tests must prove provisional publication precedes every probe.

- [ ] **Step 9: Commit**

```powershell
git add server/internal/worker server/internal/media server/internal/companion server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): stage inventory metadata and grouping"
```

---

### Task 8: Serve Both Search Modes from PostgreSQL

**Files:**
- Create: `server/internal/httpapi/library_search.go`
- Create: `server/internal/httpapi/library_search_test.go`
- Modify: `server/internal/httpapi/catalog.go`
- Modify: `server/internal/httpapi/books.go`
- Modify: `server/internal/httpapi/browse.go`
- Modify: `server/internal/httpapi/api.go`
- Modify: `server/internal/httpapi/books_test.go`
- Modify: `server/internal/httpapi/browse_test.go`
- Modify: `server/internal/library/browser.go`
- Modify: `server/internal/library/browser_test.go`

**Interfaces:**
- Consumes: `SearchLibrary(context.Context, catalog.SearchQuery) (catalog.SearchPage, error)`.
- Produces: audiobook `groups` from `/v1/books` and raw `entries` from `/v1/browse/search`.
- Produces: snapshot generation, review reason, and HTTP 503 `search_index_building`.

- [ ] **Step 1: Write failing route tests**

Copy the shared plan's panic-browser proof. Add a provisional audiobook response with `reviewState:"needs_review"`, `reviewReason:"probe_failed"`, and `catalogGeneration:1`. Assert both search routes call the catalog repository and never `Browser.Search`.

- [ ] **Step 2: Run route tests and confirm RED**

Run: `cd server; go test ./internal/httpapi -run 'Test.*Search|TestBooks.*Review' -v`

Expected: FAIL because shared response types and routes do not exist.

- [ ] **Step 3: Implement validated response shaping**

Define snapshot, audiobook group, and raw entry responses. Validate UUIDs, root IDs, relative paths, timestamps, generation > 0, duplicate consistency, review state/reason pairs, and absence of absolute paths. Map `ErrSearchIndexBuilding` to:

```json
{"error":{"code":"search_index_building","message":"search index is building"}}
```

- [ ] **Step 4: Adapt routes and delete recursive raw search**

Keep book filters and non-search sort parsing. Blank audiobook text lists all matching audiobook documents. Raw text search requires two normalized characters. Remove `Browser.Search`, its normalization helper, and recursive-walk tests. Preserve `Browser.List` for opening a selected directory.

- [ ] **Step 5: Run HTTP and library tests**

Run: `cd server; go test ./internal/httpapi ./internal/library -v`

Expected: PASS, including filesystem-panic injection during both search modes.

- [ ] **Step 6: Commit**

```powershell
git add server/internal/httpapi server/internal/library
git commit -m "feat(api): serve staged catalog search"
```

---

### Task 9: Add Path-Aware Debounced Reconciliation

**Files:**
- Modify: `server/go.mod`
- Modify: `server/go.sum`
- Create: `server/internal/monitor/root_monitor.go`
- Create: `server/internal/monitor/root_monitor_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`
- Modify: `server/cmd/bookbox-catalog-worker/main_test.go`

**Interfaces:**
- Produces: `RootMonitor.Run(context.Context) error`.
- Produces: root-relative `scan_root` payload `{rootId, relativePath, full}`.
- Uses: `github.com/fsnotify/fsnotify` v1.10.1 with five-second per-scope debounce.

- [ ] **Step 1: Write failing monitor tests**

Test root confinement, event-to-relative-path conversion, sibling coalescing to the nearest common ancestor, five-second debounce, running-scan follow-up dedupe, new-directory watches, and warning-only watcher failure.

```go
func TestRootMonitorCoalescesSiblingChangesToParentScope(t *testing.T) {
	repository := &monitorRepositoryFake{}
	monitor := monitorFixture(repository)
	monitor.accept("audiobooks", "Author/Book/Disc 1/01.mp3")
	monitor.accept("audiobooks", "Author/Book/Disc 2/01.mp3")
	monitor.fireDebounce("audiobooks")
	assertScanPayload(t, repository.jobs[0], "audiobooks", "Author/Book", false)
}
```

- [ ] **Step 2: Run monitor tests and confirm RED**

Run: `cd server; go test ./internal/monitor -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement fsnotify adapter and scoped scheduling**

Run: `cd server; go get github.com/fsnotify/fsnotify@v1.10.1`

Translate create, write, rename, and remove events to validated root-relative paths. Never log absolute event paths. Coalesce changes and enqueue `scan_root:<root>:<scope>`; when that job is running, enqueue one `scan_root_followup:<root>:<scope>:<job-id>`.

- [ ] **Step 4: Integrate monitor with periodic full inventory**

Run monitor and worker under one cancellation context. Keep startup and six-hour `full:true` jobs. Monitor errors log a root ID and error category, then periodic/manual inventory remains active.

- [ ] **Step 5: Run monitor and command tests**

Run: `cd server; go test ./internal/monitor ./cmd/bookbox-catalog-worker -v`

Expected: PASS for scoped debounce, fallback inventory, and follow-up scheduling.

- [ ] **Step 6: Commit**

```powershell
git add server/go.mod server/go.sum server/internal/monitor server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): reconcile changed library subtrees"
```

---

### Task 10: Decode and Render Shared Search on Android

**Files:**
- Create: `android/app/src/main/java/com/rendref/bookbox/core/search/DebouncedSearch.kt`
- Create: `android/app/src/main/java/com/rendref/bookbox/core/search/LibrarySearchField.kt`
- Create: `android/app/src/main/java/com/rendref/bookbox/feature/library/DuplicateBookGroupCard.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/model/ApiModels.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/CatalogApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/BrowseApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/repository/CatalogRepository.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/repository/BrowseRepository.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/BookCard.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersScreen.kt`
- Test: corresponding files under `android/app/src/test` and `android/app/src/androidTest`.

**Interfaces:**
- Produces: `SearchSnapshotDto`, `BookSearchGroupDto`, `RawSearchEntryDto`, and typed `SearchIndexBuilding`.
- Produces: `Flow<Q>.debouncedSearchUpdates(...)`.
- Preserves: cover same-origin validation, two MiB response limits, structured filters, and existing book actions.

- [ ] **Step 1: Write failing DTO and request tests**

Use singleton/duplicate groups, raw entries, generation, freshness, review reason, cursors, hostile absolute paths, negative durations, mismatched duplicate flags, and invalid review pairs.

- [ ] **Step 2: Run focused model/repository tests and confirm RED**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.model.ApiModelsTest" --tests "com.rendref.bookbox.core.repository.CatalogRepositoryTest" --tests "com.rendref.bookbox.core.repository.BrowseRepositoryTest"`

Expected: FAIL because shared search DTOs do not exist.

- [ ] **Step 3: Implement DTOs and validation**

Use the shared plan's DTOs. Add `catalogGeneration: Long`, `reviewState: String`, and `reviewReason: String?`. Require positive generation and require a nonblank review reason when `reviewState == "needs_review"`.

- [ ] **Step 4: Write failing shared debounce tests**

Cover 149/150 ms, one-character suppression, supplementary Unicode, cancellation, retained results, stale-response rejection, index-building, and provisional catalog loading.

- [ ] **Step 5: Implement shared Flow behavior and migrate ViewModels**

Use `debounce(150)`, `flatMapLatest`, code-point counting, and generation-safe updates. Audiobook mode loads blank text. Raw mode suppresses blank/one-character text. Keep existing groups/cards during replacement loading and failures.

- [ ] **Step 6: Write and implement Compose search presentation tests**

Assert collapsed duplicate parents, child actions, raw file/folder cards, duplicate badges, source-relative paths, index-building copy, stale copy, and retained cards. Keep the red grouping review action for Task 12; expose review state in card parameters now.

- [ ] **Step 7: Run Android unit and focused Compose tests**

Run: `cd android; .\gradlew.bat testDebugUnitTest`

Then run: `cd android; .\gradlew.bat connectedDebugAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=com.rendref.bookbox.feature.library.LibraryScreenTest,com.rendref.bookbox.feature.folders.FoldersScreenTest,com.rendref.bookbox.AppShellJourneyTest`

Expected: PASS for contracts, debounce, retained results, duplicate groups, and raw navigation.

- [ ] **Step 8: Commit**

```powershell
git add android/app/src/main/java/com/rendref/bookbox android/app/src/test/java/com/rendref/bookbox android/app/src/androidTest/java/com/rendref/bookbox
git commit -m "feat(android): render staged shared catalog"
```

---

### Task 11: Expand Grouping Review to Subtrees and Companions

**Files:**
- Modify: `server/internal/catalog/model.go`
- Modify: `server/internal/catalog/repository.go`
- Modify: `server/internal/catalog/postgres.go`
- Modify: `server/internal/catalog/postgres_test.go`
- Modify: `server/internal/httpapi/catalog.go`
- Modify: `server/internal/httpapi/catalog_test.go`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/model/ApiModels.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/ReviewApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/repository/ReviewRepository.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/book/BookDetailViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/book/GroupingScreen.kt`
- Modify: related unit and Compose tests.

**Interfaces:**
- Extends: `GroupingContext` with scope path, reason, evidence, companion files, and capability.
- Produces: `SaveGrouping(context.Context, GroupingCorrection) (CatalogGeneration, error)`.
- Produces: `PUT /v1/books/{bookId}/grouping` response with the new generation.

- [ ] **Step 1: Write failing repository tests**

Assert grouping context includes audio from `Disc 1` and `Disc 2`, associated companions, evidence, and the requested book. Assert saving assigns every audio once, allows explicit companion ownership/shared roles, locks groups, advances generation, and preserves corrections after inference.

- [ ] **Step 2: Run repository tests and confirm RED**

Run: `cd server; go test ./internal/catalog -run 'Test.*GroupingContext|Test.*SaveGrouping' -v`

Expected: FAIL because grouping context only loads direct audio files and `SaveGrouping` returns no generation.

- [ ] **Step 3: Extend domain types and repository behavior**

```go
type GroupingContext struct {
	BookID uuid.UUID
	RootID, ScopePath, ReviewReason string
	Evidence map[string][]string
	Files []GroupingContextFile
	Books []GroupingContextBook
	CanReorganize bool
}
type GroupingContextFile struct {
	SourceFile
	Role string
	CompanionKind string
	Shared bool
}
```

Load active files beneath the stored grouping scope, not only direct children. `SaveGrouping` validates all active audio assignment, companion assignment/shared roles, existing-book membership, and current fingerprints. It writes corrections, books, assignments, evidence reason `manual`, and one generation in a transaction.

- [ ] **Step 4: Write failing API contract tests**

Assert source-relative paths, evidence, review reason, companion role, capability, generation response, duplicate assignment rejection, stale file rejection, and no absolute paths.

- [ ] **Step 5: Implement API response and enqueue projection**

Return HTTP 200 with `{catalogGeneration}` after save. Enqueue deduplicated `publish_search:<root>` after the transaction commits. Do not expose reorganization routes until Task 13.

- [ ] **Step 6: Write failing Android ViewModel and Compose tests**

Assert a red `!` icon with content description `Review audiobook grouping`, automatic opening of review, evidence text, nested relative paths, audio radio assignment, companion checkboxes, split/merge, and catalog-only confirmation.

- [ ] **Step 7: Implement Android review changes**

Add `GroupingFileDto.role`, `companionKind`, and `shared`. Extend `GroupingDecision` with companion assignments. Keep audio exactly once; allow a shared cover in several groups only when the server marks it shared. Change the primary action label to `Confirm grouping`.

- [ ] **Step 8: Run server and Android review tests**

Run: `cd server; go test ./internal/catalog ./internal/httpapi -run 'Test.*Grouping|TestBooks.*Review' -v`

Then run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.feature.book.BookDetailViewModelTest"`

Then run: `cd android; .\gradlew.bat connectedDebugAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=com.rendref.bookbox.feature.book.BookReviewTest,com.rendref.bookbox.feature.library.LibraryScreenTest`

Expected: PASS for subtree context, companion assignments, red review semantics, and locked corrections.

- [ ] **Step 9: Commit**

```powershell
git add server/internal/catalog server/internal/httpapi android/app/src/main android/app/src/test android/app/src/androidTest
git commit -m "feat(review): confirm subtree audiobook grouping"
```

---

### Task 12: Implement Authorized Reorganization Preview and Execution

**Files:**
- Create: `server/internal/catalog/reorganization.go`
- Create: `server/internal/catalog/reorganization_test.go`
- Modify: `server/internal/catalog/model.go`
- Modify: `server/internal/catalog/repository.go`
- Modify: `server/internal/catalog/postgres.go`
- Modify: `server/internal/config/config.go`
- Modify: `server/internal/config/config_test.go`
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/worker/worker_test.go`
- Create: `server/internal/httpapi/reorganization.go`
- Create: `server/internal/httpapi/reorganization_test.go`
- Modify: `server/internal/httpapi/api.go`
- Modify: `deploy/config.example.yml`

**Interfaces:**
- Produces: `BuildReorganizationPreview(context.Context, ReorganizationRequest) (ReorganizationPreview, error)`.
- Produces: `QueueReorganization(context.Context, uuid.UUID, string) (WorkerJob, error)`.
- Produces: `ReorganizeFilesHandler` with journaled move and rollback.
- Produces: `POST /v1/books/{bookId}/reorganization/preview` and `/confirm`.

- [ ] **Step 1: Write failing configuration and preview tests**

Require `allow_library_reorganization` to default false. Test preview only when config, account capability, and injected root-write probe all allow it. Test deterministic target names, audio plus associated companions, stale generation, stale fingerprints, collisions, traversal, symlinks, cross-root moves, and existing destinations.

- [ ] **Step 2: Run config/catalog tests and confirm RED**

Run: `cd server; go test ./internal/config ./internal/catalog -run 'Test.*Reorganization|TestLoad.*Reorganization' -v`

Expected: FAIL because configuration and preview types do not exist.

- [ ] **Step 3: Implement capability and preview domain**

```go
type ReorganizationRequest struct {
	BookID uuid.UUID
	CatalogGeneration int64
	Groups []GroupingGroup
	TargetFolders map[string]string
}
type ReorganizationMove struct {
	Order int
	FileID uuid.UUID
	SourcePath, DestinationPath, Role string
	ExpectedSize int64
	ExpectedModifiedAt time.Time
}
type ReorganizationPreview struct {
	OperationID uuid.UUID
	RootID string
	CatalogGeneration int64
	Moves []ReorganizationMove
}
func (r *PostgresRepository) BuildReorganizationPreview(
	context.Context,
	ReorganizationRequest,
) (ReorganizationPreview, error)
func (r *PostgresRepository) QueueReorganization(
	context.Context,
	uuid.UUID,
	string,
) (WorkerJob, error)
```

Sanitize target folder segments through an explicit validator. Never infer authorization from mount writability alone. Persist the preview and ordered moves before returning it.

Add `AllowLibraryReorganization bool \`yaml:"allow_library_reorganization"\`` to `config.Config`. BookBox v1 has one authenticated owner account. That owner receives the reorganization capability only when this setting is true and the root-write probe succeeds. Keep the capability interface separate so a later multi-account model can deny it per account.

- [ ] **Step 4: Write failing worker rollback tests**

Use an injected confined mover. Cover success, failure before any move, failure after two moves with full rollback, rollback failure, changed source fingerprint, destination appearance after preview, and cancellation. Assert PostgreSQL grouping commits only after verified moves.

- [ ] **Step 5: Implement journaled execution**

Lock the operation, revalidate generation and fingerprints, mark it running, move files in order, and update each move state. Verify destination size and modified time after each move. On failure, reverse completed moves. Mark `rolled_back` after full rollback or `needs_attention` with relative unresolved paths after rollback failure. Successful execution saves grouping, advances generation, marks the operation succeeded, and queues targeted inventory plus search publication.

- [ ] **Step 6: Write and implement API tests**

Preview returns operation ID and relative moves. Confirm accepts only the same authenticated actor and a previewed current operation. Disabled capability returns 403 `library_reorganization_disabled`; stale preview returns 409 `reorganization_stale`; collision returns 409 `destination_conflict`.

- [ ] **Step 7: Run server tests**

Run: `cd server; go test ./internal/config ./internal/catalog ./internal/worker ./internal/httpapi -run 'Test.*Reorganization|TestLoad.*Reorganization' -v`

Expected: PASS for authorization, confinement, journaling, rollback, audit state, and API errors.

- [ ] **Step 8: Commit**

```powershell
git add server/internal/catalog server/internal/config server/internal/worker server/internal/httpapi deploy/config.example.yml
git commit -m "feat(catalog): reorganize confirmed audiobook files"
```

---

### Task 13: Add Reorganization Preview and Confirmation to Android

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/model/ApiModels.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/ReviewApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/repository/ReviewRepository.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/book/BookDetailViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/book/GroupingScreen.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/feature/book/BookDetailViewModelTest.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/core/repository/ReviewRepositoryTest.kt`
- Modify: `android/app/src/androidTest/java/com/rendref/bookbox/feature/book/BookReviewTest.kt`

**Interfaces:**
- Produces: preview DTOs with operation ID, generation, and source-relative moves.
- Produces: `PreviewReorganization`, `ConfirmReorganization`, and `CancelReorganization` actions.
- Preserves: `Confirm grouping` when reorganization is unavailable.

- [ ] **Step 1: Write failing API/repository tests**

Assert request encoding includes current generation, group assignments, companion assignments, and target folders. Reject absolute paths, duplicate destinations, invalid operation IDs, negative sizes, and mismatched generation.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.repository.ReviewRepositoryTest" --tests "com.rendref.bookbox.feature.book.BookDetailViewModelTest"`

Expected: FAIL because preview and confirmation APIs do not exist.

- [ ] **Step 3: Implement DTOs, repository methods, and ViewModel state**

Add `canReorganize`, `targetFolder` per draft group, `reorganizationPreview`, and `pendingOperationId`. `previewReorganization` must require a valid grouping and nonblank safe target folders. `confirmReorganization` sends only the current operation ID and prevents double submission.

- [ ] **Step 4: Write failing Compose tests**

Assert `Confirm and reorganize files` appears only when capability is true. Preview must list every old and new relative path, label companion roles, show conflicts, and require a second confirmation. Denied capability keeps `Confirm grouping` usable.

- [ ] **Step 5: Implement Compose preview and confirmation**

Use a modal or inline confirmation section inside `GroupingScreen`. Do not render absolute paths. After accepted confirmation, show `File reorganization queued` and keep the book in review until the server publishes the reconciled generation.

- [ ] **Step 6: Run Android tests**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.repository.ReviewRepositoryTest" --tests "com.rendref.bookbox.feature.book.BookDetailViewModelTest"`

Then run: `cd android; .\gradlew.bat connectedDebugAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=com.rendref.bookbox.feature.book.BookReviewTest`

Expected: PASS for capability gating, preview validation, confirmation, and catalog-only fallback.

- [ ] **Step 7: Commit**

```powershell
git add android/app/src/main/java/com/rendref/bookbox android/app/src/test/java/com/rendref/bookbox android/app/src/androidTest/java/com/rendref/bookbox
git commit -m "feat(android): preview audiobook reorganization"
```

---

### Task 14: Verify Integration, Performance, and Live Catalog Recovery

**Files:**
- Create: `e2e/tests/staged-catalog.tests.ps1`
- Modify: `e2e/tests/catalog-search.tests.ps1`
- Modify: `e2e/run.ps1`
- Modify: `deploy/scripts/smoke-catalog.ps1`
- Modify: `docs/catalog.md`
- Modify: `docs/android.md`
- Modify: `docs/operations.md`
- Modify: `docs/backup-and-recovery.md`

**Interfaces:**
- Produces: release-level proof for provisional visibility, incremental probes, search, review, and disposable reorganization.
- Verifies: the configured live library stores source files and books without destructive source changes.

- [ ] **Step 1: Write failing staged-catalog E2E assertions**

Create a disposable library with dedicated MP3 chapters, two MP3 books in one folder, disc subfolders, loose weak files, covers, CUE/NFO/OPF/playlist/ebook companions, duplicates, and an injected probe failure. Assert:

```powershell
Assert-Eventually -Description 'provisional books before probes finish' -Script {
    $books = Invoke-BookBoxJson '/v1/books'
    $books.groups.Count -gt 0 -and
        ($books.groups.books.reviewState -contains 'needs_review')
}
Assert-DatabaseCount -Table source_files -Minimum 1
Assert-DatabaseCount -Table books -Minimum 1
Assert-NoRequestTimeFilesystemRead -Routes @('/v1/books?q=dune','/v1/browse/search?root=audiobooks&q=dune')
```

Pause probe-job claiming to prove inventory visibility. Resume it and assert later catalog generation, refined metadata, disc merging, mixed-folder splitting, and unchanged-file probe count.

- [ ] **Step 2: Run focused E2E and confirm RED**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\staged-catalog.tests.ps1`

Expected: FAIL until staged inventory and workers exist.

- [ ] **Step 3: Add safe reorganization E2E**

Use a disposable root with reorganization enabled. Preview and confirm one split group. Assert audio and associated companions moved, bytes match, the operation succeeded, grouping persisted, search updated, and no unplanned source path changed. Repeat with a forced mid-move failure and assert rollback.

- [ ] **Step 4: Update smoke checks and documentation**

Document catalog stages, job states, review reasons, red `!`, grouping correction, capability configuration, preview/rollback, six-hour fingerprint inventory, backup tables, and recovery from `needs_attention`. Smoke checks must report active files, active books, latest generation, projection generation, queued/dead jobs, and last inventory status without printing absolute paths.

- [ ] **Step 5: Run all server tests**

Run: `cd server; go test ./...`

Expected: PASS with PostgreSQL tests enabled by `BOOKBOX_TEST_POSTGRES_DSN`.

- [ ] **Step 6: Run Android unit and lint**

Run: `cd android; .\gradlew.bat testDebugUnitTest lintDebug`

Expected: PASS.

- [ ] **Step 7: Run focused E2E and release verification**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\staged-catalog.tests.ps1`

Then run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\catalog-search.tests.ps1`

Then run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\verify-release.ps1`

Expected: PASS for staging, grouping, search, reorganization, rollback, and release checks.

- [ ] **Step 8: Rebuild the running worker and trigger one live inventory**

Run the existing monitored release/deployment workflow. Do not enable reorganization for the configured live library. Trigger one manual root inventory, then run read-only checks:

```powershell
docker compose --env-file deploy/.env -f deploy/compose.yml exec -T postgres `
  psql -U bookbox -d bookbox -P pager=off -c `
  "select count(*) active_files from source_files where missing_at is null; `
   select count(*) active_books from books where missing_at is null; `
   select root_id,generation from catalog_generations;"
```

Expected: active file and book counts are greater than zero; the inventory completes before the probe backlog; the APK shows provisional cards.

- [ ] **Step 9: Inspect and commit**

Run: `git diff --check; git status --short`

Expected: no whitespace errors; only planned files plus the user's pre-existing deployment changes appear.

```powershell
git add e2e docs deploy/scripts/smoke-catalog.ps1
git commit -m "test(catalog): verify staged library recovery"
```

---

## Final Review Checklist

- [ ] Startup recovers expired jobs before scan deduplication.
- [ ] Long jobs renew leases and stale tokens cannot renew or complete work.
- [ ] Root inventory invokes no media probe and commits provisional books.
- [ ] New or changed audio queues one fingerprint-specific probe; unchanged audio queues none.
- [ ] New or changed parseable companions queue one bounded inspection; unchanged companions queue none.
- [ ] Every active recognized audio belongs to a visible provisional or confirmed book.
- [ ] Disc subfolders combine on consistent evidence; strong identities split every supported audio format.
- [ ] Weak loose-file and failed-probe cases display `Needs review` with a red accessible action.
- [ ] User grouping corrections include subtree audio and companions and survive reconciliation.
- [ ] Search requests contain no filesystem traversal and keep the prior projection on failure.
- [ ] Duplicate-recording groups remain distinct from grouping-review state.
- [ ] Reorganization requires config, account capability, current preview, and writable confined source access.
- [ ] Successful reorganization moves associated companions; failure rolls back or records `needs_attention`.
- [ ] The 100,000-document fixture meets search plan and latency thresholds.
- [ ] The configured library produces nonzero PostgreSQL file/book counts and visible APK cards before its probe backlog drains.
- [ ] User-owned deployment changes outside named files remain untouched.

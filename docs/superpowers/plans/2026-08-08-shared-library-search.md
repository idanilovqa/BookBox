# Shared Library Search Implementation Plan

> Superseded for execution by
> `docs/superpowers/plans/2026-08-08-staged-audiobook-catalog-and-search.md`.
> Commit `f796660` approved staged inventory, background probing, subtree
> grouping, review, and optional reorganization after this plan was written.
> The replacement plan retains this document's search semantics and integrates
> the overlapping worker, projection, monitoring, and Android tasks.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace request-time raw filesystem search with one PostgreSQL search projection that serves audiobook cards and raw folder/file results, including duplicate-recording groups.

**Architecture:** The catalog worker publishes one transactional search snapshot per root after a successful scan. A shared Go search service queries audiobook, folder, and file documents through indexed PostgreSQL expressions. The existing audiobook and raw routes adapt the shared result for their screens, while Android shares request behavior and keeps separate renderers.

**Tech Stack:** Go 1.26.5, PostgreSQL with `pg_trgm`, GIN indexes, pgx v5, fsnotify, Kotlin, coroutines/Flow, Jetpack ViewModel, Jetpack Compose, OkHttp, kotlinx.serialization, JUnit.

## Global Constraints

- Search requests must not read configured source roots.
- Search uses the latest completed projection and retains the previous projection after a failed scan.
- Raw search indexes recognized audio files, recognized cover images, and their ancestor folders. It excludes unrelated and empty content.
- Duplicate identity requires equal normalized title, author identity, and nonempty narrator identity plus duration within `min(5 minutes, max(2 minutes, 0.5 percent))`.
- Android submits text search after at least two Unicode letters or digits and uses a 150-millisecond debounce.
- Warm server search p95 must stay below 50 milliseconds for 100,000 documents; cold search must stay below 150 milliseconds.
- Preserve root confinement, authentication, response-size limits, and the ban on absolute source paths.
- Run one test command at a time. Do not overlap Go, Gradle, Compose, or E2E test processes.
- Preserve the user's existing changes under `deploy/scripts/` unless a task names the same file.

## File Map

- `server/migrations/0003_library_search.sql`: schema, normalization helpers, grams, and indexes.
- `server/internal/catalog/search_model.go`: search domain types and interfaces.
- `server/internal/catalog/search_projection.go`: document construction and duplicate classification.
- `server/internal/catalog/search_postgres.go`: atomic publication, indexed queries, and cursors.
- `server/internal/monitor/root_monitor.go`: debounced filesystem events and scan scheduling.
- `server/internal/httpapi/library_search.go`: shared API response shaping and validation.
- `android/app/src/main/java/com/rendref/bookbox/core/search/DebouncedSearch.kt`: shared debounce and retained-result behavior.
- `android/app/src/main/java/com/rendref/bookbox/feature/library/DuplicateBookGroupCard.kt`: expandable duplicate presentation.

---

### Task 1: Create the PostgreSQL Search Projection

**Files:**
- Create: `server/migrations/0003_library_search.sql`
- Modify: `server/internal/database/migrate_test.go`

**Interfaces:**
- Produces: `catalog_search_compact(text) returns text` and `catalog_search_grams(text) returns text[]`.
- Produces: `library_search_snapshots` and `library_search_documents`.

- [ ] **Step 1: Write the failing migration test**

Add both tables to the expected table list, change the migration count from `2` to `3`, and add:

```go
func TestMigrateCreatesIndexedLibrarySearchProjection(t *testing.T) {
	pool := testpostgres.Open(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil { t.Fatal(err) }
	for _, index := range []string{
		"library_search_documents_trgm",
		"library_search_documents_grams",
		"library_search_documents_page",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`select to_regclass('public.' || $1) is not null`, index,
		).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s exists=%v err=%v", index, exists, err)
		}
	}
	var compact string
	var grams []string
	if err := pool.QueryRow(ctx, `
		select catalog_search_compact(' Black.Box 42 '),
		       catalog_search_grams('blackbox')
	`).Scan(&compact, &grams); err != nil { t.Fatal(err) }
	if compact != "blackbox42" || !slices.Contains(grams, "bl") ||
		!slices.Contains(grams, "bla") {
		t.Fatalf("compact=%q grams=%v", compact, grams)
	}
}
```

- [ ] **Step 2: Run the test and confirm RED**

Run: `cd server; go test ./internal/database -run TestMigrateCreatesIndexedLibrarySearchProjection -v`

Expected: FAIL because migration 0003 does not exist.

- [ ] **Step 3: Implement migration 0003**

Create `pg_trgm`, immutable normalization and gram functions, then these tables and indexes:

```sql
create table library_search_snapshots (
    root_id text primary key references library_roots(id),
    scan_run_id uuid not null,
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
    check ((entity_kind='audiobook' and book_id is not null) or entity_kind<>'audiobook')
);

create index library_search_documents_trgm
    on library_search_documents using gin (search_compact gin_trgm_ops);
create index library_search_documents_grams
    on library_search_documents using gin (search_grams);
create index library_search_documents_page
    on library_search_documents(root_id, entity_kind, display_name, relative_path, document_id);
```

`catalog_search_compact` lowercases Unicode text and removes non-letter/non-digit characters. `catalog_search_grams` returns sorted unique two- and three-character windows.

- [ ] **Step 4: Run the migration package**

Run: `cd server; go test ./internal/database -v`

Expected: PASS with three migrations and all indexes present.

- [ ] **Step 5: Commit**

```powershell
git add server/migrations/0003_library_search.sql server/internal/database/migrate_test.go
git commit -m "feat(search): add indexed library projection schema"
```

---

### Task 2: Build Search Documents and Duplicate Groups

**Files:**
- Create: `server/internal/catalog/search_model.go`
- Create: `server/internal/catalog/search_projection.go`
- Create: `server/internal/catalog/search_projection_test.go`

**Interfaces:**
- Consumes: active `Book`, `SourceFile`, and book-to-file assignments.
- Produces: `BuildSearchProjection(SearchProjectionInput) (SearchProjection, error)`.
- Produces: `SearchQuery`, `SearchMode`, `SearchPage`, `SearchGroup`, `SearchDocument`, and `SearchSnapshot`.

- [ ] **Step 1: Write failing pure-domain tests**

```go
func TestBuildSearchProjectionCreatesAudiobookAndRawDocuments(t *testing.T) {
	input := projectionFixture(
		file("Dune.m4b", "audio", 3_600_000),
		file("Frank Herbert/Dune/book.m4b", "audio", 3_601_000),
		file("Frank Herbert/Dune/cover.jpg", "cover", 0),
	)
	projection, err := BuildSearchProjection(input)
	if err != nil { t.Fatal(err) }
	assertDocument(t, projection, SearchEntityAudiobook, "Dune.m4b")
	assertDocument(t, projection, SearchEntityFile, "Dune.m4b")
	assertDocument(t, projection, SearchEntityFolder, "Frank Herbert")
	assertDocument(t, projection, SearchEntityFolder, "Frank Herbert/Dune")
	assertDocument(t, projection, SearchEntityFile, "Frank Herbert/Dune/cover.jpg")
}

func TestBuildSearchProjectionGroupsSameNarrationAndCloseDuration(t *testing.T) {
	projection, err := BuildSearchProjection(duplicateFixture(
		copyOf("one", "Narrator A", 36_000_000),
		copyOf("two", "Narrator A", 36_120_000),
		copyOf("other-narrator", "Narrator B", 36_000_000),
		copyOf("too-long", "Narrator A", 36_400_000),
	))
	if err != nil { t.Fatal(err) }
	assertDuplicateMembers(t, projection, "one", "two")
	assertNoDuplicate(t, projection, "other-narrator", "too-long")
}
```

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd server; go test ./internal/catalog -run TestBuildSearchProjection -v`

Expected: FAIL because the projection types do not exist.

- [ ] **Step 3: Define the domain types**

```go
type SearchMode string
const (
	SearchModeAudiobooks SearchMode = "audiobooks"
	SearchModeRaw SearchMode = "raw"
)

type SearchProjectionInput struct {
	RootID string
	ScanRunID uuid.UUID
	CompletedAt time.Time
	Books []Book
	Files []SourceFile
	Assignments map[uuid.UUID][]uuid.UUID
}

type SearchProjection struct {
	Snapshot SearchSnapshot
	Documents []SearchDocument
}

type SearchGroup struct {
	ID uuid.UUID
	Duplicate bool
	Documents []SearchDocument
}

type SearchPage struct {
	Snapshot SearchSnapshot
	Groups []SearchGroup
	NextCursor string
}

func BuildSearchProjection(input SearchProjectionInput) (SearchProjection, error)
```

Raw mode returns one document per singleton group; audiobook mode combines duplicate members in one group. Use UUIDv5 over `bookbox-search\x00<root>\x00<kind>\x00<identity>` for stable document IDs and duplicate-group IDs.

- [ ] **Step 4: Implement projection construction**

Build one audiobook document per active book, one file document per active recognized source file, and one folder document per nonempty ancestor. Aggregate linked book metadata into raw documents. Propagate a duplicate-group ID to each linked raw file and ancestor folder. Sum assigned audio `media.durationMs` values for audiobook duration.

Exclude candidates with missing title, author, narrator, or duration. Partition the rest by normalized title, sorted authors, and sorted narrators. Sort each partition by duration and compare each member with the cluster's first duration:

```go
func duplicateDurationTolerance(left, right int64) int64 {
	longer := max(left, right)
	percent := longer / 200
	return min(int64(5*time.Minute/time.Millisecond),
		max(int64(2*time.Minute/time.Millisecond), percent))
}
```

- [ ] **Step 5: Run pure catalog tests**

Run: `cd server; go test ./internal/catalog -run TestBuildSearchProjection -v`

Expected: PASS for entity creation, ancestors, metadata aggregation, stable IDs, and duplicate boundaries.

- [ ] **Step 6: Commit**

```powershell
git add server/internal/catalog/search_model.go server/internal/catalog/search_projection.go server/internal/catalog/search_projection_test.go
git commit -m "feat(search): build shared library search documents"
```

---

### Task 3: Publish and Query the Projection

**Files:**
- Create: `server/internal/catalog/search_postgres.go`
- Create: `server/internal/catalog/search_postgres_test.go`
- Modify: `server/internal/catalog/repository.go`
- Modify: `server/internal/catalog/postgres.go`
- Modify: `server/internal/catalog/search.go`

**Interfaces:**
- Produces: `FinalizeSuccessfulScan(context.Context, uuid.UUID, []InferredBook) error`.
- Produces: `SearchLibrary(context.Context, SearchQuery) (SearchPage, error)`.
- Preserves: structured filters and non-search sorts from `ListBooks`.

- [ ] **Step 1: Write failing atomic-publication tests**

```go
func TestFinalizeSuccessfulScanPublishesOneRootSnapshot(t *testing.T) {
	repository, scan, books := searchScanFixture(t)
	if err := repository.FinalizeSuccessfulScan(context.Background(), scan.ID, books); err != nil {
		t.Fatal(err)
	}
	page, err := repository.SearchLibrary(context.Background(), SearchQuery{
		RootID: "audiobooks", Mode: SearchModeRaw, Text: "dune", Limit: 100,
	})
	if err != nil { t.Fatal(err) }
	if page.Snapshot.ScanRunID != scan.ID || len(page.Groups) == 0 {
		t.Fatalf("page=%#v", page)
	}
}
```

Add a second publication that violates a document constraint and assert the first snapshot remains queryable.

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd server; go test ./internal/catalog -run 'TestFinalizeSuccessfulScan|TestSearchLibrary' -v`

Expected: FAIL because the methods do not exist.

- [ ] **Step 3: Implement atomic finalization**

Within one transaction: lock the running scan, replace inferred books, mark missing files/books, load active assignments, call `BuildSearchProjection`, replace that root's documents, upsert its snapshot, mark the scan succeeded, and commit. Keep failed/partial `FinishScan` behavior. Do not delete the old projection until the new document set validates.

- [ ] **Step 4: Implement indexed search and opaque cursors**

Validate mode, root, limit `1..100`, cursor, and zero-or-at-least-two normalized characters. Use `search_grams @> ARRAY[$1]` for two-character queries. Use trigram and compact substring predicates for longer queries. Apply root and entity kind before ranking.

Rank exact compact name/title/path first, prefixes second, path/filename/title substrings third, metadata substrings fourth, and trigram similarity fifth. Break ties with score, normalized display name, relative path, kind, and document ID. Keep a duplicate audiobook group on one page; move the whole group to the next page when it does not fit the remaining member limit.

Return `ErrSearchIndexBuilding` when no snapshot exists. Encode rank, score, normalized display name, relative path, and document ID as base64url JSON. Parameterize every SQL value.

- [ ] **Step 5: Add plan and performance tests**

Populate 100,000 deterministic rows with `generate_series`. Use `EXPLAIN (FORMAT JSON)` to reject a sequential scan of `library_search_documents`. Warm representative queries, run 100 samples, and assert p95 below 50 ms plus cold search below 150 ms.

- [ ] **Step 6: Run catalog PostgreSQL tests**

Run: `cd server; go test ./internal/catalog -run 'TestFinalizeSuccessfulScan|TestSearchLibrary|TestLibrarySearchPlan|TestLibrarySearchPerformance' -v`

Expected: PASS with indexed plans and both latency thresholds met.

- [ ] **Step 7: Commit**

```powershell
git add server/internal/catalog/repository.go server/internal/catalog/postgres.go server/internal/catalog/search.go server/internal/catalog/search_postgres.go server/internal/catalog/search_postgres_test.go
git commit -m "feat(search): publish and query catalog snapshots"
```

---

### Task 4: Finalize Worker Scans Through the Projection

**Files:**
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/worker/worker_test.go`

**Interfaces:**
- Consumes: `FinalizeSuccessfulScan(context.Context, uuid.UUID, []catalog.InferredBook) error`.
- Produces: a scan job that succeeds only after the projection commits.

- [ ] **Step 1: Write failing orchestration tests**

```go
func TestScanRootMarksScanFailedWhenProjectionPublicationFails(t *testing.T) {
	repository := &scanRepositoryFake{finalizeErr: errors.New("projection failed")}
	handler := scanHandlerFixture(repository)
	err := handler.Handle(context.Background(), scanJob("audiobooks"))
	if err == nil || !strings.Contains(err.Error(), "projection failed") {
		t.Fatalf("Handle() error=%v", err)
	}
	if repository.finalized != 1 || repository.failedScans != 1 {
		t.Fatalf("finalized=%d failed=%d", repository.finalized, repository.failedScans)
	}
}
```

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd server; go test ./internal/worker -run TestScanRoot -v`

Expected: FAIL because the handler uses separate replacement and finish calls.

- [ ] **Step 3: Replace the successful scan sequence**

```go
type ScanRepository interface {
	BeginScan(context.Context, string) (catalog.ScanRun, error)
	UpsertSourceFiles(context.Context, uuid.UUID, []catalog.SourceFile) error
	FinalizeSuccessfulScan(context.Context, uuid.UUID, []catalog.InferredBook) error
	FinishScan(context.Context, uuid.UUID, catalog.ScanResult) error
	ListBooks(context.Context, catalog.BookQuery) (catalog.Page[catalog.Book], error)
	EnqueueDeduplicated(context.Context, catalog.WorkerJob, string) (catalog.WorkerJob, error)
}
```

Call `FinalizeSuccessfulScan` after inference. On error, call `failScan` with `search projection publication failed`. Queue enrichment after finalization succeeds.

- [ ] **Step 4: Run worker and catalog tests**

Run: `cd server; go test ./internal/worker ./internal/catalog -v`

Expected: PASS with failed/partial scan behavior preserved.

- [ ] **Step 5: Commit**

```powershell
git add server/internal/worker/worker.go server/internal/worker/worker_test.go
git commit -m "feat(worker): publish search with successful scans"
```

---

### Task 5: Serve Both Search Modes Through the Shared Service

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
- Consumes: `CatalogRepository.SearchLibrary(context.Context, catalog.SearchQuery) (catalog.SearchPage, error)`.
- Produces: audiobook `groups` from `/v1/books` and raw `entries` from `/v1/browse/search`.
- Produces: HTTP 503 `search_index_building`.

- [ ] **Step 1: Write failing route tests**

```go
func TestRawSearchUsesCatalogWithoutReadingBrowser(t *testing.T) {
	repository := &catalogAPIFake{searchPage: rawSearchPage()}
	api := New(Dependencies{Catalog: repository, Browser: panicSearchBrowser(t)})
	response := serveRequest(api, "/v1/browse/search?root=audiobooks&q=dune")
	if response.Code != http.StatusOK { t.Fatalf("%d %s", response.Code, response.Body) }
	if repository.searchQueries[0].Mode != catalog.SearchModeRaw {
		t.Fatalf("query=%#v", repository.searchQueries[0])
	}
}
```

Also assert `/v1/books?q=dune` requests audiobook mode and serializes duplicate groups.

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd server; go test ./internal/httpapi -run 'Test.*Search' -v`

Expected: FAIL because both routes use their former search paths.

- [ ] **Step 3: Implement response shaping and validation**

Define snapshot, audiobook-group, and raw-entry responses in `library_search.go`. Validate IDs, root, relative paths, entity kinds, timestamps, duplicate counts, and absence of absolute paths. Map `ErrSearchIndexBuilding` to:

```json
{"error":{"code":"search_index_building","message":"search index is building"}}
```

- [ ] **Step 4: Adapt the routes**

Keep book filters and sort parsing. Blank audiobook text lists all matching audiobook documents. Change raw search to depend on `CatalogRepository`. Include projection timestamp and cursor in both responses.

- [ ] **Step 5: Delete recursive raw search**

Remove `Browser.Search`, `SearchResult`, `normalizeSearchText`, the result limit, and their tests. Keep `Browser.List` for current directory navigation.

- [ ] **Step 6: Run server route and library tests**

Run: `cd server; go test ./internal/httpapi ./internal/library -v`

Expected: PASS, including the panic-browser proof that search reads no filesystem.

- [ ] **Step 7: Commit**

```powershell
git add server/internal/httpapi server/internal/library/browser.go server/internal/library/browser_test.go
git commit -m "feat(api): serve shared indexed library search"
```

---

### Task 6: Add Debounced Root Monitoring

**Files:**
- Modify: `server/go.mod`
- Modify: `server/go.sum`
- Create: `server/internal/monitor/root_monitor.go`
- Create: `server/internal/monitor/root_monitor_test.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`
- Modify: `server/cmd/bookbox-catalog-worker/main_test.go`

**Interfaces:**
- Consumes: configured roots and `EnqueueDeduplicated`.
- Produces: `monitor.RootMonitor.Run(context.Context) error`.
- Uses: `github.com/fsnotify/fsnotify` with a five-second per-root debounce.

- [ ] **Step 1: Write failing tests with an injected event source**

Test coalescing, root isolation, watcher failure, and follow-up scheduling when the base dedupe call returns a running job:

```go
func TestRootMonitorSchedulesOneFollowUpForRunningScan(t *testing.T) {
	repository := &monitorRepositoryFake{base: catalog.WorkerJob{
		ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Status: catalog.JobRunning,
	}}
	monitor := monitorFixture(repository)
	monitor.accept("audiobooks")
	monitor.fireDebounce("audiobooks")
	want := []string{
		"scan_root:audiobooks",
		"scan_root_followup:audiobooks:11111111-1111-1111-1111-111111111111",
	}
	if diff := cmp.Diff(want, repository.keys); diff != "" { t.Fatal(diff) }
}
```

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd server; go test ./internal/monitor -v`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Add fsnotify and implement the monitor**

Run: `cd server; go get github.com/fsnotify/fsnotify@v1.10.1`

Wrap fsnotify behind an internal `eventSource`. Add existing directories at startup and new directories after create events. Translate create, write, rename, and remove events to a root ID without logging absolute event paths.

After debounce, enqueue `scan_root:<root>`. If the returned job is running, enqueue `scan_root_followup:<root>:<running-job-id>`. Repeated events dedupe against that follow-up key.

- [ ] **Step 4: Integrate monitoring with the worker command**

Run monitor and worker under the same cancellation context. Log monitor startup/runtime errors as warnings while startup, configurable interval, and six-hour fallback scans continue.

- [ ] **Step 5: Run monitor and command tests**

Run: `cd server; go test ./internal/monitor ./cmd/bookbox-catalog-worker -v`

Expected: PASS for debounce, follow-up dedupe, worker wiring, and periodic fallback.

- [ ] **Step 6: Commit**

```powershell
git add server/go.mod server/go.sum server/internal/monitor server/cmd/bookbox-catalog-worker
git commit -m "feat(worker): monitor roots for catalog changes"
```

---

### Task 7: Decode Shared Search Contracts on Android

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/model/ApiModels.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/CatalogApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/BrowseApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/repository/CatalogRepository.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/repository/BrowseRepository.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/core/model/ApiModelsTest.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/core/repository/CatalogRepositoryTest.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/core/repository/BrowseRepositoryTest.kt`

**Interfaces:**
- Produces: `SearchSnapshotDto`, `BookSearchGroupDto`, `RawSearchEntryDto`, and `RawSearchPageDto`.
- Preserves: cover URL same-origin validation and 2 MiB response caps.

- [ ] **Step 1: Write failing JSON and request tests**

Use fixtures with singleton/duplicate groups, folder/file results, freshness, duplicate flags, and cursors. Assert files accept size while folders reject it. Assert groups contain unique child IDs.

- [ ] **Step 2: Run focused tests and confirm RED**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.model.ApiModelsTest" --tests "com.rendref.bookbox.core.repository.CatalogRepositoryTest" --tests "com.rendref.bookbox.core.repository.BrowseRepositoryTest"`

Expected: FAIL because the DTOs do not exist.

- [ ] **Step 3: Add the DTOs and page types**

```kotlin
@Serializable
data class SearchSnapshotDto(
    val scanRunId: String,
    val completedAt: String,
)

@Serializable
data class BookSearchGroupDto(
    val groupId: String,
    val duplicate: Boolean,
    val books: List<BookCardDto>,
)

@Serializable
data class RawSearchEntryDto(
    val name: String,
    val relativePath: String,
    val kind: String,
    val size: Long? = null,
    val modifiedAt: String? = null,
    val bookId: String? = null,
    val duplicateGroupId: String? = null,
    val duplicateBook: Boolean = false,
)
```

Change `PagedBooks.items` to `groups: List<BookSearchGroupDto>`. Replace `FolderSearchDto` with `RawSearchPageDto` carrying snapshot and cursor.

Add `durationMs: Long? = null` to `BookCardDto`; reject negative values. Keep duplicate identity on the containing group so a child card does not claim a duplicate outside that response group.

- [ ] **Step 4: Validate untrusted response data**

Require UUID syntax, nonempty groups, `duplicate == (books.size > 1)`, unique book IDs, valid relative paths, allowed raw kinds, valid timestamps, nonnegative sizes, and matching root/query. Normalize each child cover URL. Map HTTP 503 `search_index_building` to a typed `SearchIndexBuilding` IOException subtype.

- [ ] **Step 5: Run focused tests**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.model.ApiModelsTest" --tests "com.rendref.bookbox.core.repository.CatalogRepositoryTest" --tests "com.rendref.bookbox.core.repository.BrowseRepositoryTest"`

Expected: PASS for decoding, encoding, and hostile-response rejection.

- [ ] **Step 6: Commit**

```powershell
git add android/app/src/main/java/com/rendref/bookbox/core android/app/src/test/java/com/rendref/bookbox/core
git commit -m "feat(android): decode shared library search results"
```

---

### Task 8: Share Debounce and Retained-Result Behavior

**Files:**
- Create: `android/app/src/main/java/com/rendref/bookbox/core/search/DebouncedSearch.kt`
- Create: `android/app/src/test/java/com/rendref/bookbox/core/search/DebouncedSearchTest.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersViewModel.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/feature/library/LibraryViewModelTest.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/feature/folders/FoldersViewModelTest.kt`

**Interfaces:**
- Produces: `Flow<Q>.debouncedSearchUpdates(...)` and `SearchUpdate<Q, P>`.
- Consumes: feature-specific page loaders.

- [ ] **Step 1: Write failing shared behavior tests**

Cover 149/150 ms timing, one-character suppression, supplementary Unicode, cancellation, retained data during loading, stale-response rejection, and index-building failures:

```kotlin
@Test
fun `loading a replacement query retains the previous page`() = runTest(dispatcher) {
    val queries = MutableSharedFlow<String>()
    val updates = mutableListOf<SearchUpdate<String, List<String>>>()
    val job = launch {
        queries.debouncedSearchUpdates(
            debounceMillis = 150,
            text = { it },
            load = { listOf(it) },
        ).toList(updates)
    }
    queries.emit("dune")
    advanceTimeBy(150)
    runCurrent()
    queries.emit("foundation")
    runCurrent()
    assertEquals(listOf("dune"), updates.last().retainedPage)
    job.cancel()
}
```

- [ ] **Step 2: Run tests and confirm RED**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.search.DebouncedSearchTest"`

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the generic Flow operator**

Use `debounce(150)`, `flatMapLatest`, Unicode code-point counting, and generation-safe `Loading`, `Loaded`, `Failed`, and `IndexBuilding` updates. Let audiobook mode load blank text for normal catalog browsing; raw mode suppresses blank and one-character text.

- [ ] **Step 4: Migrate both ViewModels**

Remove local debounce/job code. Preserve the current page/groups during loading and failure. Clear pagination on a new query. For raw file results, load `parentPath(relativePath)` and set `searchHighlightedRelativePath` after the directory response arrives.

- [ ] **Step 5: Run shared and ViewModel tests**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.search.DebouncedSearchTest" --tests "com.rendref.bookbox.feature.library.LibraryViewModelTest" --tests "com.rendref.bookbox.feature.folders.FoldersViewModelTest"`

Expected: PASS for 150 ms debounce, retained data, typed building state, pagination, file opening, and cancellation.

- [ ] **Step 6: Commit**

```powershell
git add android/app/src/main/java/com/rendref/bookbox/core/search android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryViewModel.kt android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersViewModel.kt android/app/src/test/java/com/rendref/bookbox/core/search android/app/src/test/java/com/rendref/bookbox/feature/library/LibraryViewModelTest.kt android/app/src/test/java/com/rendref/bookbox/feature/folders/FoldersViewModelTest.kt
git commit -m "refactor(android): share indexed search behavior"
```

---

### Task 9: Render Duplicate Audiobooks and Raw Files

**Files:**
- Create: `android/app/src/main/java/com/rendref/bookbox/feature/library/DuplicateBookGroupCard.kt`
- Create: `android/app/src/main/java/com/rendref/bookbox/core/search/LibrarySearchField.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/BookCard.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/ProductionBookBoxApp.kt`
- Modify: `android/app/src/androidTest/java/com/rendref/bookbox/feature/library/LibraryScreenTest.kt`
- Modify: `android/app/src/androidTest/java/com/rendref/bookbox/feature/folders/FoldersScreenTest.kt`
- Modify: `android/app/src/androidTest/java/com/rendref/bookbox/AppShellJourneyTest.kt`

**Interfaces:**
- Consumes: audiobook groups, raw entries, duplicate flags, and file-highlight state.
- Produces: expandable duplicate parents and actionable child cards.

- [ ] **Step 1: Write failing Compose tests**

Assert duplicate parents start collapsed, announce copy count, expand to child titles, and dispatch each child's existing action. Assert raw file/folder cards show distinct labels, duplicate badges, relative paths, and callbacks.

- [ ] **Step 2: Run Compose tests and confirm RED**

Run: `cd android; .\gradlew.bat connectedDebugAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=com.rendref.bookbox.feature.library.LibraryScreenTest,com.rendref.bookbox.feature.folders.FoldersScreenTest`

Expected: FAIL because groups and raw file results are not rendered.

- [ ] **Step 3: Add the shared search field**

Create `LibrarySearchField(value, label, isLoading, onValueChange)`. Use it from both screens. Keep results beneath a small progress indicator during replacement loads. Render index-building, stale, empty, and error states with distinct copy.

- [ ] **Step 4: Render audiobook groups**

Flatten groups into stable grid items. Give duplicate parents full span. Store expanded IDs with `rememberSaveable`. Render children one level down with start padding while calling `AudiobookCard`; add an optional duplicate badge to that card.

- [ ] **Step 5: Render raw folders and files**

Replace `FolderSearchResultRow` with `RawSearchResultCard`. Choose folder/file icon and action from `kind`. Show size for files, full relative path for both, and `Duplicate book` when flagged. Highlight the matching directory row after a file result opens its parent.

- [ ] **Step 6: Run Compose and shell tests**

Run: `cd android; .\gradlew.bat connectedDebugAndroidTest -Pandroid.testInstrumentationRunnerArguments.class=com.rendref.bookbox.feature.library.LibraryScreenTest,com.rendref.bookbox.feature.folders.FoldersScreenTest,com.rendref.bookbox.AppShellJourneyTest`

Expected: PASS for expansion, child actions, raw navigation, badges, highlight, and search states.

- [ ] **Step 7: Commit**

```powershell
git add android/app/src/main/java/com/rendref/bookbox android/app/src/androidTest/java/com/rendref/bookbox
git commit -m "feat(android): render shared search result modes"
```

---

### Task 10: Verify Integration, Operations, and Performance

**Files:**
- Create: `e2e/tests/catalog-search.tests.ps1`
- Modify: `e2e/run.ps1`
- Modify: `docs/android.md`
- Modify: `docs/catalog.md`
- Modify: `docs/operations.md`
- Modify: `deploy/scripts/smoke-catalog.ps1`

**Interfaces:**
- Consumes: completed server and Android contracts.
- Produces: release-level proof for PostgreSQL search, monitored refresh, and raw navigation.

- [ ] **Step 1: Write failing E2E assertions**

Extend the disposable library with loose MP3/M4B copies, a different narrator, nested audio plus cover, unrelated text, and an empty folder. Assert both modes:

```powershell
Assert-JsonPath $books '$.groups[?(@.duplicate==true)].books.length()' 2
Assert-JsonPath $raw '$.entries[?(@.kind=="file" && @.name=="Dune.m4b")].duplicateBook' $true
Assert-JsonMissing $raw 'notes.txt'
Assert-JsonMissing $raw 'Empty Folder'
```

Remove API access to the disposable source after publication, repeat searches, and require HTTP 200. Add a source file, wait for monitoring, and require a changed projection timestamp.

- [ ] **Step 2: Run focused E2E and confirm RED**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\catalog-search.tests.ps1`

Expected: FAIL until both modes, monitoring, and duplicate grouping exist.

- [ ] **Step 3: Update smoke checks and docs**

Document both presentations, the two-character threshold, 150 ms debounce, duplicate rules, six-hour fallback, manual rescan, stale/index-building states, and PostgreSQL-only request path. Add smoke requests for both endpoints.

- [ ] **Step 4: Run all server tests**

Run: `cd server; go test ./...`

Expected: PASS with PostgreSQL tests enabled by `BOOKBOX_TEST_POSTGRES_DSN`.

- [ ] **Step 5: Run Android unit and lint checks**

Run: `cd android; .\gradlew.bat testDebugUnitTest lintDebug`

Expected: PASS.

- [ ] **Step 6: Run focused E2E**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File e2e\tests\catalog-search.tests.ps1`

Expected: PASS for duplicate grouping, raw entities, no request-time filesystem access, and monitored refresh.

- [ ] **Step 7: Run release verification**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\verify-release.ps1`

Expected: PASS. If it requires fresh artifacts, run the monitored `deploy\scripts\build-release.ps1` first, then rerun verification.

- [ ] **Step 8: Inspect and commit**

Run: `git diff --check; git status --short`

Expected: no whitespace errors; only planned files plus the user's pre-existing deployment changes appear.

```powershell
git add e2e/tests/catalog-search.tests.ps1 e2e/run.ps1 docs/android.md docs/catalog.md docs/operations.md deploy/scripts/smoke-catalog.ps1
git commit -m "test(search): verify indexed library search end to end"
```

---

## Final Review Checklist

- [ ] Search code contains no `filepath.WalkDir`, `os.ReadDir`, resolver, or source-root call on either request path.
- [ ] A failed scan or projection build leaves the prior snapshot queryable.
- [ ] Audiobook search returns one child card per inferred audiobook and groups only same-narrator, close-duration copies.
- [ ] Raw search returns recognized folders/files, excludes unrelated content, opens files through their parent, and shows duplicate badges.
- [ ] Both screens use the shared 150 ms debounce and retain prior results during replacement loads.
- [ ] Two-character, trigram, cursor, root-isolation, authentication, path-confidentiality, and response-size tests pass.
- [ ] Filesystem events schedule scans while startup, periodic, and manual scans still work.
- [ ] The 100,000-document fixture meets p95 and cold-query targets.
- [ ] User-owned changes outside the planned file set remain untouched.

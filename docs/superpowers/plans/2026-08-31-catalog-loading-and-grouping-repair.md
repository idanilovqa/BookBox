# Catalog Loading and Grouping Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the Android catalog and raw search, make structural worker jobs converge ahead of enrichment, and rebuild a stable folder-aware audiobook catalog without modifying source files.

**Architecture:** Redeploy the current API so its grouped response matches the installed APK. In the catalog repository, give due structural jobs an explicit priority. In the staged worker, deduplicate regroup work by root/scope, resolve generation conflicts in place, and limit enrichment fan-out to the regrouped scope. Rebuild and replace the owned Windows worker, then republish derived catalog/search state.

**Tech Stack:** Go 1.25, PostgreSQL 18, Docker Compose, PowerShell, Kotlin/Android contract tests, Windows Service Control Manager.

## Global Constraints

- Do not rename, move, delete, retag, or rewrite audiobook source files.
- Preserve `docs/roadmap.md` and `.codex-remote-attachments/` as unrelated user changes.
- Run at most one `pytest` process; this plan uses no pytest.
- Use test-first red/green cycles for every production-code change.
- Replace only the managed `bookbox-api` container and owned `BookBoxCatalogWorker` service.
- Treat the final audiobook count as an observed, audited output, not a target value.

---

### Task 1: Prioritize Structural Worker Jobs

**Files:**
- Modify: `server/internal/catalog/postgres.go`
- Test: `server/internal/catalog/postgres_test.go`

**Interfaces:**
- Consumes: `PostgresRepository.ClaimJob(ctx, workerID, now)` and existing `WorkerJob.Kind` values.
- Produces: deterministic SQL claim priority with structural work ahead of `enrich_book` while retaining FIFO order inside each priority.

- [ ] **Step 1: Write the failing repository test**

Add a test that enqueues an older due `enrich_book` and a newer due
`regroup_scope`, claims one job, and requires the regroup job:

```go
func TestClaimJobPrioritizesStructuralWorkOverOlderEnrichment(t *testing.T) {
	repository, _ := workerJobFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	enrichment := WorkerJob{ID: uuid.New(), Kind: JobEnrichBook,
		Payload: map[string]any{"bookId": uuid.NewString()}, RunAfter: now.Add(-time.Hour)}
	regroup := WorkerJob{ID: uuid.New(), Kind: JobRegroupScope,
		Payload: map[string]any{"rootId": "audiobooks", "scopePath": "Book", "catalogGeneration": int64(1)},
		RunAfter: now.Add(-time.Minute)}
	if err := repository.Enqueue(ctx, enrichment); err != nil { t.Fatal(err) }
	if err := repository.Enqueue(ctx, regroup); err != nil { t.Fatal(err) }
	claimed, err := repository.ClaimJob(ctx, "worker-a", now)
	if err != nil { t.Fatal(err) }
	if claimed.ID != regroup.ID { t.Fatalf("claimed %s (%s), want structural %s", claimed.ID, claimed.Kind, regroup.ID) }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `cd server; go test ./internal/catalog -run TestClaimJobPrioritizesStructuralWorkOverOlderEnrichment -count=1 -v`

Expected: FAIL because FIFO claims the older `enrich_book`.

- [ ] **Step 3: Add explicit SQL priority**

Change the candidate ordering in `ClaimJob` to:

```sql
order by case kind
    when 'reorganize_files' then 10
    when 'scan_root' then 20
    when 'probe_file' then 30
    when 'inspect_companion' then 30
    when 'regroup_scope' then 40
    when 'publish_search' then 50
    when 'publish_cover' then 60
    when 'enrich_book' then 70
    else 100
  end,
  run_after, created_at, id
```

- [ ] **Step 4: Run the focused and catalog repository tests**

Run: `cd server; go test ./internal/catalog -run 'TestClaimJob|TestPostgresRepositoryDeduplicatesOnlyActiveJobs|TestRecoverExpiredJobs' -count=1 -v`

Expected: PASS.

- [ ] **Step 5: Commit the priority change**

```powershell
git add server/internal/catalog/postgres.go server/internal/catalog/postgres_test.go
git commit -m "fix(worker): prioritize structural catalog jobs"
```

### Task 2: Coalesce Regroup Jobs and Retry Conflicts In Place

**Files:**
- Modify: `server/internal/worker/staged.go`
- Modify: `server/internal/worker/staged_catalog_test.go`

**Interfaces:**
- Consumes: `enqueueRegroup(ctx, repo, rootID, scope, generation)` and `RegroupRepository`.
- Produces: active dedupe key `regroup_scope:<root>:<scope>` and a maximum of three in-handler grouping attempts using the current catalog generation after conflicts.

- [ ] **Step 1: Change existing expectations and add a conflict regression test**

Update probe and companion assertions to expect keys without the generation,
for example `regroup_scope:audiobooks:Book`. Replace the stale requeue test with:

```go
func TestRegroupScopeRetriesGenerationConflictWithoutReplacementJob(t *testing.T) {
	repository := &stagedRepositoryFake{
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 7},
		refinementErrors: []error{catalog.ErrConflict, nil},
		tree: grouping.Tree{RootID: "audiobooks", ScopePath: "Book"},
	}
	handler := RegroupScopeHandler{Repo: repository}
	job := catalog.WorkerJob{Kind: catalog.JobRegroupScope, Payload: map[string]any{
		"rootId": "audiobooks", "scopePath": "Book", "catalogGeneration": float64(6),
	}}
	if err := handler.Handle(context.Background(), job); err != nil { t.Fatal(err) }
	if repository.refinements != 2 { t.Fatalf("refinements=%d, want 2", repository.refinements) }
	if repository.lastRefinement.ExpectedGeneration != 7 {
		t.Fatalf("expected generation=%d, want 7", repository.lastRefinement.ExpectedGeneration)
	}
	if !reflect.DeepEqual(repository.dedupeKeys, []string{"publish_search:audiobooks:7"}) {
		t.Fatalf("unexpected replacement jobs: %v", repository.dedupeKeys)
	}
}
```

Add `refinementErrors []error` to `stagedRepositoryFake` and consume one error
per `ApplyGroupingRefinement` call before falling back to `refinementErr`.

- [ ] **Step 2: Run worker tests and verify RED**

Run: `cd server; go test ./internal/worker -run 'TestProbeFile|TestInspectCompanion|TestRegroupScope' -count=1 -v`

Expected: FAIL on generation-bearing keys and replacement regroup behavior.

- [ ] **Step 3: Remove generation from active regroup identity**

Keep the payload generation, but build the key as:

```go
key := "regroup_scope:" + rootID + ":" + scope
```

- [ ] **Step 4: Implement bounded in-place regroup retries**

Add `const regroupConflictAttempts = 3`. In `RegroupScopeHandler.Handle`, loop
from attempt zero to that bound. For each attempt, reload the grouping tree,
infer the result, and call `ApplyGroupingRefinement` with the current expected
generation. On `catalog.ErrConflict`, load `CurrentCatalogGeneration`, assign
its value to `expected`, and continue. Return the conflict after the final
attempt. Queue publication only after a successful refinement and never queue
a replacement regroup from the conflict branch.

- [ ] **Step 5: Run focused worker tests**

Run: `cd server; go test ./internal/worker -run 'TestProbeFile|TestInspectCompanion|TestRegroupScope|TestPublishSearch' -count=1 -v`

Expected: PASS.

- [ ] **Step 6: Commit regroup convergence**

```powershell
git add server/internal/worker/staged.go server/internal/worker/staged_catalog_test.go
git commit -m "fix(worker): coalesce catalog regroup work"
```

### Task 3: Limit Enrichment to the Regrouped Scope

**Files:**
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/worker/staged.go`
- Modify: `server/internal/worker/staged_catalog_test.go`

**Interfaces:**
- Consumes: `queueIncompleteBooks` pagination and each `catalog.Book.SourceFolder`.
- Produces: `queueIncompleteBooksInScope(ctx, repository, rootID, scope)`; root scope retains one intentional all-root pass, while non-root regroup only queues books at or below its normalized path.

- [ ] **Step 1: Write the failing scoped-enrichment test**

Create two incomplete books in the fake repository, one under `Author/Book`
and one under `Other/Title`. Run a regroup for `Author/Book` and assert the
dedupe keys contain publication plus only the first book's enrichment key.

```go
func TestRegroupScopeQueuesEnrichmentOnlyInsideScope(t *testing.T) {
	inside, outside := uuid.New(), uuid.New()
	repository := &stagedRepositoryFake{
		generation: catalog.CatalogGeneration{RootID: "audiobooks", Value: 6},
		tree: grouping.Tree{RootID: "audiobooks", ScopePath: "Author/Book"},
		books: []catalog.Book{
			{ID: inside, RootID: "audiobooks", SourceFolder: "Author/Book", Title: "Book", ReviewState: catalog.ReviewUnreviewed, MetadataSource: catalog.MetadataInferred},
			{ID: outside, RootID: "audiobooks", SourceFolder: "Other/Title", Title: "Title", ReviewState: catalog.ReviewUnreviewed, MetadataSource: catalog.MetadataInferred},
		},
	}
	job := catalog.WorkerJob{Kind: catalog.JobRegroupScope, Payload: map[string]any{
		"rootId": "audiobooks", "scopePath": "Author/Book", "catalogGeneration": float64(5),
	}}
	if err := (RegroupScopeHandler{Repo: repository}).Handle(context.Background(), job); err != nil { t.Fatal(err) }
	want := []string{"publish_search:audiobooks:6", "enrich_book:" + inside.String()}
	if !reflect.DeepEqual(repository.dedupeKeys, want) { t.Fatalf("keys=%v, want %v", repository.dedupeKeys, want) }
}
```

- [ ] **Step 2: Run the test and verify RED**

Run: `cd server; go test ./internal/worker -run TestRegroupScopeQueuesEnrichmentOnlyInsideScope -count=1 -v`

Expected: FAIL because both incomplete books are queued.

- [ ] **Step 3: Implement normalized scope filtering**

Make `queueIncompleteBooks` delegate to
`queueIncompleteBooksInScope(..., "")`. In the scoped function normalize `.`
to empty and queue a book only when:

```go
inScope := scope == "" || book.SourceFolder == scope || strings.HasPrefix(book.SourceFolder, scope+"/")
```

Retain all existing confirmed, missing, pagination, and dedupe behavior. Call
the scoped function from `RegroupScopeHandler`.

- [ ] **Step 4: Run worker tests**

Run: `cd server; go test ./internal/worker -count=1 -v`

Expected: PASS.

- [ ] **Step 5: Commit scoped enrichment**

```powershell
git add server/internal/worker/worker.go server/internal/worker/staged.go server/internal/worker/staged_catalog_test.go
git commit -m "fix(worker): scope post-grouping enrichment"
```

### Task 4: Verify, Build, Deploy, and Recover the Catalog

**Files:**
- Verify: `android/app/src/test/java/com/rendref/bookbox/core/repository/CatalogRepositoryTest.kt`
- Verify: `android/app/src/test/java/com/rendref/bookbox/core/model/ApiModelsTest.kt`
- Use: `deploy/compose.yml`
- Use: `deploy/scripts/build-release.ps1`
- Use: `deploy/scripts/remove-catalog-worker-service.ps1`
- Use: `deploy/scripts/install-catalog-worker-service.ps1`

**Interfaces:**
- Consumes: current grouped API source, release artifacts, owned Windows worker service, and existing PostgreSQL inventory.
- Produces: live grouped API, repaired native worker, converged structural queue, and current search snapshot.

- [ ] **Step 1: Run the complete server test suite**

Run: `cd server; go test ./... -count=1`

Expected: PASS.

- [ ] **Step 2: Run Android JVM contract tests**

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "com.rendref.bookbox.core.repository.CatalogRepositoryTest" --tests "com.rendref.bookbox.core.model.ApiModelsTest"`

Expected: BUILD SUCCESSFUL.

- [ ] **Step 3: Build and recreate only the API service**

Run:

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml build bookbox-api
docker compose --env-file deploy\.env -f deploy\compose.yml up -d --no-deps bookbox-api
```

Expected: the API container becomes healthy and an authenticated
`GET /v1/books?sort=title` response begins with `groups`, not `items`.

- [ ] **Step 4: Build the Windows worker release artifact**

Run: `powershell -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1`

Expected: `artifacts\bookbox-catalog-worker.exe` exists and release verification passes.

- [ ] **Step 5: Replace only the owned worker service**

Record the current service PID and stop it. Verify that exact PID exits. Use
the existing removal and installation scripts with the recorded installed
configuration and library roots; preserve ProgramData configuration, logs,
and secrets. Start `BookBoxCatalogWorker`, verify `Running`, and verify its
executable hash matches the new artifact.

- [ ] **Step 6: Trigger one controlled structural rebuild**

Enqueue one deduplicated full-root `scan_root:audiobooks:` job using the
repository's supported worker path. Do not directly edit book/search rows.
Monitor the worker log and database until due `scan_root`, `probe_file`,
`inspect_companion`, `regroup_scope`, and `publish_search` counts reach zero.

- [ ] **Step 7: Verify live behavior and audit counts**

Authenticate against the LAN API and assert:

```text
GET /v1/books?sort=title             -> 200, groups envelope
GET /v1/browse/search?...sanderson  -> 200, non-empty entries
```

Validate the catalog response with the Android invariants. Query and report
catalog generation, search generation, grouped books, active audio files,
audio-containing folders, single-file books, and multi-file books. Require
catalog and search generations to match and no due structural work to remain.

- [ ] **Step 8: Commit any verification documentation changes**

If verification required repository documentation changes, stage only those
files and commit them. Do not stage `docs/roadmap.md` or attachments.

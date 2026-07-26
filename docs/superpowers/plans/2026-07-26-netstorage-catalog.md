# netStorage Catalog and Cover Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the PostgreSQL audiobook catalog, conservative grouping, local and online metadata, search, review operations, and isolated cover publication.

**Architecture:** The API reads and updates derived catalog records through repositories. A separate worker scans the library, calls metadata providers, and publishes validated covers. Only the worker receives a read-write library mount; it exposes no inbound port.

**Tech Stack:** Go 1.26.5, PostgreSQL 18, pgx/v5, FFmpeg/ffprobe 8, Open Library REST API, optional Google Books REST API, Docker Compose.

## Global Constraints

- Complete `2026-07-26-netstorage-server-transfer.md` first.
- PostgreSQL is the only application database.
- The API audiobook mount stays read-only.
- The worker may create or replace recognized cover files; it cannot change audio bytes, tags, filenames, or folder structure.
- Scan at startup, every six hours by default, and after a rescan request.
- One audio-containing folder represents one book unless strong `.m4b` evidence supports a split.
- Online search enriches books present in configured roots; it cannot add absent books to the catalog.
- Open Library works without an API key; Google Books runs only when a key exists.
- Poor local covers have a longest edge below 800 pixels.
- A replacement needs at least twice the pixel area, no smaller dimension, and no upscaling.
- Accepted cover formats are JPEG, PNG, and WebP with a 20 MB input limit.

---

### Task 1: PostgreSQL Schema and Repository Boundary

**Files:**
- Create: `server/migrations/0001_catalog.sql`
- Create: `server/internal/database/migrate.go`
- Create: `server/internal/database/migrate_test.go`
- Create: `server/internal/catalog/model.go`
- Create: `server/internal/catalog/repository.go`
- Create: `server/internal/catalog/postgres.go`
- Create: `server/internal/catalog/postgres_test.go`
- Modify: `server/cmd/api/main.go`

**Interfaces:**
- Produces: `database.Migrate(ctx context.Context, pool *pgxpool.Pool) error`
- Produces: `catalog.Repository` with roots, files, books, assignments, matches, cover events, scan runs, and jobs
- Produces: `catalog.PostgresRepository`

- [ ] **Step 1: Write a failing migration test**

```go
func TestMigrateCreatesCatalogTablesOnce(t *testing.T) {
    pool := testpostgres.Open(t)
    if err := Migrate(context.Background(), pool); err != nil { t.Fatal(err) }
    if err := Migrate(context.Background(), pool); err != nil { t.Fatal(err) }
    for _, table := range []string{"library_roots", "source_files", "books", "book_files", "metadata_matches", "cover_events", "worker_jobs"} {
        var exists bool
        if err := pool.QueryRow(context.Background(),
            `select to_regclass('public.' || $1) is not null`, table).Scan(&exists); err != nil || !exists {
            t.Fatalf("table %s exists=%v err=%v", table, exists, err)
        }
    }
}
```

- [ ] **Step 2: Verify RED**

Run: `cd server; go test ./internal/database -run TestMigrate -v`

Expected: FAIL because migration code and SQL do not exist.

- [ ] **Step 3: Create the transactional schema**

`0001_catalog.sql` must define:

```sql
create extension if not exists pg_trgm;
create table schema_migrations(version bigint primary key, applied_at timestamptz not null default now());
create table library_roots(id text primary key, display_name text not null, scanned_at timestamptz);
create table scan_runs(id uuid primary key, root_id text not null references library_roots(id), started_at timestamptz not null, finished_at timestamptz, status text not null, error text);
create table source_files(
  id uuid primary key, root_id text not null references library_roots(id), relative_path text not null,
  size bigint not null, modified_at timestamptz not null, sha256 text, media jsonb not null default '{}',
  scan_run_id uuid references scan_runs(id), missing_at timestamptz, unique(root_id, relative_path)
);
create table books(
  id uuid primary key, root_id text not null references library_roots(id), source_folder text not null,
  grouping_key text not null, grouping_locked boolean not null default false,
  title text not null, subtitle text, authors text[] not null default '{}', narrators text[] not null default '{}',
  series text, series_position text, publisher text, published_date text, publication_year integer,
  genres text[] not null default '{}', provider_subjects text[] not null default '{}',
  identifiers jsonb not null default '{}', metadata_source text not null, confidence numeric(4,3) not null,
  review_state text not null, confirmed_match_id uuid, created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create table book_files(book_id uuid not null references books(id) on delete cascade, file_id uuid not null references source_files(id), role text not null, primary key(book_id,file_id));
create table grouping_corrections(id uuid primary key, root_id text not null references library_roots(id), source_folder text not null, groups jsonb not null, updated_at timestamptz not null default now(), unique(root_id,source_folder));
create table metadata_matches(id uuid primary key, book_id uuid not null references books(id) on delete cascade, provider text not null, provider_id text not null, score numeric(4,3) not null, payload jsonb not null, cover_url text, confirmed boolean not null default false);
alter table books add constraint books_confirmed_match_fk foreign key(confirmed_match_id) references metadata_matches(id);
create table cover_events(id uuid primary key, book_id uuid not null references books(id), action text not null, destination text not null, old_sha256 text, new_sha256 text not null, old_dimensions int[], new_dimensions int[] not null, provider text not null, backup_key text, created_at timestamptz not null default now());
create table worker_jobs(id uuid primary key, kind text not null, payload jsonb not null, status text not null default 'queued', run_after timestamptz not null default now(), attempts integer not null default 0, locked_at timestamptz, last_error text, created_at timestamptz not null default now());
create index books_title_trgm on books using gin(title gin_trgm_ops);
create index books_search on books using gin(to_tsvector('simple', coalesce(title,'') || ' ' || array_to_string(authors,' ') || ' ' || coalesce(series,'') || ' ' || array_to_string(narrators,' ') || ' ' || array_to_string(genres,' ')));
```

Embed migrations with `//go:embed`, take a PostgreSQL advisory lock, run each unapplied file in a transaction, and insert its numeric version.

- [ ] **Step 4: Define repository models and interface**

```go
type Repository interface {
    BeginScan(context.Context, string) (ScanRun, error)
    UpsertSourceFiles(context.Context, uuid.UUID, []SourceFile) error
    FinishScan(context.Context, uuid.UUID, ScanResult) error
    ReplaceInferredBooks(context.Context, string, []InferredBook) error
    ListBooks(context.Context, BookQuery) (Page[Book], error)
    GetBook(context.Context, uuid.UUID) (BookDetail, error)
    SaveMatches(context.Context, uuid.UUID, []MetadataMatch) error
    ConfirmMatch(context.Context, uuid.UUID, uuid.UUID) error
    SaveGrouping(context.Context, GroupingCorrection) error
    Enqueue(context.Context, WorkerJob) error
    ClaimJob(context.Context, string, time.Time) (WorkerJob, error)
    CompleteJob(context.Context, uuid.UUID, error) error
    RecordCoverEvent(context.Context, CoverEvent) error
}
```

Define every struct in `model.go` with JSON-independent domain fields and UUID identifiers. `ReplaceInferredBooks` must key inferred books by `(root_id, source_folder, grouping_key)`, load `grouping_corrections` first, and leave rows with `grouping_locked=true` unchanged unless their assigned source files disappeared.

- [ ] **Step 5: Test query paging and correction persistence**

Create three books in PostgreSQL, query by author and year with page size two, confirm a match, run `ReplaceInferredBooks`, and assert that the confirmed match and grouping correction remain.

Run: `cd server; go test ./internal/catalog -run 'TestPostgres' -v`

Expected: PASS.

- [ ] **Step 6: Wire migrations at API startup and commit**

Run: `cd server; go test ./internal/database ./internal/catalog -v`

Expected: PASS with a disposable PostgreSQL container and no leaked container after test cleanup.

```bash
git add server/migrations server/internal/database server/internal/catalog server/cmd/api
git commit -m "feat(catalog): add PostgreSQL catalog store"
```

### Task 2: Media Scan and Conservative Grouping

**Files:**
- Create: `server/internal/media/probe.go`
- Create: `server/internal/media/ffprobe.go`
- Create: `server/internal/media/ffprobe_test.go`
- Create: `server/internal/scanner/scanner.go`
- Create: `server/internal/scanner/scanner_test.go`
- Create: `server/internal/grouping/group.go`
- Create: `server/internal/grouping/group_test.go`

**Interfaces:**
- Produces: `media.Probe.Probe(ctx, path string) (media.Metadata, error)`
- Produces: `scanner.Scanner.Scan(ctx, root config.Root) (scanner.Result, error)`
- Produces: `grouping.Infer(folder scanner.Folder) []catalog.InferredBook`

- [ ] **Step 1: Write ffprobe parsing tests**

```go
func TestParseFFProbeExtractsAudiobookFields(t *testing.T) {
    got, err := parseFFProbe([]byte(`{"format":{"duration":"3600.5","tags":{"title":"Dune","artist":"Frank Herbert","album":"Dune","genre":"Science Fiction","date":"1965","narrator":"Simon Vance"}},"streams":[{"codec_type":"audio","codec_name":"aac"}]}`))
    if err != nil { t.Fatal(err) }
    if got.Title != "Dune" || got.Duration != 3600500*time.Millisecond || got.Narrator != "Simon Vance" {
        t.Fatalf("%#v", got)
    }
}
```

- [ ] **Step 2: Implement a bounded ffprobe adapter**

Execute:

```text
ffprobe -v error -show_entries format=duration:format_tags:stream=codec_type,codec_name,width,height -of json <absolute-path>
```

Use `exec.CommandContext`, a 30-second timeout, a 4 MiB stdout cap, and a 1 MiB stderr cap. Return a typed `ErrUnsupportedMedia` for files with no audio stream.

- [ ] **Step 3: Write scanner extension and stale-file tests**

```go
func TestScanIncludesSupportedAudioAndRecognizedCovers(t *testing.T) {
    root := createTree(t, "Book/a.m4b", "Book/b.MP3", "Book/cover.jpg", "Book/notes.txt", "Book/download.part")
    got, err := newScanner(t).Scan(context.Background(), config.Root{ID:"audiobooks", ContainerPath:root})
    if err != nil { t.Fatal(err) }
    assertPaths(t, got, "Book/a.m4b", "Book/b.MP3", "Book/cover.jpg")
}
```

Support `.m4b`, `.m4a`, `.mp3`, `.aac`, `.flac`, `.ogg`, `.opus`, and `.wav`. Recognize case-insensitive `cover`, `folder`, and `front` JPEG/PNG/WebP names. Skip dotfiles, `.part`, `.tmp`, and configured exclusions.

- [ ] **Step 4: Write grouping tests before implementation**

```go
func TestInferKeepsChaptersAndSplitsDistinctM4BBooks(t *testing.T) {
    chapters := folder("Series", audio("01 Part One.mp3", tags("Book", "Author")), audio("02 Part Two.mp3", tags("Book", "Author")))
    if got := Infer(chapters); len(got) != 1 { t.Fatalf("chapter groups=%d", len(got)) }
    distinct := folder("Omnibus", audio("Dune.m4b", tags("Dune", "Frank Herbert")), audio("Dune Messiah.m4b", tags("Dune Messiah", "Frank Herbert")))
    if got := Infer(distinct); len(got) != 2 { t.Fatalf("book groups=%d", len(got)) }
}
```

- [ ] **Step 5: Implement normalized evidence scoring**

```go
type Evidence struct {
    NormalizedTitle  string
    NormalizedAuthor string
    IsStandaloneM4B  bool
    FilenameTitle    string
    ChapterLike      bool
}
```

Strip punctuation, case, edition suffixes, chapter/disc/part numbers, and repeated whitespace for comparison only. Split standalone `.m4b` files when both normalized embedded title and filename title differ from peers, or embedded title plus author form a distinct identity. Assign `needs_review` when tags conflict or only filename evidence differs. Never split on chapter/disc/part tokens.

- [ ] **Step 6: Verify and commit**

Run: `cd server; go test ./internal/media ./internal/scanner ./internal/grouping -v`

Expected: PASS.

```bash
git add server/internal/media server/internal/scanner server/internal/grouping
git commit -m "feat(catalog): scan and group audiobook files"
```

### Task 3: Metadata Providers, Matching, and Search

**Files:**
- Create: `server/internal/metadata/provider.go`
- Create: `server/internal/metadata/openlibrary.go`
- Create: `server/internal/metadata/openlibrary_test.go`
- Create: `server/internal/metadata/googlebooks.go`
- Create: `server/internal/metadata/googlebooks_test.go`
- Create: `server/internal/metadata/matcher.go`
- Create: `server/internal/metadata/matcher_test.go`
- Create: `server/internal/catalog/search.go`
- Test: `server/internal/catalog/search_test.go`

**Interfaces:**
- Produces: `metadata.Provider.Search(ctx, metadata.Query) ([]metadata.Candidate, error)`
- Produces: `metadata.Matcher.Match(ctx, catalog.InferredBook) (metadata.Decision, error)`
- Produces: `catalog.BookQuery` search, filter, sort, and cursor paging

- [ ] **Step 1: Test provider request encoding and response mapping**

Use `httptest.Server` fixtures. Assert Open Library receives `title`, `author`, `limit=10`, and requested fields. Assert Google Books receives `q=intitle:<title> inauthor:<author>`, `printType=books`, `maxResults=10`, and the configured key. Assert provider errors include status but not the API key.

- [ ] **Step 2: Implement provider DTOs**

```go
type Query struct { Title, Author, ISBN string }
type Candidate struct {
    Provider, ProviderID, Title, Subtitle, Publisher, PublishedDate string
    Authors, Narrators, Genres, Subjects []string
    Series, SeriesPosition, ISBN, CoverURL string
}
type Provider interface { Search(context.Context, Query) ([]Candidate, error) }
```

Use an `http.Client` with a 10-second timeout, response body limit of 2 MiB, and a descriptive `User-Agent`. Retry HTTP 429 and 5xx twice with `Retry-After` support. Do not retry other 4xx responses. Inject each provider's base URL through its constructor so tests and the E2E fixture server never call a live provider.

- [ ] **Step 3: Test deterministic matching**

```go
func TestMatcherAutoAppliesHighConfidenceAndFlagsTie(t *testing.T) {
    high := score(local("Dune", "Frank Herbert"), candidate("Dune", "Frank Herbert", "1965"))
    if high < AutoApplyThreshold { t.Fatalf("score=%f", high) }
    decision := decide([]ScoredCandidate{{Score:.92}, {Score:.90}})
    if decision.ReviewState != "needs_review" { t.Fatalf("%#v", decision) }
}
```

Score normalized title 55%, author 25%, ISBN 15%, and year 5%. Auto-apply only at score `>=0.90` with a `>=0.08` lead over second place. Store lower candidates for Android review.

- [ ] **Step 4: Implement metadata precedence**

Confirmed match fields override provider and local fields. Local embedded fields override folder inference. Provider fields fill empty fields after a high-confidence match. Preserve provider subjects, normalize known aliases into genres, and store the matched edition's date and provider.

- [ ] **Step 5: Test PostgreSQL search**

Insert books with title, author, narrator, series, genre, year, folder, and source filename values. Assert one query can find each field. Test author/genre/year/root/review filters; title/author/year/recently-added/recently-scanned sorts; and opaque cursor paging without duplicates.

- [ ] **Step 6: Implement search query building**

Use parameterized SQL only. Combine `websearch_to_tsquery('simple',$1)` with trigram title similarity for misspellings. Join `book_files` and `source_files` when searching source filenames, and include `books.source_folder` in the text expression. Whitelist sort keys in Go and emit SQL fragments from a closed map. Encode cursors as base64url JSON containing sort value and book UUID.

- [ ] **Step 7: Verify and commit**

Run: `cd server; go test ./internal/metadata ./internal/catalog -v`

Expected: PASS with no live provider network calls.

```bash
git add server/internal/metadata server/internal/catalog
git commit -m "feat(catalog): enrich and search local books"
```

### Task 4: Cover Validation, Backup, and Publication

**Files:**
- Create: `server/internal/covers/image.go`
- Create: `server/internal/covers/image_test.go`
- Create: `server/internal/covers/publisher.go`
- Create: `server/internal/covers/publisher_test.go`
- Create: `server/internal/covers/downloader.go`
- Create: `server/internal/covers/downloader_test.go`

**Interfaces:**
- Produces: `covers.Inspect(io.Reader, int64) (covers.ImageInfo, []byte, error)`
- Produces: `covers.ShouldReplace(current, candidate covers.ImageInfo) bool`
- Produces: `(*covers.Publisher).Publish(ctx, covers.Request) (catalog.CoverEvent, error)`

- [ ] **Step 1: Write format, size, and quality tests**

```go
func TestShouldReplaceRequiresMaterialGain(t *testing.T) {
    current := ImageInfo{Width:600, Height:900}
    if !ShouldReplace(current, ImageInfo{Width:1200, Height:1800}) { t.Fatal("expected replacement") }
    if ShouldReplace(current, ImageInfo{Width:700, Height:1000}) { t.Fatal("accepted weak replacement") }
    if ShouldReplace(current, ImageInfo{Width:500, Height:2000}) { t.Fatal("accepted smaller dimension") }
}
```

Generate valid JPEG, PNG, and WebP fixtures. Assert rejection over 20 MB, invalid magic bytes, zero dimensions, and images exceeding 100 megapixels.

- [ ] **Step 2: Implement bounded decode and JPEG output**

Read through `io.LimitReader(max+1)`, call `image.DecodeConfig`, enforce dimensions, then decode. Register JPEG, PNG, and WebP decoders. When the destination extension is `.jpg` or `.jpeg`, encode PNG/WebP candidates at JPEG quality 92 without resizing. Preserve PNG only for an existing `.png` destination; create new artwork as `cover.jpg`.

- [ ] **Step 3: Write confinement and backup tests**

```go
func TestPublishOnlyChangesRecognizedCoverAndCreatesBackup(t *testing.T) {
    root := bookFolder(t, "Book/audio.m4b", "Book/folder.jpg")
    beforeAudio := sha256File(t, filepath.Join(root, "Book/audio.m4b"))
    event, err := newPublisher(t, root).Publish(ctx, requestFor("Book", "folder.jpg", highResJPEG(t)))
    if err != nil { t.Fatal(err) }
    if sha256File(t, filepath.Join(root, "Book/audio.m4b")) != beforeAudio { t.Fatal("audio changed") }
    if event.BackupKey == "" || !backupExists(t, event.BackupKey) { t.Fatal("backup missing") }
}
```

Also reject `../`, unknown folders, arbitrary destinations such as `audio.m4b`, symlink destinations, and candidate books absent from PostgreSQL.

- [ ] **Step 4: Implement cover publication**

Resolve the recognized book folder through the same canonical resolver as the API. Permit only existing recognized local cover basenames or new `cover.jpg`. Hash and copy an existing cover to `<backup-root>/<sha256>` with create-exclusive semantics. Write the candidate to a same-directory temporary file, `Sync`, close, and rename over the target. Record the event only after success. On failure, remove the temporary file and leave the target intact.

- [ ] **Step 5: Implement safe provider downloads**

Allow HTTPS only. Resolve redirects through the client and reject a redirect to HTTP. Limit to three redirects and 20 MB. Do not send app credentials or Google API keys to cover hosts. Feed downloaded bytes through `Inspect`.

- [ ] **Step 6: Verify mutation scope and commit**

Run: `cd server; go test ./internal/covers -v`

Expected: PASS, including a full tree snapshot that allows changes only to one expected cover path.

```bash
git add server/internal/covers
git commit -m "feat(catalog): publish audited high-quality covers"
```

### Task 5: Catalog API and Book Transfer Semantics

**Files:**
- Create: `server/internal/httpapi/books.go`
- Create: `server/internal/httpapi/books_test.go`
- Create: `server/internal/httpapi/catalog.go`
- Create: `server/internal/httpapi/catalog_test.go`
- Create: `server/internal/transfer/bookmanifest.go`
- Create: `server/internal/transfer/bookmanifest_test.go`
- Modify: `server/internal/httpapi/api.go`
- Modify: `server/internal/transfer/types.go`

**Interfaces:**
- Produces: all approved `/v1/books` and `/v1/catalog` routes
- Extends: `transfer.Selection` with `Kind: "book"` and `BookID`
- Produces: split-book destination naming from source `.m4b` stems

- [ ] **Step 1: Write API contract tests**

Assert:

- `GET /v1/books` returns paginated cards and filters.
- `GET /v1/books/{id}` returns details, attribution, and review state.
- `GET /v1/books/{id}/files` returns relative source files only.
- `POST /v1/catalog/matches/search` returns provider candidates for that local book.
- `PUT /v1/books/{id}/match` confirms a stored candidate and enqueues `publish_cover`.
- `PUT /v1/books/{id}/grouping` validates that all file IDs share the book's root/folder.
- `POST /v1/catalog/rescan` enqueues one deduplicated scan job.

- [ ] **Step 2: Define JSON request/response DTOs**

```go
type BookCardResponse struct {
    ID string `json:"id"`; Title string `json:"title"`; Authors []string `json:"authors"`
    PublicationYear *int `json:"publicationYear"`; Genres []string `json:"genres"`
    Series string `json:"series,omitempty"`; Narrators []string `json:"narrators,omitempty"`
    CoverURL string `json:"coverUrl,omitempty"`; ReviewState string `json:"reviewState"`
    SourceFolder string `json:"sourceFolder"`
}
type ConfirmMatchRequest struct { MatchID string `json:"matchId"` }
type GroupingRequest struct { Groups []struct{ BookID string; FileIDs []string } `json:"groups"` }
```

Use explicit field declarations rather than semicolon-compressed declarations in production code.

- [ ] **Step 3: Implement handlers and cover serving**

Apply body limits, UUID validation, repository transactions, and stable JSON errors. Serve cached/local card artwork through `GET /v1/books/{id}/cover` with ETag and cache headers. Never expose provider API keys, absolute paths, or backup paths.

- [ ] **Step 4: Test book-manifest naming**

```go
func TestBookManifestPreservesFolderOrUsesM4BStemForSplit(t *testing.T) {
    normal := buildBookManifest(t, normalBook("Original Folder"))
    if normal.Files[0].DestinationPath != "Original Folder/chapter.mp3" { t.Fatal(normal) }
    split := buildBookManifest(t, splitBook("Omnibus", "Dune Messiah.m4b"))
    if split.Files[0].DestinationPath != "Dune Messiah/Dune Messiah.m4b" { t.Fatal(split) }
}
```

For a split book, include assigned audio, same-stem sidecars, and the recognized shared cover. Sanitize Android-invalid `\ / : * ? " < > |` characters and control characters; trim trailing spaces/dots; fall back to detected title.

- [ ] **Step 5: Verify no API source mutation and commit**

Run: `cd server; go test ./internal/httpapi ./internal/transfer -v`

Expected: PASS. Snapshot tests must show that all API routes leave the source tree unchanged.

```bash
git add server/internal/httpapi server/internal/transfer
git commit -m "feat(catalog): expose searchable book catalog"
```

### Task 6: Worker Runtime and Container Isolation

**Files:**
- Create: `server/internal/worker/worker.go`
- Create: `server/internal/worker/worker_test.go`
- Create: `server/cmd/catalog-worker/main.go`
- Create: `deploy/Dockerfile.worker`
- Modify: `deploy/compose.yml`
- Modify: `deploy/config.example.yml`
- Create: `deploy/scripts/smoke-catalog.ps1`
- Create: `docs/catalog.md`

**Interfaces:**
- Produces: worker job kinds `scan_root`, `enrich_book`, and `publish_cover`
- Produces: startup scan and six-hour scheduled scans
- Produces: a worker container with no published or exposed port

- [ ] **Step 1: Test job claiming, retry, and scheduling**

Use a fake clock and repository. Assert one startup scan per root, periodic scan at six hours, `FOR UPDATE SKIP LOCKED` single-claim semantics, exponential retry capped at six hours, and failed provider calls leaving local metadata intact.

- [ ] **Step 2: Implement the worker loop**

```go
type Handler interface { Handle(context.Context, catalog.WorkerJob) error }
type Worker struct { Repo catalog.Repository; Handlers map[string]Handler; Clock func() time.Time }
func (w *Worker) Run(ctx context.Context) error
```

Poll every two seconds when idle. Claim jobs with a 15-minute stale-lock recovery. Handle SIGINT/SIGTERM by stopping claims and allowing the current operation 30 seconds to finish.

- [ ] **Step 3: Compose the scan pipeline**

For `scan_root`: scan, probe, upsert files, mark missing files, infer books, preserve confirmed corrections, enqueue enrichment for incomplete books, and finish the scan record. For `enrich_book`: query providers and store candidates/decision. For `publish_cover`: download/validate/compare/publish and record the event.

- [ ] **Step 4: Add the worker image and Compose service**

Build both `netstorage-catalog-worker` and FFmpeg/ffprobe. Mount:

```yaml
volumes:
  - type: bind
    source: ${NETSTORAGE_LIBRARY_PATH}
    target: /library/audiobooks
  - cover_cache:/var/lib/netstorage/covers
  - cover_backups:/var/lib/netstorage/backups
```

Do not add `ports` or `expose`. Set `cap_drop: [ALL]`, `no-new-privileges`, non-root user, read-only container root, and `/tmp` tmpfs. The library bind is the only writable source mount.

- [ ] **Step 5: Write and run the catalog smoke test**

The PowerShell script must copy `testdata/library` into a task-scoped temporary directory, start all services, wait for one completed scan, query `GET /v1/books`, request a test cover publication, and compare a before/after tree manifest. Permit only the expected cover change. Stop Compose in `finally` and verify containers exited.

Run: `powershell -ExecutionPolicy Bypass -File deploy/scripts/smoke-catalog.ps1`

Expected: PASS with no remaining task-owned containers or temporary files.

- [ ] **Step 6: Document operation and commit**

`docs/catalog.md` must cover scan status, manual rescan, review states, provider attribution, Google key setup, cover rules, audit history, and restoring a backed-up cover.

Run: `cd server; go test -race ./...`

Expected: PASS.

```bash
git add server deploy docs/catalog.md
git commit -m "feat(catalog): run isolated indexing and cover worker"
```

## Plan Completion Check

Run:

```powershell
Set-Location server
go test -race ./...
Set-Location ..
docker compose -f deploy/compose.yml config
powershell -ExecutionPolicy Bypass -File deploy/scripts/smoke-catalog.ps1
git status --short
```

Expected: tests pass, catalog cards appear from the synthetic library, only the worker's expected cover path changes, no containers remain, and the working tree is clean.

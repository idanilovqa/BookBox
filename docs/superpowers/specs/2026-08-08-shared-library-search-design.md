# Shared Library Search Design

## Goal

BookBox will search the latest completed catalog snapshot without reading the source filesystem during a request. One PostgreSQL-backed search service will support two presentations:

- Audiobook search returns inferred audiobooks for audiobook-card rendering.
- Raw library search returns physical folders and recognized files for raw-card rendering.

Both presentations will use the same query normalization, index, ranking, cancellation, pagination, and freshness rules. Search should show results within 300 milliseconds of the user's last keystroke on the private local network under the performance workload defined below.

## Scope

The project will replace the recursive filesystem implementation behind raw search. It will also move audiobook search onto the shared search projection and add duplicate-recording groups.

Raw search covers audiobook-related content:

- Recognized audio files.
- Recognized cover images.
- Folders that contain or are ancestors of recognized files.

Raw search excludes empty folders, hidden and configured-excluded content, symlinks, unrelated documents, and unsupported media. These rules match the catalog scanner's source boundary.

The latest completed catalog scan defines search freshness. Search requests do not inspect changes made after that scan.

## Domain Rules

### One audiobook per inferred recording

Each active inferred book produces one audiobook search document. A loose `Dune.m4b` and a loose `Foundation.m4b` produce two audiobook documents. `Frank Herbert/Dune/Dune.m4b` produces another audiobook document.

The Audiobooks screen renders each document as an audiobook card. Source layout does not change the card type on this screen.

### Raw entities preserve physical layout

The same loose `Dune.m4b` produces a raw file document. `Frank Herbert/Dune/Dune.m4b` produces raw folder documents for `Frank Herbert` and `Frank Herbert/Dune`, plus a raw file document for `Dune.m4b`.

A raw folder result opens that folder. A raw file result opens its parent folder and highlights the file. If one inferred audiobook uses several loose chapter files, audiobook search still returns one audiobook card while raw search can return each matching physical file.

### Duplicate recordings

BookBox classifies copies as one duplicate recording only when all of these conditions hold:

- Normalized effective title matches.
- Normalized author identity matches and is not empty.
- Normalized narrator identity matches and is not empty.
- Each copy has a known total audio duration.
- Durations differ by no more than the greater of two minutes or 0.5 percent, with a five-minute maximum tolerance.

Format, bitrate, and source path do not affect duplicate identity. Different narrator identities prevent grouping. Missing or conflicting narrator data prevents automatic grouping.

The projection builder sorts candidates by duration and forms deterministic clusters. Every member in a cluster must fall within the tolerance of the cluster's first member. A cluster requires at least two active audiobooks.

Audiobook search renders a duplicate group as a collapsed `Duplicate book` parent with its copy count. Expanding the parent renders each member as an ordinary audiobook card one level down. A user can open or transfer each member on its own.

Raw search keeps physical results flat. A raw folder or file linked to a duplicate-group member displays a `Duplicate book` badge.

## Architecture

### Search projection

A new `library_search_documents` table will hold the latest published search snapshot. Each row represents an `audiobook`, `folder`, or `file` document. The table will include:

- Stable document ID and entity kind.
- Root ID and source-relative path.
- Book ID for audiobook documents and linked raw entities when applicable.
- Display name and file metadata needed by the renderer.
- Effective title, authors, series, narrators, and source filenames.
- Total audio duration for audiobook documents.
- Duplicate-group ID and member count when applicable.
- Compact normalized name, path, metadata, and combined search values.
- Indexed character grams for two-character substring queries.
- Publishing scan ID and completion timestamp.

The database will enforce one current row for each stable entity identity within a root. It will cascade or reject references in line with the existing catalog ownership rules.

PostgreSQL will use:

- A GIN `pg_trgm` index over the normalized combined search value for substring and typo-tolerant matching of queries with at least three characters.
- A GIN index over generated character grams for two-character substring queries.
- B-tree indexes for root, kind, duplicate group, stable ordering, and cursor boundaries.

The existing catalog remains authoritative. `library_search_documents` is a derived read model that the worker can rebuild.

### Projection publication

After a root scan updates active source files and inferred books, the worker builds the root's complete document set. It derives folder documents from ancestor paths of recognized active files. It enriches raw documents with metadata from linked active audiobooks so an author or narrator query can locate the physical source.

The repository replaces one root's projection in a transaction. Readers see the prior snapshot or the new snapshot. They cannot see a partial replacement. The worker publishes the projection only after the catalog scan succeeds.

A scan or projection failure preserves the previous searchable snapshot. The repository records the failed scan through the existing scan state. API responses continue reporting the successful projection timestamp.

### Shared server service

An internal `LibrarySearch` service will accept:

- Root ID.
- Query text.
- Mode: `audiobooks` or `raw`.
- Result limit.
- Opaque cursor.

Both public search routes call this service:

- `GET /v1/books` uses audiobook mode.
- `GET /v1/browse/search` uses raw mode.

The service performs no filesystem operations. It queries only the published search projection and related read-only catalog records required to shape a response.

## Search Semantics

### Query activation and normalization

Android submits a query after it contains at least two Unicode letters or digits. The server applies the same validation.

Normalization lowercases Unicode letters and retains digits. It creates a compact form that removes spaces and punctuation, allowing `black box` to match `Black.Box`, `black-box`, and `blackbox`. The projection also retains token boundaries for metadata ranking and typo tolerance.

Search covers these fields by default:

- Title and authors.
- Series and narrators.
- Source folder path.
- Audio filename or filenames.

Raw folder and file documents include associated audiobook metadata where the catalog can establish a link. Path and filename fields remain searchable when metadata is absent or wrong.

### Ranking

The service ranks candidates in this order:

1. Exact normalized title, display name, path basename, or filename.
2. Prefix matches on those fields.
3. Compact substring matches in path, filename, or title.
4. Metadata substring matches.
5. Trigram similarity for spelling mistakes.

Within the same rank, the service uses similarity score, normalized display name, relative path, entity kind, and stable document ID. This ordering makes pagination deterministic.

The service limits and paginates by audiobook member count in audiobook mode. A duplicate group and its children stay on the same page. Raw mode paginates physical documents. Both modes cap response size through existing API response limits.

## API Contracts

### Audiobook search

The `/v1/books` response will represent ordered search groups. A singleton group contains one audiobook card. A duplicate group includes its stable group ID, `Duplicate book` label, copy count, and child audiobook cards.

Each audiobook child retains the existing book ID, card metadata, cover URL, review state, and source folder. The response adds total duration and duplicate identity fields needed by the UI.

### Raw search

The `/v1/browse/search` response will include:

- Root ID and original query.
- Projection scan ID and completion timestamp.
- Ordered folder and file entries.
- Entry kind, display name, relative path, modified time, and file size where applicable.
- Linked book ID where one active audiobook owns the entity.
- Duplicate-group ID and warning flag where applicable.
- Opaque next cursor.

Raw responses do not expose absolute paths. The server applies the existing root and relative-path validation before serialization.

### Freshness

Both responses identify the completed projection they queried. Android can show a subtle stale-catalog notice when the timestamp exceeds two configured scan intervals. Empty results, stale results, and search failures remain distinct states.

## Android Design

### Shared search behavior

Android will extract a shared search coordinator and search field used by the Audiobooks and Raw Library screens. The shared behavior owns:

- Query activation and a 150-millisecond debounce.
- Cancellation and stale-response rejection.
- Loading, pagination, empty, stale, and error states.
- Preservation of current results while a replacement query loads.

Each feature supplies its repository call and renderer. A root change or query clear cancels pending work and resets pagination.

### Audiobook presentation

The Audiobooks screen renders singleton audiobook cards and collapsed duplicate parents. Expansion inserts child audiobook cards one level below the parent. Opening and transferring a child uses its existing book ID.

The screen keeps the current catalog filters and sort controls. Text search uses the shared projection; structured filters remain explicit query parameters and apply before pagination.

### Raw presentation

The Raw Library screen renders folder and file cards. Cards show the source-relative path so metadata cannot obscure the physical location. Folder cards open the folder. File cards open their parent and highlight the selected file.

Duplicate-linked cards display a `Duplicate book` badge. The screen does not merge or nest physical raw results.

## Catalog Monitoring

The worker retains its startup scan, configurable interval, and six-hour default. It will add root change monitoring to reduce discovery delay.

The monitor will:

- Watch configured mounted roots where the platform supplies filesystem events.
- Debounce changes per root before enqueueing the existing deduplicated `scan_root` job.
- Mark a root dirty when an event arrives during a scan and enqueue one follow-up scan after completion.
- Treat monitoring errors as warnings while periodic scans continue.

Docker Desktop may omit Windows bind-mount events. Periodic scanning remains the authoritative fallback. Manual rescan remains available through the existing API and Android control.

## Failure Handling

The API returns a search error when PostgreSQL or the projection is unavailable. It does not fall back to a filesystem walk.

The worker preserves the last completed projection when scanning, metadata extraction, grouping, duplicate classification, or projection publication fails. It records enough structured context to identify the root and failed stage without logging private absolute paths.

Android keeps the current result set during a refresh failure and shows the error inline. A first-query failure shows an error state instead of `No matches`.

## Performance Requirements

The integration performance fixture will contain 100,000 mixed search documents across multiple roots, including duplicate groups and long Unicode paths.

The target environment must meet these thresholds:

- Warm server search p95 below 50 milliseconds.
- Cold server search below 150 milliseconds.
- Android results visible within 300 milliseconds of the last keystroke on the private LAN, including the 150-millisecond debounce.

Query-plan tests will verify index use for two-character exact-substring queries, longer substring queries, and typo-tolerant queries. A supported search plan that scans the full projection fails the test.

No search request may read the configured source filesystem.

API and repository tests will inject a filesystem that fails on access and confirm that search still succeeds from PostgreSQL. This test guards against reintroducing request-time filesystem traversal.

## Test Coverage

### Projection and repository

- Loose audio files, dedicated folders, shared folders, and multipart audiobooks.
- Recognized cover images, unsupported files, empty folders, exclusions, hidden content, and symlinks.
- Ancestor-folder derivation and linked metadata aggregation.
- Root isolation, stable IDs, transactional replacement, and failed-publication preservation.
- Two-character, punctuation-insensitive, Unicode, path, metadata, typo, ordering, limit, and cursor cases.
- Mode isolation between audiobook and raw entities.

### Duplicate classification

- Same recording across MP3 and M4B encodings.
- Bitrate differences.
- Durations inside and outside the tolerance.
- Different narrators.
- Missing narrator or duration.
- Stable cluster membership and IDs across scans.
- Missing and restored copies.

### API and Android

- Existing authentication and path-confidentiality rules.
- Audiobook singleton and duplicate-group contracts.
- Raw folder and file contracts, badges, pagination, and freshness.
- Shared debounce, cancellation, stale-response rejection, and retained results.
- Duplicate expansion, child actions, raw folder opening, and file highlighting.
- Empty, stale, unavailable, and failed search states.

### Monitoring

- Event debounce and job deduplication.
- A change during an active scan schedules one follow-up.
- Monitor failure leaves interval scanning active.
- Startup and manual scans publish search projections.

## Migration and Rollout

A database migration will create the projection, helper normalization functions, and indexes. Deployment will rebuild the API and worker together.

The worker will enqueue scans at startup and populate each root's projection. Until a root publishes its first snapshot, search returns a distinct `index_building` response. Android shows `Building search index` and keeps normal non-search browsing available.

After rollout validation confirms projection-backed search, the project will remove `library.Browser.Search` and its recursive `filepath.WalkDir` implementation. Normal directory browsing will continue using the confined filesystem resolver because opening a selected raw folder requires current physical contents.

## Non-goals

- General-purpose search for unrelated files.
- Request-time filesystem freshness.
- Audio fingerprinting across missing or conflicting metadata.
- Automatic deletion or merging of duplicate copies.
- A separate search service outside PostgreSQL.

# Audiobook Catalog Recovery Design

## Goal

BookBox will populate the Audiobooks screen from PostgreSQL without waiting for a full-library metadata probe. A library inventory will create provisional audiobook records within one filesystem walk. Background jobs will extract metadata, refine groupings, and republish the shared search projection without reprocessing unchanged files.

This design addresses the live failure observed on August 8, 2026. The configured library contains 24,437 files, including 22,824 recognized audio files. The current worker probes every audio file before it writes any source file or book record. Scans run beyond the 15-minute worker lease, fail, and leave PostgreSQL with zero active source files and zero active books.

## Relationship to Shared Library Search

This design extends the approved [Shared Library Search Design](2026-08-08-shared-library-search-design.md) and its implementation plan in `docs/superpowers/plans/2026-08-08-shared-library-search.md`.

The shared-search decisions remain in force:

- Search requests read PostgreSQL and do not inspect configured roots.
- `library_search_documents` is a derived read model.
- The repository publishes one complete root projection in a transaction.
- A failed publication leaves the previous projection available.
- Duplicate-recording groups use normalized title, author, narrator, and duration. They do not represent grouping uncertainty.

The shared-search plan currently treats metadata extraction and grouping as part of one full scan. Implementation must revise that boundary. A successful scan will mean that BookBox completed and committed a fast file inventory. Metadata extraction and grouping refinement will run as durable follow-up work. Both inventory and later catalog refinements may publish a new complete search projection.

## Scope

The project will:

- Replace the all-or-nothing scan with staged inventory, probe, grouping, and projection work.
- Store file fingerprints, probe state, grouping evidence, companion associations, and confirmed corrections in PostgreSQL.
- Group books across disc subfolders and split multiple books placed in one folder when evidence supports the split.
- Display uncertain books with a red review indicator.
- Let the user correct file assignments and lock the confirmed grouping.
- Optionally reorganize source files after server authorization, preview, and confirmation.
- Recover expired jobs and renew leases for work that can exceed one lease period.
- Reconcile changed paths without probing unchanged audio.

The project will not fingerprint audio content, reorganize files without explicit configuration, or make filesystem access part of an Android catalog/search request.

## Catalog Stages

### Stage 1: Fast inventory

The scanner walks each configured root and records filesystem facts without invoking `ffprobe`:

- Root ID and source-relative path.
- File name, extension, size, and modified time.
- File kind: audio, recognized cover, grouping companion, or ignored.
- Parent and ancestor folder names.
- A stable file identity derived from root and relative path.

The inventory compares each entry with its stored fingerprint. It inserts new entries, updates changed entries, detects moves when reliable evidence exists, and marks absent entries missing only after a completed root inventory. A failed or partial walk does not mark unseen records missing.

The repository creates provisional books from folder and filename evidence during inventory finalization. It then publishes a complete shared-search projection for the root in the same transaction. The Audiobooks screen can show provisional cards before metadata probes finish.

### Stage 2: Metadata extraction

The worker queues one deduplicated probe job for each new audio file and each audio file whose size, modified time, or probe-version signature changed. Each job reads one file and stores:

- Duration and codec/container facts.
- Embedded title, album, author, album artist, narrator, series, track, disc, date, genre, and identifiers when available.
- Embedded artwork facts needed by the existing cover pipeline.
- Probe version, completion time, status, and a sanitized error code.

A failed probe keeps the file and its provisional audiobook visible. BookBox marks the affected grouping for review and retries according to the worker policy. It does not restart the root inventory.

### Stage 3: Grouping refinement

A metadata or companion change queues a grouping job for the smallest affected grouping scope. The job reads evidence from PostgreSQL. It does not rescan the folder.

The grouping job updates books and assignments, reapplies user corrections, recomputes review states, and records an evidence summary. The worker coalesces changes and republishes one complete root search projection in a transaction. Readers see the prior projection until the new projection commits.

### Stage 4: Change reconciliation

Filesystem monitoring sends root-relative changed paths to a debounced reconciliation queue. The reconciler inventories the affected file or subtree, compares fingerprints, and queues probe or grouping work as required.

Docker Desktop can omit bind-mount events. A six-hour fallback performs a full fast inventory. The fallback uses fingerprints and does not probe unchanged files. Manual rescan uses the same inventory path.

## PostgreSQL State

PostgreSQL will remain authoritative for catalog behavior. The schema must retain:

- Active and missing source-file rows.
- Size and modified-time fingerprints.
- File classification and companion type.
- Probe version, status, timestamps, errors, and extracted metadata.
- Provisional and refined books.
- Book-to-file assignments and file roles.
- Grouping evidence summaries and review reasons.
- User-confirmed grouping corrections and grouping locks.
- Reorganization plans, journals, results, and audit events.
- Catalog generation and published search-snapshot identity.

`library_search_documents` remains disposable derived state. The worker can rebuild it from the active catalog without reading the source root.

The implementation may extend `source_files` or add focused evidence/job tables. It must avoid a second catalog database or request-time cache that can diverge from PostgreSQL.

## Recognition and Grouping Rules

### Evidence sources

The grouping engine will use:

- Current folder and ancestor names.
- Audio filenames, common stems, numbering, and disc/chapter tokens.
- Embedded metadata stored by probe jobs.
- Child folders such as `Disc 1`, `Disc 2`, `CD01`, and `CD02`.
- Recognized covers.
- `.cue`, `.nfo`, `.opf`, and playlist files.
- Ebook files and other companions whose names or contents identify an audiobook.

The engine must normalize case, punctuation, whitespace, track numbers, disc labels, and common audiobook/edition suffixes. It must preserve original values for display and review.

### Dedicated folders

One folder containing audio represents one audiobook by default. Consistent child disc folders join the parent audiobook when filenames or metadata agree. Chapter and track numbering alone does not split a book.

Strong evidence can split several books accidentally placed in one folder. The engine may split supported audio formats, including MP3 and FLAC collections, when metadata identities or distinct filename clusters identify separate books. The existing rule that limits automatic splitting to standalone M4B files must be removed.

### Loose and cross-folder files

Files without a dedicated book folder require stronger evidence. BookBox can group loose chapter files when title/album, author, narrator, numbering, and filename stems agree. It must not merge files across unrelated folders based on a short or generic title alone.

When a parent folder contains several child folders, BookBox may combine them only when disc structure or consistent identity indicates one audiobook. Otherwise each child remains separate or receives `Needs review`.

### Uncertainty

The engine assigns one of these outcomes:

- Confident provisional grouping.
- Confirmed user grouping.
- `Needs review` with a provisional assignment.

Every recognized audio file must belong to a visible provisional or confirmed audiobook unless the file is unsupported or missing. Uncertainty must not hide audio from the Audiobooks screen.

The stored review reason will identify the conflict category, such as multiple embedded titles, conflicting authors, weak loose-file evidence, failed metadata probe, or an incomplete reorganization.

### Companion association

BookBox associates a companion when its folder, filename stem, referenced paths, or parsed metadata link it to one audiobook. A shared folder cover can belong to several provisional groups without forcing those groups to merge.

Confirmed physical reorganization moves associated companion files with their audiobook. The preview will show every companion move. A companion that cannot be assigned safely keeps the group in `Needs review` until the user assigns it or leaves it in place explicitly.

## Worker Jobs and Leases

The worker will use bounded jobs for root inventory, file probing, grouping refinement, search projection publication, and physical reorganization.

The repository must:

- Reap or requeue expired jobs before startup scheduling performs deduplication.
- Renew the active lease while a job reports progress.
- Reject renewal and completion from stale lease tokens.
- Store stage-specific errors without source-absolute paths.
- Resume by reading committed PostgreSQL state instead of repeating completed work.
- Coalesce projection publication after a burst of probe/group changes.

A root inventory should finish within one normal lease because it does not probe media. Lease renewal still protects large or slow roots. One failed file probe cannot fail the root inventory.

## Android Presentation and Review

### Audiobook cards

The Audiobooks screen renders all provisional and confirmed books returned by PostgreSQL. A book with `Needs review` displays a red `!` icon with an accessibility description such as `Review audiobook grouping`.

The icon opens the grouping review screen. Duplicate-recording groups from shared search retain their separate `Duplicate book` presentation. A book can be both a duplicate member and in `Needs review`; the UI shows both states without merging their meanings.

### Grouping review

The review screen displays:

- Suggested audiobook groups.
- Assigned and unassigned audio files.
- Associated companion files.
- Source-relative paths.
- Embedded and filename/folder evidence.
- The stored reason for review.

The user can select the correct files for each audiobook, create another group, merge groups, split a group, and assign companions. A valid group requires at least one active audio file. The screen rejects duplicate audio assignment unless the domain explicitly marks a companion as shared.

`Confirm grouping` saves assignments and grouping locks in PostgreSQL. Future inventories and metadata refinements reapply the correction while its files remain available.

### Optional physical reorganization

`Confirm and reorganize files` appears only when all conditions hold:

- Server configuration sets `allow_library_reorganization: true`.
- The authenticated account has the reorganization capability.
- The worker can write the configured root.
- The client has loaded a current preview.

The preview lists source-relative old and new paths for audio and companion files. It reports collisions, missing files, unsupported cross-root moves, and names that require correction before confirmation.

The server validates root confinement, relative paths, symlink boundaries, current fingerprints, destination uniqueness, and source availability. It rejects cross-root moves and never overwrites an existing destination.

The worker records a durable move journal before changing the filesystem. It moves files, verifies their resulting fingerprints, commits the confirmed grouping, and triggers targeted reconciliation. If a move fails, the worker rolls back completed moves in reverse order. A rollback failure records the unresolved relative paths, leaves the affected book in `Needs review`, and blocks another reorganization until reconciliation completes.

`Confirm grouping` remains available when configuration, account rights, or filesystem permissions do not allow source writes.

## Shared Search Publication

Inventory finalization and grouping refinement will publish search through one repository boundary. Publication will:

1. Lock the catalog generation for the root.
2. Validate active books, files, and assignments.
3. Build audiobook, raw file, and ancestor-folder documents.
4. Recompute duplicate-recording groups from refined metadata where possible.
5. Replace the root projection and snapshot in one transaction.
6. Record the catalog generation that produced the snapshot.

If the catalog changes during projection construction, the repository rejects the stale publication and queues another projection job. Search continues serving the last committed snapshot.

The shared-search implementation plan must revise these overlapping tasks:

- Task 3 must support publication from a catalog generation after inventory or grouping refinement, rather than only from `FinalizeSuccessfulScan` after full probing.
- Task 4 must make scan finalization commit fast inventory and provisional grouping. Probe and grouping jobs publish later generations.
- Task 6 must schedule path-aware reconciliation when event details are available. The six-hour root inventory remains the authoritative fallback.
- Tasks 7 through 9 must preserve review state and the red grouping-review action inside singleton and duplicate-group cards.

## Failure and User-Visible States

BookBox will distinguish:

- `Building catalog`: no inventory snapshot exists yet.
- Available provisional catalog: inventory exists while metadata work continues.
- Stale catalog: the last projection remains available after a failed refresh.
- Empty catalog: a completed inventory found no supported audio.
- Catalog failure: no usable snapshot exists and inventory failed.

Android keeps the last available cards during refresh or projection failures. It shows progress without clearing results.

A failed metadata probe produces a provisional card with `Needs review`. A failed grouping job preserves the prior assignments. A failed projection publication preserves the prior search snapshot. A partial filesystem inventory does not mark unseen files or books missing.

## Security and Audit

The API will continue to hide absolute Windows paths. Logs, job errors, reviews, and move journals exposed to Android use root IDs and source-relative paths.

Source writes require an explicit configuration option and account capability. Docker mount writability alone does not authorize reorganization. The API has no general upload, delete, rename, move, or tag-edit endpoint. It exposes only validated grouping-preview and confirmed reorganization operations.

Audit records will include actor, book/group IDs, root ID, relative source and destination paths, companion roles, prior and resulting fingerprints, outcome, and timestamps. They will not include credentials or absolute paths.

## Testing

### Inventory and persistence

- A 22,824-audio-file fixture or generated equivalent creates visible provisional books without media probes.
- Inventory commits before any probe job runs.
- Unchanged fingerprints queue no probe work.
- Changed, moved, missing, restored, and partially scanned files update correctly.
- Restarted workers resume committed progress.
- Expired jobs recover before startup deduplication; active jobs renew leases.

### Grouping

- One folder with chapter files becomes one book.
- Disc subfolders with consistent identity become one book.
- Several M4B, MP3, or FLAC books in one folder split when evidence is strong.
- Loose files require stronger evidence and become visible as `Needs review` when evidence is weak.
- Folder, filename, embedded metadata, cover, cue, NFO, OPF, playlist, and ebook evidence contribute as defined.
- User corrections survive inventory and metadata changes.
- Every active recognized audio file appears in one visible provisional or confirmed grouping.

### Search integration

- Fast inventory publishes the first complete root projection.
- Group refinement publishes a later catalog generation.
- Failed and stale publication preserves the prior snapshot.
- Search requests succeed after filesystem access is removed.
- Duplicate-recording rules remain independent from review state.
- Shared-search performance and indexed-plan thresholds continue to pass.

### Review and reorganization

- Red `!` semantics and accessibility actions open review.
- File selection, split, merge, companion assignment, and confirmation update PostgreSQL.
- Disabled configuration and missing rights expose catalog-only confirmation.
- Preview rejects stale fingerprints, collisions, traversal, symlinks, cross-root moves, and overwrites.
- Successful moves include associated companions and produce audit records.
- Partial failure rolls back; rollback failure records unresolved relative paths and requires review.

### End-to-end acceptance

Against the configured large library, BookBox will:

1. Complete a fast inventory and store active source files and provisional books.
2. Return visible cards from `/v1/books` before all metadata probes finish.
3. Continue probing and refining without clearing the catalog.
4. Search audiobook and raw entities through PostgreSQL only.
5. Show ambiguous groups with the red review action.
6. Save a corrected grouping and preserve it after reconciliation.
7. Reorganize a disposable test group only when the server capability is enabled.

## Rollout Order

Implementation will use one integrated plan rather than executing overlapping worker tasks independently:

1. Add shared-search projection schema and catalog inventory/probe state migrations in a conflict-free sequence.
2. Add job recovery and lease renewal.
3. Implement fast inventory, provisional grouping, and first projection publication.
4. Implement incremental file probes, grouping refinement, and catalog-generation publication.
5. Serve and render shared search while preserving review state.
6. Add grouping review and catalog-only confirmation.
7. Add explicitly authorized reorganization, journaling, rollback, and audit.
8. Add path-aware monitoring and fingerprint-based fallback inventory.
9. Run server, PostgreSQL, Android, E2E, performance, and live-library verification.

The integrated implementation plan will reference shared-search tasks that remain unchanged and replace the overlapping scan, projection, monitoring, and Android-card steps.

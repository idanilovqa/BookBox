# Audiobook catalog

The catalog worker inventories configured roots at startup, after monitored
filesystem changes, and every six hours as a fallback. Inventory records file
size and modification time, publishes provisional books, and queues later work.
The API can show those books before `ffprobe` finishes. Probe jobs read audio
metadata, companion jobs inspect bounded CUE, NFO, OPF, and playlist content,
and regroup jobs refine book boundaries. A publication job replaces the indexed
search projection after the catalog generation advances. PostgreSQL stores each
stage, review decision, and cover audit event.

Grouping refinement and publication-job enqueue are separate commits. A worker
crash between them can leave the prior search snapshot visible until the scope is
regrouped again or an operator requests a rescan; the publication job itself is
deduplicated once enqueued.

Worker jobs use these staged kinds: `scan_root`, `probe_file`,
`inspect_companion`, `regroup_scope`, `publish_search`, `enrich_book`,
`publish_cover`, and `reorganize_files`. Jobs move through `queued`, `running`,
`succeeded`, or `dead`. The worker renews leases for long jobs and recovers an
expired lease before it deduplicates startup scans. An unchanged fingerprint
does not queue another probe or companion inspection.

## Container and Windows hybrid workers

Linux and NAS deployments start the worker through the `container-worker`
Compose profile. Windows hybrid deployments use `deploy\compose.hybrid.yml`,
leave that profile disabled, and run the native `BookBoxCatalogWorker` service.
The hybrid overlay publishes PostgreSQL at `127.0.0.1:55432`; the API keeps its
private-LAN address, and the Windows worker opens no inbound port.

The Windows worker accepts roots on local fixed and removable drives. It rejects
mapped drives and UNC paths. Configure an external ffprobe executable in
`%ProgramData%\BookBox\catalog-worker.yml`; BookBox does not bundle ffprobe.
Worker logs and cover backups belong under `%ProgramData%\BookBox`.

The worker validates a root before it starts missing-file reconciliation. If a
drive is missing, the worker records a failed attempt and retains the prior
catalog and search projection. Filesystem events speed up indexing, while the
six-hour full-root scan remains the authoritative fallback.

Fingerprint work is activated through a durable PostgreSQL outbox only when a
full or scoped inventory finalization commits successfully. Partial inventories
cannot make probe jobs claimable. A bounded dispatcher delivers outbox entries
to worker jobs; the entry remains until the matching probe or companion result
commits, so worker crashes and exhausted delivery attempts can be retried. The
upgrade migration recovers pending fingerprints from the latest successfully
published inventory without exposing source paths in routine status output.

## Indexed library search

After a successful scan, the worker atomically publishes one PostgreSQL search
snapshot for that root. Audiobook search can combine multiple configured roots;
each response names every root snapshot used. Raw search intentionally accepts
one selected root and returns recognized audio files, recognized cover images,
and their nonempty ancestor folders. Neither request path walks or reads a
configured source root. If publication fails, PostgreSQL retains the previous
completed snapshot.

Search normalizes Unicode text and ignores case, whitespace, punctuation, and
other symbols. Text queries require at least two Unicode letters or digits.
Audiobook results preserve the catalog's author, genre, review-state, year, and
sort filters. Cursors are opaque and bound to the exact query and snapshots.

Two recordings form a duplicate group only when normalized title, sorted author
identity, and nonempty sorted narrator identity match and their durations differ
by no more than the smaller of five minutes and the larger of two minutes or
0.5 percent of the longer recording. Duplicate groups never cross roots. A raw
file or ancestor associated with such a recording is marked as duplicate;
another narrator remains a separate audiobook.

The Android app can browse, search, download, request a scan, and confirm a
metadata match. The worker publishes recognized cover files (`cover`, `folder`,
or `front` with a supported image extension). It does not edit audio tags.
Source reorganization stays disabled unless an operator enables the guarded
workflow described below.

## Scan status and manual scans

The worker coalesces create, write, rename, and remove events for five seconds
per root before scheduling a scan. The six-hour scan remains a fallback for
missed or unsupported watcher events. The worker logs scan failures without exposing host paths. A failed or partial
filesystem walk does not replace the previous inferred catalog. Jobs use
15-minute leases, bounded retries, and stale-lease recovery.

An authenticated client can request a scan:

```http
POST /v1/catalog/rescan
Content-Type: application/json

{"rootId":"audiobooks"}
```

The API returns `202 Accepted` with the deduplicated job ID. Startup and periodic
jobs use the same deduplication key, so concurrent requests do not run duplicate
scans for one root.

Before the first successful scan, search returns HTTP 503 with
`search_index_building`. Clients should keep retained results when a later
refresh is pending or fails; operators can use the manual rescan endpoint above
without clearing the last completed projection.

## Review states and provider attribution

- `unreviewed` means only local/inferred information is available.
- `needs_review` means candidates exist but no single candidate safely won, or
  the local grouping is ambiguous.
- `confirmed` means a person or the high-confidence matcher selected a candidate.

Book responses expose `metadataSource`; candidate responses expose their
provider. Provider-derived titles, authors, dates, genres, subjects, and covers
retain Open Library or Google Books attribution in PostgreSQL.

Grouping evidence records a reason such as `folder_default`,
`consistent_subtree`, `distinct_identity`, `conflicting_title`,
`conflicting_author`, `weak_loose_files`, `probe_failed`, `manual`, or
`reorganization_incomplete`. Android shows a red `!` action on a book that needs
grouping review. The grouping screen shows the subtree audio and associated
companions. A saved correction assigns the complete scope to explicit groups,
locks the result, advances the catalog generation once, and queues one search
publication. Later inventories reuse the correction while fingerprints still
match.

Search projections may expose `legacy_review_state` only as a compatibility
fallback for a pre-evidence `needs_review` book. It is not a grouping-evidence
reason code; a later grouping refinement replaces it with persisted evidence.

## Optional source reorganization

Keep `allow_library_reorganization: false` for normal and read-only deployments.
Set it to `true` to opt in. The current server grants the
`library:reorganize` capability to the configured owner username. Give the API
write-probe access and the worker confined move access to the selected source
root. The grouping response sets `canReorganize` only after all checks pass.

Android first submits the complete grouping, current catalog generation, and a
relative target folder for each group. The server rejects stale generations,
absolute or traversing paths, symlink ancestors, collisions, and destinations
outside the configured root. Preview returns relative source and destination
paths, expected sizes, modification times, and roles. Confirmation queues the
durable operation by ID.

The worker validates each fingerprint before moving it and records each move in
`reorganization_operations` and `reorganization_moves`. Success commits the new
paths and grouping in one catalog generation. A mid-move failure rolls completed
moves back in reverse order. Failed rollback changes the operation to
`needs_attention`; stop the worker and follow the recovery procedure in
`docs/operations.md` before confirming another move in that root.

## Optional Google Books key

Open Library works without a key. To add Google Books, place the API key in a
regular file readable only by the worker user, mount it read-only into the worker
container, and set:

```yaml
environment:
  BOOKBOX_GOOGLE_BOOKS_API_KEY_FILE: /run/secrets/google_books_api_key
```

Do not put the key itself in Compose, the application configuration, logs, or a
URL. If the variable is absent, the worker simply uses Open Library.

## Cover publication rules

Cover downloads must use credential-free HTTPS URLs. Redirects and response size
are bounded, and the bytes are decoded as a supported image before publication.
The worker selects an existing recognized cover name or creates `cover.jpg`. It
refuses path components, symlinks, non-cover destinations, and candidates that
are not a material quality improvement.

Existing cover bytes are stored content-addressed under
`/var/lib/bookbox/backups` before replacement. Creating or replacing a cover
records its hashes, dimensions, provider, destination, action, and backup key in
the cover event history. An already-good cover is a successful no-op.

## Restoring a backed-up cover

1. Stop `bookbox-catalog-worker` so it cannot publish concurrently.
2. Find the replacement event and its `backupKey` in PostgreSQL cover history.
3. Verify the backup file name equals the SHA-256 hash of its bytes.
4. Copy that file back to the event's exact recognized cover destination using
   an atomic replace on the Windows host.
5. Start the worker and request a rescan.

Never restore to a path supplied by an Android client. The audit destination and
the configured library root are the authority.

## Container boundary

The worker publishes no port, runs as UID/GID `10001`, drops every capability,
uses `no-new-privileges`, and has a read-only container root plus a restricted
`/tmp` tmpfs. The default API library bind remains read-only. A deployment that
enables reorganization must opt that bind into write probing for the selected
root. Cover cache and backup volumes are separate from the source library.

The worker's library bind is read-write because Docker and NTFS cannot grant
write access by filename pattern. This gives the worker container mount-level
ability to modify any library file if it is compromised. The application code
does not expose a generic write operation and limits publication to validated
`cover`, `folder`, or `front` image files, but Docker itself cannot enforce that
filename-only rule. Accept this risk before enabling the worker service. Keep
the API bind read-only unless reorganization requires its guarded write probe,
and do not expose a worker port.

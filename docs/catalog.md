# Audiobook catalog

The catalog worker indexes configured roots when it starts, after monitored
filesystem changes, and every six hours as a fallback. It uses `ffprobe` to read audio metadata, groups audio files into books, queries
Open Library (and optionally Google Books), and publishes a confirmed cover only
when it is a material improvement. PostgreSQL stores the catalog, jobs, review
decisions, and cover audit events.

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

The Android app and API remain source-read-only. They can browse, search,
download, request a scan, and confirm a metadata match. Only the isolated worker
publishes recognized cover files (`cover`, `folder`, or `front` with a supported
image extension). It never edits tags, audio files, names, or directory layout.

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
`/tmp` tmpfs. The API library bind remains read-only. Cover cache and backup
volumes are separate from the source library.

The worker's library bind is read-write because Docker and NTFS cannot grant
write access by filename pattern. This gives the worker container mount-level
ability to modify any library file if it is compromised. The application code
does not expose a generic write operation and limits publication to validated
`cover`, `folder`, or `front` image files, but Docker itself cannot enforce that
filename-only rule. This risk must be accepted before enabling the worker
service; keep the API bind read-only and never expose a worker port.

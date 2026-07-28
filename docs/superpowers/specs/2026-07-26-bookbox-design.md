# BookBox Design

**Status:** Approved
**Date:** 2026-07-26  
**Repository:** `C:\Repositories\BookBox`
**Primary device:** Samsung Galaxy S23 Ultra running Android 16 (API 36)

## Purpose

BookBox lets one Android device browse and copy audiobooks from `H:\Audiobooks` over a home network. The app presents the same library in two ways:

- A searchable audiobook catalog with cards, covers, and book metadata.
- A raw folder browser that mirrors the configured Windows paths.

The Android-facing service can read source files and derived catalog data. It cannot upload, rename, move, delete, edit tags, or change audio content. A separate catalog worker may add or replace cover-image files under controlled rules.

## Version-One Outcomes

Version one delivers:

- A Docker Compose deployment for Windows and Docker Desktop.
- An HTTPS API, an isolated catalog worker, and PostgreSQL.
- A native Android 16 app built with Kotlin and Jetpack Compose.
- A sideloadable APK.
- Persistent server setup and automatic sign-in on Android.
- Raw folder browsing and recursive folder copying.
- Audiobook cards, local catalog search, filters, sorting, and metadata review.
- Resumable background transfers into a user-selected Android folder.
- Local and online metadata extraction.
- Controlled publication of improved covers into Windows book folders.
- Tests and setup documentation.

## Non-Goals

Version one excludes:

- Audio playback or streaming.
- Internet exposure, router port forwarding, or a hosted relay.
- Automatic LAN server discovery.
- Multiple user accounts or per-folder permissions.
- Android uploads or source-library file management.
- Android tag editing.
- Google Play publishing.
- Changes to audio files, embedded audio tags, audio filenames, or source folder structure.

The architecture leaves room for Tailscale or a trusted HTTPS reverse proxy in a later release.

## Repository Structure

The repository will contain these top-level areas:

```text
BookBox/
  android/                 Native Android application
  server/                  Shared Go modules and two server commands
    cmd/api/               Android-facing HTTPS API
    cmd/catalog-worker/    Scanner, enrichment, and cover publisher
    internal/              Focused domain packages
    migrations/            PostgreSQL schema migrations
  deploy/                  Dockerfiles, Compose, and sample configuration
  docs/                    Setup, operation, and architecture documentation
  testdata/                Small synthetic audiobook library
```

The API and catalog worker share domain packages but build as separate executables and container images. The separation keeps Android-facing code away from the worker's writable library mount.

## Deployment Architecture

Docker Compose runs three services:

1. `bookbox-api`
   - Publishes HTTPS port `8443` to the home LAN.
   - Mounts `H:\Audiobooks` at `/library/audiobooks` with read-only access.
   - Reads catalog data from PostgreSQL.
   - Streams source files to authenticated Android clients.
   - Accepts catalog corrections and rescan requests as derived-data operations.

2. `bookbox-catalog-worker`
   - Exposes no inbound port.
   - Scans the audiobook library at startup, every six hours by default, and after a rescan request.
   - Extracts metadata, groups audio files into books, enriches records, and maintains the search index.
   - Mounts `H:\Audiobooks` with write access because it publishes cover images.
   - Limits source-library writes in application code to validated cover-image creation or replacement.

3. `postgres`
   - Stores catalog records, file assignments, scan state, metadata matches, user corrections, search data, cover history, and queued worker jobs.
   - Stores no audio content and no Android transfer state.

Docker-managed volumes hold PostgreSQL data, TLS material, cached cover candidates, and original-cover backups. The Compose file uses health checks and starts dependent services only after PostgreSQL becomes ready.

### Filesystem Safety Boundary

Docker supports a read-only mount but cannot grant write access based on a filename such as `cover.jpg`. The worker's read-write mount gives that container broader technical access than its intended behavior. The design constrains risk through process isolation, no inbound worker port, a non-root user, dropped Linux capabilities, destination validation, focused worker code, cover-write audits, and tests.

The Android API has a separate read-only mount. Its API contains no general source-file mutation operation. A compromised or defective Android client cannot ask the API to edit tags, upload files, rename paths, move files, or delete source content.

## Configured Library Roots

Version one configures this root:

```yaml
roots:
  - id: audiobooks
    name: Audiobooks
    container_path: /library/audiobooks
```

Docker Compose maps `H:\Audiobooks` to that container path. A future configuration may add named roots without changing the Android protocol.

Clients send a root ID and relative path. Server code resolves the final path, checks that it remains under the configured root, and rejects traversal, absolute paths, malformed encodings, and symlinks that escape the root.

## Authentication and Transport Security

The server generates a self-signed TLS certificate on first startup and stores it in a Docker volume. Startup output prints its SHA-256 fingerprint.

First-time Android setup follows this sequence:

1. The user enters `https://<windows-lan-address>:8443`.
2. The app connects without sending credentials and displays the observed certificate fingerprint.
3. The user compares it with the fingerprint from the server and approves it.
4. The app pins the certificate, then sends the username and password over that approved TLS connection.

The server stores an Argon2id password hash in an uncommitted secret file. It does not store the plaintext password. Login rate limiting applies per source address and account.

After login, the server issues:

- A signed access token with a 15-minute lifetime.
- A signed refresh token with a 30-day lifetime.

Android encrypts the refresh token with a key protected by Android Keystore. It discards the password after login. The app saves the server address and approved certificate pin, opens the catalog on later launches, and refreshes access tokens without prompting. Sign out deletes local tokens. A server password-change command rotates the session-signing key and invalidates existing sessions.

Android backup excludes tokens, key material, and certificate pins. A **Change server** action clears the saved connection and requires certificate approval again.

The setup guide instructs the user to reserve the Windows PC's LAN address in the router. Automatic discovery remains outside version one.

## Read-Only Android API

The versioned API provides these operations:

- `GET /v1/health`
- `POST /v1/session`
- `POST /v1/session/refresh`
- `GET /v1/roots`
- `GET /v1/browse`
- `GET /v1/books`
- `GET /v1/books/{bookId}`
- `GET /v1/books/{bookId}/files`
- `GET /v1/catalog/filters`
- `POST /v1/catalog/matches/search`
- `PUT /v1/books/{bookId}/match`
- `PUT /v1/books/{bookId}/grouping`
- `POST /v1/catalog/rescan`
- `POST /v1/transfers/manifest`
- `GET /v1/files/{fileId}`

The `POST` and `PUT` operations authenticate sessions, build transfer manifests, or change derived catalog records. They do not mutate audio files or source folder structure. The API has no upload, tag-edit, source rename, source move, or source delete route.

File responses support HTTP byte ranges and stable entity tags. The API stops a transfer with a source-changed error when file size, modification time, or identity changes after manifest creation.

## Catalog Scan and Book Grouping

The worker scans supported audio extensions, including `.m4b`, `.m4a`, `.mp3`, `.aac`, `.flac`, `.ogg`, `.opus`, and `.wav`. It ignores temporary files and paths excluded by configuration.

For each file, the worker records:

- Root and relative path.
- Size and modification time.
- Container and codec information.
- Duration.
- Embedded title, album, author, album artist, narrator, series, track, disc, date, genre, and identifiers when present.
- Embedded artwork dimensions and digest.
- Nearby recognized image files.

The grouping engine applies conservative rules:

1. It treats audio files in one folder as one audiobook by default.
2. It keeps chapter and part files together when tags share a normalized title, author, series, or album identity.
3. It splits multiple `.m4b` files only when embedded identities differ or both tags and filenames provide strong distinct-book evidence.
4. Chapter numbers, disc numbers, and `part` tokens do not trigger a split by themselves.
5. The worker marks conflicting or weak evidence as **Needs review**.

Android's grouping review lets the user assign source files to inferred books, split a book, or merge inferred books. PostgreSQL stores that correction and reapplies it after rescans. These actions change catalog assignments only; they do not move or rename Windows files.

## Metadata and Online Enrichment

The worker chooses metadata in this order:

1. A user-confirmed catalog match or correction.
2. Embedded audio tags.
3. Folder and filename evidence.
4. Open Library search results.
5. Google Books results when the user supplies an API key.

The worker searches online providers only for books found under a configured root. Android catalog search never returns books absent from the local library.

The catalog stores:

- Title and subtitle.
- Authors.
- Series name and position.
- Narrator when available.
- Publisher and publication date or year.
- Genres and provider subjects.
- ISBN and provider identifiers when available.
- Metadata source, confidence score, and review state.

The worker normalizes genre labels for filtering while retaining provider subjects. It records the matched edition's publication year and source because providers may return different editions.

High-confidence matches apply without user input. Ambiguous results receive **Needs review** status. Android can search alternatives, preview metadata and covers, confirm a result, and retain that choice across scans.

## Cover Selection and Publication

The worker checks artwork in this order:

1. A user-confirmed cover.
2. A recognized local image such as `cover`, `folder`, or `front`.
3. Embedded audio artwork.
4. Open Library cover candidates.
5. Google Books cover candidates when configured.

The worker decodes each candidate instead of trusting its extension or response content type. It accepts JPEG, PNG, or WebP images no larger than 20 MB and rejects invalid dimensions or decompression bombs.

The worker considers a local cover poor when its longest edge is below 800 pixels. It replaces an existing local image only when the candidate has at least twice the pixel area, neither candidate dimension is smaller, and the candidate requires no upscaling. If embedded art is poor and no local cover exists, the worker creates `cover.jpg`.

Before replacement, the worker copies the original image into its private backup volume under a content-addressed name. It writes the new image to a temporary file in the destination folder, flushes it, and replaces the cover path. PostgreSQL records the book, source provider, old and new digests, dimensions, destination, and timestamp.

The worker may create or replace recognized cover-image files. It does not change embedded tags, audio bytes, audio filenames, or folders. Android can confirm a metadata or cover match, after which the worker performs the cover operation through its queued job. Android receives no general filesystem write capability.

## Android Application

The native app uses Kotlin, Jetpack Compose, and Android API 36 as its minimum, compile, and target API. Version one supports the Galaxy S23 Ultra and does not implement compatibility paths for older Android releases.

### Navigation

The app provides:

- First-time connection and certificate approval.
- **Audiobooks** mode.
- **Folders** mode.
- Transfer setup and transfer history.
- Settings, sign out, change server, and destination selection.

The app opens in Audiobooks mode after setup.

### Audiobooks Mode

Audiobooks mode displays an adaptive card grid. Each card shows:

- Best available cover.
- Title and author.
- Publication year and normalized genre.
- Series and narrator when available.
- Metadata confidence or review status.
- Selection control for copying.
- **View raw files** action.

A persistent search field searches the local PostgreSQL-backed catalog by title, author, series, narrator, genre, publication year, source folder, and source filename. Results use pagination.

Filters include author, genre, year, configured root, and review status. Sort choices include title, author, year, recently added, and recently scanned.

The book detail screen shows metadata, transfer selection, source-file assignments, provider attribution, match alternatives, and grouping review. **View raw files** opens Folders mode at the source folder and highlights the files assigned to that book. Other files in a shared source folder remain visible.

### Folders Mode

Folders mode mirrors configured root paths without exposing absolute Windows paths. It provides breadcrumbs, folders-first sorting, file sizes, modification dates, and multi-folder selection.

The mode remains read-only. It contains no rename, move, delete, edit, upload, or new-folder actions for Windows content.

## Android Destination Storage

The user chooses a destination once with Android's `ACTION_OPEN_DOCUMENT_TREE` system picker. The app takes persistable read and write URI permission for that tree. Android does not need broad storage permission.

The app stores the destination URI outside SQLite and verifies access before each transfer. If the user revokes permission or removes the destination, the app asks for a new folder.

Files remain on Android after app uninstall because they live in the user-selected document tree. App-private credentials and transfer journals do not.

## Copy Semantics

Raw folder copying preserves the selected folder name and its recursive contents.

Audiobook copying follows these rules:

- A normal one-book source folder copies under its original folder name.
- A book inferred from a shared multi-book folder copies into a subfolder named from its original `.m4b` filename stem.
- If no usable `.m4b` stem exists, the app uses the detected title after replacing Android-invalid path characters.
- Original audio filenames remain unchanged.
- A split book includes its assigned audio files, matching same-stem sidecars, and the recognized shared cover.
- The app does not edit tags or inject a cover after copying.
- A cover that the worker already published into the Windows folder transfers as an ordinary source file.

The server manifest lists relative destination paths, sizes, modification times, stable source identifiers, and SHA-256 digests. The API computes and caches missing digests for selected content.

## Background Transfer and Resumption

The user starts a transfer from the app. Android schedules an API 36 user-initiated data-transfer job and shows a progress notification.

The app keeps a JSON transfer journal in app-internal storage. It uses no Room or SQLite database. The journal stores source identifiers, destination document IDs, byte counts, expected digests, and job state.

Each file downloads to a temporary document in the selected destination. The app resumes with an HTTP byte-range request when the destination provider supports seeking. If the provider cannot seek, the app restarts that one partial file while retaining progress for completed files.

After receiving all bytes, the app verifies size and SHA-256. It renames the temporary document to the final filename when the provider supports rename. Otherwise it copies the verified temporary content to the final document and removes the temporary document.

Transfers survive app backgrounding, screen-off, network interruption, and device restart. The app recreates eligible jobs from its journal after reboot.

### Existing Destination Files

- A file with the expected size and digest is skipped.
- A matching partial transfer resumes.
- A different file at the same path becomes a conflict.
- The app never overwrites a conflict without a user choice.
- Conflict choices are **Replace**, **Keep both**, and **Skip**.
- Cancellation keeps partial data until the user resumes or discards it.

All mutations in this section apply to the user-selected Android destination, not the Windows source library.

## Error Handling

The app gives a recovery action for:

- Server unavailable: retry or change server.
- Certificate mismatch: block credentials and require explicit certificate review.
- Expired session: refresh automatically; request login if refresh fails.
- Authentication throttling: show the server-provided retry time.
- Source changed during transfer: discard the affected partial result and request a new manifest.
- Source missing: mark the file unavailable and leave completed files intact.
- Android destination permission revoked: open the system folder picker.
- Insufficient destination space: stop before starting when Android reports capacity, or stop safely on the first write failure.
- Digest mismatch: keep no final file, report corruption, and allow retry.
- Online metadata provider failure: retain local metadata and retry during a later scan.
- Ambiguous grouping or metadata: mark the book for review.
- Cover write failure: retain the prior cover, record the failure, and continue scanning other books.

Server logs exclude passwords, tokens, and full private source paths. Android logs exclude credentials and destination document URIs.

## Database Constraint

PostgreSQL is the only application database. The project will not use SQLite, Room, or another embedded SQL database on the server or Android.

Android stores small preferences with DataStore, secrets with Android Keystore-backed encryption, and transfer state in scoped JSON files.

## Configuration and Secrets

The repository commits examples but excludes live values:

- Root definitions.
- Scan interval.
- Cover quality thresholds.
- PostgreSQL database and user names.
- Password-hash generation instructions.
- TLS certificate location.
- Optional Google Books API key.

Uncommitted secret files hold the PostgreSQL password, Argon2id login hash, session-signing material, and optional provider key. The documentation includes key rotation and backup steps.

## Testing Strategy

### Server Unit Tests

Focused tests cover:

- Path normalization, traversal rejection, and symlink escape rejection.
- Authentication, rate limits, token expiry, refresh, and session invalidation.
- Folder listing, stable sorting, manifests, range responses, entity tags, and digests.
- Conservative grouping and corrected grouping persistence.
- Metadata precedence, confidence, genre normalization, and provider fallback.
- Cover validation, quality comparison, destination checks, backup, and atomic replacement.

### Server Integration Tests

Tests start PostgreSQL and the two server processes against a synthetic library. They verify migrations, scans, search, pagination, metadata review, rescan jobs, transfer manifests, and cover history.

A mutation test snapshots the synthetic library, exercises every Android-facing endpoint, and proves that the API changed no source file. Separate worker tests prove that cover publication changes only an expected cover path and backup/audit records.

### Android Tests

Unit and instrumented tests cover:

- Certificate pinning and mismatch handling.
- Saved server setup and Keystore-backed sign-in.
- Card search, filters, sorting, pagination, and raw-file navigation.
- Folder selection and persisted Storage Access Framework permission.
- Transfer journals, byte-range resumption, process recreation, and reboot recovery.
- Digest rejection, source-change errors, low-space failures, and all conflict choices.
- Proof that no Android UI or network client exposes source edit, move, rename, upload, tag-edit, or delete operations.

### End-to-End Test

An API 36 emulator connects to a Compose deployment backed by the synthetic library. The test:

1. Approves the test certificate and signs in.
2. Finds a book through catalog search.
3. Opens its raw files.
4. Copies a nested normal book and a split `.m4b` book.
5. Interrupts and resumes one file.
6. Compares destination paths and bytes with the transfer manifest.
7. Confirms that the source snapshot changed only when the worker publishes the expected test cover.

The final manual checklist installs the APK on the Galaxy S23 Ultra and tests the real LAN connection without running destructive checks against `H:\Audiobooks`.

## Operations Documentation

The project documents:

- Docker Desktop access to `H:`.
- Building and starting Compose.
- Creating PostgreSQL and app secrets.
- Finding and reserving the Windows PC's LAN address.
- Opening port `8443` in Windows Firewall for the private network profile.
- Comparing the TLS fingerprint.
- Choosing the Android destination.
- Building, signing, and sideloading the APK.
- Running a scan and reviewing metadata.
- Reading cover-write history and restoring a backed-up cover.
- Backing up and restoring PostgreSQL, TLS material, corrections, cached covers, and cover backups.
- Rotating the password and invalidating Android sessions.
- Adding another named root later.

## Acceptance Criteria

Version one is complete when:

1. Docker Compose starts all healthy services on the Windows PC.
2. The API reads `H:\Audiobooks` through a read-only mount.
3. The Galaxy remembers its approved server and signs in without storing the password.
4. The app browses the same source through Audiobooks and Folders modes.
5. Search finds local books by title, author, genre, year, series, narrator, folder, or filename.
6. Each book card opens its assigned raw files.
7. The worker scans on startup, on schedule, and after a rescan request.
8. Confirmed metadata and grouping corrections survive rescans in PostgreSQL.
9. The worker can publish and audit a better cover without modifying audio content, tags, names, or folders.
10. The app copies selected folders or inferred books into the chosen Android tree with the approved naming rules.
11. A transfer resumes after interruption and verifies each completed file.
12. Destination conflicts require an explicit Android-side choice.
13. Android-facing API tests prove that client operations do not change Windows source files.
14. The project uses no SQLite or Room database.
15. The repository produces a sideloadable API 36 APK and contains setup, backup, and recovery instructions.

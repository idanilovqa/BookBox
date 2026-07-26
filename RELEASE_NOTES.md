# netStorage v1.0.0

Release status: implementation candidate. The API source keeps a `dev` default for local Go runs, while the release Docker build stamps `1.0.0` through linker flags. Automated gates and the Galaxy S23 Ultra checklist determine release readiness.

## Included

- Authenticated HTTPS access to configured Windows audiobook roots
- Pinned server certificate and remembered sign-in on Android
- Audiobook cards with title, author, year, genre, cover, and search
- Audiobook and raw-folder navigation modes
- Raw-file access from each book card
- Copy to a user-selected Android Storage Access Framework folder
- Range-based resume, SHA-256 verification, and Replace, Keep both, or Skip conflicts
- Android user-initiated background transfers with notifications
- PostgreSQL catalog, metadata review, grouping corrections, worker jobs, and cover audit history
- Open Library metadata plus optional Google Books metadata
- Isolated worker cover publication with content-addressed original-cover backups
- Docker Compose hardening, health checks, source-mutation checks, recovery tooling, and API 36 tests

## Source-write boundary

The Android app and API cannot write to the Windows audiobook library. Android copies become independent files on the phone. The catalog worker holds the sole writable library mount and limits its output to recognized cover basenames after validation and backup.

## Sideload packages

`build-release.ps1` creates `artifacts\netStorage-v1.0.0-debug.apk`. You can sideload this debug-signed APK.

If `local-secrets\netstorage-release.jks` and all three signing environment variables exist, the script creates `artifacts\netStorage-v1.0.0-release.apk`:

- `NETSTORAGE_KEY_ALIAS`
- `NETSTORAGE_KEYSTORE_PASSWORD`
- `NETSTORAGE_KEY_PASSWORD`

`verify-release.ps1` checks the application ID, API 36 minimum and target, approved permissions, signature, and release debuggable flag. It writes `SHA256SUMS.txt` and `APK_METADATA.json`.

## Manual gate

No one has recorded the Galaxy S23 Ultra checklist yet. Treat screen-off continuation, reboot behavior, Wi-Fi interruption, private-LAN access, fingerprint comparison, and Android destination conflicts as unverified until someone dates and signs the checklist.

## Deferred

The Goodreads CSV library, shelf filters, incremental matching, ambiguous choices, removal confirmation, and hidden history belong to version two. See the [approved Goodreads design](docs/superpowers/specs/2026-07-26-goodreads-list-matching-design.md).

Version one also excludes automatic discovery, playback, Play Store distribution, router port forwarding, and source-file management from Android.

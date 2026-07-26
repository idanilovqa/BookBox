# netStorage

netStorage copies selected audiobooks from a Windows library to Android over a private home network. The Android app offers audiobook cards, search, filters, and raw-folder navigation. The server streams source files through an authenticated HTTPS API.

The Android-facing API mounts the Windows library read-only. It has no upload, rename, move, tag-edit, or delete route. A separate catalog worker can write validated cover files after it backs up the prior cover and records an audit event. PostgreSQL stores the catalog and review history. The project uses no SQLite or Room database.

Version 1 targets Android 16, API 36, and the Galaxy S23 Ultra. It uses a manually entered server address and remembers the approved certificate fingerprint.

## Start here

- [Install the Windows server and Android app](docs/installation.md)
- [Use the Android app](docs/android.md)
- [Run and maintain the server](docs/operations.md)
- [Back up and recover netStorage](docs/backup-and-recovery.md)
- [Complete the Galaxy S23 Ultra checklist](docs/galaxy-s23-checklist.md)
- [Read the v1.0.0 release notes](RELEASE_NOTES.md)

## Design and implementation records

- [Version-one design](docs/superpowers/specs/2026-07-26-netstorage-design.md)
- [Server transfer plan](docs/superpowers/plans/2026-07-26-netstorage-server-transfer.md)
- [Catalog plan](docs/superpowers/plans/2026-07-26-netstorage-catalog.md)
- [Android plan](docs/superpowers/plans/2026-07-26-netstorage-android.md)
- [Integration and release plan](docs/superpowers/plans/2026-07-26-netstorage-integration-release.md)

The approved [Goodreads list-matching design](docs/superpowers/specs/2026-07-26-goodreads-list-matching-design.md) targets version two. Version one does not import Goodreads CSV files, scrape pages, or store Goodreads credentials.

## Repository layout

- `server/`: Go API, catalog worker, migrations, and tests
- `android/`: Kotlin and Jetpack Compose app
- `deploy/`: Dockerfiles, Compose configuration, and operator scripts
- `e2e/`: isolated Compose and API 36 journey
- `docs/`: setup, operations, recovery, and design records

Run the full release gate from PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1
```

The script produces a sideloadable debug APK in `artifacts\`. If you provide the optional local keystore and signing environment variables, it also produces a signed release APK. The script writes checksums and an APK metadata report after it verifies package identity, SDK levels, permissions, and signatures.

# BookBox

BookBox copies audiobooks from a Windows library to Android across a private home network. The Android app provides catalog search, raw-folder browsing, and resumable downloads. The server exposes an authenticated HTTP API on the Windows computer.

The API mounts the source library read-only and has no upload, rename, move, tag-edit, or delete route. A separate catalog worker can publish validated cover images after it saves the previous cover and records an audit event. PostgreSQL stores the catalog and review history.

BookBox uses `http://bookbox.local:8787` by default. A headless Windows discovery process advertises that name with mDNS. You can enter the server's private IPv4 address if `.local` name resolution fails.

BookBox v1 sends the username, password, session tokens, catalog data, and audiobook bytes over unencrypted HTTP. Use a trusted private LAN. Keep Windows Firewall rules limited to the Private profile and `LocalSubnet`; do not expose BookBox through a guest network, public network, WAN, or router port forwarding.

## Start here

- [Install BookBox](docs/installation.md)
- [Set up the Windows server](docs/setup-server.md)
- [Use the Android app](docs/android.md)
- [Operate BookBox](docs/operations.md)
- [Migrate from netStorage](docs/migration-from-netstorage.md)
- [Review backup and recovery status](docs/backup-and-recovery.md)
- [Run the Galaxy S23 Ultra checklist](docs/galaxy-s23-checklist.md)
- [Read the roadmap](docs/roadmap.md)
- [Read the v1.0.0 release notes](RELEASE_NOTES.md)

## Repository layout

- `server/`: Go API, catalog worker, discovery process, migrations, and tests
- `android/`: Kotlin and Jetpack Compose app
- `deploy/`: Dockerfiles, Compose configuration, and Windows scripts
- `e2e/`: isolated Compose and Android API 36 journey
- `docs/`: setup, operation, recovery, roadmap, and design records

Run the release gate from PowerShell:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1
```

The script writes `BookBox-v1.0.0-debug.apk` and its verification reports to `artifacts\`. Optional release signing creates `BookBox-v1.0.0-release.apk`.

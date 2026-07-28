# BookBox v1.0.0

Release status: implementation candidate. Automated gates and the Galaxy S23 Ultra checklist determine release readiness.

## Included

- BookBox identity throughout the Android package, Go commands, Docker project, database, configuration, and artifacts
- Authenticated HTTP access to configured Windows audiobook roots on a private LAN
- `bookbox.local` discovery through a headless Windows process, with private IPv4 fallback
- Username-and-password sign-in on Android
- Audiobook cards, search, filters, and raw-folder navigation
- Copy to an Android Storage Access Framework folder
- Range-based resume, SHA-256 verification, and destination conflict handling
- Android user-initiated background transfers with notifications
- PostgreSQL catalog, metadata review, grouping corrections, worker jobs, and cover audit history
- Open Library metadata plus optional Google Books metadata
- Docker Compose hardening, health checks, source-mutation checks, synthetic recovery validation, and API 36 tests

## Network boundary

BookBox v1.0.0 uses unencrypted HTTP. Devices on the same network can observe credentials, tokens, metadata, and audiobook traffic. Run BookBox only on a trusted private LAN. The supported firewall scope is Private profile plus `LocalSubnet` for TCP 8787 and UDP 5353. Guest Wi-Fi, public networks, WAN access, and router port forwarding are outside the supported boundary.

## Clean reinstall boundary

The rename changes application and deployment identities. Android treats `com.rendref.bookbox` as a different app, and Docker uses the `bookbox-prod` project and `bookbox` database. Existing netStorage preferences, sessions, secrets, and catalog data do not migrate in place.

Preserve Android audiobook downloads and the Windows source library before removing the old app or deployment. Install BookBox, generate new secrets, and let the catalog rebuild from the source library. See [Migrate from netStorage](docs/migration-from-netstorage.md).

## Sideload packages

`build-release.ps1` creates `artifacts\BookBox-v1.0.0-debug.apk`. Optional local signing creates `artifacts\BookBox-v1.0.0-release.apk` when `local-secrets\bookbox-release.jks` and these variables exist:

- `BOOKBOX_KEY_ALIAS`
- `BOOKBOX_KEYSTORE_PASSWORD`
- `BOOKBOX_KEY_PASSWORD`

`verify-release.ps1` checks `com.rendref.bookbox`, API 36 SDK levels, permissions, signature, and release debuggable state. It writes `SHA256SUMS.txt` and `APK_METADATA.json`.

## Manual gate

No one has recorded the Galaxy S23 Ultra checklist. Treat screen-off continuation, reboot behavior, Wi-Fi interruption, private-LAN access, discovery, and Android destination conflicts as unverified until a tester dates and signs the checklist.

## Planned

The [roadmap](docs/roadmap.md) records QR-based HTTPS pairing and a Windows tray approval companion. Neither feature exists in this release. After a future tray approval, the Android user must still enter the BookBox username and password.

The Goodreads CSV library, shelf filters, incremental matching, ambiguous choices, removal confirmation, and hidden history also remain deferred.

# Migrate from netStorage

BookBox uses new Android, Docker, database, command, configuration, and storage identities. Treat the rename as a clean reinstall. Do not copy old preferences, sessions, secrets, database volumes, or catalog state into BookBox.

## Preserve user files

Before stopping the old deployment:

1. Confirm the Windows source audiobook path and make a normal filesystem backup.
2. Confirm that Android downloads exist in the chosen document folder.
3. Copy any Android downloads you cannot recreate to another folder or backup device.
4. Record the old source roots and any confirmed catalog corrections you need to repeat.

Uninstalling the old Android app can remove app-private state. Files in a user-selected document tree should remain, but verify them before uninstalling.

## Stop the old deployment

Open the old checkout and inspect its active project:

```powershell
docker compose --env-file deploy\.env -f deploy\compose.yml ps
docker compose --env-file deploy\.env -f deploy\compose.yml stop
```

Use the old checkout's Compose files and environment. Do not run `down --volumes`; keep the old volumes until BookBox has scanned the source and copied a test audiobook.

Stop any old discovery or helper process through its documented command. Confirm that the old API port no longer listens before starting BookBox.

## Install BookBox

Follow [Install BookBox](installation.md). Use:

- Android application ID `com.rendref.bookbox`
- Docker project `bookbox-prod`
- PostgreSQL database and user `bookbox`
- default URL `http://bookbox.local:8787`

Run `deploy\scripts\new-secrets.ps1` to generate BookBox secrets. Choose a new application password. Do not reuse the old password hash or session key.

Point `BOOKBOX_LIBRARY_PATH` at the preserved Windows source. BookBox creates a new PostgreSQL catalog and scans the source library. The source audiobook files remain in place. Existing cover files also remain; the worker records future cover changes in the new catalog.

## Verify before removing old state

1. Confirm `bookbox.local` resolves to the reserved Windows address.
2. Sign in to BookBox with the new password.
3. Check several catalog entries and raw folders.
4. Copy one audiobook to Android and verify its bytes.
5. Confirm the old Android downloads remain available.

Remove old app data, containers, or volumes only after these checks and a separate backup. Resolve exact old resource names before deletion so BookBox resources remain untouched.

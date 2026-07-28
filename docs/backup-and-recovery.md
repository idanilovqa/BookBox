# Backup and recovery

The automated recovery test uses a synthetic Docker project and temporary library:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\test-recovery.ps1
```

The test verifies a PostgreSQL dump, non-secret configuration, cover cache, original-cover backups, and secret rotation. Its backup payload contains:

- `database.dump`
- `cover_cache.tar.gz`
- `cover_backups.tar.gz`
- `config.yml`
- `manifest.json`

The manifest records file sizes and SHA-256 digests. The restore test rejects missing, extra, renamed, or changed payload files.

The synthetic test never reads `H:\Audiobooks`. It removes only resources that match its `bookbox-recovery-<run-id>` project and temporary root.

## Production status

The current implementation candidate does not enable production backup or restore. `backup.ps1` and `restore.ps1` accept only synthetic `bookbox-recovery-*` projects under the guarded temporary recovery root. Do not weaken those checks for a production deployment.

Keep a separate backup of the Windows audiobook source with your normal Windows backup tool. BookBox does not include source audio in its catalog backup. Android downloads also remain outside the server backup.

If the catalog database becomes unavailable and no approved production restore exists, preserve the Windows source library, create new BookBox secrets and empty BookBox volumes, then let the worker rebuild the catalog. Review decisions, worker jobs, and cover audit history do not survive a clean catalog rebuild.

## Secret handling

Do not place these files in a recovery archive:

- application password hash
- PostgreSQL password
- session key
- Google Books key

Generate new secrets for a clean installation. Each Android user must enter the current BookBox password after session invalidation.

## Cover recovery boundary

An operator cover rollback must use an audited cover-event UUID, verify the recorded hashes and confined cover path, replace one recognized cover file from its backup, append a new audit event, and request a catalog rescan. Android must not expose this operation. Stop if the current cover digest differs from the selected event.

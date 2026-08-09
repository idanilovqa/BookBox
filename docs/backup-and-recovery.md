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

The PostgreSQL backup must retain staged catalog state and audit history. Check
that it includes `source_files`, `books`, `book_files`, `scan_runs`,
`worker_jobs`, `catalog_generations`, `book_grouping_evidence`,
`grouping_corrections`, `library_search_snapshots`,
`library_search_documents`, `reorganization_operations`, and
`reorganization_moves`. Restore these tables as one database snapshot so file
fingerprints, grouping decisions, projection generations, and move journals
refer to the same point in time.

If the catalog database becomes unavailable and no approved production restore exists, preserve the Windows source library, create new BookBox secrets and empty BookBox volumes, then let the worker rebuild the catalog. Review decisions, worker jobs, and cover audit history do not survive a clean catalog rebuild.

After a database restore, keep the worker stopped while you compare the latest
reorganization journal with the source backup. A `running` operation at backup
time may have moved files after PostgreSQL wrote the dump. Treat it as
`needs_attention`: verify each journaled fingerprint and relative path, restore
the source layout from backup when needed, then start the worker and request a
full inventory. Do not replay a confirmation from the restored database.

## Secret handling

Do not place these files in a recovery archive:

- application password hash
- PostgreSQL password
- session key
- Google Books key

Generate new secrets for a clean installation. Each Android user must enter the current BookBox password after session invalidation.

## Cover recovery boundary

An operator cover rollback must use an audited cover-event UUID, verify the recorded hashes and confined cover path, replace one recognized cover file from its backup, append a new audit event, and request a catalog rescan. Android must not expose this operation. Stop if the current cover digest differs from the selected event.

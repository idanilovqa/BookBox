# Backup and recovery

BookBox's recovery test uses a unique synthetic Docker project and a temporary
library. It verifies a PostgreSQL dump, the TLS identity volume, non-secret
configuration, the cover cache, original-cover backups, and password/session
rotation:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File deploy\scripts\test-recovery.ps1
```

The test never reads `H:\Audiobooks`. It removes only its matching
`bookbox-recovery-<run-id>` containers, networks, volumes, local images, and
temporary directory. Failure diagnostics exclude secret files and backup
payloads.

## Production status

Production backup, restore, and operator cover rollback are intentionally
disabled in this implementation candidate until their high-impact permissions
are explicitly approved and the guarded production paths pass the release
tests. Do not adapt the synthetic scripts by removing their project-name or
temporary-root checks.

The intended production backup payload is PostgreSQL, the TLS
certificate/private-key volume, cover cache, original-cover backup volume, and
non-secret configuration. It must exclude the application password hash,
PostgreSQL password, session key, Google Books key, and Android credentials.

An eventual cover rollback command must accept an audited cover-event UUID,
verify the recorded hashes and confined recognized cover path, replace only
that one cover from its backup, append a new audit event, and trigger a catalog
rescan. It must never be callable from Android or change audio, tags, or
folders.

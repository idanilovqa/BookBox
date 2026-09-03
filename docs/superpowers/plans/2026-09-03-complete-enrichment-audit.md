# Complete enrichment audit and restart

Goal: Audit all 276 historical unavailable jobs, 22 provider rejections, and 3 invalid metadata records; implement missing evidence-based cleanup/grouping rules; install the corrected worker and restart automatic enrichment.

Architecture: Preserve original tags and current/manual identities. Generate safe title/author/series alternatives, corroborate provider identity, group actual chapters, skip obsolete records, and recover retryable work through the existing worker queues.

Constraints: One pytest process maximum; no source-file deletion; no blanket process termination; preserve unrelated roadmap changes. Existing Docker PostgreSQL and API, native Windows worker. User explicitly authorized restarting enrichment after verification.

- [x] Inventory every historical entry, its rule coverage, current/replacement record candidates, and failure category.
- [x] Add regression coverage for entries 209–276 and newly uncovered patterns, including grouping and provider validation failures.
- [x] Fix parser/provider boundary/queue recovery failures; verify focused suites and review.
- [ ] Build and install current Windows worker with backup/rollback; deploy API; verify installed executable hash and service logs.
- [ ] Requeue eligible active metadata and cover jobs, exclude retired/empty records, and verify live completion/cover progress.
- [x] Publish audit report with unresolved cases and operational status.

Verified 2026-09-03: commit 334148b passes the complete Go suite with isolated PostgreSQL integration tests. API rebuilt/restarted and healthy. All 301 historical entries are documented at artifacts/full-enrichment-audit/every-entry.md. Three malformed-tag records confirmed; Wuthering Heights (48 tracks), Kun (213 tracks), and Moorcock (15 tracks) repaired with backups and locked groupings.

Remaining operational gate: Windows administrator consent is pending for the prepared service installer. Installed worker hash remains 612931894DF957803F149C1A1A82DB175DD4254D35B522CA5EA6E40FE00497DB; prepared verified hash is AACDD78DFD29F21CB380200A7C603BA7E21DFB6C9586D3F0D1685BC485F28D75. Installer and retry SQL validated; retry SQL has not been executed. After consent, verify installed hash, service startup, recovered leases, queue progress and new cover events. Do not claim the new worker is running before that verification.

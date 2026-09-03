# Complete enrichment audit and restart

Goal: Audit all 276 historical unavailable jobs, 22 provider rejections, and 3 invalid metadata records; implement missing evidence-based cleanup/grouping rules; install the corrected worker and restart automatic enrichment.

Architecture: Preserve original tags and current/manual identities. Generate safe title/author/series alternatives, corroborate provider identity, group actual chapters, skip obsolete records, and recover retryable work through the existing worker queues.

Constraints: One pytest process maximum; no source-file deletion; no blanket process termination; preserve unrelated roadmap changes. Existing Docker PostgreSQL and API, native Windows worker. User explicitly authorized restarting enrichment after verification.

- [ ] Inventory every historical entry, its rule coverage, current/replacement record candidates, and failure category.
- [ ] Add regression coverage for entries 209–276 and newly uncovered patterns, including grouping and provider validation failures.
- [ ] Fix parser/provider boundary/queue recovery failures; verify focused suites and review.
- [ ] Build and install current Windows worker with backup/rollback; deploy API; verify installed executable hash and service logs.
- [ ] Requeue eligible active metadata and cover jobs, exclude retired/empty records, and verify live completion/cover progress.
- [ ] Publish audit report with unresolved cases and operational status.

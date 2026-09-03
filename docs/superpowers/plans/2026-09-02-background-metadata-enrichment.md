# Automatic metadata enrichment implementation plan

Goal: resolve dirty book identities and process enrichment automatically.
Architecture: metadata query strategy plus isolated leased background processing using the existing PostgreSQL queue.

- [x] Add failing matcher/provider regressions for M01: tHe final Empire, author fallback, ambiguous titles, duplicate candidates and provider errors.
- [x] Implement shared title cleanup and bounded fallback queries in server/internal/metadata; keep the worker's existing audiobook suffix behavior compatible.
- [x] Add PostgreSQL tests for enrichment retry cooldowns and filtered job claiming, then implement both in server/internal/catalog.
- [x] Add worker tests for independent startup/periodic enrichment and shutdown; wire a separate metadata lane in server/cmd/bookbox-catalog-worker.
- [x] Verify focused metadata, worker, catalog, HTTP API and command packages in disposable PostgreSQL; inspect diff and cross-compile Windows worker.
- [ ] Deploy validated binaries, inspect service health and job progress, and report the remaining ambiguity/provider limitations.

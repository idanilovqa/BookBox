# Automatic metadata enrichment

The server must recognize dirty audiobook identities such as M01: tHe final Empire by Brandon Sanderson and enrich existing and newly discovered books without an Android action.

## Matching
Use bounded staged queries: ISBN, cleaned title with author, original title with author when different, cleaned title alone, then author-only fallback. Normalize casing and punctuation for comparison; strip explicit sequence prefixes and audiobook suffixes without stripping meaningful numbers such as 1984 or 2001: A Space Odyssey. Keep the original source names. Rank candidates against cleaned title and author, deduplicate provider identities, and require both confidence and separation from alternatives before automatic selection. Author-only results require title evidence; substring matching alone must not select omnibus collections or sequels. Preserve ambiguous candidates for manual review.

Open Library author searches retrieve up to 300 works in three pages and share its existing rate limiter and cache. Other providers use their bounded search response. Stop fallback searches once a decisive result exists. A failed provider does not discard a good result from another provider; unsuccessful searches with provider errors remain retryable.

## Background processing
Run one metadata/cover lane alongside the structural scan lane, with disjoint claims and shared shutdown supervision. Metadata startup and periodic maintenance enqueue all active unconfirmed books independently of scans. Keep job leases and idempotency. Version the enrichment identity to revisit books after the new matcher ships. Retry exhausted jobs after one day and completed unresolved searches after one week; avoid retry storms on each scan. Confirmed metadata remains protected. Retrying a confirmed enrichment job must repair a cover enqueue that failed after confirmation.

## Validation
Use deterministic provider fixtures for dirty names, fallback, duplicate results, ambiguous matches and outages. Use disposable PostgreSQL tests for lane isolation and retry cooldowns. Verify worker scheduling and shutdown, build API and Windows worker, then update the local service only after checks pass. No source-folder renaming is involved.

## Review and deployment findings
Automatic confirmation compares the book update timestamp inside a book-first
locked transaction, so regrouping or manual confirmation during provider I/O
causes a retry. Maintenance also revisits confirmed books without a local cover;
cover jobs receive the same bounded retry cooldown. Equivalent title/author
editions share an identity for confidence comparison, and responses retain at
most 20 provider candidates. A failed provider keeps unresolved searches retryable.
Startup root synchronization skips unchanged root and generation rows to avoid
waiting on a scan merely to start the API.

Validated with the affected Go packages and disposable PostgreSQL, including
the catalog search-plan fixture; metadata, worker, HTTP API and worker command
packages also passed the race detector. A separate regression reproduces and
verifies startup while root and generation rows are locked.
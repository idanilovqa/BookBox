# Catalog Loading and Grouping Repair Design

## Goal

Restore the installed Android application's audiobook catalog and raw-folder
search, replace the inflated provisional book count with stable folder-aware
grouping, and prevent metadata-enrichment failures from blocking structural
catalog work.

The repair must not rename, move, delete, or rewrite audiobook source files.
It may rebuild derived PostgreSQL catalog/search state and replace the managed
API container and native Windows catalog-worker binary.

## Confirmed Failure State

The installed Android build uses the current grouped catalog contract. It
expects `GET /v1/books` to return `groups`, `snapshots`, pagination, and filter
data. The running API container returns the retired `items` envelope. The API
therefore responds with HTTP 200 while the Android client correctly rejects
the body as an invalid catalog response.

Raw-folder search is a separate live failure. Requests to
`GET /v1/browse/search` return HTTP 500 for intermediate queries, and a final
query can take roughly four seconds. The running API is older than the current
search projection and query implementation.

The worker completed file inventory and probing, but the published count of
7,413 books is not a credible finished audiobook count. Of 7,413 active book
records, 6,907 contain only one audio file. In 433 multi-file folders, 18,317
audio files share one album per folder but currently produce 5,394 book
records. These are provisional per-file groups, not final audiobook cards.

The durable queue explains why the provisional state persisted:

- large waves of failing `enrich_book` jobs are older than structural jobs and
  win the current FIFO claim order;
- `regroup_scope` jobs include the global catalog generation in their dedupe
  key, so jobs for the same scope and different generations accumulate;
- each regroup commits against a global generation, making sibling regroup
  jobs stale after any successful commit;
- stale regroup jobs enqueue replacements instead of resolving the conflict in
  a bounded handler retry;
- every successful scope regroup queues enrichment for every incomplete book
  in the root, recreating the enrichment wave.

## Chosen Approach

The repair uses two coordinated phases.

First, deploy the current API source so the server and installed APK share the
same response contract and current raw-search implementation. This is an
operational replacement of the API container; it does not wait for the worker
repair because it immediately restores contract compatibility.

Second, change worker scheduling and regroup behavior so structural catalog
jobs converge before optional online enrichment. Rebuild the native Windows
worker, install it through the existing owned-service lifecycle, and rebuild
the derived catalog/search state from the already inventoried source data.

Rejected alternatives:

- Reinstalling or weakening the Android client would hide an obsolete server
  contract and reduce response validation without repairing the server.
- Merely reordering the current queue would expose the global-generation
  conflict storm and could take quadratic work to drain.
- Treating every audio file as a book would preserve the known-wrong count and
  discard the album/folder evidence the scanner already collected.

## Worker Scheduling

Job claiming uses an explicit structural priority before `run_after` and
creation time. Structural correctness jobs are ordered ahead of optional
enrichment:

1. `reorganize_files`
2. `scan_root`
3. `probe_file` and `inspect_companion`
4. `regroup_scope`
5. `publish_search`
6. `publish_cover`
7. `enrich_book`

Within one priority, existing due-time and FIFO ordering remains intact.
Retries and leases retain their current durability rules. This changes which
due job is claimed, not job ownership or at-least-once delivery.

The implementation will define the priority in the catalog repository rather
than relying on enqueue timing. A repository test will seed older enrichment
work and newer structural work and prove the structural job is claimed first.

## Regroup Coalescing and Conflict Handling

An active regroup is identified by root and normalized scope, independent of
catalog generation. Its dedupe key is:

`regroup_scope:<root-id>:<normalized-scope>`

The generation remains in the payload as the caller's observation, but it is
not part of active-job identity. Repeated probe completions for one scope
therefore converge on one queued or running regroup.

The regroup handler treats a stale global generation as expected concurrent
progress. It reloads the current generation and grouping evidence and retries
the same scope in place with a small fixed bound. It does not enqueue a new
generation-keyed regroup for each conflict. If the bound is exhausted, the
current durable job is retried through the normal job retry mechanism with a
sanitized error.

Successful regrouping queues one generation-specific `publish_search` job.
Search publication retains its existing stale-generation protection so an
older projection cannot replace a newer catalog.

Tests will prove that repeated generations dedupe to one active scope job and
that a single simulated generation conflict is resolved without a replacement
job wave.

## Enrichment Boundary

Scope regrouping no longer scans the whole root and queues every incomplete
book. It queues enrichment only for incomplete books produced or materially
changed by that regroup scope. If the existing repository boundary cannot
return the changed book identifiers safely, the first repair will defer bulk
enrichment until the root's structural queue is empty and queue one bounded
root enrichment pass. It will not reintroduce an all-root pass after every
scope.

Provider failures remain non-fatal to catalog structure. A book can be
published from local tags and paths while external metadata is unavailable.
Existing provider retry limits and sanitized errors remain in force.

## Recovery and Deployment

Deployment follows the repository's existing hybrid boundaries:

- build and recreate `bookbox-api` with `deploy/compose.yml` and
  `deploy/.env`;
- build `artifacts/bookbox-catalog-worker.exe` using the release process;
- stop and replace only the owned `BookBoxCatalogWorker` installation through
  the existing remove/install scripts, preserving its ProgramData config,
  logs, secrets, and source-library contents;
- start the service and verify its executable, state, and log progress;
- enqueue or perform one controlled full-root structural rebuild from the
  existing library inventory, then publish a fresh search snapshot.

Before replacing the worker, the current service is stopped and its exact PID
is verified exited. The API remains available during worker replacement. A
failed API deployment rolls back to the prior Compose image. A failed worker
replacement uses the installer lifecycle's existing rollback behavior; no
broad process termination is permitted.

Existing dead enrichment records are historical evidence and do not count as
active work. They may remain for audit unless their volume demonstrably harms
the repaired queries. Any cleanup would be a separate, explicitly scoped
database operation.

## Verification

Automated verification is test-first and covers:

- current grouped `/v1/books` response shaping and Android contract fixtures;
- structural job priority over older enrichment work;
- generation-independent regroup deduplication;
- bounded in-place generation-conflict recovery;
- no root-wide enrichment fan-out after each scope regroup;
- raw search for a known term using the current projection.

Deployment verification requires all of the following:

- live `/v1/books?sort=title` returns `groups`, not `items`;
- the response passes the same snapshot, group, path, review-state, and cover
  URL invariants enforced by Android;
- live raw search for `sanderson` returns HTTP 200 with matching entries inside
  the API timeout budget;
- no due structural jobs remain after the recovery run settles;
- the published search generation equals the current catalog generation;
- the final audiobook count is computed from stable grouped books and is
  materially consistent with folder/tag evidence, rather than assumed from a
  target number;
- the Android catalog displays cards and raw-folder search returns results.

The final count is an observed output, not a hard-coded acceptance value. The
repair reports the final books, active audio files, audio-containing folders,
single-file books, and multi-file books so the result can be audited.

## Non-Goals

- Changing Android validation to accept the obsolete `items` contract.
- Modifying audiobook source filenames, folders, tags, or audio bytes.
- Guaranteeing external metadata providers are available.
- Deleting historical failed jobs as part of the correctness repair.
- Redesigning manual grouping or reorganization workflows.

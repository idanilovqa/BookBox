# Open Library Enrichment Reliability Design

## Goal

BookBox will use Open Library for identified, bounded metadata lookups without sending invalid queries or creating an unbounded worker backlog. Local and inferred metadata will remain available during provider failures.

## Provider Requests

BookBox will include an operator-configured contact address in its Open Library `User-Agent`. The repository will contain a placeholder; the local deployment will use the approved contact address. BookBox will not log the address or include it in API responses.

The client will omit empty parameters. It will choose the strongest available query in this order:

1. ISBN;
2. title and author;
3. title through the general `q` parameter.

BookBox will not call Open Library when all three inputs are empty. It will treat an empty successful result as a valid response.

## Rate and Usage Controls

The API and worker will share a PostgreSQL-backed reservation gate for Open Library. The gate will space requests by at least one third of a second, limiting the deployment to three requests per second without bursts.

Provider calls will support user-triggered searches and bounded enrichment for new or materially changed audiobooks. BookBox will not use the API to sweep the full catalog repeatedly. A full bulk import remains outside this change and requires Open Library data dumps or explicit provider permission.

The client will make at most two retries after the initial request for `429` and transient `5xx` responses. It will honor `Retry-After` up to two seconds and otherwise use exponential delay. It will not multiply provider retries by repeatedly recreating the same worker job.

## Query Cache and Enrichment State

PostgreSQL will cache normalized provider queries and their candidate or empty result. Candidate results will expire after 30 days, empty results after seven days, and transient failures will block another automatic attempt for five minutes.

Each audiobook will store an enrichment input fingerprint derived from its normalized title, author, ISBN, and stable assigned-audio fingerprints. The worker will enqueue enrichment only when this fingerprint has no completed or active state. A material input change creates a new attempt.

The rollout migration will mark queued enrichment jobs from the old unversioned scheme as superseded. It will preserve completed and dead history while preventing the old backlog from running.

## Failure Handling

Provider failure will leave current book metadata, matches, and covers unchanged. User-triggered search will return a stable provider-unavailable error instead of a generic internal error. Worker jobs will record a sanitized category and next retry time without query text or contact information.

## Test Coverage

Tests will cover omitted empty parameters, ISBN/title query selection, title-only `q`, the identified `User-Agent`, shared request spacing, cache hits, empty-result caching, retry boundaries, unchanged-book deduplication, changed-input re-enrichment, superseded legacy jobs, provider failure preservation, and log redaction.

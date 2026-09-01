# Open Library Enrichment Reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Open Library lookups valid, identified, deployment-rate-limited, cached, and deduplicated by audiobook input.

**Architecture:** The metadata client omits empty fields and accepts an injected request gate. Migration 0008 adds provider cache, rate reservation, and enrichment fingerprint state used by both the API and worker.

**Tech Stack:** Go 1.26, PostgreSQL 18, pgx/v5, Windows service configuration, Docker Compose.

## Global Constraints

- Open Library requests are spaced by at least 333,333,334 ns across the deployment.
- `User-Agent` contains configured contact information that never enters logs or responses.
- Candidate cache TTL is 30 days; empty TTL is seven days; transient suppression is five minutes.
- Provider failure preserves local metadata.

---

### Task 1: Send valid identified requests

**Files:**
- Modify: `server/internal/metadata/openlibrary.go`
- Modify: `server/internal/metadata/provider.go`
- Test: `server/internal/metadata/openlibrary_test.go`

**Interfaces:**
- Produces variadic-compatible `NewOpenLibrary(baseURL string, client *http.Client, contact ...string)`.

- [ ] **Step 1: Add failing tests for omitted empty author, ISBN preference, title-only `q`, and identified `User-Agent`**

```go
if request.URL.Query().Has("author") { t.Fatal("empty author was sent") }
if got := request.URL.Query().Get("q"); got != "GL16" { t.Fatalf("q=%q", got) }
```

- [ ] **Step 2: Run `go test ./internal/metadata -run OpenLibrary -count=1`; verify RED**
- [ ] **Step 3: Set only non-empty parameters, use `q` for title-only input, and validate contact text**
- [ ] **Step 4: Re-run the test; verify PASS**
- [ ] **Step 5: Commit `fix(metadata): send valid identified Open Library queries`**

### Task 2: Add coordination storage

**Files:**
- Create: `server/migrations/0008_metadata_provider_coordination.sql`
- Modify: `server/internal/database/migrate_test.go`

**Interfaces:**
- Produces `metadata_provider_cache`, `metadata_provider_rate_limits`, and `book_enrichment_states`.

- [ ] **Step 1: Add a failing migration test for all tables and legacy queued-job supersession**
- [ ] **Step 2: Run `go test ./internal/database -run MetadataProvider -count=1`; verify RED**
- [ ] **Step 3: Add keyed cache rows, one rate row per provider, and fingerprint state per book**
- [ ] **Step 4: Mark queued legacy `enrich_book` jobs dead with sanitized `superseded enrichment scheme`**
- [ ] **Step 5: Run migration tests and commit `feat(database): coordinate metadata provider work`**

### Task 3: Implement gate and cache

**Files:**
- Create: `server/internal/catalog/provider_coordination.go`
- Create: `server/internal/catalog/provider_coordination_test.go`
- Modify: `server/internal/metadata/provider.go`
- Modify: `server/internal/metadata/matcher.go`

**Interfaces:**
- Produces `RequestGate.Wait(context.Context, string, time.Duration) error`.
- Produces repository methods `ReserveProviderRequest`, `LoadProviderCache`, and `SaveProviderCache`.

- [ ] **Step 1: Write failing transaction tests for request spacing and cache expiries**
- [ ] **Step 2: Run `go test ./internal/catalog -run ProviderCoordination -count=1`; verify RED**
- [ ] **Step 3: Implement `FOR UPDATE` reservation and bounded JSON candidate cache**
- [ ] **Step 4: Inject the gate before `client.Do`; cache candidates and empty responses by normalized query hash**
- [ ] **Step 5: Run catalog/metadata tests and commit `feat(metadata): coordinate and cache provider calls`**

### Task 4: Fingerprint enrichment work

**Files:**
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/catalog/postgres.go`
- Test: `server/internal/worker/worker_test.go`
- Test: `server/internal/worker/staged_catalog_test.go`
- Test: `server/internal/catalog/postgres_test.go`

**Interfaces:**
- Produces `EnrichmentFingerprint(catalog.BookDetail) string` and fingerprinted dedupe keys.

- [ ] **Step 1: Write failing tests showing unchanged completed work is not queued and changed input is queued**
- [ ] **Step 2: Run focused worker tests; verify RED**
- [ ] **Step 3: Hash normalized title, first author, ISBN, and sorted audio ID/SHA-256 pairs**
- [ ] **Step 4: Persist active, success, empty, and transient states with retry times**
- [ ] **Step 5: Run tests and commit `fix(worker): deduplicate metadata enrichment inputs`**

### Task 5: Wire configuration and stable failure

**Files:**
- Modify: `server/cmd/bookbox-api/main.go`
- Modify: `server/cmd/bookbox-catalog-worker/main.go`
- Modify: `server/internal/httpapi/catalog.go`
- Modify: `deploy/compose.yml`
- Modify: `deploy/windows/catalog-worker.example.yml`
- Test: corresponding command and HTTP API tests.

**Interfaces:**
- Consumes `BOOKBOX_OPENLIBRARY_CONTACT`.
- Produces HTTP 503 code `metadata_provider_unavailable`.

- [ ] **Step 1: Add failing configuration, redaction, and HTTP error tests**
- [ ] **Step 2: Run focused tests; verify RED**
- [ ] **Step 3: Wire contact, gate, and cache into API and worker; check in placeholders only**
- [ ] **Step 4: Run focused tests; verify PASS**
- [ ] **Step 5: Commit `fix(metadata): bound Open Library integration`**

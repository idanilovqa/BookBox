# Local Cover Discovery and Loading Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Discover plausible local artwork, assign it safely, serve it through the catalog, and authenticate Android requests.

**Architecture:** Inventory classifies supported images as candidates, the worker inspects them with bounded decoders, grouping assigns a scored winner, and the cover endpoint serves the assigned current file. Android installs its existing authenticated Coil loader.

**Tech Stack:** Go image decoders, PostgreSQL, confined resolver, Kotlin, Coil 3, OkHttp.

## Global Constraints

- Source artwork stays read-only.
- Assignment requires score 70 and lead 15.
- Inspection retains 20 MiB and 100 million pixel limits.
- Provider artwork never receives BookBox credentials.

---

### Task 1: Inventory and inspect candidates

**Files:**
- Modify: `server/internal/scanner/scanner.go`
- Modify: `server/internal/scanner/inventory_test.go`
- Modify: `server/internal/worker/staged.go`
- Modify: `server/internal/worker/staged_catalog_test.go`
- Modify: `server/internal/covers/image.go`

**Interfaces:**
- Produces companion kind `image` for supported non-standard names.
- Stores `width`, `height`, and `format` in `SourceFile.Media`.

- [ ] **Step 1: Write failing scanner tests for title-named JPEG/PNG/WebP candidates and excluded temporary/unsupported files**
- [ ] **Step 2: Run scanner tests; verify RED**
- [ ] **Step 3: Keep standard names as cover and classify other supported images as companion/image**
- [ ] **Step 4: Write failing worker tests, then inspect through `covers.Inspect` using confined handles**
- [ ] **Step 5: Run focused tests and commit `feat(scanner): inspect local cover candidates`**

### Task 2: Score and assign artwork

**Files:**
- Create: `server/internal/grouping/covers.go`
- Create: `server/internal/grouping/covers_test.go`
- Modify: `server/internal/grouping/group.go`

**Interfaces:**
- Produces `RankCovers(Hypothesis, []scanner.File) []CoverCandidate`.

- [ ] **Step 1: Write failing ranking tests for standard names, title/author agreement, portrait resolution, negative names, alternates, shared folders, threshold, and lead**
- [ ] **Step 2: Run cover-ranking tests; verify RED**
- [ ] **Step 3: Implement named signals and assign only the winning image as `FileRoleCover`**
- [ ] **Step 4: Run grouping tests; verify PASS**
- [ ] **Step 5: Commit `feat(grouping): assign local artwork by evidence`**

### Task 3: Serve assigned covers

**Files:**
- Modify: `server/internal/catalog/postgres.go`
- Modify: `server/internal/httpapi/books.go`
- Modify: `server/internal/httpapi/books_test.go`

**Interfaces:**
- Produces `GetAssignedBookCover(context.Context, uuid.UUID) (catalog.SourceFile, error)`.

- [ ] **Step 1: Write failing tests proving assigned title-named art is served and unassigned nearby art is ignored**
- [ ] **Step 2: Run `go test ./internal/httpapi -run BookCover -count=1`; verify RED**
- [ ] **Step 3: Load the assigned cover, validate ownership/fingerprint/type, and retain exact-name fallback only without an assignment**
- [ ] **Step 4: Run cover/confinement tests; verify PASS**
- [ ] **Step 5: Commit `fix(api): serve catalog-assigned local covers`**

### Task 4: Install the authenticated Android loader

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/BookBoxRuntime.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/CatalogImageLoader.kt`
- Test: `android/app/src/androidTest/java/com/rendref/bookbox/ConnectedAppWiringTest.kt`

**Interfaces:**
- Calls `installCatalogImageLoader(calls)` from `BookBoxRuntime.connected` before connected UI composition.

- [ ] **Step 1: Write a failing wiring test that observes a bearer token on a cover request and one authorized refresh**
- [ ] **Step 2: Run the focused Android test; verify RED**
- [ ] **Step 3: Install the singleton loader from the connection's `AuthenticatedCall`; retain the public provider loader**
- [ ] **Step 4: Re-run tests; verify PASS**
- [ ] **Step 5: Commit `fix(android): authenticate local cover images`**

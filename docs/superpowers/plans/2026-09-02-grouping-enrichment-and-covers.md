# Grouping, Enrichment, and Covers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Group numbered collection tracks into one audiobook, send meaningful Open Library queries, and assign title-named local art.

**Architecture:** The scanner inventories every supported image. The grouping package filters placeholder media titles, recognizes coherent track sequences, and selects a scoped cover candidate. The worker blocks meaningless enrichment inputs and normalizes provider-only query text without changing the displayed local title.

**Tech Stack:** Go, filesystem inventory, image header inspection, PostgreSQL worker jobs, Open Library HTTP fixtures.

## Global Constraints

- Placeholder track titles cannot define a book or provider query.
- Conflicting descriptive album or author evidence still splits or flags a layout.
- Local cover files stay read-only and take display precedence.
- Live provider availability cannot determine automated test results.

---

### Task 1: Recognize image candidates and placeholder titles

**Files:**
- Create: `server/internal/scanner/image.go`
- Create: `server/internal/scanner/image_test.go`
- Modify: `server/internal/scanner/scanner.go`
- Modify: `server/internal/scanner/inventory.go`
- Modify: `server/internal/catalog/model.go`
- Modify: `server/internal/catalog/inventory.go`
- Modify: `server/internal/grouping/evidence.go`
- Test: `server/internal/scanner/scanner_test.go`
- Test: `server/internal/scanner/inventory_test.go`
- Test: `server/internal/catalog/inventory_test.go`
- Test: `server/internal/grouping/evidence_test.go`

**Interfaces:**
- Produces: supported `.jpg`, `.jpeg`, `.png`, and `.webp` files with `FileKindCover`.
- Produces: bounded `inspectImageHeader(io.Reader, int64) (width int, height int, format string, ok bool)`.
- Produces: `placeholderTrackTitle(string) bool` used by identity and display selection.

- [ ] **Step 1: Write failing classification and identity tests**

Add scanner cases for `Oathbringer.jpg` and `Dune - cover art.jpg`. Use small JPEG, PNG, and WebP fixtures to assert bounded header inspection records width, height, and format. Add grouping cases for `- 01/16`, `01/16`, and `Track 01 of 16`; assert their normalized identity title is empty.

- [ ] **Step 2: Run the focused tests and confirm failure**

Run: `Set-Location server; go test ./internal/scanner ./internal/grouping -run 'Test.*Image|Test.*Placeholder|TestInferTree.*Arcanum' -count=1`

Expected: FAIL because arbitrary supported images are ignored and counters become identities.

- [ ] **Step 3: Implement classification and placeholder filtering**

Classify all supported image extensions as `KindCover`. Inspect at most 512 KiB with `image.DecodeConfig`, reject dimensions above the existing cover pixel bound, and carry valid `width`, `height`, and `imageFormat` fields from `scanner.InventoryFile` through `catalog.InventoryFile` into `source_files.media`. An invalid header remains inventoried with no dimension evidence and cannot win automatic cover assignment.

Add a case-insensitive anchored expression that accepts counter-only forms:

```go
var trackCounterTitle = regexp.MustCompile(`(?i)^\s*[-_.]*\s*(?:track\s*)?\d+\s*(?:/|of)\s*\d+\s*$`)

func placeholderTrackTitle(value string) bool {
    return trackCounterTitle.MatchString(strings.TrimSpace(value))
}
```

Return an empty title from `identityForTreeFile` and skip placeholder values in `treeDisplayValue`.

- [ ] **Step 4: Run scanner and grouping tests**

Run: `Set-Location server; go test ./internal/scanner ./internal/grouping -count=1`

Expected: PASS for classification and existing audiobook cases.

- [ ] **Step 5: Commit**

```powershell
git add -- server/internal/scanner/image.go server/internal/scanner/image_test.go server/internal/scanner/scanner.go server/internal/scanner/scanner_test.go server/internal/scanner/inventory.go server/internal/scanner/inventory_test.go server/internal/catalog/model.go server/internal/catalog/inventory.go server/internal/catalog/inventory_test.go server/internal/grouping/evidence.go server/internal/grouping/evidence_test.go
git commit -m "fix: ignore counter titles during grouping"
```

### Task 2: Group coherent collection tracks by folder

**Files:**
- Modify: `server/internal/grouping/evidence.go`
- Test: `server/internal/grouping/evidence_test.go`
- Test: `server/internal/worker/inventory_grouping_test.go`

**Interfaces:**
- Produces: `coherentTrackSequence([]catalog.SourceFile) bool`.
- Produces: one `folder_default` cluster for coherent placeholder-title tracks.

- [ ] **Step 1: Add the sixteen-track Arcanum fixture**

Create sixteen MP3 source files under `Brandon Sanderson/Arcanum Unbounded`, with media titles `- NN/16`, track numbers `N`, and descriptive filenames. Assert one book titled `Arcanum Unbounded`, sixteen audio roles, and a stable grouping key.

Add rejection cases for conflicting albums, conflicting authors, and separate M4B files.

- [ ] **Step 2: Run the grouping fixture and confirm failure**

Run: `Set-Location server; go test ./internal/grouping -run 'TestInferTree.*Arcanum|TestInferTree.*Collection' -count=1`

Expected: FAIL with one inferred book per counter title or filename.

- [ ] **Step 3: Implement sequence-backed folder grouping**

Before filename grouping, detect files whose descriptive identity is empty and whose numeric track evidence covers one coherent sequence. Return:

```go
treeCluster{
    audio: audio,
    reason: "folder_default",
    review: catalog.ReviewUnreviewed,
}
```

Keep existing distinct-identity handling when consistent non-placeholder album and author pairs exist. Do not combine multiple standalone M4B files on sequence evidence alone.

- [ ] **Step 4: Run grouping and staged catalog tests**

Run: `Set-Location server; go test ./internal/grouping ./internal/worker -run 'TestInferTree|Test.*Inventory.*Grouping|TestRegroup' -count=1`

Expected: PASS and existing mixed-folder splits remain intact.

- [ ] **Step 5: Commit**

```powershell
git add -- server/internal/grouping/evidence.go server/internal/grouping/evidence_test.go server/internal/worker/inventory_grouping_test.go
git commit -m "fix: group numbered audiobook collections"
```

### Task 3: Block meaningless enrichment and improve title-only lookup

**Files:**
- Modify: `server/internal/worker/worker.go`
- Modify: `server/internal/metadata/openlibrary.go`
- Test: `server/internal/worker/worker_test.go`
- Test: `server/internal/metadata/openlibrary_test.go`

**Interfaces:**
- Produces: `meaningfulEnrichmentTitle(string) bool`.
- Produces: `providerTitle(string) string`, removing terminal audiobook-edition suffixes.

- [ ] **Step 1: Write failing worker and HTTP request tests**

Assert queueing skips `- 01/16`, punctuation-only values, and `Track 2 of 9`. Assert `Oathbringer (Unabridged)` searches Open Library with `title=Oathbringer` and `author=Brandon Sanderson`. Assert title-only `Arcanum Unbounded` uses `q=Arcanum Unbounded` without empty `author`, `title`, or `isbn` parameters.

- [ ] **Step 2: Run focused tests and confirm failure**

Run: `Set-Location server; go test ./internal/worker ./internal/metadata -run 'Test.*Enrich|TestOpenLibrary.*Query' -count=1`

Expected: FAIL because placeholder books are queued and title-only requests use the title field.

- [ ] **Step 3: Implement eligibility and request normalization**

Use the same placeholder semantics at the queue boundary and inside `EnrichBookHandler` for defense in depth. Normalize terminal suffixes matching `(?i)\s*\((?:unabridged|abridged|audiobook)\)\s*$` only in `inferredFromBook` or the provider query adapter.

Build Open Library parameters in this order:

```go
switch {
case isbn != "": parameters.Set("isbn", isbn)
case title != "" && author != "":
    parameters.Set("title", title)
    parameters.Set("author", author)
case title != "": parameters.Set("q", title)
default: return nil, ErrInvalidQuery
}
```

- [ ] **Step 4: Run worker and metadata packages**

Run: `Set-Location server; go test ./internal/worker ./internal/metadata -count=1`

Expected: PASS, including confirmed-match cover publication tests.

- [ ] **Step 5: Commit**

```powershell
git add -- server/internal/worker/worker.go server/internal/worker/worker_test.go server/internal/metadata/openlibrary.go server/internal/metadata/openlibrary_test.go
git commit -m "fix: send meaningful enrichment queries"
```

### Task 4: Select and serve local cover candidates

**Files:**
- Create: `server/internal/grouping/covers.go`
- Create: `server/internal/grouping/covers_test.go`
- Modify: `server/internal/grouping/evidence.go`
- Modify: `server/internal/httpapi/books.go`
- Test: `server/internal/httpapi/books_test.go`

**Interfaces:**
- Produces: `coversForCluster(tree Tree, cluster treeCluster, candidates []catalog.SourceFile) []catalog.SourceFile`.
- Produces: `currentBookCover(resolver, detail catalog.BookDetail) (string, error)` using assigned cover files first.

- [ ] **Step 1: Write failing selection and endpoint tests**

Use real small JPEG and PNG fixtures. Assert a title-named portrait beside one M4B is selected, negative names lose, tiny and landscape images do not win, ambiguous equal candidates remain unassigned, and an assigned Oathbringer JPEG returns HTTP 200.

- [ ] **Step 2: Run focused tests and confirm failure**

Run: `Set-Location server; go test ./internal/grouping ./internal/httpapi -run 'Test.*Cover|Test.*Artwork' -count=1`

Expected: FAIL because grouping assigns every recognized cover and the endpoint ignores assignments.

- [ ] **Step 3: Implement deterministic candidate scoring**

Score standard stems, normalized title agreement, author agreement, dimensions in `SourceFile.Media`, portrait ratio, and proximity. Penalize `back`, `map`, `booklet`, `disc`, `logo`, `thumb`, numbered alternatives, small images, and landscape ratios. Return one candidate at score 70 with a 15-point lead; otherwise return none.

Pass the selected candidates into `inferredTreeBook`. Update the endpoint to locate a `FileRoleCover` assignment in `BookDetail.Files`, validate its root and folder, then use the migration fallback directory scan.

- [ ] **Step 4: Run cover, grouping, HTTP, and worker tests**

Run: `Set-Location server; go test ./internal/grouping ./internal/httpapi ./internal/worker ./internal/covers -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add -- server/internal/grouping/covers.go server/internal/grouping/covers_test.go server/internal/grouping/evidence.go server/internal/httpapi/books.go server/internal/httpapi/books_test.go
git commit -m "fix: discover title named local covers"
```

### Task 5: Verify the server repair

**Files:**
- Modify only if verification exposes a defect in the files above.

- [ ] **Step 1: Format modified Go files**

Run: `Set-Location server; gofmt -w internal/scanner internal/grouping internal/metadata internal/worker internal/httpapi`

- [ ] **Step 2: Run the complete server suite**

Run: `Set-Location server; go test ./... -count=1`

Expected: PASS.

- [ ] **Step 3: Inspect the diff**

Run: `git diff --check HEAD~4..HEAD; git status --short`

Expected: no whitespace errors and no unrelated files staged.

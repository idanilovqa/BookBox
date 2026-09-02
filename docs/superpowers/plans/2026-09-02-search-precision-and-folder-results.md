# Search Precision and Folder Results Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reject unrelated fuzzy catalog matches and return one folder-first raw result per audiobook identity.

**Architecture:** PostgreSQL remains responsible for candidate filtering, resolution, ranking, and cursor pagination. Shared-folder exceptions use existing projected `book_id` evidence and audio extensions, then deduplicate by audiobook before pagination.

**Tech Stack:** Go, PostgreSQL 18, pg_trgm, pgx, Go tests.

## Global Constraints

- Exact normalized substring matches keep their current behavior.
- Fuzzy matches require `word_similarity >= 0.40` and `strict_word_similarity >= 0.25` against `search_terms`.
- Nested files appear only for catalog-linked audiobooks in shared folders; root files remain files.
- Grouping and deduplication occur before ranking and cursor pagination.

---

### Task 1: Centralize the fuzzy SQL predicate

**Files:**
- Modify: `server/internal/catalog/search_postgres.go`
- Test: `server/internal/catalog/search_postgres_test.go`

**Interfaces:**
- Produces: `fuzzyTextPredicate(queryPlaceholder string) string`
- Produces: `fuzzyTextScore(queryPlaceholder string) string`

- [ ] **Step 1: Write failing SQL-shape and behavior tests**

Add assertions that raw and audiobook page builders and cursor resolvers contain:

```go
"word_similarity($", "search_terms)>=0.4", "strict_word_similarity($", "search_terms)>=0.25"
```

Add an integration fixture with `Brandon Sanderson`, `Amazing Ban Clan Historian`, and `China's Buddhist Monks`. Assert `sanderson` returns only the Sanderson record and `sandyrsen` still returns it.

- [ ] **Step 2: Run the focused test and confirm failure**

Run: `Set-Location server; go test ./internal/catalog -run 'Test.*Fuzzy|TestBuild.*Search' -count=1`

Expected: FAIL because the SQL uses `word_similarity(...,search_compact)>=0.3`.

- [ ] **Step 3: Add one predicate and score implementation**

Use these expressions in all four query paths:

```go
func fuzzyTextPredicate(query string) string {
    return `(strpos(search_compact,` + query + `)>0 or (` +
        `word_similarity(` + query + `,search_terms)>=0.4 and ` +
        `strict_word_similarity(` + query + `,search_terms)>=0.25))`
}

func fuzzyTextScore(query string) string {
    return `word_similarity(` + query + `,search_terms)::float8`
}
```

Keep `fuzzyGramCondition` ahead of the predicate so PostgreSQL can use the GIN candidate set.

- [ ] **Step 4: Run focused and performance tests**

Run: `Set-Location server; go test ./internal/catalog -run 'Test.*Fuzzy|TestBuild.*Search|TestSearchPerformance' -count=1`

Expected: PASS, including query-plan bounds.

- [ ] **Step 5: Commit**

```powershell
git add -- server/internal/catalog/search_postgres.go server/internal/catalog/search_postgres_test.go server/internal/catalog/search_performance_test.go
git commit -m "fix: tighten fuzzy catalog search"
```

### Task 2: Resolve raw matches by folder or audiobook identity

**Files:**
- Modify: `server/internal/catalog/search_postgres.go`
- Test: `server/internal/catalog/search_postgres_test.go`
- Test: `server/internal/catalog/search_projection_behavior_test.go`

**Interfaces:**
- Consumes: projected file and folder `book_id` values.
- Produces: raw results containing folders, root files, or one representative audio file for a shared-folder audiobook.

- [ ] **Step 1: Add failing raw-search fixtures**

Create fixtures for a dedicated folder, two linked books in one shared folder, a multi-file linked book in that shared folder, and a missing parent projection document. Assert:

```go
want := map[string]SearchEntityKind{
    "Dedicated Book": SearchEntityFolder,
    "First Book.m4b": SearchEntityFile,
    "Second Book Part 01.mp3": SearchEntityFile,
    "root-book.m4b": SearchEntityFile,
}
```

Assert only one file represents `Second Book` and a missing parent yields `ErrInvalidData`.

- [ ] **Step 2: Run the failing raw-search tests**

Run: `Set-Location server; go test ./internal/catalog -run 'TestSearchLibrary.*Raw|TestBuildRaw' -count=1`

Expected: FAIL because every nested match resolves to its parent document.

- [ ] **Step 3: Replace `resolved_targets` with evidence-based resolution**

Build CTEs that load the immediate parent and assign a resolution key:

```sql
case
  when m.entity_kind='file' and strpos(m.relative_path,'/')=0 then 'file:'||m.document_id
  when m.entity_kind='file'
   and m.book_id is not null
   and parent.book_id is distinct from m.book_id
   and lower(m.relative_path) ~ '\.(m4b|m4a|mp3|aac|flac|ogg|opus|wav)$'
    then 'book:'||m.book_id
  else 'document:'||parent.document_id
end as resolution_key
```

Choose one matched audio document for a `book:` key, use the parent document for a dedicated folder, reject a nested file with no parent, and perform `distinct on (root_id,resolution_key)` before pagination.

- [ ] **Step 4: Run raw search and cursor tests**

Run: `Set-Location server; go test ./internal/catalog -run 'TestSearchLibrary.*Raw|TestBuildRaw|Test.*Cursor' -count=1`

Expected: PASS with stable result identities across pages.

- [ ] **Step 5: Commit**

```powershell
git add -- server/internal/catalog/search_postgres.go server/internal/catalog/search_postgres_test.go server/internal/catalog/search_projection_behavior_test.go
git commit -m "fix: prefer folders in raw search"
```

### Task 3: Verify the search package

**Files:**
- Modify only if verification reveals a defect in the files above.

- [ ] **Step 1: Format the Go files**

Run: `Set-Location server; gofmt -w internal/catalog/search_postgres.go internal/catalog/search_postgres_test.go internal/catalog/search_projection_behavior_test.go`

- [ ] **Step 2: Run the complete catalog package**

Run: `Set-Location server; go test ./internal/catalog -count=1`

Expected: PASS.

- [ ] **Step 3: Inspect the final diff**

Run: `git diff --check HEAD~2..HEAD; git status --short`

Expected: no whitespace errors and only unrelated pre-existing workspace changes remain.


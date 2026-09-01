# Folder-First Raw Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Return one parent-folder result for matching files while retaining matching files directly under the selected root.

**Architecture:** PostgreSQL resolves file matches to their immediate folder document before deduplication, ordering, and cursor pagination. The API allows file results only at root, while Android keeps its current open-and-highlight behavior for those files.

**Tech Stack:** Go 1.26, PostgreSQL 18, pgx/v5, Kotlin, Jetpack Compose.

## Global Constraints

- Search performs no request-time filesystem traversal.
- Root-level matching files remain file results.
- Non-root file matches resolve before pagination.
- Responses and logs contain no absolute paths.

---

### Task 1: Resolve raw matches before pagination

**Files:**
- Modify: `server/internal/catalog/search_postgres.go`
- Test: `server/internal/catalog/search_postgres_test.go`
- Test: `server/internal/catalog/search_performance_test.go`

**Interfaces:**
- Keeps `buildRawSearchStatement(SearchQuery, []string, string, *searchCursor) rawSearchStatement` stable.
- Produces cursors from resolved result documents.

- [ ] **Step 1: Write failing repository tests**

Add cases proving two file matches under `Author/Book` return that folder once, a direct folder match merges with it, and root `loose.m4b` remains a file.

```go
if got := rawPaths(page); !slices.Equal(got, []string{"Author/Book", "loose.m4b"}) {
    t.Fatalf("raw paths = %v", got)
}
```

- [ ] **Step 2: Run `go test ./internal/catalog -run 'RawSearch|BuildRawSearch' -count=1`**

Expected: FAIL because nested files remain separate results.

- [ ] **Step 3: Refactor the SQL into `matched`, `resolved`, and `ranked` CTEs**

For file rows containing `/`, join the folder document at `regexp_replace(relative_path, '/[^/]+$', '')`. Keep folders and files without `/`. Group by resolved identity, retain the best rank/score, then apply cursor and limit.

```sql
case when matched.entity_kind='file' and strpos(matched.relative_path,'/')>0
     then parent.document_id else matched.document_id end
```

- [ ] **Step 4: Run `go test ./internal/catalog -run 'RawSearch|BuildRawSearch|SearchPerformance' -count=1`**

Expected: PASS with indexed candidate selection intact.

- [ ] **Step 5: Commit**

```powershell
git add server/internal/catalog/search_postgres.go server/internal/catalog/search_postgres_test.go server/internal/catalog/search_performance_test.go
git commit -m "fix(search): collapse raw file matches to folders"
```

### Task 2: Enforce API and Android contracts

**Files:**
- Modify: `server/internal/httpapi/library_search.go`
- Modify: `server/internal/httpapi/library_search_test.go`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersScreen.kt`
- Modify: `android/app/src/androidTest/java/com/rendref/bookbox/feature/folders/FoldersScreenTest.kt`

**Interfaces:**
- Keeps `shapeRawEntries` and `RawSearchEntryDto` stable.
- Rejects nested file responses as invalid projection output.

- [ ] **Step 1: Write failing API and Compose tests**

Require nested file documents to fail shaping and empty copy to read `No matching folders or root files.`

- [ ] **Step 2: Run focused Go tests and the FoldersScreen instrumentation test; verify RED**
- [ ] **Step 3: Reject file entries whose relative path contains `/`, and update the empty copy**
- [ ] **Step 4: Re-run focused tests; verify PASS**
- [ ] **Step 5: Commit**

```powershell
git add server/internal/httpapi/library_search.go server/internal/httpapi/library_search_test.go android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersScreen.kt android/app/src/androidTest/java/com/rendref/bookbox/feature/folders/FoldersScreenTest.kt
git commit -m "fix(raw): enforce folder-first search results"
```

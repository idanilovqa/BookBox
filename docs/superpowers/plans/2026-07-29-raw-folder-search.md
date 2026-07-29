# Raw Folder Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add symbol-insensitive, recursive raw-folder path search to the Android app and its server API.

**Architecture:** The Go server walks the selected configured root and returns at most 100 sorted folder matches. Android sends debounced queries through the existing browse network/repository boundary, keeps search state in `FoldersViewModel`, and switches `FoldersScreen` between directory browsing and search results.

**Tech Stack:** Go `net/http` and `filepath.WalkDir`; Kotlin, coroutines, StateFlow, OkHttp, and Jetpack Compose.

## Global Constraints

- Search the full relative folder path under the selected root.
- Lowercase Unicode letters, retain Unicode letters and digits, and remove other characters before substring matching.
- Return folders only and do not follow directory symlinks.
- Return no more than 100 results.
- Keep all source operations read-only.
- Debounce Android queries by 300 milliseconds and reject stale responses.

---

### Task 1: Server folder search

**Files:**
- Modify: `server/internal/library/browser.go`
- Modify: `server/internal/library/browser_test.go`
- Modify: `server/internal/httpapi/browse.go`
- Modify: `server/internal/httpapi/browse_test.go`
- Modify: `server/internal/httpapi/api.go`

**Interfaces:**
- Produces: `func (b *Browser) Search(ctx context.Context, rootID, query string) (SearchResult, error)`
- Produces: `type SearchResult struct { RootID string; Query string; Entries []Entry }`
- Produces: authenticated `GET /v1/browse/search?root=<id>&q=<query>`

- [ ] **Step 1: Write failing library tests**

Add tests that create `Author/Black.Box`, `Blacksmith`, and files, then assert:

```go
got, err := browser.Search(context.Background(), "audiobooks", "black box")
if err != nil { t.Fatal(err) }
if paths := entryPaths(got.Entries); !slices.Equal(paths, []string{"Author/Black.Box"}) {
    t.Fatalf("paths = %v", paths)
}
```

Also test full-path matches, symbol-only queries, sorted truncation at 100, cancellation, and a directory symlink whose target must not appear.

- [ ] **Step 2: Verify the library tests fail**

Run: `go test ./internal/library`

Expected: FAIL because `Browser.Search` and `SearchResult` do not exist.

- [ ] **Step 3: Implement the minimal library search**

Add `normalizeSearchText`, `SearchResult`, and `Browser.Search`. Resolve the root with `Resolver.Resolve(rootID, "")`, walk with `filepath.WalkDir`, skip the root itself, skip symlink entries, check `ctx.Err()`, normalize slash-separated relative paths, collect folder `Entry` values from `DirEntry.Info()`, sort by lowercase relative path plus original relative path, and truncate to 100.

- [ ] **Step 4: Verify the library tests pass**

Run: `go test ./internal/library`

Expected: PASS.

- [ ] **Step 5: Write failing HTTP tests**

Add an endpoint test:

```go
response := serveRequest(api, "/v1/browse/search?root=audiobooks&q=black%20box")
if response.Code != http.StatusOK { t.Fatalf("status = %d", response.Code) }
var result library.SearchResult
if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil { t.Fatal(err) }
```

Assert the route requires the existing bearer-token wrapper, decodes the query once, returns safe errors, and exposes no absolute paths.

- [ ] **Step 6: Verify the HTTP tests fail**

Run: `go test ./internal/httpapi`

Expected: FAIL because `/v1/browse/search` is not registered.

- [ ] **Step 7: Add the HTTP route and handler**

Register `GET /v1/browse/search` before `/v1/browse`, parse `root` and `q` with `url.ParseQuery`, call `Browser.Search`, and serialize `SearchResult` with the existing helpers.

- [ ] **Step 8: Verify server tests**

Run: `go test ./internal/library ./internal/httpapi`

Expected: PASS.

### Task 2: Android network and repository support

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/model/ApiModels.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/network/BrowseApi.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/repository/BrowseRepository.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/core/model/ApiModelsTest.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/core/repository/BrowseRepositoryTest.kt`
- Modify fake `BrowseSource` implementations found by `rg "BrowseSource"`

**Interfaces:**
- Produces: `FolderSearchDto(rootId: String, query: String, entries: List<BrowseEntryDto>)`
- Produces: `suspend fun BrowseApi.searchFolders(rootId: String, query: String): FolderSearchDto`
- Produces: `fun BrowseSource.searchFolders(rootId: String, query: String): Flow<FolderSearchDto>`

- [ ] **Step 1: Write failing model and repository tests**

Decode a representative `FolderSearchDto`. In the MockWebServer repository test, call:

```kotlin
val result = repository.searchFolders("audiobooks", "black box").first()
assertEquals("/v1/browse/search?root=audiobooks&q=black%20box", server.takeRequest().target)
assertEquals("Author/Black.Box", result.entries.single().relativePath)
```

Add invalid-response cases for the wrong root/query, files in results, malformed paths, duplicate paths, and more than 100 entries.

- [ ] **Step 2: Verify Android tests fail**

Run: `.\gradlew.bat :app:testDebugUnitTest --tests "*ApiModelsTest" --tests "*BrowseRepositoryTest"`

Expected: FAIL because the DTO and search interfaces do not exist.

- [ ] **Step 3: Implement the DTO, API validation, and repository flow**

Add the interfaces above. Validate the root and nonblank query, add both query parameters with OkHttp, require response identity to match the request, and require 100 or fewer unique folder entries with valid relative paths.

- [ ] **Step 4: Update test fakes**

Every `BrowseSource` fake returns `flowOf(FolderSearchDto(rootId, query, emptyList()))` unless its test needs custom results.

- [ ] **Step 5: Verify Android data tests**

Run: `.\gradlew.bat :app:testDebugUnitTest --tests "*ApiModelsTest" --tests "*BrowseRepositoryTest"`

Expected: PASS.

### Task 3: Android search state and UI

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersViewModel.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/folders/FoldersScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/ProductionBookBoxApp.kt`
- Modify: `android/app/src/test/java/com/rendref/bookbox/feature/folders/FoldersViewModelTest.kt`
- Modify: `android/app/src/androidTest/java/com/rendref/bookbox/feature/folders/FoldersScreenTest.kt`

**Interfaces:**
- Produces: `FoldersViewModel.updateSearchQuery(query: String)`
- Produces: `FoldersViewModel.openSearchResult(relativePath: String)`
- Produces: `FolderAction.UpdateSearchQuery(query: String)` and `FolderAction.OpenSearchResult(relativePath: String)`

- [ ] **Step 1: Write failing view-model tests**

Test that `updateSearchQuery("black box")` makes no request at 299 ms, sends one request at 300 ms, exposes results, and ignores a stale delayed request. Test clearing, root changes, invalid symbol-only queries, errors, and `openSearchResult`.

- [ ] **Step 2: Verify view-model tests fail**

Run: `.\gradlew.bat :app:testDebugUnitTest --tests "*FoldersViewModelTest"`

Expected: FAIL because search state and methods do not exist.

- [ ] **Step 3: Implement view-model search state**

Add `searchQuery`, `searchResults`, `isSearching`, and `searchErrorMessage` to `FoldersState`. Store a `Job?`, cancel it on each edit, wait `300.milliseconds`, call `browse.searchFolders`, compare query and root snapshots before applying results, and clear search before loading a selected result.

- [ ] **Step 4: Verify view-model tests pass**

Run: `.\gradlew.bat :app:testDebugUnitTest --tests "*FoldersViewModelTest"`

Expected: PASS.

- [ ] **Step 5: Write failing Compose tests**

Enter `black box` in the `Search raw folders` field, assert that the callback action contains the query, render a result state, assert the full path, tap it, and assert `OpenSearchResult`. Add progress, `No matching folders.`, and error-state assertions.

- [ ] **Step 6: Add the Compose search presentation**

Add a full-width `OutlinedTextField`. When `searchQuery` contains a letter or digit, render the progress, error, empty state, or a `LazyColumn` of folder-only result rows showing both name and relative path. Keep normal breadcrumbs visible and route the two new actions in `ProductionBookBoxApp`.

- [ ] **Step 7: Compile instrumentation tests and run affected unit tests**

Run: `.\gradlew.bat :app:testDebugUnitTest :app:compileDebugAndroidTestKotlin`

Expected: BUILD SUCCESSFUL.

- [ ] **Step 8: Run server tests and build the APK**

Run in `server`: `go test ./...`

Run in `android`: `.\gradlew.bat :app:assembleDebug`

Expected: all tests pass and `android/app/build/outputs/apk/debug/app-debug.apk` exists.

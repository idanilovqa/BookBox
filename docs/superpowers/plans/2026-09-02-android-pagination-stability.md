# Android Pagination Stability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop compact and grid lists from retrying a failed page cursor in a loop and provide a stable manual retry action.

**Architecture:** `LibraryViewModel` owns a failed-cursor latch and exposes a separate pagination error. Compose observes that state before requesting another page and renders one fixed-height footer in either layout.

**Tech Stack:** Kotlin, coroutines and Flow, Jetpack Compose, JUnit, Android Compose tests.

## Global Constraints

- One automatic request is allowed for each cursor.
- Failure preserves loaded groups and the next cursor.
- Retry requires a user action.
- Query and listing changes clear the failure latch.

---

### Task 1: Latch failed page cursors in the view model

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryViewModel.kt`
- Test: `android/app/src/test/java/com/rendref/bookbox/feature/library/LibraryViewModelTest.kt`

**Interfaces:**
- Produces: `LibraryState.paginationError: String?`.
- Produces: `LibraryState.canLoadNextPage: Boolean`.
- Produces: `LibraryViewModel.retryNextPage()`.

- [ ] **Step 1: Write failing state-machine tests**

Add tests that call `loadNextPage()` after an `IOException` several times and assert one cursor request. Assert `retryNextPage()` permits one more request, success clears the error, and `setSearchText`, filters, sort, refresh updates, and initial replacement loads clear the latch.

- [ ] **Step 2: Run the unit tests and confirm failure**

Run: `Set-Location android; .\gradlew.bat testDebugUnitTest --tests com.rendref.bookbox.feature.library.LibraryViewModelTest`

Expected: FAIL because no pagination latch or retry method exists.

- [ ] **Step 3: Implement the cursor latch**

Add `paginationError` to the constructor and expose `canLoadNextPage` as a derived property so `copy` cannot retain a stale value:

```kotlin
val paginationError: String? = null,

// LibraryState body
val canLoadNextPage: Boolean
    get() = nextCursor != null && paginationError == null
```

Track `private var failedPaginationCursor: String? = null`. `loadNextPage()` returns when the current cursor equals the failed cursor. Both failure branches set the cursor and `paginationError = "Couldn't load more"`, leaving `errorMessage` for initial-load failures. `retryNextPage()` clears the latch and error, then calls `loadNextPage()`.

Reset the latch in `updateQuery` and every accepted replacement-result state.

- [ ] **Step 4: Run the unit tests**

Run: `Set-Location android; .\gradlew.bat testDebugUnitTest --tests com.rendref.bookbox.feature.library.LibraryViewModelTest`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add -- android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryViewModel.kt android/app/src/test/java/com/rendref/bookbox/feature/library/LibraryViewModelTest.kt
git commit -m "fix: latch failed catalog pages"
```

### Task 2: Render one stable retry footer

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/ProductionBookBoxApp.kt`
- Test: `android/app/src/androidTest/java/com/rendref/bookbox/feature/library/LibraryScreenTest.kt`

**Interfaces:**
- Consumes: `LibraryState.paginationError`, `LibraryState.canLoadNextPage`, `LibraryViewModel.retryNextPage()`.
- Produces: `PaginationFooter(isLoading: Boolean, error: String?, onRetry: () -> Unit)`.

- [ ] **Step 1: Write failing Compose tests**

Assert both layouts show `Couldn't load more` and a `Retry` button, tapping Retry invokes the callback once, and the near-end effect does not invoke automatic loading while `canLoadNextPage` is false.

- [ ] **Step 2: Run the instrumentation test compilation**

Run: `Set-Location android; .\gradlew.bat compileDebugAndroidTestKotlin`

Expected: FAIL because the screen has no retry callback or footer.

- [ ] **Step 3: Implement the footer and wire retry**

Add `onRetryNextPage` to `LibraryScreen`. Gate the effect with `state.canLoadNextPage`. Render a full-width fixed-height footer in the compact list and a max-span footer in the grid:

```kotlin
@Composable
private fun PaginationFooter(isLoading: Boolean, error: String?, onRetry: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().height(64.dp),
        horizontalArrangement = Arrangement.Center,
        verticalAlignment = Alignment.CenterVertically,
    ) {
        when {
            isLoading -> CircularProgressIndicator()
            error != null -> {
                Text(error)
                TextButton(onClick = onRetry) { Text("Retry") }
            }
        }
    }
}
```

Wire `onRetryNextPage = library::retryNextPage` in `ProductionBookBoxApp`.

- [ ] **Step 4: Run unit and Android test compilation**

Run: `Set-Location android; .\gradlew.bat testDebugUnitTest compileDebugAndroidTestKotlin`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add -- android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryScreen.kt android/app/src/main/java/com/rendref/bookbox/ProductionBookBoxApp.kt android/app/src/androidTest/java/com/rendref/bookbox/feature/library/LibraryScreenTest.kt
git commit -m "fix: show stable pagination retry"
```

### Task 3: Verify and build Android

**Files:**
- Modify only if verification exposes a defect in the files above.

- [ ] **Step 1: Run Android static checks and unit tests**

Run: `Set-Location android; .\gradlew.bat testDebugUnitTest lintDebug`

Expected: PASS.

- [ ] **Step 2: Build the release artifact**

Run: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File deploy\scripts\build-release.ps1`

Expected: versioned APK artifacts are produced by the repository release script.

- [ ] **Step 3: Inspect the final workspace**

Run: `git diff --check; git status --short`

Expected: no whitespace errors and only known user or generated files remain uncommitted.

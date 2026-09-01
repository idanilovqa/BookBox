# Compact Audiobook View Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a persisted dense full-width Audiobooks view with small left covers and series name/position.

**Architecture:** A DataStore-backed preference feeds `LibraryViewModel`. `LibraryScreen` switches between the current adaptive grid and a `LazyColumn` of compact pills while sharing paging, filters, selection, and duplicate groups.

**Tech Stack:** Kotlin, Jetpack Compose Material 3, Preferences DataStore, Coil 3.

## Global Constraints

- Grid is the first-run default.
- Compact pills are 76 to 84 dp tall with 6 dp spacing and 48 by 64 dp covers.
- Series uses `labelSmall` and `Series name · position`.
- Grid and Compact retain independent scroll positions.

---

### Task 1: Add series position to cards

**Files:**
- Modify: `server/internal/catalog/search_model.go`
- Modify: `server/internal/catalog/search_projection.go`
- Modify: `server/internal/httpapi/books.go`
- Modify: `android/app/src/main/java/com/rendref/bookbox/core/model/ApiModels.kt`
- Test: corresponding projection, API, and Kotlin model tests.

**Interfaces:**
- Adds optional `seriesPosition` to `SearchDocument`, `BookCardResponse`, and `BookCardDto`.

- [ ] **Step 1: Write failing serialization and projection tests for `seriesPosition: "0.5"`**
- [ ] **Step 2: Run focused Go and Kotlin model tests; verify RED**
- [ ] **Step 3: Thread the optional string through projection, shaping, and DTO validation**
- [ ] **Step 4: Re-run tests; verify PASS**
- [ ] **Step 5: Commit `feat(api): include series position on audiobook cards`**

### Task 2: Persist view mode

**Files:**
- Create: `android/app/src/main/java/com/rendref/bookbox/core/storage/LibraryPreferences.kt`
- Create: `android/app/src/test/java/com/rendref/bookbox/core/storage/LibraryPreferencesTest.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/BookBoxRuntime.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryViewModel.kt`

**Interfaces:**
- Produces `LibraryViewMode { GRID, COMPACT }`, `Flow<LibraryViewMode>`, and `setViewMode`.

- [ ] **Step 1: Write failing DataStore tests for GRID default and COMPACT persistence**
- [ ] **Step 2: Run unit tests; verify RED**
- [ ] **Step 3: Implement preferences and add view mode plus separate grid/list positions to `LibraryState`**
- [ ] **Step 4: Run storage/view-model tests; verify PASS**
- [ ] **Step 5: Commit `feat(android): persist audiobook view mode`**

### Task 3: Build the compact pill

**Files:**
- Create: `android/app/src/main/java/com/rendref/bookbox/feature/library/CompactAudiobookCard.kt`
- Modify: `android/app/src/androidTest/java/com/rendref/bookbox/feature/library/LibraryScreenTest.kt`

**Interfaces:**
- Produces `CompactAudiobookCard(BookCardDto, Boolean, Boolean, (LibraryAction) -> Unit, Modifier)`.

- [ ] **Step 1: Write failing Compose assertions for title, small author, `Series · 0.5`, cover, checkbox, raw action, and maximum height**
- [ ] **Step 2: Run the focused instrumentation test; verify RED**
- [ ] **Step 3: Implement the rounded full-width row with 48 by 64 dp cover, one-line text, `labelSmall` series, inline badges, checkbox, and icon action**
- [ ] **Step 4: Re-run the test; verify PASS**
- [ ] **Step 5: Commit `feat(android): add compact audiobook pills`**

### Task 4: Switch layouts and preserve behavior

**Files:**
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryScreen.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/ProductionBookBoxApp.kt`
- Modify: `android/app/src/main/java/com/rendref/bookbox/feature/library/LibraryViewModel.kt`
- Test: `android/app/src/androidTest/java/com/rendref/bookbox/feature/library/LibraryScreenTest.kt`
- Test: `android/app/src/test/java/com/rendref/bookbox/feature/library/LibraryViewModelTest.kt`

**Interfaces:**
- Adds `onViewModeChange` and `onListPositionChange` callbacks to `LibraryScreen`.

- [ ] **Step 1: Write failing tests for toggle semantics, duplicate rendering, independent positions, and paging in both modes**
- [ ] **Step 2: Run focused tests; verify RED**
- [ ] **Step 3: Add Grid/Compact control and render either the existing grid or a 6 dp-spaced `LazyColumn`**
- [ ] **Step 4: Wire callbacks and preserve filters, loading, selection, actions, and pagination**
- [ ] **Step 5: Run tests and commit `feat(android): switch audiobook grid and compact views`**

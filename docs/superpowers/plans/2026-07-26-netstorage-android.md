# netStorage Android Client Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Android 16 app that remembers one pinned server, browses books or folders, reviews catalog matches, and copies selected content through verified resumable background jobs.

**Architecture:** A single Compose app uses feature packages, immutable screen state, repositories over OkHttp, DataStore for non-secret settings, Android Keystore for the refresh token, Storage Access Framework for destination files, and JobScheduler UIDT jobs for long transfers. The app contains no SQLite or Room dependency.

**Tech Stack:** Android API 36, AGP 9.2.0, Gradle 9.4.1, JDK 17, Kotlin 2.3.21, Compose BOM 2026.06.00, Navigation3 1.1.4, Lifecycle 2.11.0, DataStore 1.2.1, OkHttp 5.3.0, Coil 3.5.0, kotlinx.serialization, coroutines.

## Global Constraints

- Complete the server transfer and catalog plans first.
- Namespace and application ID: `com.rendref.netstorage`.
- `minSdk`, `compileSdk`, and `targetSdk` are API 36.
- The app enters the server address once, pins the approved certificate, and signs in without storing the password.
- The app saves no secret or certificate pin in Android backup.
- The app requests no broad storage permission.
- The app contains no upload, source rename, source move, source delete, tag edit, or source-folder creation action.
- PostgreSQL is the only application database; Android uses no SQLite or Room.
- Raw copies preserve selected folder names.
- Normal audiobook copies preserve the source folder name; split books use the `.m4b` filename stem, then detected title.
- The app never overwrites a destination conflict without Replace, Keep both, or Skip.
- Android does not edit copied tags or inject a cover.

---

### Task 1: Android Project and Contract Models

**Files:**
- Create: `android/settings.gradle.kts`
- Create: `android/build.gradle.kts`
- Create: `android/gradle.properties`
- Create: `android/gradle/wrapper/gradle-wrapper.properties`
- Create: `android/gradlew`
- Create: `android/gradlew.bat`
- Create: `android/app/build.gradle.kts`
- Create: `android/app/src/main/AndroidManifest.xml`
- Create: `android/app/src/main/java/com/rendref/netstorage/NetStorageApp.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/MainActivity.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/model/ApiModels.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/model/ApiModelsTest.kt`
- Create: `android/app/src/main/res/values/strings.xml`
- Create: `android/app/src/main/res/values/themes.xml`
- Modify: `.gitignore`

**Interfaces:**
- Produces: a buildable API 36 application
- Produces: serializable `RootDto`, `BrowseDto`, `BookCardDto`, `BookDetailDto`, `ManifestDto`, and `ApiErrorDto`

- [ ] **Step 1: Create a failing serialization test**

```kotlin
class ApiModelsTest {
    @Test fun `book card decodes approved server contract`() {
        val card = Json { ignoreUnknownKeys = true }.decodeFromString<BookCardDto>(
            """{"id":"b1","title":"Dune","authors":["Frank Herbert"],"publicationYear":1965,"genres":["Science Fiction"],"reviewState":"confirmed","sourceFolder":"Dune"}"""
        )
        assertEquals("Dune", card.title)
        assertEquals(1965, card.publicationYear)
        assertEquals(listOf("Frank Herbert"), card.authors)
    }
}
```

- [ ] **Step 2: Add build configuration and verify RED**

Use:

```kotlin
plugins {
    id("com.android.application") version "9.2.0" apply false
    id("org.jetbrains.kotlin.plugin.compose") version "2.3.21" apply false
    id("org.jetbrains.kotlin.plugin.serialization") version "2.3.21" apply false
}
```

Set Gradle distribution to `gradle-9.4.1-bin.zip`. In the app module apply `com.android.application`, Compose, and serialization plugins. Set namespace/application ID, all SDK levels to 36, Java toolchain 17, Compose true, unit tests with Android resources, and instrumentation runner.

Dependencies must pin:

```kotlin
implementation(platform("androidx.compose:compose-bom:2026.06.00"))
implementation("androidx.activity:activity-compose:1.13.0")
implementation("androidx.lifecycle:lifecycle-runtime-compose:2.11.0")
implementation("androidx.lifecycle:lifecycle-viewmodel-compose:2.11.0")
implementation("androidx.navigation3:navigation3-runtime:1.1.4")
implementation("androidx.navigation3:navigation3-ui:1.1.4")
implementation("androidx.datastore:datastore-preferences:1.2.1")
implementation("com.squareup.okhttp3:okhttp:5.3.0")
implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.9.0")
implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.10.2")
implementation("io.coil-kt.coil3:coil-compose:3.5.0")
implementation("io.coil-kt.coil3:coil-network-okhttp:3.5.0")
testImplementation("junit:junit:4.13.2")
testImplementation("com.squareup.okhttp3:mockwebserver3:5.3.0")
androidTestImplementation(platform("androidx.compose:compose-bom:2026.06.00"))
androidTestImplementation("androidx.compose.ui:ui-test-junit4")
```

Run: `cd android; .\gradlew.bat testDebugUnitTest --tests "*ApiModelsTest"`

Expected: FAIL because `BookCardDto` does not exist.

- [ ] **Step 3: Implement immutable API models**

```kotlin
@Serializable data class BookCardDto(
    val id: String,
    val title: String,
    val authors: List<String>,
    val publicationYear: Int? = null,
    val genres: List<String> = emptyList(),
    val series: String? = null,
    val narrators: List<String> = emptyList(),
    val coverUrl: String? = null,
    val reviewState: String,
    val sourceFolder: String,
)
@Serializable data class ManifestFileDto(
    val fileId: String,
    val sourcePath: String,
    val destinationPath: String,
    val size: Long,
    val modifiedAt: String,
    val etag: String,
    val sha256: String,
)
```

Define the remaining DTOs from the server responses with defaults only for optional fields. Keep API DTOs out of composables.

- [ ] **Step 4: Add an edge-to-edge app shell**

`MainActivity` calls `enableEdgeToEdge()` and renders `NetStorageApp` inside Material 3. Use `WindowInsets.safeDrawing` on the top-level scaffold. Use Navigation3 back stack APIs so Android 16 predictive back works without `OnBackPressedDispatcher`.

- [ ] **Step 5: Add manifest permissions and backup rules**

Declare only:

```xml
<uses-permission android:name="android.permission.INTERNET" />
<uses-permission android:name="android.permission.ACCESS_NETWORK_STATE" />
<uses-permission android:name="android.permission.RUN_USER_INITIATED_JOBS" />
<uses-permission android:name="android.permission.RECEIVE_BOOT_COMPLETED" />
```

Set `android:allowBackup="false"` and `android:usesCleartextTraffic="false"`. Do not request storage, media, all-files, discovery, or local-network permissions in version one.

- [ ] **Step 6: Verify and commit**

Run: `cd android; .\gradlew.bat testDebugUnitTest lintDebug assembleDebug`

Expected: PASS and `android/app/build/outputs/apk/debug/app-debug.apk` exists.

```bash
git add .gitignore android
git commit -m "feat(android): scaffold API 36 Compose client"
```

### Task 2: Certificate Approval, Saved Server, and Automatic Sign-In

**Files:**
- Create: `android/app/src/main/java/com/rendref/netstorage/core/network/CertificateProbe.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/network/PinnedClientFactory.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/network/CertificateProbeTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/storage/ConnectionStore.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/storage/KeystoreTokenStore.kt`
- Create: `android/app/src/androidTest/java/com/rendref/netstorage/core/storage/KeystoreTokenStoreTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/network/SessionApi.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/network/SessionApiTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/connect/ConnectViewModel.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/connect/ConnectScreen.kt`

**Interfaces:**
- Produces: `CertificateProbe.inspect(httpsUrl: HttpUrl): ObservedCertificate`
- Produces: `PinnedClientFactory.create(SavedConnection): OkHttpClient`
- Produces: `ConnectionStore.state: Flow<SavedConnection?>`
- Produces: `KeystoreTokenStore.read/write/clear`
- Produces: `SessionApi.login` and `SessionApi.refresh`

- [ ] **Step 1: Test credential-free certificate probing**

Use an HTTPS `MockWebServer` with a self-signed certificate. Assert `inspect` returns uppercase colon-separated SHA-256, sends no `Authorization`, cookie, username, or password, and rejects non-HTTPS URLs.

- [ ] **Step 2: Implement probe and exact pinning**

```kotlin
data class ObservedCertificate(val fingerprint: String, val der: ByteArray)
data class SavedConnection(val baseUrl: String, val certificateSha256: String, val username: String)
```

The probe uses a one-request trust manager to capture the leaf certificate. It does not become the authenticated client. The pinned client accepts a chain only when the leaf DER SHA-256 equals the stored value and the requested host equals the saved URL host. Reject redirects to another host.

- [ ] **Step 3: Test DataStore connection persistence**

Assert server URL, uppercase pin, username, and destination URI survive a new `ConnectionStore` instance. Assert `clearServer()` removes the pin and token reference. Do not store a password.

- [ ] **Step 4: Implement Keystore-encrypted refresh tokens**

Create an AES-256 GCM key named `netstorage_refresh_token_v1` in Android Keystore with randomized encryption required and no user authentication requirement. Store `[version][12-byte nonce][ciphertext+tag]` in `noBackupFilesDir/session.token`. Use create-temp, fsync, and rename. Delete the file and key on `clear`.

- [ ] **Step 5: Test login, refresh, and redaction**

Use MockWebServer. Assert login JSON contains username/password only over the pinned client, response tokens decode, refresh sends only refresh token, HTTP 401 clears the token, and exception/log strings do not contain password or token.

- [ ] **Step 6: Implement connect state flow**

```kotlin
sealed interface ConnectState {
    data object EnterServer : ConnectState
    data class ReviewCertificate(val observed: ObservedCertificate) : ConnectState
    data class SignIn(val connection: SavedConnection) : ConnectState
    data object Ready : ConnectState
    data class Error(val message: String, val retry: Action) : ConnectState
}
```

Require explicit fingerprint approval before enabling sign-in. Save the server only after successful login. On app start, refresh the encrypted token; navigate to Audiobooks on success and SignIn on expiry.

- [ ] **Step 7: Verify and commit**

Run: `cd android; .\gradlew.bat testDebugUnitTest connectedDebugAndroidTest`

Expected: PASS on an API 36 emulator.

```bash
git add android/app/src
git commit -m "feat(android): pin server and remember sign-in"
```

### Task 3: Catalog Search and Audiobook Cards

**Files:**
- Create: `android/app/src/main/java/com/rendref/netstorage/core/network/AuthenticatedCall.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/network/CatalogApi.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/repository/CatalogRepository.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/repository/CatalogRepositoryTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/library/LibraryViewModel.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/library/LibraryScreen.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/library/BookCard.kt`
- Create: `android/app/src/androidTest/java/com/rendref/netstorage/feature/library/LibraryScreenTest.kt`

**Interfaces:**
- Produces: `CatalogRepository.books(BookQuery): Flow<PagedBooks>`
- Produces: `LibraryState` with query, filters, sort, cards, selection, loading, and errors
- Produces: card actions `OpenBook`, `ToggleSelected`, and `ViewRawFiles`

- [ ] **Step 1: Test authenticated retry once**

Assert a request receiving 401 calls refresh once, retries with the new access token, and never loops after a second 401. Serialize refresh calls through a mutex so concurrent 401 responses cause one refresh.

- [ ] **Step 2: Test catalog query mapping**

```kotlin
@Test fun `repository sends local search filters and cursor`() = runTest {
    repository.books(BookQuery(text="dune", genre="Science Fiction", year=1965, sort=YEAR, cursor="c1")).first()
    assertEquals("/v1/books?q=dune&genre=Science%20Fiction&year=1965&sort=year&cursor=c1", server.takeRequest().path)
}
```

- [ ] **Step 3: Implement repository paging**

```kotlin
data class BookQuery(
    val text: String = "", val author: String? = null, val genre: String? = null,
    val year: Int? = null, val rootId: String? = null, val reviewState: String? = null,
    val sort: BookSort = BookSort.TITLE, val cursor: String? = null,
)
data class PagedBooks(val items: List<BookCardDto>, val nextCursor: String?, val filters: CatalogFiltersDto)
```

Debounce text by 300 ms, cancel stale requests with `flatMapLatest`, append cursor pages once, and preserve selection by book ID.

- [ ] **Step 4: Write Compose card tests**

Assert a card shows cover semantics, title, author, year, genre, optional series/narrator, review badge, checkbox, and **View raw files**. Assert no node contains Rename, Move, Delete, Upload, Edit tags, or New folder.

- [ ] **Step 5: Implement adaptive card UI**

Use `LazyVerticalGrid(GridCells.Adaptive(160.dp))`, Material 3 cards, Coil `AsyncImage`, stable item keys, content descriptions, placeholder/error art, filter sheet, and sort menu. Keep search visible at the top. Restore query, scroll, and selection across configuration changes through `SavedStateHandle`.

Configure Coil's singleton `ImageLoader` with `coil-network-okhttp` and the same pinned, authenticated OkHttp call factory. Cover requests must refresh an expired access token once and must reject redirects away from the saved server host.

- [ ] **Step 6: Verify and commit**

Run: `cd android; .\gradlew.bat testDebugUnitTest connectedDebugAndroidTest`

Expected: PASS.

```bash
git add android/app/src
git commit -m "feat(android): browse searchable audiobook cards"
```

### Task 4: Raw Folder Mode and Book-to-Files Navigation

**Files:**
- Create: `android/app/src/main/java/com/rendref/netstorage/core/network/BrowseApi.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/repository/BrowseRepository.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/repository/BrowseRepositoryTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/folders/FoldersViewModel.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/folders/FoldersScreen.kt`
- Create: `android/app/src/androidTest/java/com/rendref/netstorage/feature/folders/FoldersScreenTest.kt`
- Modify: `android/app/src/main/java/com/rendref/netstorage/NetStorageApp.kt`

**Interfaces:**
- Produces: root and directory browsing
- Consumes: `ViewRawFiles(bookId)` from a card/detail screen
- Produces: highlighted file IDs while retaining all sibling files

- [ ] **Step 1: Test URL encoding and absolute-path privacy**

Assert `BrowseRepository.list("audiobooks","Author One/Book #1")` sends encoded query parameters. Reject any response entry whose `relativePath` begins with `/`, contains `..`, or has a Windows volume. Do not display an absolute server path.

- [ ] **Step 2: Test folders-first state**

Feed unsorted entries and assert the ViewModel emits folders first with case-insensitive stable sorting, breadcrumbs `Audiobooks > Author One > Book`, and selected folder IDs.

- [ ] **Step 3: Implement Folders UI**

Provide root picker, breadcrumbs, folder rows, file rows, size/date formatting, multi-folder checkboxes, pull-to-refresh, and highlighted assigned files. Folder tap navigates inward. System back moves to the parent before leaving Folders mode.

- [ ] **Step 4: Implement View raw files navigation**

Fetch `/v1/books/{id}/files`, navigate to the common source folder, and pass assigned relative paths in state. Highlight assigned rows with a **This book** label. Leave unrelated files visible. A toolbar action returns to the originating card/detail.

- [ ] **Step 5: Verify read-only UI and commit**

Run: `cd android; .\gradlew.bat connectedDebugAndroidTest`

Expected: PASS, including a semantics-tree assertion that no source-mutation action exists.

```bash
git add android/app/src
git commit -m "feat(android): add read-only raw folder navigation"
```

### Task 5: Book Details, Metadata Matches, and Grouping Review

**Files:**
- Create: `android/app/src/main/java/com/rendref/netstorage/core/network/ReviewApi.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/repository/ReviewRepository.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/repository/ReviewRepositoryTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/book/BookDetailViewModel.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/book/BookDetailScreen.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/book/MatchPickerScreen.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/book/GroupingScreen.kt`
- Create: `android/app/src/androidTest/java/com/rendref/netstorage/feature/book/BookReviewTest.kt`

**Interfaces:**
- Produces: provider alternative search and confirmation
- Produces: catalog-only file grouping corrections
- Consumes: book details and source-file IDs

- [ ] **Step 1: Test review requests**

Assert match search posts only local book ID and search terms, match confirmation sends one stored `matchId`, and grouping sends book/file UUIDs. Assert no request contains an absolute path, new filename, tag data, delete flag, move destination, or upload body.

- [ ] **Step 2: Implement book details**

Show cover, attribution, title/subtitle, authors, series, narrator, publisher, date/year, genres, identifiers, review state, assigned files, **View raw files**, **Choose metadata match**, and **Review grouping**.

- [ ] **Step 3: Implement match alternatives**

Search on open with current title/author and allow edited search terms. Show provider, edition date, subjects, cover, and score. Confirmation displays: “The server may publish this cover in the Windows book folder. Audio files and tags will not change.” Require confirmation before the PUT request.

- [ ] **Step 4: Implement grouping corrections**

Let the user create two or more logical groups, assign each listed source audio file to one group, merge groups, and validate that each audio file belongs to exactly one group. Save catalog assignments without presenting rename or move controls.

- [ ] **Step 5: Verify and commit**

Run: `cd android; .\gradlew.bat testDebugUnitTest connectedDebugAndroidTest`

Expected: PASS.

```bash
git add android/app/src
git commit -m "feat(android): review audiobook metadata and grouping"
```

### Task 6: Destination Selection, Transfer Journal, and Verified File Engine

**Files:**
- Create: `android/app/src/main/java/com/rendref/netstorage/core/storage/DestinationStore.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/storage/DocumentTree.kt`
- Create: `android/app/src/androidTest/java/com/rendref/netstorage/core/storage/DocumentTreeTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/transfer/TransferJournal.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/transfer/TransferJournalTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/transfer/TransferEngine.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/transfer/TransferEngineTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/transfers/TransferSetupScreen.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/transfers/ConflictScreen.kt`

**Interfaces:**
- Produces: persisted `ACTION_OPEN_DOCUMENT_TREE` destination
- Produces: atomic JSON `TransferJournal`
- Produces: `suspend TransferEngine.run(transferId: String, network: Network?, onProgress: (TransferProgress) -> Unit): TransferResult`
- Produces: conflict decisions Replace, KeepBoth, and Skip

- [ ] **Step 1: Test destination permission persistence**

Launch `ActivityResultContracts.OpenDocumentTree`, pass read/write/persistable/prefix flags, call `takePersistableUriPermission`, store the URI, and verify `DocumentFile.fromTreeUri` remains writable after Activity recreation. Treat missing persisted permission as `DestinationPermissionRevoked`.

- [ ] **Step 2: Test atomic JSON journals**

```kotlin
@Serializable data class TransferRecord(
    val id: String, val manifest: ManifestDto, val files: List<FileProgress>,
    val destinationTree: String, val state: TransferState,
)
@Serializable data class FileProgress(
    val fileId: String, val temporaryDocument: String?, val bytesWritten: Long,
    val state: FileState, val conflictChoice: ConflictChoice? = null,
)
```

Assert a partial write cannot corrupt the last valid journal. Store journals under `filesDir/transfers/<id>.json` through temp, `FileDescriptor.sync`, and rename. Do not use SQLite.

- [ ] **Step 3: Test byte-range resume and digest rejection**

Use MockWebServer and a fake document tree. Start with four bytes of a ten-byte file. Assert `Range: bytes=4-`, append to the partial, and verify SHA-256 before finalization. Return wrong bytes and assert no final document exists.

- [ ] **Step 4: Implement document-tree path creation**

Create each path segment with `DocumentsContract` under the selected tree. Sanitize no names on Android; the manifest is authoritative. Reject empty, `.`, `..`, slash, backslash, control characters, and duplicate case-folded sibling paths. Cache document IDs per transfer.

- [ ] **Step 5: Implement conflicts**

Hash an existing same-size destination file before skipping. If it differs, pause that file and emit a conflict. Replace deletes only the Android destination document after explicit choice. Keep both adds ` (netStorage 1)` before the extension with collision increments. Skip records completion without downloading.

- [ ] **Step 6: Implement verified streaming**

Bind calls to the network supplied by JobParameters when present. Stream with a 1 MiB buffer, update journal every 4 MiB or five seconds, stop on coroutine cancellation, compare expected size and digest, and finalize through rename. If the provider cannot seek, remove and restart that one partial file.

- [ ] **Step 7: Verify and commit**

Run: `cd android; .\gradlew.bat testDebugUnitTest connectedDebugAndroidTest`

Expected: PASS.

```bash
git add android/app/src
git commit -m "feat(android): copy and verify selected audiobooks"
```

### Task 7: User-Initiated Background Jobs and Transfer UI

**Files:**
- Create: `android/app/src/main/java/com/rendref/netstorage/core/transfer/TransferScheduler.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/transfer/TransferJobService.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/transfer/TransferNotifications.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/transfer/TransferReceiver.kt`
- Create: `android/app/src/test/java/com/rendref/netstorage/core/transfer/TransferSchedulerTest.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/transfers/TransfersViewModel.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/transfers/TransfersScreen.kt`
- Modify: `android/app/src/main/AndroidManifest.xml`

**Interfaces:**
- Produces: API 36 user-initiated transfer jobs
- Produces: progress notification with Pause/Cancel actions
- Produces: transfer history from JSON journals

- [ ] **Step 1: Test JobInfo construction**

Assert:

```kotlin
jobInfo.isUserInitiated
jobInfo.isPersisted
jobInfo.isRequireStorageNotLow
jobInfo.requiredNetwork != null
jobInfo.estimatedNetworkDownloadBytes == manifest.totalBytes
```

Use a required network with `NET_CAPABILITY_NOT_VPN`; do not require `NET_CAPABILITY_INTERNET` because the server is on the LAN. Schedule only from the visible transfer-setup action and handle `RESULT_FAILURE`.

- [ ] **Step 2: Implement `TransferJobService`**

Call `setNotification` before transfer work, launch on `Dispatchers.IO`, pass `params.network` to the engine, call `jobFinished(params, false)` on terminal success/user cancellation, and `jobFinished(params, true)` after resumable system/network failure. `onStopJob` cancels the scope, flushes the journal, records `stopReason`, and returns true.

- [ ] **Step 3: Add notification actions**

Create channel `netstorage_transfers`. Show title, current book/file, byte progress, and remaining count. Pause/Cancel broadcasts target one transfer UUID. Cancel retains partial data until **Discard partial data** is chosen in the app.

- [ ] **Step 4: Implement transfer screens**

Transfer setup lists selected books/folders, destination, item count, required bytes, and Start. Transfers lists running, paused, failed, conflicted, and complete jobs with per-file/overall progress and recovery actions.

- [ ] **Step 5: Test process death and reboot**

Persist the JobScheduler job and journal. Instrumentation kills the app process during a transfer, restarts it, and asserts resume from the recorded byte. Simulate reboot completion and verify journals with no active job get rescheduled only when their state allows automatic retry; user-cancelled jobs stay cancelled.

- [ ] **Step 6: Verify and commit**

Run: `cd android; .\gradlew.bat testDebugUnitTest connectedDebugAndroidTest`

Expected: PASS.

```bash
git add android/app/src
git commit -m "feat(android): run resilient background transfers"
```

### Task 8: Settings, Recovery Copy, and Full Android Verification

**Files:**
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/settings/SettingsScreen.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/feature/settings/SettingsViewModel.kt`
- Create: `android/app/src/main/java/com/rendref/netstorage/core/error/UserError.kt`
- Create: `android/app/src/androidTest/java/com/rendref/netstorage/AppJourneyTest.kt`
- Create: `docs/android.md`
- Modify: `android/app/src/main/java/com/rendref/netstorage/NetStorageApp.kt`

**Interfaces:**
- Produces: destination change, sign out, change server, rescan, and diagnostic version actions
- Produces: recovery messages for each approved error case
- Produces: a tested debug APK

- [ ] **Step 1: Define typed user errors**

Map server unavailable, pin mismatch, session expiry, throttling, source changed/missing, revoked destination, insufficient space, digest mismatch, provider failure, ambiguous metadata, and cover failure to specific text and actions. Never include tokens, passwords, full document URIs, or absolute source paths.

- [ ] **Step 2: Implement Settings**

Show saved host, abbreviated certificate fingerprint with a full-copy action, username, destination display name, app version, server version, scan status, **Rescan**, **Choose destination**, **Sign out**, and **Change server**. Changing server clears tokens and the pin after confirmation; it leaves downloaded Android files and transfer history intact.

- [ ] **Step 3: Write the end-to-end UI journey against MockWebServer**

Test certificate review, login, card search, filters, card selection, raw-file navigation, book detail, metadata confirmation, destination selection, transfer setup, conflict resolution, progress, completion, sign out, and reopen. Assert the app never sends a source-mutation request.

- [ ] **Step 4: Run static and runtime checks**

Run:

```powershell
Set-Location android
.\gradlew.bat testDebugUnitTest lintDebug connectedDebugAndroidTest assembleDebug
```

Expected: PASS with no lint errors and a debug APK.

Run: `rg -n "androidx\\.room|sqlite|SQLiteDatabase|MANAGE_EXTERNAL_STORAGE|WRITE_EXTERNAL_STORAGE|READ_EXTERNAL_STORAGE" android`

Expected: no matches.

- [ ] **Step 5: Document and commit**

`docs/android.md` must cover API 36 emulator creation, server entry, certificate comparison, sign-in, folder picker, UI modes, background transfers, conflict choices, Samsung battery settings, sideloading, sign out, and change server.

```bash
git add android docs/android.md
git commit -m "feat(android): complete netStorage client workflow"
```

## Plan Completion Check

Run:

```powershell
Set-Location android
.\gradlew.bat testDebugUnitTest lintDebug connectedDebugAndroidTest assembleDebug
Set-Location ..
rg -n "androidx\\.room|sqlite|SQLiteDatabase|MANAGE_EXTERNAL_STORAGE|WRITE_EXTERNAL_STORAGE|READ_EXTERNAL_STORAGE" android
git status --short
```

Expected: all Gradle tasks pass, the forbidden-dependency scan returns no matches, the debug APK exists, and the working tree is clean.

package com.rendref.bookbox

import android.content.Context
import android.net.Uri
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.test.ExperimentalTestApi
import androidx.compose.ui.test.StateRestorationTester
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.runComposeUiTest
import androidx.test.espresso.Espresso.pressBack
import androidx.test.platform.app.InstrumentationRegistry
import com.rendref.bookbox.core.model.AcceptedJobDto
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.model.BookDetailDto
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.BrowseDto
import com.rendref.bookbox.core.model.BookSearchGroupDto
import com.rendref.bookbox.core.model.RawSearchPageDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.model.GroupingContextDto
import com.rendref.bookbox.core.model.MatchSearchDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.repository.BrowseSource
import com.rendref.bookbox.core.repository.CatalogFiltersDto
import com.rendref.bookbox.core.repository.CatalogSource
import com.rendref.bookbox.core.repository.GroupingDecision
import com.rendref.bookbox.core.repository.PagedBooks
import com.rendref.bookbox.core.repository.ReviewSource
import com.rendref.bookbox.core.storage.DestinationAccess
import com.rendref.bookbox.core.storage.DestinationStore
import com.rendref.bookbox.core.storage.DocumentTree
import com.rendref.bookbox.core.storage.SavedConnection
import com.rendref.bookbox.core.transfer.TransferJournal
import com.rendref.bookbox.core.transfer.TransferScheduler
import com.rendref.bookbox.core.transfer.TransferStartRequest
import com.rendref.bookbox.core.transfer.TransferStartResult
import com.rendref.bookbox.core.transfer.TransferStartSource
import com.rendref.bookbox.feature.settings.ScanStatus
import com.rendref.bookbox.feature.settings.SettingsSnapshot
import com.rendref.bookbox.feature.settings.SettingsSource
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalTestApi::class)
class ConnectedAppWiringTest {
    @Test
    fun connectionScopeKeySeparatesServersAndUsernames() {
        val serverA = SavedConnection(
            baseUrl = "http://192.168.1.25:8787/",
            username = "listener",
        )
        val sameConnection = serverA.copy()
        val anotherUser = serverA.copy(username = "another-user")
        val serverB = serverA.copy(baseUrl = "http://192.168.1.26:8787/")

        assertEquals(connectionInstanceKey(serverA), connectionInstanceKey(sameConnection))
        assertNotEquals(connectionInstanceKey(serverA), connectionInstanceKey(anotherUser))
        assertNotEquals(connectionInstanceKey(serverA), connectionInstanceKey(serverB))
        assertEquals(64, connectionInstanceKey(serverA).length)
    }

    @Test
    fun notificationGrantAndDenialBothStartExactlyOnce() {
        listOf(true, false).forEach { granted ->
            var starts = 0
            var denials = 0
            startAfterNotificationDecision(
                requester = NotificationPermissionRequester { result ->
                    result(granted)
                    result(granted)
                },
                onDenied = { denials += 1 },
                start = { starts += 1 },
            )
            assertEquals(1, starts)
            assertEquals(if (granted) 0 else 1, denials)
        }
    }

    @Test
    fun productionShellRestoresDetailBacksOutStartsCopyAndSignsOut() =
        runComposeUiTest {
        val context = InstrumentationRegistry.getInstrumentation()
            .targetContext
        val started = AtomicReference<TransferStartRequest?>()
        val signedOut = AtomicBoolean()
        val reset = AtomicBoolean()
        val permissionRequested = AtomicBoolean()
        val runtime = fakeRuntime(context, "server-a", "Server A Dune", started, signedOut)
        val restoration = StateRestorationTester(this)
        restoration.setContent {
            MaterialTheme {
                ConnectedApp(
                    runtime = runtime,
                    onConnectionReset = { reset.set(true) },
                    notificationPermissionRequester = NotificationPermissionRequester {
                        permissionRequested.set(true)
                        it(false)
                    },
                )
            }
        }

        waitUntil(timeoutMillis = 5_000) {
            onAllNodes(
                androidx.compose.ui.test.hasText("Server A Dune"),
            ).fetchSemanticsNodes().isNotEmpty()
        }
        onNodeWithText("Server A Dune").performClick()
        onNodeWithText("Choose metadata match").assertIsDisplayed()
        onNodeWithText("Choose metadata match").performClick()
        onNodeWithText("Search providers").assertIsDisplayed()
        restoration.emulateSaveAndRestore()
        onNodeWithText("Search providers").assertIsDisplayed()
        pressBack()
        onNodeWithText("Choose metadata match").assertIsDisplayed()
        onNodeWithText("Search providers").assertDoesNotExist()

        onNodeWithText("Choose metadata match").performClick()
        onNodeWithText("Search providers").assertIsDisplayed()
        onNodeWithText("Audiobooks").performClick()
        waitUntil(timeoutMillis = 5_000) {
            onAllNodes(
                androidx.compose.ui.test.hasText("AUDIOBOOK CATALOG"),
            ).fetchSemanticsNodes().isNotEmpty()
        }
        onNodeWithText("AUDIOBOOK CATALOG").assertIsDisplayed()
        onNodeWithText("Server A Dune").performClick()
        onNodeWithText("Choose metadata match").assertIsDisplayed()
        onNodeWithText("Search providers").assertDoesNotExist()
        pressBack()
        onNodeWithText("AUDIOBOOK CATALOG").assertIsDisplayed()

        onNodeWithContentDescription("Select Server A Dune").performClick()
        onNodeWithText("Copy 1 selected").performClick()
        onNodeWithText("Copy to Android").assertIsDisplayed()
        onNodeWithText(
            "Exact file count and required space are checked when the copy starts.",
        ).assertIsDisplayed()
        waitUntil(timeoutMillis = 5_000) {
            onAllNodes(
                androidx.compose.ui.test.hasText("Destination: Audiobooks"),
            ).fetchSemanticsNodes().isNotEmpty()
        }
        onNodeWithText("Copy selected").performClick()
        waitUntil(timeoutMillis = 5_000) { started.get() != null }
        assertTrue(permissionRequested.get())
        assertEquals(1, started.get()?.selections?.size)
        onNodeWithText(
            "Notifications are off. Copy progress remains in BookBox and Android Task Manager.",
        ).assertIsDisplayed()

        onNodeWithText("Settings").performClick()
        waitUntil(timeoutMillis = 5_000) {
            onAllNodes(
                androidx.compose.ui.test.hasText("Saved server"),
            ).fetchSemanticsNodes().isNotEmpty()
        }
        onNode(hasScrollToIndexAction())
            .performScrollToNode(hasText("Sign in again"))
        val signInAgain = onNode(
            hasText("Sign in again").and(hasClickAction()),
        )
        signInAgain.assertIsDisplayed()
        signInAgain.performSemanticsAction(SemanticsActions.OnClick) { click ->
            click()
        }
        waitForIdle()
        waitUntil(timeoutMillis = 10_000) {
            signedOut.get() && reset.get()
        }
        assertTrue(signedOut.get())
        assertTrue(reset.get())
    }

    @Test
    fun switchingRuntimeKeyCannotReusePreviousServerLibraryViewModel() =
        runComposeUiTest {
        val context = InstrumentationRegistry.getInstrumentation()
            .targetContext
        var runtime by mutableStateOf(
            fakeRuntime(
                context,
                "server-a",
                "Server A Dune",
                AtomicReference(),
                AtomicBoolean(),
            ),
        )
        setContent {
            MaterialTheme {
                ConnectedApp(
                    runtime = runtime,
                    onConnectionReset = {},
                )
            }
        }
        waitUntil(timeoutMillis = 5_000) {
            onAllNodes(
                androidx.compose.ui.test.hasText("Server A Dune"),
            ).fetchSemanticsNodes().isNotEmpty()
        }
        onNodeWithText("Server A Dune").assertIsDisplayed()

        runOnUiThread {
            runtime = fakeRuntime(
                context,
                "server-b",
                "Server B Foundation",
                AtomicReference(),
                AtomicBoolean(),
            )
        }
        waitUntil(timeoutMillis = 5_000) {
            onAllNodes(
                androidx.compose.ui.test.hasText("Server B Foundation"),
            ).fetchSemanticsNodes().isNotEmpty()
        }
        onNodeWithText("Server B Foundation").assertIsDisplayed()
        onNodeWithText("Server A Dune").assertDoesNotExist()
    }
}

private fun fakeRuntime(
    context: Context,
    instanceKey: String,
    title: String,
    started: AtomicReference<TransferStartRequest?>,
    signedOut: AtomicBoolean,
): ConnectedRuntime {
    val book = TEST_BOOK.copy(title = title)
    val directory = File(context.cacheDir, "connected-shell-$instanceKey").apply {
        mkdirs()
    }
    return ConnectedRuntime(
        instanceKey = instanceKey,
        catalog = CatalogSource {
            flowOf(
                PagedBooks(
                    groups = listOf(BookSearchGroupDto(book.id, false, listOf(book))),
                    snapshots = listOf(
                        SearchSnapshotDto(
                            rootId = "audiobooks",
                            scanRunId = "22222222-2222-4222-8222-222222222222",
                            completedAt = "2026-08-08T00:00:00Z",
                        ),
                    ),
                    nextCursor = null,
                    filters = CatalogFiltersDto(),
                ),
            )
        },
        browse = object : BrowseSource {
            override fun roots() = flowOf(listOf(RootDto("audiobooks", "Audiobooks")))

            override fun list(rootId: String, path: String) = flowOf(
                BrowseDto(rootId, path, emptyList()),
            )

            override fun searchRaw(rootId: String, query: String, cursor: String?) =
                flowOf(
                    RawSearchPageDto(
                        rootId = rootId,
                        query = query,
                        snapshot = SearchSnapshotDto(
                            rootId = rootId,
                            scanRunId = "22222222-2222-4222-8222-222222222222",
                            completedAt = "2026-08-08T00:00:00Z",
                        ),
                        entries = emptyList(),
                    ),
                )

            override fun bookFiles(bookId: String) = flowOf(TEST_FILES.copy(bookId = bookId))
        },
        reviews = object : ReviewSource {
            override suspend fun bookDetail(bookId: String) = BookDetailDto(
                id = bookId,
                rootId = "audiobooks",
                title = title,
                authors = listOf("Test Author"),
                reviewState = "confirmed",
                sourceFolder = title,
            )

            override suspend fun bookFiles(bookId: String) =
                TEST_FILES.copy(bookId = bookId)

            override suspend fun groupingContext(bookId: String) =
                GroupingContextDto(
                    bookId = bookId,
                    rootId = "audiobooks",
                    sourceFolder = title,
                    files = emptyList(),
                    books = emptyList(),
                )

            override suspend fun searchMatches(
                bookId: String,
                title: String,
                author: String,
                isbn: String,
            ) = MatchSearchDto(bookId, emptyList())

            override suspend fun confirmMatch(
                bookId: String,
                matchId: String,
            ) = AcceptedJobDto("job", "queued")

            override suspend fun saveGrouping(
                bookId: String,
                groups: List<GroupingDecision>,
            ) = Unit
        },
        destination = DestinationStore(
            readDestination = { "content://test/audiobooks" },
            writeDestination = {},
            access = object : DestinationAccess {
                override fun persist(uri: Uri, flags: Int) = Unit

                override fun hasReadWritePermission(uri: Uri) = true

                override fun open(uri: Uri): DocumentTree =
                    error("Destination is not opened by this wiring test")
            },
        ),
        journal = TransferJournal(directory),
        scheduler = TransferScheduler(context),
        starter = TransferStartSource { request ->
            started.set(request)
            TransferStartResult.Scheduled("33333333-3333-4333-8333-333333333333")
        },
        settings = object : SettingsSource {
            override suspend fun load() = SettingsSnapshot(
                baseUrl = "http://192.168.1.25:8787/",
                username = "listener",
                destinationLabel = "Audiobooks",
                appVersion = "1.0.0",
                serverVersion = "1.0.0",
                scanStatus = ScanStatus.Complete,
            )

            override suspend fun rescan() = ScanStatus.Queued

            override suspend fun signOut() {
                signedOut.set(true)
            }

            override suspend fun clearServerCredentials() = Unit
        },
    )
}

private val TEST_BOOK = BookCardDto(
    id = "11111111-1111-4111-8111-111111111111",
    title = "Test book",
    authors = listOf("Test Author"),
    reviewState = "confirmed",
    sourceFolder = "Test book",
)

private val TEST_FILES = BookFilesDto(
    bookId = TEST_BOOK.id,
    rootId = "audiobooks",
    sourceFolder = "Test book",
    files = emptyList(),
)

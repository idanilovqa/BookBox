package com.rendref.bookbox.feature.library

import androidx.lifecycle.SavedStateHandle
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.model.BookSearchGroupDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.network.SearchIndexBuilding
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.repository.BookSort
import com.rendref.bookbox.core.repository.CatalogFiltersDto
import com.rendref.bookbox.core.repository.CatalogSource
import com.rendref.bookbox.core.repository.PagedBooks
import java.time.Clock
import java.time.Instant
import java.time.ZoneOffset
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class LibraryViewModelTest {
    private val dispatcher = StandardTestDispatcher()

    @Before
    fun setUp() {
        Dispatchers.setMain(dispatcher)
    }

    @After
    fun tearDown() {
        Dispatchers.resetMain()
    }

    @Test
    fun `search waits 150ms and only submits the latest text`() = runTest(dispatcher) {
        val source = RecordingCatalogSource()
        val viewModel = LibraryViewModel(SavedStateHandle(), source)
        viewModel.setSearchText("du")
        advanceTimeBy(149)
        runCurrent()
        assertTrue(source.queries.isEmpty())

        viewModel.setSearchText("dune")
        advanceTimeBy(149)
        runCurrent()
        assertTrue(source.queries.isEmpty())

        advanceTimeBy(1)
        runCurrent()
        assertEquals(listOf("dune"), source.queries.map(BookQuery::text))
    }

    @Test
    fun `a newer query cancels a stale catalog request`() = runTest(dispatcher) {
        val oldStarted = CompletableDeferred<Unit>()
        var oldCancelled = false
        val source = object : CatalogSource {
            override fun books(query: BookQuery): Flow<PagedBooks> =
                if (query.text == "old") {
                    flow {
                        oldStarted.complete(Unit)
                        try {
                            awaitCancellation()
                        } finally {
                            oldCancelled = true
                        }
                    }
                } else {
                    flowOf(page(book(query.text)))
                }
        }
        val viewModel = LibraryViewModel(SavedStateHandle(), source)

        viewModel.setSearchText("old")
        advanceTimeBy(150)
        runCurrent()
        assertTrue(oldStarted.isCompleted)

        viewModel.setSearchText("new")
        advanceTimeBy(150)
        advanceUntilIdle()

        assertTrue(oldCancelled)
        assertEquals(listOf("new"), viewModel.state.value.books.map(BookCardDto::id))
    }

    @Test
    fun `next cursor is requested once and duplicate books are not appended`() =
        runTest(dispatcher) {
            val source = RecordingCatalogSource { query ->
                when (query.cursor) {
                    null -> page(book("one"), nextCursor = "cursor-2")
                    "cursor-2" -> page(book("one"), book("two"))
                    else -> error("Unexpected cursor")
                }
            }
            val viewModel = LibraryViewModel(SavedStateHandle(), source)
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.loadNextPage()
            viewModel.loadNextPage()
            advanceUntilIdle()

            assertEquals(1, source.queries.count { it.cursor == "cursor-2" })
            assertEquals(listOf("one", "two"), viewModel.state.value.books.map(BookCardDto::id))
            assertFalse(viewModel.state.value.isLoadingMore)
        }

    @Test
    fun `audiobook results preserve multi-root groups and snapshots`() =
        runTest(dispatcher) {
            val groups = listOf(
                group("group-a", book("a1", "main"), book("a2", "main")),
                group("group-b", book("b1", "archive")),
            )
            val snapshots = listOf(snapshot("archive"), snapshot("main"))
            val source = RecordingCatalogSource {
                page(groups = groups, snapshots = snapshots)
            }
            val viewModel = LibraryViewModel(SavedStateHandle(), source)

            viewModel.setRoots(listOf("main", "archive", "main"))
            advanceTimeBy(150)
            advanceUntilIdle()

            assertEquals(listOf("archive", "main"), source.queries.last().rootIds)
            assertEquals(groups, viewModel.state.value.groups)
            assertEquals(snapshots, viewModel.state.value.snapshots)
            assertEquals(listOf("a1", "a2", "b1"), viewModel.state.value.books.map { it.id })
        }

    @Test
    fun `replacement loading and index building retain audiobook groups`() =
        runTest(dispatcher) {
            val retained = group("group-a", book("a1", "main"))
            val source = object : CatalogSource {
                override fun books(query: BookQuery): Flow<PagedBooks> = when (query.text) {
                    "building" -> flow { throw SearchIndexBuilding() }
                    else -> flowOf(page(groups = listOf(retained), snapshots = listOf(snapshot("main"))))
                }
            }
            val viewModel = LibraryViewModel(SavedStateHandle(), source)
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.setSearchText("building")
            runCurrent()
            assertTrue(viewModel.state.value.isLoading)
            assertEquals(listOf(retained), viewModel.state.value.groups)
            advanceTimeBy(150)
            advanceUntilIdle()

            assertEquals(listOf(retained), viewModel.state.value.groups)
            assertTrue(viewModel.state.value.isIndexBuilding)
            assertEquals(null, viewModel.state.value.errorMessage)
        }

    @Test
    fun `pagination appends complete groups and ignores a repeated group`() =
        runTest(dispatcher) {
            val first = group("group-a", book("a1", "main"), book("a2", "main"))
            val second = group("group-b", book("b1", "archive"), book("b2", "archive"))
            val source = RecordingCatalogSource { query ->
                when (query.cursor) {
                    null -> page(groups = listOf(first), nextCursor = "next")
                    "next" -> page(groups = listOf(first, second))
                    else -> error("Unexpected cursor")
                }
            }
            val viewModel = LibraryViewModel(SavedStateHandle(), source)
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.loadNextPage()
            advanceUntilIdle()

            assertEquals(listOf("group-a", "group-b"), viewModel.state.value.groups.map { it.groupId })
            assertEquals(listOf("a1", "a2", "b1", "b2"), viewModel.state.value.books.map { it.id })
        }

    @Test
    fun `query change cancels the exact pagination job`() = runTest(dispatcher) {
        val pageStarted = CompletableDeferred<Unit>()
        val pageCancelled = CompletableDeferred<Unit>()
        val source = object : CatalogSource {
            override fun books(query: BookQuery): Flow<PagedBooks> = when (query.cursor) {
                null -> flowOf(page(book("one"), nextCursor = "next"))
                else -> flow {
                    pageStarted.complete(Unit)
                    try {
                        awaitCancellation()
                    } finally {
                        pageCancelled.complete(Unit)
                    }
                }
            }
        }
        val viewModel = LibraryViewModel(SavedStateHandle(), source)
        advanceTimeBy(150)
        advanceUntilIdle()
        viewModel.loadNextPage()
        runCurrent()
        assertTrue(pageStarted.isCompleted)

        viewModel.setSearchText("dune")
        runCurrent()

        assertTrue(pageCancelled.isCompleted)
        assertFalse(viewModel.state.value.isLoadingMore)
    }

    @Test
    fun `pagination rejects a different snapshot and duplicate child ids`() =
        runTest(dispatcher) {
            val firstSnapshot = snapshot("main")
            val changedSnapshot = firstSnapshot.copy(catalogGeneration = 2)
            var invalidPage = page(
                groups = listOf(group("group-b", book("b1"))),
                snapshots = listOf(changedSnapshot),
            )
            val source = RecordingCatalogSource { query ->
                if (query.cursor == null) {
                    page(
                        groups = listOf(group("group-a", book("a1"))),
                        snapshots = listOf(firstSnapshot),
                        nextCursor = "next",
                    )
                } else {
                    invalidPage
                }
            }
            val viewModel = LibraryViewModel(SavedStateHandle(), source)
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.loadNextPage()
            advanceUntilIdle()
            assertEquals(listOf("group-a"), viewModel.state.value.groups.map { it.groupId })
            assertEquals("Unable to load more audiobooks", viewModel.state.value.errorMessage)

            invalidPage = page(
                groups = listOf(group("group-b", book("a1"))),
                snapshots = listOf(firstSnapshot),
            )
            viewModel.loadNextPage()
            advanceUntilIdle()
            assertEquals(listOf("group-a"), viewModel.state.value.groups.map { it.groupId })
            assertEquals("Unable to load more audiobooks", viewModel.state.value.errorMessage)
        }

    @Test
    fun `catalog older than two scan intervals exposes stale state and copy`() =
        runTest(dispatcher) {
            val now = Instant.parse("2026-08-09T12:00:01Z")
            val staleSnapshot = snapshot(
                rootId = "main",
                completedAt = "2026-08-09T00:00:00Z",
            )
            val viewModel = LibraryViewModel(
                SavedStateHandle(),
                RecordingCatalogSource {
                    page(
                        groups = listOf(group("group-a", book("a1"))),
                        snapshots = listOf(staleSnapshot),
                    )
                },
                clock = Clock.fixed(now, ZoneOffset.UTC),
            )

            advanceTimeBy(150)
            advanceUntilIdle()

            assertTrue(viewModel.state.value.isCatalogStale)
            assertEquals(
                "Search results may be out of date",
                viewModel.state.value.catalogStatusMessage,
            )
        }

    @Test
    fun `older catalog generation cannot replace retained audiobook groups`() =
        runTest(dispatcher) {
            val retained = group("current", book("current"))
            val stale = group("stale", book("stale"))
            val source = RecordingCatalogSource { query ->
                if (query.text == "replacement") {
                    page(
                        groups = listOf(stale),
                        snapshots = listOf(snapshot("main", catalogGeneration = 6)),
                    )
                } else {
                    page(
                        groups = listOf(retained),
                        snapshots = listOf(snapshot("main", catalogGeneration = 7)),
                    )
                }
            }
            val viewModel = LibraryViewModel(SavedStateHandle(), source)
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.setSearchText("replacement")
            advanceTimeBy(150)
            advanceUntilIdle()

            assertEquals(listOf(retained), viewModel.state.value.groups)
            assertEquals(7L, viewModel.state.value.snapshots.single().catalogGeneration)
        }

    @Test
    fun `inactive text clears transient state and retains audiobook groups`() =
        runTest(dispatcher) {
            val retained = group("group-a", book("a1"))
            val viewModel = LibraryViewModel(
                SavedStateHandle(),
                RecordingCatalogSource { page(groups = listOf(retained)) },
            )
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.setSearchText("x")
            runCurrent()

            assertEquals(listOf(retained), viewModel.state.value.groups)
            assertFalse(viewModel.state.value.isLoading)
            assertFalse(viewModel.state.value.isLoadingMore)
            assertFalse(viewModel.state.value.isIndexBuilding)
            assertEquals(null, viewModel.state.value.errorMessage)
        }

    @Test
    fun `query selection and grid position restore from saved state`() =
        runTest(dispatcher) {
            val savedState = SavedStateHandle()
            val source = RecordingCatalogSource()
            val first = LibraryViewModel(savedState, source)
            first.setSearchText("murderbot")
            first.setGenre("Science Fiction")
            first.setSort(BookSort.YEAR)
            first.toggleSelection("book-7")
            first.saveGridPosition(index = 18, offset = 42)

            val restored = LibraryViewModel(savedState, source)

            assertEquals("murderbot", restored.state.value.query.text)
            assertEquals("Science Fiction", restored.state.value.query.genre)
            assertEquals(BookSort.YEAR, restored.state.value.query.sort)
            assertEquals(setOf("book-7"), restored.state.value.selectedBookIds)
            assertEquals(18, restored.state.value.scrollIndex)
            assertEquals(42, restored.state.value.scrollOffset)
        }

    private class RecordingCatalogSource(
        private val response: (BookQuery) -> PagedBooks = { page() },
    ) : CatalogSource {
        val queries = mutableListOf<BookQuery>()

        override fun books(query: BookQuery): Flow<PagedBooks> {
            queries += query
            return flowOf(response(query))
        }
    }
}

private fun book(id: String, rootId: String = "main") = BookCardDto(
    id = id,
    title = id.replaceFirstChar(Char::uppercase),
    authors = listOf("Author"),
    reviewState = "matched",
    sourceFolder = "H:/Audiobooks/$id",
    rootId = rootId,
)

private fun group(groupId: String, vararg books: BookCardDto) = BookSearchGroupDto(
    groupId = groupId,
    duplicate = books.size > 1,
    books = books.toList(),
)

private fun snapshot(
    rootId: String,
    catalogGeneration: Long = 1,
    completedAt: String = "2026-08-08T00:00:00Z",
) = SearchSnapshotDto(
    rootId = rootId,
    scanRunId = "00000000-0000-4000-8000-000000000001",
    completedAt = completedAt,
    catalogGeneration = catalogGeneration,
)

private fun page(
    vararg books: BookCardDto,
    nextCursor: String? = null,
    groups: List<BookSearchGroupDto> = emptyList(),
    snapshots: List<SearchSnapshotDto> = emptyList(),
) = PagedBooks(
    groups = if (groups.isEmpty()) books.map { group(it.id, it) } else groups,
    snapshots = if (snapshots.isEmpty()) {
        books.map(BookCardDto::rootId).distinct().map(::snapshot)
    } else {
        snapshots
    },
    nextCursor = nextCursor,
    filters = CatalogFiltersDto(genres = listOf("Science Fiction")),
)

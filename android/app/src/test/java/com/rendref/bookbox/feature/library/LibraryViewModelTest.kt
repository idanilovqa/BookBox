package com.rendref.bookbox.feature.library

import androidx.lifecycle.SavedStateHandle
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.repository.BookSort
import com.rendref.bookbox.core.repository.CatalogFiltersDto
import com.rendref.bookbox.core.repository.CatalogSource
import com.rendref.bookbox.core.repository.PagedBooks
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
    fun `search waits 300ms and only submits the latest text`() = runTest(dispatcher) {
        val source = RecordingCatalogSource()
        val viewModel = LibraryViewModel(SavedStateHandle(), source)
        viewModel.setSearchText("du")
        advanceTimeBy(299)
        runCurrent()
        assertTrue(source.queries.isEmpty())

        viewModel.setSearchText("dune")
        advanceTimeBy(299)
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
        advanceTimeBy(300)
        runCurrent()
        assertTrue(oldStarted.isCompleted)

        viewModel.setSearchText("new")
        advanceTimeBy(300)
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
            advanceTimeBy(300)
            advanceUntilIdle()

            viewModel.loadNextPage()
            viewModel.loadNextPage()
            advanceUntilIdle()

            assertEquals(1, source.queries.count { it.cursor == "cursor-2" })
            assertEquals(listOf("one", "two"), viewModel.state.value.books.map(BookCardDto::id))
            assertFalse(viewModel.state.value.isLoadingMore)
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

private fun book(id: String) = BookCardDto(
    id = id,
    title = id.replaceFirstChar(Char::uppercase),
    authors = listOf("Author"),
    reviewState = "matched",
    sourceFolder = "H:/Audiobooks/$id",
)

private fun page(
    vararg books: BookCardDto,
    nextCursor: String? = null,
) = PagedBooks(
    items = books.toList(),
    nextCursor = nextCursor,
    filters = CatalogFiltersDto(genres = listOf("Science Fiction")),
)

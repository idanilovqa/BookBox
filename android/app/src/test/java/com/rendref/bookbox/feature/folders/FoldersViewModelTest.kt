package com.rendref.bookbox.feature.folders

import com.rendref.bookbox.core.model.BookFileDto
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.BrowseDto
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.repository.BrowseSource
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.test.StandardTestDispatcher
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
class FoldersViewModelTest {
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
    fun `folders sort first stably and breadcrumbs follow navigation`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()

            viewModel.openFolder("Author One")
            advanceUntilIdle()
            viewModel.openFolder("Author One/Book")
            advanceUntilIdle()

            assertEquals(
                listOf(
                    "Alpha",
                    "alpha",
                    "beta",
                    "chapter.m4b",
                    "notes.txt",
                    "zeta.mp3",
                ),
                viewModel.state.value.entries.map(BrowseEntryDto::name),
            )
            assertEquals(
                listOf("Audiobooks", "Author One", "Book"),
                viewModel.state.value.breadcrumbs.map(Breadcrumb::label),
            )

            viewModel.toggleFolderSelection("Author One/Book/Alpha")
            assertEquals(
                setOf("Author One/Book/Alpha"),
                viewModel.state.value.selectedFolderPaths,
            )
            assertTrue(viewModel.navigateBack())
            advanceUntilIdle()
            assertEquals("Author One", viewModel.state.value.path)
        }

    @Test
    fun `raw files opens common folder highlights assigned ids and keeps siblings`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()

            viewModel.viewRawFiles("book-1")
            advanceUntilIdle()

            val state = viewModel.state.value
            assertEquals("Author One/Book", state.path)
            assertEquals("book-1", state.originBookId)
            assertEquals(setOf("file-1"), state.highlightedFileIds)
            assertEquals(
                listOf(
                    "Alpha",
                    "alpha",
                    "beta",
                    "chapter.m4b",
                    "notes.txt",
                    "zeta.mp3",
                ),
                state.entries.map(BrowseEntryDto::name),
            )
            assertTrue("Author One/Book/chapter.m4b" in state.assignedRelativePaths)
            assertFalse("Author One/Book/notes.txt" in state.assignedRelativePaths)
        }

    @Test
    fun `explicit raw navigation wins while initial roots are still loading`() =
        runTest(dispatcher) {
            val rootsResult = CompletableDeferred<List<RootDto>>()
            val listedPaths = mutableListOf<String>()
            val source = object : BrowseSource {
                override fun roots(): Flow<List<RootDto>> = flow {
                    emit(rootsResult.await())
                }

                override fun list(rootId: String, path: String): Flow<BrowseDto> {
                    listedPaths += path
                    return flowOf(BrowseDto(rootId, path, emptyList()))
                }

                override fun bookFiles(bookId: String): Flow<BookFilesDto> =
                    flowOf(
                        BookFilesDto(
                            bookId = bookId,
                            rootId = "audiobooks",
                            sourceFolder = "Author One/Book",
                            files = emptyList(),
                        ),
                    )
            }
            val viewModel = FoldersViewModel(source)
            runCurrent()

            viewModel.viewRawFiles("book-1")
            runCurrent()
            rootsResult.complete(listOf(RootDto("audiobooks", "Audiobooks")))
            advanceUntilIdle()

            assertEquals("Author One/Book", viewModel.state.value.path)
            assertEquals("book-1", viewModel.state.value.originBookId)
            assertEquals(listOf("Author One/Book"), listedPaths)
        }

    private class FakeBrowseSource : BrowseSource {
        override fun roots(): Flow<List<RootDto>> =
            flowOf(listOf(RootDto("audiobooks", "Audiobooks")))

        override fun list(rootId: String, path: String): Flow<BrowseDto> =
            flowOf(
                BrowseDto(
                    rootId = rootId,
                    path = path,
                    entries = when (path) {
                        "" -> listOf(
                            BrowseEntryDto(
                                name = "Author One",
                                relativePath = "Author One",
                                kind = "folder",
                                modifiedAt = "2026-07-26T00:00:00Z",
                            ),
                        )
                        "Author One" -> listOf(
                            BrowseEntryDto(
                                name = "Book",
                                relativePath = "Author One/Book",
                                kind = "folder",
                                modifiedAt = "2026-07-26T00:00:00Z",
                            ),
                        )
                        "Author One/Book" -> listOf(
                            entry("zeta.mp3", "file"),
                            entry("beta", "folder"),
                            entry("Alpha", "folder"),
                            entry("alpha", "folder"),
                            entry("chapter.m4b", "file"),
                            entry("notes.txt", "file"),
                        )
                        else -> emptyList()
                    },
                ),
            )

        override fun bookFiles(bookId: String): Flow<BookFilesDto> =
            flowOf(
                BookFilesDto(
                    bookId = bookId,
                    rootId = "audiobooks",
                    sourceFolder = "Author One/Book",
                    files = listOf(
                        BookFileDto(
                            id = "file-1",
                            relativePath = "Author One/Book/chapter.m4b",
                            name = "chapter.m4b",
                            size = 100,
                            modifiedAt = "2026-07-26T00:00:00Z",
                        ),
                    ),
                ),
            )

        private fun entry(name: String, kind: String) = BrowseEntryDto(
            name = name,
            relativePath = "Author One/Book/$name",
            kind = kind,
            size = if (kind == "file") 10 else null,
            modifiedAt = "2026-07-26T00:00:00Z",
        )
    }
}

package com.rendref.bookbox.feature.folders

import com.rendref.bookbox.core.model.BookFileDto
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.BrowseDto
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.FolderSearchDto
import com.rendref.bookbox.core.model.RawSearchEntryDto
import com.rendref.bookbox.core.model.RawSearchPageDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.network.SearchIndexBuilding
import com.rendref.bookbox.core.repository.BrowseSource
import java.io.IOException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.advanceTimeBy
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

                override fun searchFolders(
                    rootId: String,
                    query: String,
                ): Flow<FolderSearchDto> =
                    flowOf(FolderSearchDto(rootId, query, emptyList()))

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

    @Test
    fun `folder search waits 150ms and matches full paths`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()

            viewModel.updateSearchQuery("black box")
            advanceTimeBy(149)
            runCurrent()
            assertTrue(source.searches.isEmpty())
            assertTrue(viewModel.state.value.isSearching)

            advanceTimeBy(1)
            advanceUntilIdle()

            assertEquals(listOf("black box"), source.searches)
            assertEquals(
                listOf("Author/Black.Box"),
                viewModel.state.value.searchResults.map(RawSearchEntryDto::relativePath),
            )
            assertFalse(viewModel.state.value.isSearching)
        }

    @Test
    fun `new folder query cancels the pending search and symbols clear results`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()

            viewModel.updateSearchQuery("old")
            advanceTimeBy(149)
            viewModel.updateSearchQuery("black box")
            advanceTimeBy(150)
            advanceUntilIdle()

            assertEquals(listOf("black box"), source.searches)

            viewModel.updateSearchQuery(" ... ")
            advanceUntilIdle()
            assertTrue(viewModel.state.value.searchResults.isEmpty())
            assertFalse(viewModel.state.value.isSearching)
            assertEquals(listOf("black box"), source.searches)
        }

    @Test
    fun `two supplementary Unicode letters activate folder search`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            val deseretLetter = "\uD801\uDC00\uD801\uDC01"

            viewModel.updateSearchQuery(deseretLetter)
            advanceUntilIdle()

            assertEquals(listOf(deseretLetter), source.searches)
            assertFalse(viewModel.state.value.isSearching)
        }

    @Test
    fun `clearing search restores the open directory listing`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            viewModel.openFolder("Author One")
            advanceUntilIdle()
            val directoryEntries = viewModel.state.value.entries

            viewModel.updateSearchQuery("black box")
            advanceUntilIdle()
            viewModel.updateSearchQuery("")

            assertEquals("Author One", viewModel.state.value.path)
            assertEquals(directoryEntries, viewModel.state.value.entries)
            assertTrue(viewModel.state.value.searchResults.isEmpty())
        }

    @Test
    fun `opening a search result clears search and loads that folder`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            viewModel.updateSearchQuery("black box")
            advanceUntilIdle()

            viewModel.openSearchResult("Author/Black.Box")
            advanceUntilIdle()

            assertEquals("", viewModel.state.value.searchQuery)
            assertTrue(viewModel.state.value.searchResults.isEmpty())
            assertEquals("Author/Black.Box", viewModel.state.value.path)
        }

    @Test
    fun `changing roots clears a pending folder search`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()

            viewModel.updateSearchQuery("black box")
            advanceTimeBy(149)
            viewModel.selectRoot("archive")
            advanceUntilIdle()

            assertEquals("archive", viewModel.state.value.selectedRootId)
            assertEquals("", viewModel.state.value.searchQuery)
            assertTrue(viewModel.state.value.searchResults.isEmpty())
            assertFalse(viewModel.state.value.isSearching)
            assertTrue(source.searches.isEmpty())
        }

    @Test
    fun `folder search failure preserves directory navigation state`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource().apply {
                failSearch = true
            }
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            viewModel.openFolder("Author One")
            advanceUntilIdle()

            viewModel.updateSearchQuery("black box")
            advanceUntilIdle()

            assertEquals("Author One", viewModel.state.value.path)
            assertEquals("black box", viewModel.state.value.searchQuery)
            assertEquals(
                "Unable to search library folders",
                viewModel.state.value.searchErrorMessage,
            )
            assertFalse(viewModel.state.value.isSearching)
        }

    @Test
    fun `raw search is selected-root only and retains results while index builds`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            viewModel.selectRoot("archive")
            advanceUntilIdle()

            viewModel.updateSearchQuery("black box")
            advanceTimeBy(150)
            advanceUntilIdle()
            val retained = viewModel.state.value.searchResults
            source.buildingQueries += "building"

            viewModel.updateSearchQuery("building")
            runCurrent()
            assertEquals(retained, viewModel.state.value.searchResults)
            assertTrue(viewModel.state.value.isSearching)
            advanceTimeBy(150)
            advanceUntilIdle()

            assertEquals(listOf("archive" to "black box", "archive" to "building"), source.rawSearches)
            assertEquals(retained, viewModel.state.value.searchResults)
            assertTrue(viewModel.state.value.isSearchIndexBuilding)
            assertEquals(null, viewModel.state.value.searchErrorMessage)
        }

    @Test
    fun `raw pagination requires the same snapshot and rejects repeated entries`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource().apply { rawNextCursor = "next" }
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            viewModel.updateSearchQuery("black box")
            advanceTimeBy(150)
            advanceUntilIdle()
            val retained = viewModel.state.value.searchResults

            source.nextRawPage = rawPage(
                rootId = "audiobooks",
                query = "black box",
                entries = listOf(rawEntry("other", "Other/Path", "folder")),
                snapshot = rawSnapshot("audiobooks").copy(
                    scanRunId = "00000000-0000-4000-8000-000000000002",
                ),
            )
            viewModel.loadNextSearchPage()
            assertFalse(viewModel.state.value.isSearching)
            assertTrue(viewModel.state.value.isSearchLoadingMore)
            advanceUntilIdle()
            assertEquals(retained, viewModel.state.value.searchResults)
            assertFalse(viewModel.state.value.isSearchLoadingMore)
            assertEquals("Unable to load more search results", viewModel.state.value.searchErrorMessage)

            source.nextRawPage = rawPage(
                rootId = "audiobooks",
                query = "black box",
                entries = listOf(retained.first()),
            )
            viewModel.loadNextSearchPage()
            advanceUntilIdle()
            assertEquals(retained, viewModel.state.value.searchResults)
            assertEquals("Unable to load more search results", viewModel.state.value.searchErrorMessage)
        }

    @Test
    fun `opening a raw file loads its parent then records the relative path highlight`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            viewModel.updateSearchQuery("chapter file")
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.openSearchResult("Author One/Book/chapter.m4b")
            advanceUntilIdle()

            assertEquals("Author One/Book", viewModel.state.value.path)
            assertEquals(
                "Author One/Book/chapter.m4b",
                viewModel.state.value.searchHighlightedRelativePath,
            )
            assertEquals("", viewModel.state.value.searchQuery)
        }

    @Test
    fun `raw file highlight is omitted when the loaded parent does not contain the file`() =
        runTest(dispatcher) {
            val source = FakeBrowseSource()
            val viewModel = FoldersViewModel(source)
            advanceUntilIdle()
            viewModel.updateSearchQuery("missing file")
            advanceTimeBy(150)
            advanceUntilIdle()

            viewModel.openSearchResult("Author One/Book/missing.m4b")
            advanceUntilIdle()

            assertEquals("Author One/Book", viewModel.state.value.path)
            assertEquals(null, viewModel.state.value.searchHighlightedRelativePath)
        }

    private class FakeBrowseSource : BrowseSource {
        val searches = mutableListOf<String>()
        val rawSearches = mutableListOf<Pair<String, String>>()
        val buildingQueries = mutableSetOf<String>()
        var rawNextCursor: String? = null
        var nextRawPage: RawSearchPageDto? = null
        var failSearch = false

        override fun roots(): Flow<List<RootDto>> =
            flowOf(
                listOf(
                    RootDto("audiobooks", "Audiobooks"),
                    RootDto("archive", "Archive"),
                ),
            )

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

        override fun searchFolders(
            rootId: String,
            query: String,
        ): Flow<FolderSearchDto> {
            searches += query
            if (failSearch) {
                return flow { throw IOException("search failed") }
            }
            return flowOf(
                FolderSearchDto(
                    rootId = rootId,
                    query = query,
                    entries = if (query == "black box") {
                        listOf(
                            BrowseEntryDto(
                                name = "Black.Box",
                                relativePath = "Author/Black.Box",
                                kind = "folder",
                                modifiedAt = "2026-07-29T00:00:00Z",
                            ),
                        )
                    } else {
                        emptyList()
                    },
                ),
            )
        }

        override fun searchRaw(rootId: String, query: String, cursor: String?): Flow<RawSearchPageDto> {
            rawSearches += rootId to query
            searches += query
            if (failSearch) return flow { throw IOException("search failed") }
            if (query in buildingQueries) return flow { throw SearchIndexBuilding() }
            if (cursor != null) return flowOf(checkNotNull(nextRawPage))
            val entries = when (query) {
                "black box" -> listOf(rawEntry("black-box", "Author/Black.Box", "folder"))
                "chapter file" -> listOf(
                    rawEntry("chapter", "Author One/Book/chapter.m4b", "file"),
                )
                "missing file" -> listOf(
                    rawEntry("missing", "Author One/Book/missing.m4b", "file"),
                )
                else -> emptyList()
            }
            return flowOf(rawPage(rootId, query, entries, nextCursor = rawNextCursor))
        }

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

private fun rawEntry(id: String, path: String, kind: String) = RawSearchEntryDto(
    id = id,
    name = path.substringAfterLast('/'),
    relativePath = path,
    kind = kind,
    size = if (kind == "file") 10 else null,
    modifiedAt = if (kind == "file") "2026-08-08T00:00:00Z" else null,
)

private fun rawSnapshot(rootId: String) = SearchSnapshotDto(
    rootId = rootId,
    scanRunId = "00000000-0000-4000-8000-000000000001",
    completedAt = "2026-08-08T00:00:00Z",
)

private fun rawPage(
    rootId: String,
    query: String,
    entries: List<RawSearchEntryDto>,
    snapshot: SearchSnapshotDto = rawSnapshot(rootId),
    nextCursor: String? = null,
) = RawSearchPageDto(rootId, query, snapshot, entries, nextCursor)

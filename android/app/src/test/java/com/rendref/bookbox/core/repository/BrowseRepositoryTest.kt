package com.rendref.bookbox.core.repository

import com.rendref.bookbox.core.model.ApiJson
import com.rendref.bookbox.core.model.BookFileDto
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.BrowseDto
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.model.RootsDto
import com.rendref.bookbox.core.model.RawSearchEntryDto
import com.rendref.bookbox.core.model.RawSearchPageDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.network.AuthenticatedCall
import com.rendref.bookbox.core.network.BrowseApi
import com.rendref.bookbox.core.network.BookBoxClientFactory
import com.rendref.bookbox.core.network.SessionTokens
import com.rendref.bookbox.core.network.TokenSession
import com.rendref.bookbox.core.network.SearchIndexBuilding
import com.rendref.bookbox.core.storage.SavedConnection
import java.io.IOException
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.encodeToString
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class BrowseRepositoryTest {
    @Test
    fun `raw search encodes cursor and validates folder file page`() = runTest {
        withRepository { server, repository ->
            val snapshot = SearchSnapshotDto(
                "audiobooks",
                "11111111-1111-1111-1111-111111111111",
                "2026-08-08T00:00:00Z",
            )
            val page = RawSearchPageDto(
                "audiobooks",
                "dune",
                snapshot,
                listOf(
                    RawSearchEntryDto(
                        "22222222-2222-2222-2222-222222222222",
                        "Dune",
                        "Dune",
                        "folder",
                        duplicateBook = true,
                    ),
                    RawSearchEntryDto(
                        "33333333-3333-3333-3333-333333333333",
                        "book.m4b",
                        "Dune/book.m4b",
                        "file",
                        10,
                        "2026-08-08T00:00:00Z",
                        "44444444-4444-4444-4444-444444444444",
                    ),
                ),
                "next",
            )
            server.enqueue(MockResponse(body = ApiJson.encodeToString(page)))
            val actual = repository.searchRaw("audiobooks", "dune", "c1").first()
            assertEquals(
                "/v1/browse/search?root=audiobooks&q=dune&cursor=c1",
                server.takeRequest().target,
            )
            assertTrue(actual.entries.first().duplicateBook)
            assertEquals(10L, actual.entries.last().size)
        }
    }

    @Test
    fun `raw search rejects hostile shapes and maps exact building response`() = runTest {
        withRepository { server, repository ->
            val snapshot="""{"rootId":"audiobooks","scanRunId":"11111111-1111-1111-1111-111111111111","completedAt":"2026-08-08T00:00:00Z"}"""
            val invalid=listOf(
                """{"rootId":"other","query":"dune","snapshot":$snapshot,"entries":[]}""",
                """{"rootId":"audiobooks","query":"other","snapshot":$snapshot,"entries":[]}""",
                """{"rootId":"audiobooks","query":"dune","snapshot":$snapshot,"entries":[{"id":"22222222-2222-2222-2222-222222222222","name":"bad","relativePath":"../bad","kind":"folder"}]}""",
                """{"rootId":"audiobooks","query":"dune","snapshot":$snapshot,"entries":[{"id":"22222222-2222-2222-2222-222222222222","name":"file","relativePath":"file","kind":"file"}]}""",
                """{"rootId":"audiobooks","query":"dune","snapshot":$snapshot,"entries":[{"id":"22222222-2222-2222-2222-222222222222","name":"A","relativePath":"A","kind":"folder","duplicateGroupId":"not-a-uuid"}]}"""
            )
            invalid.forEach { body -> server.enqueue(MockResponse(body=body)); assertThrows(IOException::class.java){kotlinx.coroutines.runBlocking{repository.searchRaw("audiobooks","dune").first()}} }
            server.enqueue(
                MockResponse(
                    code = 503,
                    body = "{\"error\":{\"code\":\"search_index_building\",\"message\":\"building\"}}",
                ),
            )
            assertThrows(SearchIndexBuilding::class.java){kotlinx.coroutines.runBlocking{repository.searchRaw("audiobooks","dune").first()}}
        }
    }
    @Test
    fun `list encodes root and relative path exactly once`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse(
                    body = ApiJson.encodeToString(
                        directory(path = "Author One/Book #1"),
                    ),
                ),
            )

            repository.list("audiobooks", "Author One/Book #1").first()

            assertEquals(
                "/v1/browse?root=audiobooks&path=Author%20One%2FBook%20%231",
                server.takeRequest().target,
            )
        }
    }

    @Test
    fun `cancelling raw search cancels the in-flight HTTP call`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse.Builder()
                    .body(
                        ApiJson.encodeToString(
                            RawSearchPageDto(
                                rootId = "audiobooks",
                                query = "book",
                                snapshot = SearchSnapshotDto(
                                    rootId = "audiobooks",
                                    scanRunId = "11111111-1111-1111-1111-111111111111",
                                    completedAt = "2026-08-08T00:00:00Z",
                                ),
                                entries = emptyList(),
                            ),
                        ),
                    )
                    .bodyDelay(10, TimeUnit.SECONDS)
                    .build(),
            )
            val search = backgroundScope.launch(Dispatchers.IO) {
                repository.searchRaw("audiobooks", "book").first()
            }
            server.takeRequest()

            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(2_000) {
                    search.cancelAndJoin()
                }
            }

            assertTrue(search.isCancelled)
        }
    }

    @Test
    fun `browse rejects absolute volume and traversal response paths`() = runTest {
        withRepository { server, repository ->
            listOf(
                "/server/absolute",
                "../secret",
                "Author/../secret",
                "C:/Windows",
                "H:\\Audiobooks",
            ).forEach { unsafe ->
                server.enqueue(
                    MockResponse(
                        body = ApiJson.encodeToString(
                            directory(
                                BrowseEntryDto(
                                    name = "unsafe",
                                    relativePath = unsafe,
                                    kind = "file",
                                    size = 1,
                                    modifiedAt = "2026-07-26T00:00:00Z",
                                ),
                            ),
                        ),
                    ),
                )

                assertThrows(IOException::class.java) {
                    kotlinx.coroutines.runBlocking {
                        repository.list("audiobooks", "Author One").first()
                    }
                }
            }
        }
    }

    @Test
    fun `browse rejects non-child mismatched unknown and negative entries`() = runTest {
        withRepository { server, repository ->
            val invalidEntries = listOf(
                BrowseEntryDto(
                    name = "chapter.m4b",
                    relativePath = "Another Book/chapter.m4b",
                    kind = "file",
                    size = 1,
                    modifiedAt = "2026-07-26T00:00:00Z",
                ),
                BrowseEntryDto(
                    name = "control\u0000name",
                    relativePath = "Author One/control\u0000name",
                    kind = "file",
                    size = 1,
                    modifiedAt = "2026-07-26T00:00:00Z",
                ),
                BrowseEntryDto(
                    name = "bad-date.m4b",
                    relativePath = "Author One/bad-date.m4b",
                    kind = "file",
                    size = 1,
                    modifiedAt = "not-an-instant",
                ),
                BrowseEntryDto(
                    name = "different.m4b",
                    relativePath = "Author One/chapter.m4b",
                    kind = "file",
                    size = 1,
                    modifiedAt = "2026-07-26T00:00:00Z",
                ),
                BrowseEntryDto(
                    name = "link",
                    relativePath = "Author One/link",
                    kind = "symlink",
                    modifiedAt = "2026-07-26T00:00:00Z",
                ),
                BrowseEntryDto(
                    name = "negative.m4b",
                    relativePath = "Author One/negative.m4b",
                    kind = "file",
                    size = -1,
                    modifiedAt = "2026-07-26T00:00:00Z",
                ),
            )
            invalidEntries.forEach { entry ->
                server.enqueue(MockResponse(body = ApiJson.encodeToString(directory(entry))))
                assertThrows(IOException::class.java) {
                    kotlinx.coroutines.runBlocking {
                        repository.list("audiobooks", "Author One").first()
                    }
                }
            }
        }
    }

    @Test
    fun `roots reject ids outside lowercase server contract`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse(
                    body = ApiJson.encodeToString(
                        RootsDto(listOf(RootDto("Bad.Root", "Leaked\u0000Name"))),
                    ),
                ),
            )

            assertThrows(IOException::class.java) {
                kotlinx.coroutines.runBlocking { repository.roots().first() }
            }
        }
    }

    @Test
    fun `book files use encoded id and expose only validated relative paths`() = runTest {
        withRepository { server, repository ->
            val files = BookFilesDto(
                bookId = "book #1",
                rootId = "audiobooks",
                sourceFolder = "Author One/Book One",
                files = listOf(
                    BookFileDto(
                        id = "file-1",
                        relativePath = "Author One/Book One/chapter.m4b",
                        name = "chapter.m4b",
                        size = 42,
                        modifiedAt = "2026-07-26T00:00:00Z",
                    ),
                ),
            )
            server.enqueue(MockResponse(body = ApiJson.encodeToString(files)))

            val result = repository.bookFiles("book #1").first()

            assertEquals("/v1/books/book%20%231/files", server.takeRequest().target)
            assertEquals(files, result)

            server.enqueue(
                MockResponse(
                    body = ApiJson.encodeToString(files.copy(sourceFolder = "H:/Audiobooks")),
                ),
            )
            assertThrows(IOException::class.java) {
                kotlinx.coroutines.runBlocking { repository.bookFiles("book #1").first() }
            }

            listOf(
                files.copy(
                    files = files.files.map {
                        it.copy(relativePath = "Author One/Other/chapter.m4b")
                    },
                ),
                files.copy(
                    files = files.files.map { it.copy(name = "different.m4b") },
                ),
                files.copy(
                    files = files.files.map { it.copy(size = -1) },
                ),
            ).forEach { invalid ->
                server.enqueue(MockResponse(body = ApiJson.encodeToString(invalid)))
                assertThrows(IOException::class.java) {
                    kotlinx.coroutines.runBlocking { repository.bookFiles("book #1").first() }
                }
            }
        }
    }

    private suspend fun withRepository(
        block: suspend (MockWebServer, BrowseRepository) -> Unit,
    ) {
        val server = MockWebServer()
        server.start()
        val connection = SavedConnection(
            baseUrl = server.url("/").toString(),
            username = "owner",
        )
        try {
            val calls = AuthenticatedCall(
                BookBoxClientFactory().create(connection),
                StaticTokenSession,
            )
            block(server, BrowseRepository(BrowseApi(calls, server.url("/"))))
        } finally {
            server.close()
        }
    }
}

private fun directory(
    vararg entries: BrowseEntryDto,
    path: String = "Author One",
) = BrowseDto(
    rootId = "audiobooks",
    path = path,
    entries = entries.toList(),
)

private object StaticTokenSession : TokenSession {
    override val accessToken = "access"

    override suspend fun refresh() = SessionTokens("access", "refresh", "expiry")
}

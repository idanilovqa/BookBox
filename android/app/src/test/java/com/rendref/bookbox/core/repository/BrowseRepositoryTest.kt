package com.rendref.bookbox.core.repository

import com.rendref.bookbox.core.model.ApiJson
import com.rendref.bookbox.core.model.BookFileDto
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.BrowseDto
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.FolderSearchDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.model.RootsDto
import com.rendref.bookbox.core.network.AuthenticatedCall
import com.rendref.bookbox.core.network.BrowseApi
import com.rendref.bookbox.core.network.BookBoxClientFactory
import com.rendref.bookbox.core.network.SessionTokens
import com.rendref.bookbox.core.network.TokenSession
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
    fun `folder search encodes query and returns validated folder paths`() = runTest {
        withRepository { server, repository ->
            val result = FolderSearchDto(
                rootId = "audiobooks",
                query = "black box",
                entries = listOf(
                    BrowseEntryDto(
                        name = "Black.Box",
                        relativePath = "Author/Black.Box",
                        kind = "folder",
                        modifiedAt = "2026-07-29T00:00:00Z",
                    ),
                ),
            )
            server.enqueue(MockResponse(body = ApiJson.encodeToString(result)))

            val actual = repository.searchFolders("audiobooks", "black box").first()

            assertEquals(
                "/v1/browse/search?root=audiobooks&q=black%20box",
                server.takeRequest().target,
            )
            assertEquals("Author/Black.Box", actual.entries.single().relativePath)
        }
    }

    @Test
    fun `folder search rejects mismatched files duplicate paths and oversized results`() =
        runTest {
            withRepository { server, repository ->
                val folder = BrowseEntryDto(
                    name = "Book",
                    relativePath = "Author/Book",
                    kind = "folder",
                    modifiedAt = "2026-07-29T00:00:00Z",
                )
                val invalid = listOf(
                    FolderSearchDto("other", "book", listOf(folder)),
                    FolderSearchDto("audiobooks", "other", listOf(folder)),
                    FolderSearchDto(
                        "audiobooks",
                        "book",
                        listOf(folder.copy(kind = "file", size = 1)),
                    ),
                    FolderSearchDto(
                        "audiobooks",
                        "book",
                        listOf(folder.copy(relativePath = "../Book")),
                    ),
                    FolderSearchDto("audiobooks", "book", listOf(folder, folder)),
                    FolderSearchDto(
                        "audiobooks",
                        "book",
                        List(101) { index ->
                            folder.copy(
                                name = "Book $index",
                                relativePath = "Author/Book $index",
                            )
                        },
                    ),
                )
                invalid.forEach { response ->
                    server.enqueue(MockResponse(body = ApiJson.encodeToString(response)))
                    assertThrows(IOException::class.java) {
                        kotlinx.coroutines.runBlocking {
                            repository.searchFolders("audiobooks", "book").first()
                        }
                    }
                }
            }
        }

    @Test
    fun `cancelling folder search cancels the in-flight HTTP call`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse.Builder()
                    .body(
                        ApiJson.encodeToString(
                            FolderSearchDto("audiobooks", "book", emptyList()),
                        ),
                    )
                    .bodyDelay(10, TimeUnit.SECONDS)
                    .build(),
            )
            val search = backgroundScope.launch(Dispatchers.IO) {
                repository.searchFolders("audiobooks", "book").first()
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

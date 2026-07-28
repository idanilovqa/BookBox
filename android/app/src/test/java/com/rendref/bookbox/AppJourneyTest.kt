package com.rendref.bookbox

import com.rendref.bookbox.core.network.AuthenticatedCall
import com.rendref.bookbox.core.network.BrowseApi
import com.rendref.bookbox.core.network.CatalogApi
import com.rendref.bookbox.core.network.BookBoxClientFactory
import com.rendref.bookbox.core.network.ReviewApi
import com.rendref.bookbox.core.network.ReviewGroupingRequest
import com.rendref.bookbox.core.network.SessionTokens
import com.rendref.bookbox.core.network.TokenSession
import com.rendref.bookbox.core.network.TransferManifestApi
import com.rendref.bookbox.core.network.TransferSelection
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.storage.SavedConnection
import java.util.UUID
import kotlinx.coroutines.test.runTest
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class AppJourneyTest {
    @Test
    fun `authenticated app journey sends only approved read and derived catalog routes`() =
        runTest {
            val bookId = UUID.randomUUID().toString()
            val matchId = UUID.randomUUID().toString()
            val jobId = UUID.randomUUID().toString()
            withBookBoxServer(
                listOf(
                    MockResponse(
                        body = """
                        {
                          "items":[{
                            "id":"$bookId",
                            "title":"Dune",
                            "authors":["Frank Herbert"],
                            "publicationYear":1965,
                            "genres":["Science Fiction"],
                            "reviewState":"matched",
                            "sourceFolder":"Frank Herbert/Dune"
                          }]
                        }
                        """.trimIndent(),
                    ),
                    MockResponse(body = """{"roots":[{"id":"audiobooks","name":"Audiobooks"}]}"""),
                    MockResponse(
                        body = """
                        {
                          "id":"$bookId",
                          "rootId":"audiobooks",
                          "title":"Dune",
                          "authors":["Frank Herbert"],
                          "reviewState":"matched",
                          "sourceFolder":"Frank Herbert/Dune"
                        }
                        """.trimIndent(),
                    ),
                    MockResponse(
                        body = """
                        {
                          "bookId":"$bookId",
                          "candidates":[{
                            "matchId":"$matchId",
                            "provider":"open_library",
                            "providerId":"OL123M",
                            "title":"Dune",
                            "authors":["Frank Herbert"],
                            "score":0.98
                          }]
                        }
                        """.trimIndent(),
                    ),
                    MockResponse(
                        code = 202,
                        body = """{"jobId":"$jobId","status":"queued"}""",
                    ),
                    MockResponse(code = 204),
                    MockResponse(body = validJourneyManifest()),
                ),
            ) { server, calls ->
                CatalogApi(calls, server.url("/")).books(BookQuery(text = "dune"))
                BrowseApi(calls, server.url("/")).roots()
                val review = ReviewApi(calls, server.url("/"))
                review.bookDetail(bookId)
                review.searchMatches(bookId, "Dune", "Frank Herbert", "")
                review.confirmMatch(bookId, matchId)
                review.saveGrouping(
                    bookId,
                    listOf(
                        ReviewGroupingRequest(
                            bookId = bookId,
                            fileIds = listOf(UUID.randomUUID().toString()),
                        ),
                        ReviewGroupingRequest(
                            fileIds = listOf(UUID.randomUUID().toString()),
                        ),
                    ),
                )
                TransferManifestApi(calls, server.url("/")).create(
                    listOf(TransferSelection.Book(bookId)),
                )

                val actual = buildList {
                    repeat(server.requestCount) {
                        val request = server.takeRequest()
                        assertEquals("Bearer access-sentinel", request.headers["Authorization"])
                        add("${request.method} ${request.url.encodedPath}")
                    }
                }
                assertEquals(
                    listOf(
                        "GET /v1/books",
                        "GET /v1/roots",
                        "GET /v1/books/$bookId",
                        "POST /v1/catalog/matches/search",
                        "PUT /v1/books/$bookId/match",
                        "PUT /v1/books/$bookId/grouping",
                        "POST /v1/transfers/manifest",
                    ),
                    actual,
                )
                actual.forEach { route ->
                    assertFalse(route.startsWith("DELETE "))
                    assertFalse(route.startsWith("PATCH "))
                    assertFalse(route.contains("/upload"))
                    assertFalse(route.contains("/rename"))
                    assertFalse(route.contains("/move"))
                    assertFalse(route.contains("/tags"))
                }
            }
        }

    private suspend fun withBookBoxServer(
        responses: List<MockResponse>,
        block: suspend (MockWebServer, AuthenticatedCall) -> Unit,
    ) {
        val server = MockWebServer()
        responses.forEach(server::enqueue)
        server.start()
        val connection = SavedConnection(
            baseUrl = server.url("/").toString(),
            username = "owner",
        )
        val session = object : TokenSession {
            override val accessToken: String = "access-sentinel"

            override suspend fun refresh(): SessionTokens =
                error("Refresh was not expected")
        }
        try {
            block(
                server,
                AuthenticatedCall(BookBoxClientFactory().create(connection), session),
            )
        } finally {
            server.close()
        }
    }
}

private fun validJourneyManifest() =
    """
    {
      "id":"AQIDBAUGBwgJCgsMDQ4PEA",
      "files":[{
        "fileId":"signed-file-id",
        "sourcePath":"Frank Herbert/Dune/Dune.m4b",
        "destinationPath":"Dune/Dune.m4b",
        "size":4,
        "modifiedAt":"2026-07-26T12:00:00Z",
        "etag":"\"source-etag\"",
        "sha256":"72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0"
      }],
      "totalBytes":4
    }
    """.trimIndent()

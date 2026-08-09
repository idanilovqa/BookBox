package com.rendref.bookbox.core.repository

import com.rendref.bookbox.core.network.AuthenticatedCall
import com.rendref.bookbox.core.network.BookBoxClientFactory
import com.rendref.bookbox.core.network.ReviewApi
import com.rendref.bookbox.core.network.SessionTokens
import com.rendref.bookbox.core.network.TokenSession
import com.rendref.bookbox.core.storage.SavedConnection
import java.io.IOException
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class ReviewRepositoryTest {
    @Test
    fun `match search sends only local id and bounded search terms`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse(
                    body = """{"bookId":"$BOOK_ID","candidates":[]}""",
                ),
            )

            repository.searchMatches(
                bookId = BOOK_ID,
                title = "Dune",
                author = "Frank Herbert",
                isbn = "9780441172719",
            )

            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/v1/catalog/matches/search", request.target)
            val body = Json.parseToJsonElement(request.body?.utf8().orEmpty()).jsonObject
            assertEquals(setOf("bookId", "title", "author", "isbn"), body.keys)
            assertEquals("\"$BOOK_ID\"", body["bookId"].toString())
            assertNoSourceMutationFields(request.body?.utf8().orEmpty())
        }
    }

    @Test
    fun `match search accepts opaque Open Library provider ids`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse(
                    body = """
                        {
                          "bookId":"$BOOK_ID",
                          "candidates":[{
                            "matchId":"$MATCH_ID",
                            "provider":"openlibrary",
                            "providerId":"/works/OL893415W",
                            "title":"Dune",
                            "authors":["Frank Herbert"],
                            "score":0.98
                          }]
                        }
                    """.trimIndent(),
                ),
            )

            val result = repository.searchMatches(
                bookId = BOOK_ID,
                title = "Dune",
                author = "Frank Herbert",
            )

            assertEquals("/works/OL893415W", result.candidates.single().providerId)
        }
    }

    @Test
    fun `detail accepts opaque Open Library identifier values`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse(
                    body = """
                        {
                          "id":"$BOOK_ID","rootId":"audiobooks","title":"Dune",
                          "authors":["Frank Herbert"],"reviewState":"confirmed",
                          "sourceFolder":"Dune",
                          "identifiers":{"openlibrary":"/works/OL893415W"}
                        }
                    """.trimIndent(),
                ),
            )

            val result = repository.bookDetail(BOOK_ID)

            assertEquals("/works/OL893415W", result.identifiers["openlibrary"])
        }
    }

    @Test
    fun `grouping context includes sibling assignments and loose folder audio`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse(
                    body = """
                        {
                          "bookId":"$BOOK_ID",
                          "rootId":"audiobooks",
                          "sourceFolder":"Omnibus",
                          "files":[
                            {
                              "id":"$FILE_ONE_ID",
                              "relativePath":"Omnibus/First.m4b",
                              "name":"First.m4b",
                              "size":101,
                              "modifiedAt":"2026-07-26T12:34:56Z"
                            },
                            {
                              "id":"$FILE_TWO_ID",
                              "relativePath":"Omnibus/Second.m4b",
                              "name":"Second.m4b",
                              "size":202,
                              "modifiedAt":"2026-07-26T12:34:56Z"
                            },
                            {
                              "id":"$LOOSE_FILE_ID",
                              "relativePath":"Omnibus/Loose.flac",
                              "name":"Loose.flac",
                              "size":303,
                              "modifiedAt":"2026-07-26T12:34:56Z"
                            }
                          ],
                          "books":[
                            {
                              "bookId":"$BOOK_ID",
                              "title":"First",
                              "fileIds":["$FILE_ONE_ID"]
                            },
                            {
                              "bookId":"$SIBLING_BOOK_ID",
                              "title":"Second",
                              "fileIds":[]
                            }
                          ]
                        }
                    """.trimIndent(),
                ),
            )

            val result = repository.groupingContext(BOOK_ID)

            assertEquals("/v1/books/$BOOK_ID/grouping", server.takeRequest().target)
            assertEquals(3, result.files.size)
            assertEquals(
                listOf(BOOK_ID, SIBLING_BOOK_ID),
                result.books.map { it.bookId },
            )
            assertFalse(result.books.flatMap { it.fileIds }.contains(LOOSE_FILE_ID))
        }
    }

    @Test
    fun `expanded grouping context decodes evidence roles companions capability and save generation`() =
        runTest {
            withRepository { server, repository ->
                server.enqueue(
                    MockResponse(
                        body = """
                            {
                              "bookId":"$BOOK_ID",
                              "rootId":"audiobooks",
                              "scopePath":"Omnibus",
                              "reviewReason":"probe_failed",
                              "evidence":{
                                "audio":["Omnibus/Disc 1/First.m4b"],
                                "sidecar":["Omnibus/book.cue"]
                              },
                              "canReorganize":false,
                              "files":[
                                {"id":"$FILE_ONE_ID","relativePath":"Omnibus/Disc 1/First.m4b","name":"First.m4b","size":101,"modifiedAt":"2026-07-26T12:34:56Z","role":"audio","companionKind":"","shared":false},
                                {"id":"$FILE_TWO_ID","relativePath":"Omnibus/Disc 2/Second.flac","name":"Second.flac","size":202,"modifiedAt":"2026-07-26T12:34:56Z","role":"audio","companionKind":"","shared":false},
                                {"id":"$COMPANION_ID","relativePath":"Omnibus/book.cue","name":"book.cue","size":12,"modifiedAt":"2026-07-26T12:34:56Z","role":"sidecar","companionKind":"cue","shared":false},
                                {"id":"$COVER_ID","relativePath":"Omnibus/cover.jpg","name":"cover.jpg","size":22,"modifiedAt":"2026-07-26T12:34:56Z","role":"cover","companionKind":"","shared":true}
                              ],
                              "books":[
                                {"bookId":"$BOOK_ID","title":"First","fileIds":["$FILE_ONE_ID","$COMPANION_ID","$COVER_ID"]},
                                {"bookId":"$SIBLING_BOOK_ID","title":"Second","fileIds":["$FILE_TWO_ID","$COVER_ID"]}
                              ]
                            }
                        """.trimIndent(),
                    ),
                )

                val context = repository.groupingContext(BOOK_ID)

                assertEquals("Omnibus", context.scopePath)
                assertEquals("probe_failed", context.reviewReason)
                assertEquals(listOf("Omnibus/book.cue"), context.evidence["sidecar"])
                assertFalse(context.canReorganize)
                assertEquals("audio", context.files.first().role)
                assertEquals("cue", context.files[2].companionKind)
                assertTrue(context.files.last().shared)
                assertEquals("/v1/books/$BOOK_ID/grouping", server.takeRequest().target)

                server.enqueue(MockResponse(body = """{"catalogGeneration":8}"""))
                val generation = repository.saveGrouping(
                    BOOK_ID,
                    groups = listOf(
                        GroupingDecision(
                            bookId = BOOK_ID,
                            fileIds = listOf(FILE_ONE_ID),
                            companionFileIds = listOf(COMPANION_ID, COVER_ID),
                        ),
                        GroupingDecision(
                            bookId = SIBLING_BOOK_ID,
                            fileIds = listOf(FILE_TWO_ID),
                            companionFileIds = listOf(COVER_ID),
                        ),
                    ),
                    expectedFiles = context.files,
                )

                assertEquals(8L, generation)
                val body = Json.parseToJsonElement(
                    server.takeRequest().body?.utf8().orEmpty(),
                ).jsonObject
                assertEquals(setOf("expectedFiles", "groups"), body.keys)
                assertEquals(4, body.getValue("expectedFiles").jsonArray.size)
                val groups = body.getValue("groups").jsonArray
                assertEquals(2, groups.size)
                assertEquals(
                    listOf(FILE_ONE_ID, COMPANION_ID, COVER_ID),
                    groups[0].jsonObject.getValue("fileIds").jsonArray.map {
                        it.jsonPrimitive.content
                    },
                )
                assertEquals(
                    COVER_ID,
                    groups[1].jsonObject.getValue("fileIds").jsonArray.last()
                        .jsonPrimitive.content,
                )
            }
        }

    @Test
    fun `confirmation sends one stored match id and grouping sends UUID assignments only`() =
        runTest {
            withRepository { server, repository ->
                server.enqueue(
                    MockResponse(
                        code = 202,
                        body = """{"jobId":"$JOB_ID","status":"pending"}""",
                    ),
                )
                repository.confirmMatch(BOOK_ID, MATCH_ID)
                val confirmation = server.takeRequest()
                assertEquals("PUT", confirmation.method)
                assertEquals("/v1/books/$BOOK_ID/match", confirmation.target)
                assertEquals(
                    setOf("matchId"),
                    Json.parseToJsonElement(
                        confirmation.body?.utf8().orEmpty(),
                    ).jsonObject.keys,
                )
                assertNoSourceMutationFields(confirmation.body?.utf8().orEmpty())

                server.enqueue(MockResponse(code = 204))
                repository.saveGrouping(
                    BOOK_ID,
                    listOf(
                        GroupingDecision(BOOK_ID, listOf(FILE_ONE_ID)),
                        GroupingDecision(null, listOf(FILE_TWO_ID)),
                    ),
                )
                val grouping = server.takeRequest()
                assertEquals("PUT", grouping.method)
                assertEquals("/v1/books/$BOOK_ID/grouping", grouping.target)
                val body = grouping.body?.utf8().orEmpty()
                val root = Json.parseToJsonElement(body).jsonObject
                assertEquals(setOf("groups"), root.keys)
                assertFalse(body.contains("chapter-one.m4b"))
                assertNoSourceMutationFields(body)
            }
        }

    @Test
    fun `detail rejects absolute source folders before UI`() = runTest {
        withRepository { server, repository ->
            server.enqueue(
                MockResponse(
                    body = """
                        {
                          "id":"$BOOK_ID","rootId":"audiobooks","title":"Dune",
                          "authors":["Frank Herbert"],"reviewState":"confirmed",
                          "sourceFolder":"H:/Audiobooks/Dune"
                        }
                    """.trimIndent(),
                ),
            )

            assertThrows(IOException::class.java) {
                kotlinx.coroutines.runBlocking { repository.bookDetail(BOOK_ID) }
            }
        }
    }

    private suspend fun withRepository(
        block: suspend (MockWebServer, ReviewRepository) -> Unit,
    ) {
        val server = MockWebServer()
        server.start()
        val saved = SavedConnection(
            baseUrl = server.url("/").toString(),
            username = "owner",
        )
        try {
            val calls = AuthenticatedCall(
                BookBoxClientFactory().create(saved),
                ReviewTokenSession,
            )
            block(server, ReviewRepository(ReviewApi(calls, server.url("/"))))
        } finally {
            server.close()
        }
    }
}

private fun assertNoSourceMutationFields(body: String) {
    listOf(
        "absolutePath",
        "sourcePath",
        "newFilename",
        "filename",
        "tags",
        "delete",
        "moveDestination",
        "upload",
        "H:/",
        "H:\\\\",
    ).forEach { forbidden ->
        assertFalse("body contains $forbidden: $body", body.contains(forbidden, true))
    }
}

private object ReviewTokenSession : TokenSession {
    override val accessToken = "access"

    override suspend fun refresh() = SessionTokens("access", "refresh", "expiry")
}

private const val BOOK_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
private const val MATCH_ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
private const val JOB_ID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
private const val FILE_ONE_ID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
private const val FILE_TWO_ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
private const val LOOSE_FILE_ID = "ffffffff-ffff-4fff-8fff-ffffffffffff"
private const val SIBLING_BOOK_ID = "11111111-1111-4111-8111-111111111111"
private const val COMPANION_ID = "22222222-2222-4222-8222-222222222222"
private const val COVER_ID = "33333333-3333-4333-8333-333333333333"

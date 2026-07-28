package com.rendref.bookbox.core.network

import java.io.IOException
import java.util.UUID
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okhttp3.OkHttpClient
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class TransferManifestApiTest {
    @Test
    fun `book request is one authenticated approved manifest post`() = runTest {
        withServer(validManifestResponse()) { server, client ->
            val api = TransferManifestApi(client, server.url("/"))
            val bookId = UUID.randomUUID().toString()

            val manifest = api.create(
                listOf(TransferSelection.Book(bookId)),
            )

            assertEquals(MANIFEST_ID, manifest.id)
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/v1/transfers/manifest", request.url.encodedPath)
            assertEquals("Bearer access-sentinel", request.headers["Authorization"])
            assertEquals("application/json; charset=utf-8", request.headers["Content-Type"])
            val root = Json.parseToJsonElement(checkNotNull(request.body).utf8()).jsonObject
            assertEquals(setOf("selections"), root.keys)
            val selection = root.getValue("selections").jsonArray.single().jsonObject
            assertEquals(setOf("kind", "bookId"), selection.keys)
            assertEquals("book", selection.getValue("kind").jsonPrimitive.content)
            assertEquals(bookId, selection.getValue("bookId").jsonPrimitive.content)
            assertEquals(1, server.requestCount)
        }
    }

    @Test
    fun `raw folder request contains only root relative path and folder kind`() = runTest {
        withServer(validManifestResponse()) { server, client ->
            val api = TransferManifestApi(client, server.url("/"))

            api.create(
                listOf(
                    TransferSelection.Folder(
                        rootId = "audiobooks",
                        relativePath = "Author One/Book #1",
                    ),
                ),
            )

            val request = server.takeRequest()
            val selection = Json.parseToJsonElement(
                checkNotNull(request.body).utf8(),
            ).jsonObject.getValue("selections").jsonArray.single().jsonObject
            assertEquals(setOf("rootId", "relativePath", "kind"), selection.keys)
            assertEquals("audiobooks", selection.getValue("rootId").jsonPrimitive.content)
            assertEquals(
                "Author One/Book #1",
                selection.getValue("relativePath").jsonPrimitive.content,
            )
            assertEquals("folder", selection.getValue("kind").jsonPrimitive.content)
            assertEquals(1, server.requestCount)
        }
    }

    @Test
    fun `mixed unsafe or empty selections are rejected before any request`() = runTest {
        withServer(validManifestResponse()) { server, client ->
            val api = TransferManifestApi(client, server.url("/"))
            val book = TransferSelection.Book(UUID.randomUUID().toString())

            listOf(
                emptyList(),
                listOf(book, TransferSelection.Folder("audiobooks", "Book")),
                listOf(TransferSelection.Book("not-a-uuid")),
                listOf(TransferSelection.Folder("audiobooks", "../private")),
                listOf(TransferSelection.Folder("H:", "Audiobooks")),
            ).forEach { selections ->
                assertThrows(IllegalArgumentException::class.java) {
                    kotlinx.coroutines.runBlocking { api.create(selections) }
                }
            }

            assertEquals(0, server.requestCount)
        }
    }

    @Test
    fun `invalid manifest paths sizes digests and duplicates are rejected privately`() =
        runTest {
            val invalidBodies = listOf(
                validManifestBody().replace(
                    "\"id\":\"$MANIFEST_ID\"",
                    "\"id\":\"../private\"",
                ),
                validManifestBody().replace(
                    "\"sourcePath\":\"Book/chapter.m4b\"",
                    "\"sourcePath\":\"/storage/private.m4b\"",
                ),
                validManifestBody().replace(
                    "\"sourcePath\":\"Book/chapter.m4b\"",
                    "\"sourcePath\":\"../private.m4b\"",
                ),
                validManifestBody().replace(
                    "\"destinationPath\":\"Book/chapter.m4b\"",
                    "\"destinationPath\":\"H:/Audiobooks/private.m4b\"",
                ),
                validManifestBody()
                    .replace("\"size\":4", "\"size\":-1")
                    .replace("\"totalBytes\":4", "\"totalBytes\":-1"),
                validManifestBody().replace("\"totalBytes\":4", "\"totalBytes\":5"),
                validManifestBody().replace(
                    SHA256,
                    "not-a-digest",
                ),
                validManifestBody().replace(
                    "\"modifiedAt\":\"2026-07-26T12:00:00Z\"",
                    "\"modifiedAt\":\"not-an-instant\"",
                ),
                validManifestBody().replace(
                    "\"etag\":\"\\\"source-etag\\\"\"",
                    "\"etag\":\"\"",
                ),
                validManifestBody().replace(
                    "\"etag\":\"\\\"source-etag\\\"\"",
                    "\"etag\":\"bad\\netag\"",
                ),
                """
                {"id":"$MANIFEST_ID","files":[],"totalBytes":0}
                """.trimIndent(),
                validManifestBody().replace(
                    "\n  ]",
                    ",\n    " +
                        validFileBody().replace(
                            "\"destinationPath\":\"Book/chapter.m4b\"",
                            "\"destinationPath\":\"book/CHAPTER.m4b\"",
                        ) +
                        "\n  ]",
                ).replace("\"totalBytes\":4", "\"totalBytes\":8"),
                overflowingManifestBody(),
            )

            invalidBodies.forEach { body ->
                withServer(MockResponse(body = body)) { server, client ->
                    val api = TransferManifestApi(client, server.url("/"))

                    val failure = assertThrows(IOException::class.java) {
                        kotlinx.coroutines.runBlocking {
                            api.create(
                                listOf(TransferSelection.Book(UUID.randomUUID().toString())),
                            )
                        }
                    }

                    assertEquals("Server returned an invalid transfer manifest", failure.message)
                    assertEquals(1, server.requestCount)
                }
            }
        }

    private suspend fun withServer(
        response: MockResponse,
        block: suspend (MockWebServer, OkHttpClient) -> Unit,
    ) {
        val server = MockWebServer()
        server.enqueue(response)
        server.start()
        val client = OkHttpClient.Builder()
            .addInterceptor { chain ->
                chain.proceed(
                    chain.request().newBuilder()
                        .header("Authorization", "Bearer access-sentinel")
                        .build(),
                )
            }
            .build()
        try {
            block(server, client)
        } finally {
            server.close()
        }
    }
}

private fun validManifestResponse() = MockResponse(body = validManifestBody())

private fun validManifestBody() =
    """
    {
      "id":"$MANIFEST_ID",
      "files":[
        ${validFileBody()}
      ],
      "totalBytes":4
    }
    """.trimIndent()

private fun validFileBody() =
    """
    {
      "fileId":"signed-file-id",
      "sourcePath":"Book/chapter.m4b",
      "destinationPath":"Book/chapter.m4b",
      "size":4,
      "modifiedAt":"2026-07-26T12:00:00Z",
      "etag":"\"source-etag\"",
      "sha256":"$SHA256"
    }
    """.trimIndent()

private fun overflowingManifestBody() =
    """
    {
      "id":"$MANIFEST_ID",
      "files":[
        ${validFileBody().replace("\"size\":4", "\"size\":9223372036854775807")},
        ${validFileBody()
            .replace("\"fileId\":\"signed-file-id\"", "\"fileId\":\"signed-file-id-2\"")
            .replace(
                "\"sourcePath\":\"Book/chapter.m4b\"",
                "\"sourcePath\":\"Book/chapter2.m4b\"",
            )
            .replace(
                "\"destinationPath\":\"Book/chapter.m4b\"",
                "\"destinationPath\":\"Book/chapter2.m4b\"",
            )
            .replace("\"size\":4", "\"size\":1")}
      ],
      "totalBytes":9223372036854775807
    }
    """.trimIndent()

private const val SHA256 =
    "72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0"
private const val MANIFEST_ID = "AQIDBAUGBwgJCgsMDQ4PEA"

package com.rendref.netstorage.core.repository

import com.rendref.netstorage.core.network.AuthenticatedCall
import com.rendref.netstorage.core.network.CatalogApi
import com.rendref.netstorage.core.network.SessionTokens
import com.rendref.netstorage.core.network.TokenSession
import com.rendref.netstorage.core.network.sha256Fingerprint
import com.rendref.netstorage.core.network.PinnedClientFactory
import com.rendref.netstorage.core.storage.SavedConnection
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.io.IOException
import java.util.Collections
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runTest
import mockwebserver3.Dispatcher
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import mockwebserver3.RecordedRequest
import okhttp3.Request
import okhttp3.Call
import okhttp3.Callback
import okhttp3.MediaType
import okhttp3.RequestBody
import okhttp3.Response
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import okio.BufferedSink
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class CatalogRepositoryTest {
    @Test
    fun `concurrent 401 responses perform one serialized refresh and retry once`() = runTest {
        withPinnedServer { server, client ->
            val oldRequests = CountDownLatch(2)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    return if (request.headers["Authorization"] == "Bearer access-old") {
                        oldRequests.countDown()
                        check(oldRequests.await(5, TimeUnit.SECONDS))
                        MockResponse(code = 401)
                    } else {
                        MockResponse(code = 200, body = """{"ok":true}""")
                    }
                }
            }
            val session = FakeTokenSession("access-old")
            val calls = AuthenticatedCall(client, session)
            val request = Request.Builder().url(server.url("/v1/books")).build()

            val statuses = coroutineScope {
                listOf(
                    async(Dispatchers.IO) {
                        calls.newCall(request).execute().use { it.code }
                    },
                    async(Dispatchers.IO) {
                        calls.newCall(request).execute().use { it.code }
                    },
                ).awaitAll()
            }

            assertEquals(listOf(200, 200), statuses)
            assertEquals(1, session.refreshCalls.get())
            assertEquals(4, server.requestCount)
        }
    }

    @Test
    fun `a second 401 is returned without another refresh loop`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(MockResponse(code = 401))
            server.enqueue(MockResponse(code = 401))
            val session = FakeTokenSession("access-old")
            val calls = AuthenticatedCall(client, session)
            val request = Request.Builder().url(server.url("/v1/books")).build()

            val status = calls.newCall(request).execute().use { it.code }

            assertEquals(401, status)
            assertEquals(1, session.refreshCalls.get())
            assertEquals(2, server.requestCount)
        }
    }

    @Test
    fun `asynchronous concurrent 401 responses share one refresh for Coil calls`() = runTest {
        withPinnedServer { server, client ->
            val oldRequests = CountDownLatch(2)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse =
                    if (request.headers["Authorization"] == "Bearer access-old") {
                        oldRequests.countDown()
                        check(oldRequests.await(5, TimeUnit.SECONDS))
                        MockResponse(code = 401)
                    } else {
                        MockResponse(code = 200, body = "cover")
                    }
            }
            val session = FakeTokenSession("access-old")
            val calls = AuthenticatedCall(client, session)
            val callbackLatch = CountDownLatch(2)
            val statuses = Collections.synchronizedList(mutableListOf<Int>())
            val failures = Collections.synchronizedList(mutableListOf<IOException>())
            val callback = object : Callback {
                override fun onFailure(call: Call, e: IOException) {
                    failures += e
                    callbackLatch.countDown()
                }

                override fun onResponse(call: Call, response: Response) {
                    response.use { statuses += it.code }
                    callbackLatch.countDown()
                }
            }

            calls.newCall(Request.Builder().url(server.url("/cover/1")).build())
                .enqueue(callback)
            calls.newCall(Request.Builder().url(server.url("/cover/2")).build())
                .enqueue(callback)
            assertTrue(callbackLatch.await(10, TimeUnit.SECONDS))

            assertEquals(emptyList<IOException>(), failures)
            assertEquals(listOf(200, 200), statuses.sorted())
            assertEquals(1, session.refreshCalls.get())
        }
    }

    @Test
    fun `null token refreshes before request and caller authorization is replaced`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(MockResponse(code = 200))
            val session = FakeTokenSession(null)
            val calls = AuthenticatedCall(client, session)
            val request = Request.Builder()
                .url(server.url("/v1/books"))
                .header("Authorization", "Bearer caller-controlled")
                .build()

            calls.newCall(request).execute().close()

            assertEquals("Bearer access-new", server.takeRequest().headers["Authorization"])
            assertEquals(1, session.refreshCalls.get())
        }
    }

    @Test
    fun `refresh failure is redacted and sends no retry`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(MockResponse(code = 401))
            val session = FakeTokenSession(
                initialAccessToken = "access-old",
                refreshFailure = IllegalStateException("refresh-token-sentinel"),
            )
            val calls = AuthenticatedCall(client, session)

            val failure = assertThrows(IOException::class.java) {
                calls.newCall(Request.Builder().url(server.url("/v1/books")).build())
                    .execute()
                    .close()
            }

            assertFalse(failure.stackTraceToString().contains("refresh-token-sentinel"))
            assertEquals(1, server.requestCount)
        }
    }

    @Test
    fun `refresh cancellation is preserved and sends no retry`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(MockResponse(code = 401))
            val calls = AuthenticatedCall(
                client,
                FakeTokenSession(
                    initialAccessToken = "access-old",
                    refreshFailure = CancellationException("cancel refresh"),
                ),
            )

            assertThrows(CancellationException::class.java) {
                calls.newCall(Request.Builder().url(server.url("/v1/books")).build())
                    .execute()
                    .close()
            }
            assertEquals(1, server.requestCount)
        }
    }

    @Test
    fun `one shot request body is never retried`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(MockResponse(code = 401))
            val session = FakeTokenSession("access-old")
            val calls = AuthenticatedCall(client, session)
            val oneShotBody = object : RequestBody() {
                override fun contentType(): MediaType? = null

                override fun contentLength(): Long = 0

                override fun isOneShot(): Boolean = true

                override fun writeTo(sink: BufferedSink) = Unit
            }
            val request = Request.Builder()
                .url(server.url("/one-shot"))
                .post(oneShotBody)
                .build()

            val status = calls.newCall(request).execute().use { it.code }

            assertEquals(401, status)
            assertEquals(0, session.refreshCalls.get())
            assertEquals(1, server.requestCount)
        }
    }

    @Test
    fun `repository sends local search filters sort and cursor`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(
                MockResponse(
                    body = """{"items":[],"nextCursor":null,"filters":{"genres":["Science Fiction"],"years":[1965]}}""",
                ),
            )
            val session = FakeTokenSession("access-current")
            val repository = CatalogRepository(
                CatalogApi(AuthenticatedCall(client, session), server.url("/")),
            )

            val page = repository.books(
                BookQuery(
                    text = "dune",
                    genre = "Science Fiction",
                    year = 1965,
                    sort = BookSort.YEAR,
                    cursor = "c1",
                ),
            ).first()

            assertEquals(
                "/v1/books?q=dune&genre=Science%20Fiction&year=1965&sort=year&cursor=c1",
                server.takeRequest().target,
            )
            assertEquals(listOf("Science Fiction"), page.filters.genres)
            assertNull(page.nextCursor)
        }
    }

    @Test
    fun `repository decodes audiobook cards and next cursor`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(
                MockResponse(
                    body = """
                        {
                          "items":[{
                            "id":"b1","title":"Dune","authors":["Frank Herbert"],
                            "publicationYear":1965,"genres":["Science Fiction"],
                            "series":"Dune","narrators":["Simon Vance"],
                            "coverUrl":"/v1/books/b1/cover","reviewState":"confirmed",
                            "sourceFolder":"Dune"
                          }],
                          "nextCursor":"page-2",
                          "filters":{"genres":["Science Fiction"],"years":[1965],"reviewStates":["confirmed"]}
                        }
                    """.trimIndent(),
                ),
            )
            val repository = CatalogRepository(
                CatalogApi(
                    AuthenticatedCall(client, FakeTokenSession("access-current")),
                    server.url("/"),
                ),
            )

            val page = repository.books(BookQuery()).first()

            assertEquals("Dune", page.items.single().title)
            assertEquals(
                server.url("/v1/books/b1/cover").toString(),
                page.items.single().coverUrl,
            )
            assertEquals("page-2", page.nextCursor)
            assertEquals(listOf("confirmed"), page.filters.reviewStates)
        }
    }

    @Test
    fun `repository rejects cover URL outside saved exact origin`() = runTest {
        withPinnedServer { server, client ->
            server.enqueue(
                MockResponse(
                    body = """
                        {
                          "items":[{
                            "id":"b1","title":"Dune","authors":["Frank Herbert"],
                            "coverUrl":"https://localhost:443/cover",
                            "reviewState":"confirmed","sourceFolder":"Dune"
                          }],
                          "filters":{}
                        }
                    """.trimIndent(),
                ),
            )
            val repository = CatalogRepository(
                CatalogApi(
                    AuthenticatedCall(client, FakeTokenSession("access-current")),
                    server.url("/"),
                ),
            )

            assertThrows(IOException::class.java) {
                kotlinx.coroutines.runBlocking { repository.books(BookQuery()).first() }
            }
        }
    }

    @Test
    fun `repository rejects credentials and non-cover routes in cover URLs`() = runTest {
        withPinnedServer { server, client ->
            val invalidCoverUrls = listOf(
                server.url("/v1/books/b1/cover").newBuilder()
                    .username("injected-user")
                    .password("injected-password")
                    .build()
                    .toString(),
                server.url("/v1/books/b1").toString(),
                server.url("/v1/books/b1/cover?redirect=1").toString(),
            )
            val repository = CatalogRepository(
                CatalogApi(
                    AuthenticatedCall(client, FakeTokenSession("access-current")),
                    server.url("/"),
                ),
            )

            invalidCoverUrls.forEach { coverUrl ->
                server.enqueue(
                    MockResponse(
                        body = """
                            {
                              "items":[{
                                "id":"b1","title":"Dune","authors":["Frank Herbert"],
                                "coverUrl":"$coverUrl",
                                "reviewState":"confirmed","sourceFolder":"Dune"
                              }],
                              "filters":{}
                            }
                        """.trimIndent(),
                    ),
                )
                assertThrows(IOException::class.java) {
                    kotlinx.coroutines.runBlocking {
                        repository.books(BookQuery()).first()
                    }
                }
            }
        }
    }

    private suspend fun withPinnedServer(
        block: suspend (MockWebServer, okhttp3.OkHttpClient) -> Unit,
    ) {
        val certificate = HeldCertificate.Builder()
            .addSubjectAlternativeName("localhost")
            .build()
        val serverCertificates = HandshakeCertificates.Builder()
            .heldCertificate(certificate)
            .build()
        val server = MockWebServer()
        server.useHttps(serverCertificates.sslSocketFactory())
        server.start()
        val connection = SavedConnection(
            baseUrl = server.url("/").toString(),
            certificateSha256 = certificate.certificate.encoded
                .let(::sha256Fingerprint),
            username = "owner",
        )
        try {
            block(server, PinnedClientFactory().create(connection))
        } finally {
            server.close()
        }
    }

    private class FakeTokenSession(
        initialAccessToken: String?,
        private val refreshFailure: Exception? = null,
    ) : TokenSession {
        @Volatile
        private var currentAccessToken = initialAccessToken
        val refreshCalls = AtomicInteger()

        override val accessToken: String?
            get() = currentAccessToken

        override suspend fun refresh(): SessionTokens {
            refreshCalls.incrementAndGet()
            refreshFailure?.let { throw it }
            return SessionTokens(
                accessToken = "access-new",
                refreshToken = "refresh-new",
                accessExpiry = "2026-07-27T01:02:03Z",
            ).also { currentAccessToken = it.accessToken }
        }
    }
}

package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.storage.SavedConnection
import java.io.IOException
import java.util.concurrent.TimeUnit
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okhttp3.Headers.Companion.headersOf
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test

class BookBoxClientFactoryTest {
    @Test
    fun `client allows slow manifest preparation`() {
        val server = MockWebServer()
        server.start()
        try {
            val client = factoryFor(server)

            assertEquals(TimeUnit.MINUTES.toMillis(5).toInt(), client.readTimeoutMillis)
        } finally {
            server.close()
        }
    }

    @Test
    fun `client follows a same-origin GET redirect`() {
        val server = MockWebServer()
        server.enqueue(MockResponse(code = 302, headers = headersOf("Location", "/final")))
        server.enqueue(MockResponse(body = "ready"))
        server.start()
        try {
            val client = factoryFor(server)
            client.newCall(Request.Builder().url(server.url("/start")).build())
                .execute()
                .use { response ->
                    assertEquals(200, response.code)
                    assertEquals("ready", response.body.string())
                }
        } finally {
            server.close()
        }
    }

    @Test
    fun `client rejects redirects outside the saved origin`() {
        val origin = MockWebServer()
        val other = MockWebServer()
        other.enqueue(MockResponse(body = "must not be reached"))
        other.start()
        origin.enqueue(
            MockResponse(
                code = 302,
                headers = headersOf("Location", other.url("/elsewhere").toString()),
            ),
        )
        origin.start()
        try {
            assertThrows(IOException::class.java) {
                factoryFor(origin)
                    .newCall(Request.Builder().url(origin.url("/start")).build())
                    .execute()
                    .close()
            }
            assertEquals(0, other.requestCount)
        } finally {
            origin.close()
            other.close()
        }
    }

    @Test
    fun `client never redirects a credential-bearing POST`() {
        val server = MockWebServer()
        server.enqueue(MockResponse(code = 302, headers = headersOf("Location", "/other")))
        server.enqueue(MockResponse(body = "must not be reached"))
        server.start()
        try {
            val request = Request.Builder()
                .url(server.url("/v1/session"))
                .post("""{"password":"sentinel"}""".toRequestBody())
                .build()
            assertThrows(IOException::class.java) {
                factoryFor(server).newCall(request).execute().close()
            }
            assertEquals(1, server.requestCount)
        } finally {
            server.close()
        }
    }

    private fun factoryFor(server: MockWebServer) = BookBoxClientFactory().create(
        SavedConnection(server.url("/").toString(), "owner"),
    )
}

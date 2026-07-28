package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.storage.RefreshTokenStore
import com.rendref.bookbox.core.storage.SavedConnection
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonObject
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test

class SessionApiTest {
    @Test
    fun `login sends credentials once and stores decoded refresh token`() = runTest {
        withBookBoxServer(
            MockResponse(
                body = """{"accessToken":"access-1","refreshToken":"refresh-1","accessExpiry":"2026-07-27T01:02:03Z"}""",
            ),
        ) { server, client, tokenStore ->
            val api = SessionApi(client, server.url("/"), tokenStore)

            val tokens = api.login("owner", "password-sentinel")

            assertEquals("access-1", tokens.accessToken)
            assertEquals("refresh-1", tokens.refreshToken)
            assertEquals("refresh-1", tokenStore.value)
            val request = server.takeRequest()
            assertEquals("/v1/session", request.url.encodedPath)
            val json = Json.parseToJsonElement(checkNotNull(request.body).utf8()).jsonObject
            assertEquals(setOf("username", "password"), json.keys)
            assertEquals("\"owner\"", json.getValue("username").toString())
            assertEquals("\"password-sentinel\"", json.getValue("password").toString())
        }
    }

    @Test
    fun `refresh sends only refresh token and stores rotation`() = runTest {
        withBookBoxServer(
            MockResponse(
                body = """{"accessToken":"access-2","refreshToken":"refresh-2","accessExpiry":"2026-07-27T01:02:03Z"}""",
            ),
        ) { server, client, tokenStore ->
            tokenStore.value = "refresh-sentinel"
            val api = SessionApi(client, server.url("/"), tokenStore)

            val tokens = api.refresh()

            assertEquals("access-2", tokens.accessToken)
            assertEquals("refresh-2", tokenStore.value)
            val json = Json.parseToJsonElement(
                checkNotNull(server.takeRequest().body).utf8(),
            ).jsonObject
            assertEquals(setOf("refreshToken"), json.keys)
            assertEquals("\"refresh-sentinel\"", json.getValue("refreshToken").toString())
        }
    }

    @Test
    fun `refresh 401 clears token and redacts secrets`() = runTest {
        withBookBoxServer(
            MockResponse(code = 401, body = """{"error":{"code":"unauthorized"}}"""),
        ) { server, client, tokenStore ->
            tokenStore.value = "refresh-token-sentinel"
            val api = SessionApi(client, server.url("/"), tokenStore)

            val failure = assertThrows(SessionException::class.java) {
                kotlinx.coroutines.runBlocking { api.refresh() }
            }

            assertEquals(null, tokenStore.value)
            assertEquals(1, tokenStore.clearCalls)
            assertFalse(failure.stackTraceToString().contains("refresh-token-sentinel"))
        }
    }

    @Test
    fun `login errors redact password`() = runTest {
        withBookBoxServer(MockResponse(code = 500, body = "password-sentinel")) {
                server,
                client,
                tokenStore,
            ->
            val api = SessionApi(client, server.url("/"), tokenStore)

            val failure = assertThrows(SessionException::class.java) {
                kotlinx.coroutines.runBlocking { api.login("owner", "password-sentinel") }
            }

            assertFalse(failure.stackTraceToString().contains("password-sentinel"))
        }
    }

    @Test
    fun `oversized or invalid token responses are rejected before persistence`() = runTest {
        withBookBoxServer(MockResponse(body = "x".repeat(64 * 1024 + 1))) {
                server,
                client,
                tokenStore,
            ->
            val api = SessionApi(client, server.url("/"), tokenStore)

            assertThrows(SessionException::class.java) {
                kotlinx.coroutines.runBlocking { api.login("owner", "password") }
            }
            assertNull(tokenStore.value)

            server.enqueue(
                MockResponse(
                    body = """{"accessToken":"","refreshToken":"refresh","accessExpiry":"not-an-instant"}""",
                ),
            )
            assertThrows(SessionException::class.java) {
                kotlinx.coroutines.runBlocking { api.login("owner", "password") }
            }
            assertNull(tokenStore.value)
        }
    }

    @Test
    fun `refresh 401 clears access token before redacted token cleanup failure`() = runTest {
        withBookBoxServer(
            MockResponse(
                body = """{"accessToken":"access-live","refreshToken":"refresh-live","accessExpiry":"2026-07-27T01:02:03Z"}""",
            ),
        ) { server, client, tokenStore ->
            val api = SessionApi(client, server.url("/"), tokenStore)
            api.login("owner", "password")
            server.enqueue(MockResponse(code = 401))
            tokenStore.throwOnClear = true

            val failure = assertThrows(SessionException::class.java) {
                kotlinx.coroutines.runBlocking { api.refresh() }
            }

            assertNull(api.accessToken)
            assertFalse(failure.stackTraceToString().contains("refresh-token-sentinel"))
        }
    }

    private suspend fun withBookBoxServer(
        response: MockResponse,
        block: suspend (MockWebServer, okhttp3.OkHttpClient, RecordingTokenStore) -> Unit,
    ) {
        val server = MockWebServer()
        server.enqueue(response)
        server.start()
        val connection = SavedConnection(
            baseUrl = server.url("/").toString(),
            username = "owner",
        )
        try {
            block(server, BookBoxClientFactory().create(connection), RecordingTokenStore())
        } finally {
            server.close()
        }
    }

    private class RecordingTokenStore : RefreshTokenStore {
        var value: String? = null
        var clearCalls = 0
        var throwOnClear = false

        override suspend fun read(): String? = value

        override suspend fun write(token: String) {
            value = token
        }

        override suspend fun clear() {
            value = null
            clearCalls += 1
            if (throwOnClear) {
                throw IllegalStateException("refresh-token-sentinel")
            }
        }
    }
}

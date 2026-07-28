package com.rendref.bookbox.feature.connect

import com.rendref.bookbox.core.network.SessionException
import com.rendref.bookbox.core.network.SessionService
import com.rendref.bookbox.core.network.SessionTokens
import com.rendref.bookbox.core.storage.ConnectionRepository
import com.rendref.bookbox.core.storage.SavedConnection
import java.net.ConnectException
import java.net.UnknownHostException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class ConnectViewModelTest {
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
    fun `bare default hostname becomes BookBox HTTP origin`() {
        assertEquals(
            "http://bookbox.local:8787/",
            normalizeServerOrigin("bookbox.local").toString(),
        )
    }

    @Test
    fun `private IP without scheme receives HTTP and default port`() {
        assertEquals(
            "http://192.168.1.25:8787/",
            normalizeServerOrigin("192.168.1.25").toString(),
        )
    }

    @Test
    fun `explicit HTTP port 80 is preserved`() {
        assertEquals(
            "http://bookbox.local/",
            normalizeServerOrigin("http://bookbox.local:80").toString(),
        )
    }

    @Test
    fun `HTTPS credentials paths queries and fragments are rejected`() {
        listOf(
            "https://bookbox.local",
            "http://owner:secret@bookbox.local",
            "http://bookbox.local/path",
            "http://bookbox.local/?query=1",
            "http://bookbox.local/#fragment",
        ).forEach { input ->
            runCatching { normalizeServerOrigin(input) }
                .onSuccess { error("$input should be rejected") }
                .onFailure { assertTrue(it is IllegalArgumentException) }
        }
    }

    @Test
    fun `first screen contains default server and signs in without trust setup`() =
        runTest(dispatcher) {
            val connections = FakeConnectionRepository()
            val sessions = FakeSessionService()
            val viewModel = ConnectViewModel(connections, { sessions }, dispatcher)
            advanceUntilIdle()

            assertEquals(
                ConnectState.EnterCredentials(server = "bookbox.local"),
                viewModel.state.value,
            )

            viewModel.connect("bookbox.local", "owner", "correct horse")
            advanceUntilIdle()

            assertEquals(ConnectState.Ready, viewModel.state.value)
            assertEquals(
                SavedConnection("http://bookbox.local:8787/", "owner"),
                connections.saved.single(),
            )
            assertFalse(viewModel.state.value.toString().contains("correct horse"))
        }

    @Test
    fun `blank fields stay on credentials with a useful error`() = runTest(dispatcher) {
        val viewModel = ConnectViewModel(
            FakeConnectionRepository(),
            { FakeSessionService() },
            dispatcher,
        )
        advanceUntilIdle()

        viewModel.connect("", "", "")

        val error = viewModel.state.value as ConnectState.Error
        assertEquals(ConnectRecovery.EditCredentials, error.recovery)
        assertTrue(error.message.contains("required", ignoreCase = true))
    }

    @Test
    fun `connection failures have distinct recovery messages`() = runTest(dispatcher) {
        val cases = listOf(
            UnknownHostException() to "find",
            ConnectException() to "reach",
            SessionException("expired", 401) to "username or password",
            SessionException("invalid response", 200) to "compatible",
            SessionException("route not found", 404) to "compatible",
            SessionException("method not allowed", 405) to "compatible",
        )
        cases.forEach { (failure, expectedText) ->
            val viewModel = ConnectViewModel(
                FakeConnectionRepository(),
                { FakeSessionService(loginFailure = failure) },
                dispatcher,
            )
            advanceUntilIdle()

            viewModel.connect("bookbox.local", "owner", "secret")
            advanceUntilIdle()

            val error = viewModel.state.value as ConnectState.Error
            assertTrue(error.message.contains(expectedText, ignoreCase = true))
        }
    }

    @Test
    fun `expired refresh returns to credentials and generic refresh can retry`() =
        runTest(dispatcher) {
            val expired = savedConnection()
            val expiredViewModel = ConnectViewModel(
                FakeConnectionRepository(expired),
                { FakeSessionService(refreshFailure = SessionException("expired", 401)) },
                dispatcher,
            )
            advanceUntilIdle()
            assertEquals(
                ConnectState.EnterCredentials("bookbox.local", "owner"),
                expiredViewModel.state.value,
            )

            val service = FakeSessionService(refreshFailure = ConnectException())
            val retryViewModel = ConnectViewModel(
                FakeConnectionRepository(expired),
                { service },
                dispatcher,
            )
            advanceUntilIdle()
            assertEquals(
                ConnectRecovery.RetryRefresh,
                (retryViewModel.state.value as ConnectState.Error).recovery,
            )
            service.refreshFailure = null
            retryViewModel.retry()
            advanceUntilIdle()
            assertEquals(ConnectState.Ready, retryViewModel.state.value)
        }

    private fun savedConnection() = SavedConnection(
        baseUrl = "http://bookbox.local:8787/",
        username = "owner",
    )

    private class FakeConnectionRepository(
        initial: SavedConnection? = null,
    ) : ConnectionRepository {
        private val values = MutableStateFlow(initial)
        val saved = mutableListOf<SavedConnection>()

        override val state: Flow<SavedConnection?> = values

        override suspend fun save(connection: SavedConnection) {
            saved += connection
            values.value = connection
        }

        override suspend fun clearServer() {
            values.value = null
        }
    }

    private class FakeSessionService(
        var refreshFailure: Exception? = null,
        private val loginFailure: Exception? = null,
    ) : SessionService {
        override suspend fun login(username: String, password: String): SessionTokens {
            loginFailure?.let { throw it }
            return SessionTokens("access", "refresh", "2026-07-27T01:02:03Z")
        }

        override suspend fun refresh(): SessionTokens {
            refreshFailure?.let { throw it }
            return SessionTokens("access", "refresh", "2026-07-27T01:02:03Z")
        }
    }
}

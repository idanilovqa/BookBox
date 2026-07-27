package com.rendref.netstorage.feature.connect

import com.rendref.netstorage.core.network.CertificateInspector
import com.rendref.netstorage.core.network.ObservedCertificate
import com.rendref.netstorage.core.network.SessionException
import com.rendref.netstorage.core.network.SessionService
import com.rendref.netstorage.core.network.SessionTokens
import com.rendref.netstorage.core.storage.ConnectionRepository
import com.rendref.netstorage.core.storage.SavedConnection
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import okhttp3.HttpUrl
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
    fun `server is not saved until exact fingerprint approval and successful login`() =
        runTest(dispatcher) {
            val repository = FakeConnectionRepository()
            val fingerprint = "AB:".repeat(31) + "AB"
            val observed = ObservedCertificate(fingerprint, byteArrayOf(1, 2, 3))
            var probes = 0
            val inspector = CertificateInspector {
                probes += 1
                observed
            }
            val sessions = FakeSessionService()
            val viewModel = ConnectViewModel(repository, inspector, { sessions }, dispatcher)

            viewModel.inspectServer("https://server.lan:8443/?not=an-origin")
            advanceUntilIdle()
            assertTrue(viewModel.state.value is ConnectState.Error)
            assertEquals(0, probes)

            viewModel.inspectServer("https://server.lan:8443")
            advanceUntilIdle()
            assertTrue(viewModel.state.value is ConnectState.ReviewCertificate)
            assertEquals(0, repository.saved.size)

            viewModel.approveFingerprint(fingerprint.lowercase())
            assertTrue(viewModel.state.value is ConnectState.ReviewCertificate)
            viewModel.approveFingerprint(fingerprint)
            assertTrue(viewModel.state.value is ConnectState.SignIn)
            assertEquals(0, repository.saved.size)

            viewModel.signIn("owner", "password-sentinel")
            advanceUntilIdle()

            assertTrue(viewModel.state.value is ConnectState.Ready)
            assertEquals(1, repository.saved.size)
            assertEquals("owner", repository.saved.single().username)
            assertEquals("https://server.lan:8443/", repository.saved.single().baseUrl)
        }

    @Test
    fun `saved server refreshes automatically on launch`() = runTest(dispatcher) {
        val saved = savedConnection()
        val repository = FakeConnectionRepository(saved)
        val sessions = FakeSessionService()

        val viewModel =
            ConnectViewModel(repository, unexpectedInspector(), { sessions }, dispatcher)
        advanceUntilIdle()

        assertTrue(viewModel.state.value is ConnectState.Ready)
        assertEquals(1, sessions.refreshCalls)
    }

    @Test
    fun `expired automatic refresh returns to sign in`() = runTest(dispatcher) {
        val saved = savedConnection()
        val repository = FakeConnectionRepository(saved)
        val sessions = FakeSessionService(
            refreshFailure = SessionException("Sign-in has expired", 401),
        )

        val viewModel =
            ConnectViewModel(repository, unexpectedInspector(), { sessions }, dispatcher)
        advanceUntilIdle()

        val state = viewModel.state.value
        assertTrue(state is ConnectState.SignIn)
        val target = (state as ConnectState.SignIn).connection
        assertEquals(saved.baseUrl, target.baseUrl)
        assertEquals(saved.certificateSha256, target.certificateSha256)
        assertEquals(saved.username, target.username)
    }

    @Test
    fun `failed login retains safe retry target without retaining password`() =
        runTest(dispatcher) {
            val repository = FakeConnectionRepository()
            val fingerprint = "EF:".repeat(31) + "EF"
            val sessions = FakeSessionService(
                loginFailure = SessionException("Invalid credentials", 401),
            )
            val viewModel = ConnectViewModel(
                repository,
                CertificateInspector { ObservedCertificate(fingerprint, byteArrayOf(1)) },
                { sessions },
                dispatcher,
            )
            viewModel.inspectServer("https://server.lan:8443")
            advanceUntilIdle()
            viewModel.approveFingerprint(fingerprint)
            viewModel.signIn("owner", "password-sentinel")
            advanceUntilIdle()

            assertTrue(viewModel.state.value is ConnectState.Error)
            assertFalse(viewModel.state.value.toString().contains("password-sentinel"))

            viewModel.retry()
            val retryState = viewModel.state.value as ConnectState.SignIn
            assertEquals("owner", retryState.connection.username)

            sessions.loginFailure = null
            viewModel.signIn("owner", "new-password")
            advanceUntilIdle()
            assertTrue(viewModel.state.value is ConnectState.Ready)
            assertEquals(1, repository.saved.size)
        }

    @Test
    fun `latest server inspection wins when an older probe completes last`() =
        runTest(dispatcher) {
            val oldResult = CompletableDeferred<ObservedCertificate>()
            val oldFingerprint = "11:".repeat(31) + "11"
            val newFingerprint = "22:".repeat(31) + "22"
            val inspector = CertificateInspector { url ->
                if (url.host == "old.lan") {
                    oldResult.await()
                } else {
                    ObservedCertificate(newFingerprint, byteArrayOf(2))
                }
            }
            val viewModel = ConnectViewModel(
                FakeConnectionRepository(),
                inspector,
                { FakeSessionService() },
                dispatcher,
            )

            viewModel.inspectServer("https://old.lan:8443")
            runCurrent()
            viewModel.inspectServer("https://new.lan:8443")
            advanceUntilIdle()
            assertEquals(
                newFingerprint,
                (viewModel.state.value as ConnectState.ReviewCertificate)
                    .observed.fingerprint,
            )

            oldResult.complete(ObservedCertificate(oldFingerprint, byteArrayOf(1)))
            advanceUntilIdle()
            assertEquals(
                newFingerprint,
                (viewModel.state.value as ConnectState.ReviewCertificate)
                    .observed.fingerprint,
            )
        }

    @Test
    fun `invalid new input clears prior certificate approval`() = runTest(dispatcher) {
        val fingerprint = "33:".repeat(31) + "33"
        val viewModel = ConnectViewModel(
            FakeConnectionRepository(),
            CertificateInspector { ObservedCertificate(fingerprint, byteArrayOf(3)) },
            { FakeSessionService() },
            dispatcher,
        )
        viewModel.inspectServer("https://first.lan:8443")
        advanceUntilIdle()
        assertTrue(viewModel.state.value is ConnectState.ReviewCertificate)

        viewModel.inspectServer("https://second.lan:8443/path")
        viewModel.approveFingerprint(fingerprint)

        assertTrue(viewModel.state.value is ConnectState.Error)
    }

    private fun savedConnection() = SavedConnection(
        baseUrl = "https://server.lan:8443/",
        certificateSha256 = "CD:".repeat(31) + "CD",
        username = "owner",
    )

    private fun unexpectedInspector() = CertificateInspector {
        error("Certificate probe should not run for a saved server")
    }

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
        private val refreshFailure: SessionException? = null,
        var loginFailure: SessionException? = null,
    ) : SessionService {
        var refreshCalls = 0

        override suspend fun login(username: String, password: String): SessionTokens {
            loginFailure?.let { throw it }
            return SessionTokens("access", "refresh", "2026-07-27T01:02:03Z")
        }

        override suspend fun refresh(): SessionTokens {
            refreshCalls += 1
            refreshFailure?.let { throw it }
            return SessionTokens("access", "refresh", "2026-07-27T01:02:03Z")
        }
    }
}

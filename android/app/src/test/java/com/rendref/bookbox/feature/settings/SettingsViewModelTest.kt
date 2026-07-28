package com.rendref.bookbox.feature.settings

import com.rendref.bookbox.core.error.UserError
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class SettingsViewModelTest {
    private val dispatcher = StandardTestDispatcher()

    @Before
    fun setUp() = Dispatchers.setMain(dispatcher)

    @After
    fun tearDown() = Dispatchers.resetMain()

    @Test
    fun `loads diagnostics with only safe abbreviated connection details`() =
        runTest(dispatcher) {
            val source = FakeSettingsSource()
            val viewModel = SettingsViewModel(source)

            assertTrue(viewModel.state.value.isLoading)
            advanceUntilIdle()

            val state = viewModel.state.value
            assertFalse(state.isLoading)
            assertEquals("nas.home:9443", state.host)
            assertEquals("owner", state.username)
            assertEquals("Audiobooks", state.destinationLabel)
            assertEquals("0.1.0", state.appVersion)
            assertEquals("1.0.0", state.serverVersion)
            assertEquals(ScanStatus.Complete, state.scanStatus)
        }

    @Test
    fun `unsafe diagnostic labels never reach settings state`() = runTest(dispatcher) {
        val source = FakeSettingsSource(
            snapshot = snapshot().copy(
                username = "owner\naccess-token",
                destinationLabel = "content://documents/tree/secret",
                serverVersion = "H:/Audiobooks/private",
            ),
        )
        val viewModel = SettingsViewModel(source)

        advanceUntilIdle()

        val visible = listOfNotNull(
            viewModel.state.value.host,
            viewModel.state.value.username,
            viewModel.state.value.destinationLabel,
            viewModel.state.value.serverVersion,
            viewModel.state.value.message,
        ).joinToString(" ")
        listOf("access-token", "content://", "H:/Audiobooks").forEach {
            assertFalse(visible.contains(it, ignoreCase = true))
        }
        assertEquals("Saved user", viewModel.state.value.username)
        assertNull(viewModel.state.value.destinationLabel)
        assertNull(viewModel.state.value.serverVersion)
    }

    @Test
    fun `rescan sign in again and confirmed server change use narrow operations`() =
        runTest(dispatcher) {
            val source = FakeSettingsSource()
            val viewModel = SettingsViewModel(source)
            var connectionResets = 0
            advanceUntilIdle()

            viewModel.rescan()
            advanceUntilIdle()
            assertEquals(listOf("rescan"), source.operations)
            assertEquals(ScanStatus.Queued, viewModel.state.value.scanStatus)

            viewModel.signOut { connectionResets += 1 }
            advanceUntilIdle()
            assertEquals(listOf("rescan", "sign-out"), source.operations)
            assertTrue(viewModel.state.value.signedOut)
            assertEquals(1, connectionResets)

            viewModel.requestServerChange()
            assertTrue(viewModel.state.value.confirmServerChange)
            assertEquals(2, source.operations.size)
            viewModel.confirmServerChange { connectionResets += 1 }
            advanceUntilIdle()

            assertEquals(
                listOf("rescan", "sign-out", "clear-server-credentials"),
                source.operations,
            )
            assertTrue(viewModel.state.value.serverChanged)
            assertEquals(2, connectionResets)
            assertEquals(
                "Tokens and the saved server were cleared. " +
                    "Android downloads and transfer history were kept.",
                viewModel.state.value.message,
            )
        }

    @Test
    fun `remote settings failure maps to typed recovery copy`() = runTest(dispatcher) {
        val source = FakeSettingsSource(failRescan = true)
        val viewModel = SettingsViewModel(source)
        advanceUntilIdle()

        viewModel.rescan()
        advanceUntilIdle()

        assertEquals(UserError.ServerUnavailable, viewModel.state.value.error)
        assertFalse(viewModel.state.value.isBusy)
    }

    @Test
    fun `failed credential reset does not reconnect`() = runTest(dispatcher) {
        val source = FakeSettingsSource(failSignOut = true)
        val viewModel = SettingsViewModel(source)
        var connectionResets = 0
        advanceUntilIdle()

        viewModel.signOut { connectionResets += 1 }
        advanceUntilIdle()

        assertEquals(0, connectionResets)
        assertFalse(viewModel.state.value.signedOut)
        assertEquals(UserError.ServerUnavailable, viewModel.state.value.error)
    }
}

private class FakeSettingsSource(
    private val snapshot: SettingsSnapshot = snapshot(),
    private val failRescan: Boolean = false,
    private val failSignOut: Boolean = false,
) : SettingsSource {
    val operations = mutableListOf<String>()

    override suspend fun load(): SettingsSnapshot = snapshot

    override suspend fun rescan(): ScanStatus {
        operations += "rescan"
        if (failRescan) error("offline")
        return ScanStatus.Queued
    }

    override suspend fun signOut() {
        operations += "sign-out"
        if (failSignOut) error("offline")
    }

    override suspend fun clearServerCredentials() {
        operations += "clear-server-credentials"
    }
}

private fun snapshot() = SettingsSnapshot(
    baseUrl = "http://nas.home:9443/",
    username = "owner",
    destinationLabel = "Audiobooks",
    appVersion = "0.1.0",
    serverVersion = "1.0.0",
    scanStatus = ScanStatus.Complete,
)

package com.rendref.netstorage.core.storage

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Test

class ConnectionStoreTest {
    @Test
    fun `connection and destination survive a new store instance without a password`() = runTest {
        val dataStore = InMemoryPreferencesDataStore()
        val tokens = RecordingTokenStore()
        val firstStore = ConnectionStore(dataStore, tokens)
        val connection = SavedConnection(
            baseUrl = "https://192.168.1.25:8443/",
            certificateSha256 = "AB:".repeat(31) + "AB",
            username = "owner",
            destinationUri = "content://com.android.externalstorage.documents/tree/primary%3AAudiobooks",
        )

        firstStore.save(connection)
        val secondStore = ConnectionStore(dataStore, tokens)

        assertEquals(connection, secondStore.state.first())
        val storedKeyNames = dataStore.data.first().asMap().keys.map { it.name }
        assertFalse(storedKeyNames.any { it.contains("password", ignoreCase = true) })
    }

    @Test
    fun `clear server removes connection pin and encrypted token but retains destination`() = runTest {
        val dataStore = InMemoryPreferencesDataStore()
        val tokens = RecordingTokenStore()
        val store = ConnectionStore(dataStore, tokens)
        store.save(
            SavedConnection(
                baseUrl = "https://server.lan:8443/",
                certificateSha256 = "CD:".repeat(31) + "CD",
                username = "owner",
                destinationUri = "content://destination",
            ),
        )

        store.clearServer()

        assertNull(store.state.first())
        assertEquals("content://destination", store.destinationUri.first())
        assertEquals(1, tokens.clearCalls)
        val remaining = dataStore.data.first().asMap()
        assertFalse(remaining.keys.any { it.name.contains("certificate", ignoreCase = true) })
    }

    private class RecordingTokenStore : RefreshTokenStore {
        var clearCalls = 0

        override suspend fun read(): String? = null

        override suspend fun write(token: String) = Unit

        override suspend fun clear() {
            clearCalls += 1
        }
    }

    private class InMemoryPreferencesDataStore : DataStore<Preferences> {
        private val values = MutableStateFlow<Preferences>(emptyPreferences())
        private val mutex = Mutex()

        override val data = values

        override suspend fun updateData(
            transform: suspend (t: Preferences) -> Preferences,
        ): Preferences = mutex.withLock {
            transform(values.value).also { updated ->
                values.value = updated
            }
        }
    }
}

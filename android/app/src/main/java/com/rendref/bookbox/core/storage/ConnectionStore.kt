package com.rendref.bookbox.core.storage

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import okhttp3.HttpUrl.Companion.toHttpUrl

data class SavedConnection(
    val baseUrl: String,
    val username: String,
    val destinationUri: String? = null,
) {
    init {
        val url = baseUrl.toHttpUrl()
        require(url.scheme == "http") { "Server must use HTTP" }
        require(url.encodedUsername.isEmpty() && url.encodedPassword.isEmpty()) {
            "Server URL must not contain credentials"
        }
        require(url.query == null && url.fragment == null && url.encodedPath == "/") {
            "Server URL must be a clean HTTP origin"
        }
        require(username.isNotBlank()) { "Username must not be blank" }
    }
}

interface RefreshTokenStore {
    suspend fun read(): String?

    suspend fun write(token: String)

    suspend fun clear()
}

interface ConnectionRepository {
    val state: Flow<SavedConnection?>

    suspend fun save(connection: SavedConnection)

    suspend fun clearServer()
}

class ConnectionStore(
    private val dataStore: DataStore<Preferences>,
    private val tokenStore: RefreshTokenStore,
) : ConnectionRepository {
    override val state: Flow<SavedConnection?> = dataStore.data.map { preferences ->
        val baseUrl = preferences[BASE_URL] ?: return@map null
        val username = preferences[USERNAME] ?: return@map null
        SavedConnection(
            baseUrl = baseUrl,
            username = username,
            destinationUri = preferences[DESTINATION],
        )
    }

    val destinationUri: Flow<String?> = dataStore.data.map { it[DESTINATION] }

    override suspend fun save(connection: SavedConnection) {
        dataStore.edit { preferences ->
            preferences[BASE_URL] = connection.baseUrl.toHttpUrl().toString()
            preferences[USERNAME] = connection.username
            connection.destinationUri?.let { preferences[DESTINATION] = it }
        }
    }

    suspend fun setDestination(uri: String?) {
        dataStore.edit { preferences ->
            if (uri == null) {
                preferences.remove(DESTINATION)
            } else {
                preferences[DESTINATION] = uri
            }
        }
    }

    override suspend fun clearServer() {
        tokenStore.clear()
        dataStore.edit { preferences ->
            preferences.remove(BASE_URL)
            preferences.remove(USERNAME)
        }
    }

    private companion object {
        val BASE_URL = stringPreferencesKey("server_base_url")
        val USERNAME = stringPreferencesKey("server_username")
        val DESTINATION = stringPreferencesKey("destination_uri")
    }
}

package com.rendref.bookbox.feature.settings

import com.rendref.bookbox.core.storage.ConnectionRepository
import com.rendref.bookbox.core.storage.RefreshTokenStore
import kotlinx.coroutines.flow.first

class ConnectionSettingsSource(
    private val connections: ConnectionRepository,
    private val sessions: RefreshTokenStore,
    private val appVersion: String,
    private val destinationName: suspend (String?) -> String?,
    private val serverVersion: suspend () -> String?,
    private val requestRescan: suspend () -> ScanStatus,
) : SettingsSource {
    override suspend fun load(): SettingsSnapshot {
        val saved = connections.state.first()
            ?: throw IllegalStateException("No saved server")
        return SettingsSnapshot(
            baseUrl = saved.baseUrl,
            username = saved.username,
            destinationLabel = destinationName(saved.destinationUri),
            appVersion = appVersion,
            serverVersion = serverVersion(),
            scanStatus = ScanStatus.NotScanned,
        )
    }

    override suspend fun rescan(): ScanStatus = requestRescan()

    override suspend fun signOut() {
        sessions.clear()
    }

    override suspend fun clearServerCredentials() {
        connections.clearServer()
    }
}

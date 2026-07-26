package com.rendref.netstorage

import android.content.Context
import android.provider.DocumentsContract
import com.rendref.netstorage.core.network.AuthenticatedCall
import com.rendref.netstorage.core.network.BrowseApi
import com.rendref.netstorage.core.network.CatalogApi
import com.rendref.netstorage.core.network.CertificateInspector
import com.rendref.netstorage.core.network.CertificateProbe
import com.rendref.netstorage.core.network.PinnedClientFactory
import com.rendref.netstorage.core.network.ReviewApi
import com.rendref.netstorage.core.network.SessionApi
import com.rendref.netstorage.core.network.SettingsApi
import com.rendref.netstorage.core.network.TransferManifestApi
import com.rendref.netstorage.core.repository.BrowseRepository
import com.rendref.netstorage.core.repository.BrowseSource
import com.rendref.netstorage.core.repository.CatalogRepository
import com.rendref.netstorage.core.repository.CatalogSource
import com.rendref.netstorage.core.repository.ReviewRepository
import com.rendref.netstorage.core.repository.ReviewSource
import com.rendref.netstorage.core.storage.ConnectionStore
import com.rendref.netstorage.core.storage.DestinationStore
import com.rendref.netstorage.core.storage.KeystoreTokenStore
import com.rendref.netstorage.core.storage.SavedConnection
import com.rendref.netstorage.core.storage.netStorageConnectionDataStore
import com.rendref.netstorage.core.transfer.TransferDestinationSource
import com.rendref.netstorage.core.transfer.TransferJournal
import com.rendref.netstorage.core.transfer.TransferScheduler
import com.rendref.netstorage.core.transfer.TransferStarter
import com.rendref.netstorage.core.transfer.TransferStartSource
import com.rendref.netstorage.feature.connect.ConnectViewModel
import com.rendref.netstorage.feature.settings.ConnectionSettingsSource
import com.rendref.netstorage.feature.settings.ScanStatus
import com.rendref.netstorage.feature.settings.SettingsSource
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl.Companion.toHttpUrl
import java.security.MessageDigest

internal class NetStorageRuntime private constructor(
    private val context: Context,
) {
    private val tokenStore = KeystoreTokenStore(context)
    val connections = ConnectionStore(
        context.netStorageConnectionDataStore,
        tokenStore,
    )
    private val clients = PinnedClientFactory()
    private val sessions = mutableMapOf<String, SessionApi>()

    fun connectViewModel(): ConnectViewModel = ConnectViewModel(
        connections = connections,
        certificateInspector = CertificateInspector { url ->
            withContext(Dispatchers.IO) { CertificateProbe().inspect(url) }
        },
        sessionFactory = ::session,
    )

    fun connected(saved: SavedConnection): ConnectedRuntime {
        val baseUrl = saved.baseUrl.toHttpUrl()
        val pinned = clients.create(saved)
        val session = session(saved)
        val calls = AuthenticatedCall(pinned, session)
        val browseApi = BrowseApi(calls, baseUrl)
        val settingsApi = SettingsApi(calls, baseUrl)
        val destination = DestinationStore(
            connectionStore = connections,
            contentResolver = context.contentResolver,
        )
        val journal = TransferJournal(context.filesDir)
        val scheduler = TransferScheduler(context)
        return ConnectedRuntime(
            instanceKey = connectionInstanceKey(saved),
            catalog = CatalogRepository(CatalogApi(calls, baseUrl)),
            browse = BrowseRepository(browseApi),
            reviews = ReviewRepository(ReviewApi(calls, baseUrl)),
            destination = destination,
            journal = journal,
            scheduler = scheduler,
            starter = TransferStarter(
                manifests = TransferManifestApi(calls, baseUrl),
                destination = TransferDestinationSource {
                    destination.requireTree().treeUri.toString()
                },
                journal = journal,
                scheduler = scheduler,
            ),
            settings = ConnectionSettingsSource(
                connections = connections,
                sessions = tokenStore,
                appVersion = runCatching {
                    context.packageManager
                        .getPackageInfo(context.packageName, 0)
                        .versionName
                }.getOrNull() ?: "unknown",
                destinationName = ::destinationDisplayName,
                serverVersion = settingsApi::serverVersion,
                requestRescan = {
                    settingsApi.rescan(browseApi.roots().map { it.id })
                    ScanStatus.Queued
                },
            ),
        )
    }

    @Synchronized
    private fun session(saved: SavedConnection): SessionApi =
        sessions.getOrPut(saved.baseUrl + "\u0000" + saved.certificateSha256) {
            SessionApi(
                client = clients.create(saved),
                baseUrl = saved.baseUrl.toHttpUrl(),
                tokenStore = tokenStore,
            )
        }

    private fun destinationDisplayName(rawUri: String?): String? {
        val uri = rawUri?.let(android.net.Uri::parse) ?: return null
        return runCatching {
            val documentId = DocumentsContract.getTreeDocumentId(uri)
            val documentUri = DocumentsContract.buildDocumentUriUsingTree(uri, documentId)
            context.contentResolver.query(
                documentUri,
                arrayOf(DocumentsContract.Document.COLUMN_DISPLAY_NAME),
                null,
                null,
                null,
            )?.use { cursor ->
                if (cursor.moveToFirst()) cursor.getString(0) else null
            }
        }.getOrNull()?.takeIf {
            it.isNotBlank() &&
                it.length <= 100 &&
                it.none(Char::isISOControl) &&
                !it.contains("://")
        } ?: "Selected Android folder"
    }

    companion object {
        @Volatile
        private var instance: NetStorageRuntime? = null

        fun get(context: Context): NetStorageRuntime =
            instance ?: synchronized(this) {
                instance ?: NetStorageRuntime(context.applicationContext).also {
                    instance = it
                }
            }
    }
}

internal fun connectionInstanceKey(saved: SavedConnection): String =
    MessageDigest.getInstance("SHA-256")
        .digest(
            (saved.baseUrl + "\u0000" + saved.certificateSha256)
                .toByteArray(Charsets.UTF_8),
        )
        .joinToString("") { byte -> "%02x".format(byte.toInt() and 0xff) }

internal data class ConnectedRuntime(
    val instanceKey: String,
    val catalog: CatalogSource,
    val browse: BrowseSource,
    val reviews: ReviewSource,
    val destination: DestinationStore,
    val journal: TransferJournal,
    val scheduler: TransferScheduler,
    val starter: TransferStartSource,
    val settings: SettingsSource,
)

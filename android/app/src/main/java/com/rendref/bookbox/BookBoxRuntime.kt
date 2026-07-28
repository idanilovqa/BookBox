package com.rendref.bookbox

import android.content.Context
import android.provider.DocumentsContract
import com.rendref.bookbox.core.network.AuthenticatedCall
import com.rendref.bookbox.core.network.BrowseApi
import com.rendref.bookbox.core.network.CatalogApi
import com.rendref.bookbox.core.network.CertificateInspector
import com.rendref.bookbox.core.network.CertificateProbe
import com.rendref.bookbox.core.network.PinnedClientFactory
import com.rendref.bookbox.core.network.ReviewApi
import com.rendref.bookbox.core.network.SessionApi
import com.rendref.bookbox.core.network.SettingsApi
import com.rendref.bookbox.core.network.TransferManifestApi
import com.rendref.bookbox.core.repository.BrowseRepository
import com.rendref.bookbox.core.repository.BrowseSource
import com.rendref.bookbox.core.repository.CatalogRepository
import com.rendref.bookbox.core.repository.CatalogSource
import com.rendref.bookbox.core.repository.ReviewRepository
import com.rendref.bookbox.core.repository.ReviewSource
import com.rendref.bookbox.core.storage.ConnectionStore
import com.rendref.bookbox.core.storage.DestinationStore
import com.rendref.bookbox.core.storage.KeystoreTokenStore
import com.rendref.bookbox.core.storage.SavedConnection
import com.rendref.bookbox.core.storage.BookBoxConnectionDataStore
import com.rendref.bookbox.core.transfer.TransferDestinationSource
import com.rendref.bookbox.core.transfer.TransferJournal
import com.rendref.bookbox.core.transfer.TransferScheduler
import com.rendref.bookbox.core.transfer.TransferStarter
import com.rendref.bookbox.core.transfer.TransferStartSource
import com.rendref.bookbox.feature.connect.ConnectViewModel
import com.rendref.bookbox.feature.settings.ConnectionSettingsSource
import com.rendref.bookbox.feature.settings.ScanStatus
import com.rendref.bookbox.feature.settings.SettingsSource
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl.Companion.toHttpUrl
import java.security.MessageDigest

internal class BookBoxRuntime private constructor(
    private val context: Context,
) {
    private val tokenStore = KeystoreTokenStore(context)
    val connections = ConnectionStore(
        context.BookBoxConnectionDataStore,
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
        private var instance: BookBoxRuntime? = null

        fun get(context: Context): BookBoxRuntime =
            instance ?: synchronized(this) {
                instance ?: BookBoxRuntime(context.applicationContext).also {
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

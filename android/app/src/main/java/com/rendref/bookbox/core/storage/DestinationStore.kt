package com.rendref.bookbox.core.storage

import android.content.ContentResolver
import android.content.Intent
import android.net.Uri
import android.provider.DocumentsContract
import java.io.IOException
import kotlinx.coroutines.flow.first

class DestinationPermissionRevoked(
    message: String = "The selected Android destination is no longer writable",
    cause: Throwable? = null,
) : IOException(message, cause)

interface DestinationAccess {
    fun persist(uri: Uri, flags: Int)

    fun hasReadWritePermission(uri: Uri): Boolean

    fun open(uri: Uri): DocumentTree
}

class DestinationStore internal constructor(
    private val readDestination: suspend () -> String?,
    private val writeDestination: suspend (String?) -> Unit,
    private val access: DestinationAccess,
) {
    constructor(
        connectionStore: ConnectionStore,
        contentResolver: ContentResolver,
    ) : this(
        readDestination = { connectionStore.destinationUri.first() },
        writeDestination = connectionStore::setDestination,
        access = ContentResolverDestinationAccess(contentResolver),
    )

    suspend fun persist(uri: Uri): DocumentTree {
        require(uri.scheme == ContentResolver.SCHEME_CONTENT) {
            "Destination must be a document tree"
        }
        try {
            access.persist(uri, PERSISTED_PERMISSION_FLAGS)
        } catch (failure: Exception) {
            throw DestinationPermissionRevoked(cause = failure)
        }
        if (!access.hasReadWritePermission(uri)) {
            throw DestinationPermissionRevoked()
        }
        val tree = try {
            access.open(uri)
        } catch (failure: Exception) {
            throw DestinationPermissionRevoked(cause = failure)
        }
        writeDestination(uri.toString())
        return tree
    }

    suspend fun requireTree(): DocumentTree {
        val saved = readDestination() ?: throw DestinationPermissionRevoked(
            "Choose an Android destination folder",
        )
        val uri = runCatching { Uri.parse(saved) }.getOrElse {
            throw DestinationPermissionRevoked(cause = it)
        }
        if (uri.scheme != ContentResolver.SCHEME_CONTENT ||
            !access.hasReadWritePermission(uri)
        ) {
            throw DestinationPermissionRevoked()
        }
        return try {
            access.open(uri)
        } catch (failure: Exception) {
            throw DestinationPermissionRevoked(cause = failure)
        }
    }

    suspend fun clear() {
        writeDestination(null)
    }

    companion object {
        const val PERSISTED_PERMISSION_FLAGS =
            Intent.FLAG_GRANT_READ_URI_PERMISSION or
                Intent.FLAG_GRANT_WRITE_URI_PERMISSION

        const val DOCUMENT_TREE_INTENT_FLAGS =
            PERSISTED_PERMISSION_FLAGS or
                Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION or
                Intent.FLAG_GRANT_PREFIX_URI_PERMISSION
    }
}

private class ContentResolverDestinationAccess(
    private val resolver: ContentResolver,
) : DestinationAccess {
    override fun persist(uri: Uri, flags: Int) {
        resolver.takePersistableUriPermission(uri, flags)
    }

    override fun hasReadWritePermission(uri: Uri): Boolean =
        resolver.persistedUriPermissions.any {
            it.uri == uri && it.isReadPermission && it.isWritePermission
        }

    override fun open(uri: Uri): DocumentTree {
        val rootID = DocumentsContract.getTreeDocumentId(uri)
        require(rootID.isNotBlank()) { "Document tree has no root" }
        return DocumentTree(
            treeUri = uri,
            rootDocumentId = rootID,
            backend = ContentResolverDocumentBackend(resolver, uri),
        )
    }
}

package com.rendref.netstorage.core.storage

import android.content.ContentResolver
import android.database.Cursor
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.DocumentsContract
import android.system.ErrnoException
import android.system.Os
import android.system.OsConstants
import com.rendref.netstorage.core.transfer.DestinationDocument
import com.rendref.netstorage.core.transfer.DestinationSeekUnsupported
import com.rendref.netstorage.core.transfer.TransferDestination
import java.io.FileNotFoundException
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.security.MessageDigest
import java.util.Locale

class InvalidDestinationPath : IOException("The destination path is invalid")

class DuplicateDestinationPath :
    IOException("The destination contains duplicate case-insensitive names")

data class DocumentNode(
    val documentId: String,
    val displayName: String,
    val isDirectory: Boolean,
    val size: Long,
)

interface DocumentBackend {
    fun children(parentDocumentId: String): List<DocumentNode>

    fun createDirectory(parentDocumentId: String, name: String): DocumentNode

    fun createFile(parentDocumentId: String, name: String): DocumentNode

    fun stat(documentId: String): DocumentNode

    fun openInput(documentId: String): InputStream

    fun openOutput(documentId: String, offset: Long): OutputStream

    fun delete(documentId: String)

    fun rename(documentId: String, displayName: String): DocumentNode
}

class DocumentTree(
    val treeUri: Uri,
    private val rootDocumentId: String,
    private val backend: DocumentBackend,
) : TransferDestination {
    private val directoryCache = mutableMapOf("" to rootDocumentId)

    fun ensureParent(destinationPath: String): String {
        val segments = destinationSegments(destinationPath)
        var relative = ""
        var parentID = rootDocumentId
        for (segment in segments.dropLast(1)) {
            relative = if (relative.isEmpty()) segment else "$relative/$segment"
            val cached = directoryCache[relative]
            if (cached != null) {
                parentID = cached
                continue
            }
            val matches = foldedMatches(backend.children(parentID), segment)
            if (matches.size > 1 ||
                matches.singleOrNull()?.displayName?.let { it != segment } == true
            ) {
                throw DuplicateDestinationPath()
            }
            val directory = matches.singleOrNull()
                ?: backend.createDirectory(parentID, segment)
            if (!directory.isDirectory || directory.displayName != segment) {
                throw InvalidDestinationPath()
            }
            parentID = directory.documentId
            directoryCache[relative] = parentID
        }
        return parentID
    }

    override fun find(relativePath: String): DestinationDocument? {
        val segments = destinationSegments(relativePath)
        val parentID = ensureParent(relativePath)
        val matches = foldedMatches(backend.children(parentID), segments.last())
        if (matches.size > 1 ||
            matches.singleOrNull()?.displayName?.let { it != segments.last() } == true
        ) {
            throw DuplicateDestinationPath()
        }
        val node = matches.singleOrNull() ?: return null
        if (node.isDirectory) {
            throw InvalidDestinationPath()
        }
        return node.destination()
    }

    override fun createTemporary(
        destinationPath: String,
        transferId: String,
        fileId: String,
    ): DestinationDocument {
        val segments = destinationSegments(destinationPath)
        val parentID = ensureParent(destinationPath)
        val digest = MessageDigest.getInstance("SHA-256")
            .digest(fileId.toByteArray())
            .take(6)
            .joinToString("") { "%02x".format(it) }
        val stem = ".${segments.last()}.netstorage-$transferId-$digest"
        val siblings = backend.children(parentID)
        var index = 0
        while (true) {
            val name = if (index == 0) "$stem.part" else "$stem-$index.part"
            if (foldedMatches(siblings, name).isEmpty()) {
                val created = backend.createFile(parentID, name)
                if (created.isDirectory || created.displayName != name) {
                    throw DuplicateDestinationPath()
                }
                return created.destination()
            }
            index += 1
        }
    }

    override fun stat(documentId: String): DestinationDocument =
        backend.stat(documentId).destination()

    override fun openInput(documentId: String): InputStream =
        backend.openInput(documentId)

    override fun openOutput(documentId: String, offset: Long): OutputStream =
        backend.openOutput(documentId, offset)

    override fun delete(documentId: String) {
        backend.delete(documentId)
    }

    override fun finalize(
        documentId: String,
        destinationPath: String,
    ): DestinationDocument {
        val name = destinationSegments(destinationPath).last()
        if (find(destinationPath) != null) {
            throw DuplicateDestinationPath()
        }
        val renamed = backend.rename(documentId, name)
        if (renamed.isDirectory || renamed.displayName != name) {
            throw DuplicateDestinationPath()
        }
        return renamed.destination()
    }

    private fun DocumentNode.destination(): DestinationDocument =
        DestinationDocument(documentId, size)

    private fun foldedMatches(nodes: List<DocumentNode>, name: String): List<DocumentNode> {
        val folded = name.lowercase(Locale.ROOT)
        return nodes.filter { it.displayName.lowercase(Locale.ROOT) == folded }
    }
}

private fun destinationSegments(relativePath: String): List<String> {
    if (relativePath.isEmpty() ||
        relativePath.startsWith("/") ||
        relativePath.contains('\\') ||
        relativePath.any(Char::isISOControl)
    ) {
        throw InvalidDestinationPath()
    }
    val segments = relativePath.split('/')
    if (segments.any { it.isEmpty() || it == "." || it == ".." }) {
        throw InvalidDestinationPath()
    }
    return segments
}

class ContentResolverDocumentBackend(
    private val resolver: ContentResolver,
    private val treeUri: Uri,
) : DocumentBackend {
    override fun children(parentDocumentId: String): List<DocumentNode> {
        val uri = DocumentsContract.buildChildDocumentsUriUsingTree(
            treeUri,
            parentDocumentId,
        )
        return resolver.query(uri, PROJECTION, null, null, null)?.use { cursor ->
            buildList {
                while (cursor.moveToNext()) {
                    add(cursor.node())
                }
            }
        } ?: throw IOException("Unable to list Android destination")
    }

    override fun createDirectory(parentDocumentId: String, name: String): DocumentNode =
        create(parentDocumentId, DocumentsContract.Document.MIME_TYPE_DIR, name)

    override fun createFile(parentDocumentId: String, name: String): DocumentNode =
        create(parentDocumentId, "application/octet-stream", name)

    override fun stat(documentId: String): DocumentNode {
        val uri = documentUri(documentId)
        return resolver.query(uri, PROJECTION, null, null, null)?.use { cursor ->
            if (!cursor.moveToFirst()) {
                throw FileNotFoundException("Android destination document is missing")
            }
            cursor.node()
        } ?: throw FileNotFoundException("Android destination document is missing")
    }

    override fun openInput(documentId: String): InputStream =
        resolver.openInputStream(documentUri(documentId))
            ?: throw FileNotFoundException("Android destination document is missing")

    override fun openOutput(documentId: String, offset: Long): OutputStream {
        require(offset >= 0)
        val mode = if (offset == 0L) "rwt" else "rw"
        val descriptor = resolver.openFileDescriptor(documentUri(documentId), mode)
            ?: throw FileNotFoundException("Android destination document is missing")
        if (offset > 0) {
            try {
                Os.lseek(descriptor.fileDescriptor, offset, OsConstants.SEEK_SET)
            } catch (failure: ErrnoException) {
                descriptor.close()
                throw DestinationSeekUnsupported(failure)
            }
        }
        return ParcelFileDescriptor.AutoCloseOutputStream(descriptor)
    }

    override fun delete(documentId: String) {
        if (!DocumentsContract.deleteDocument(resolver, documentUri(documentId))) {
            throw IOException("Unable to delete Android destination document")
        }
    }

    override fun rename(documentId: String, displayName: String): DocumentNode {
        val renamed = DocumentsContract.renameDocument(
            resolver,
            documentUri(documentId),
            displayName,
        ) ?: throw IOException("Unable to finalize Android destination document")
        return stat(DocumentsContract.getDocumentId(renamed))
    }

    private fun create(parentDocumentId: String, mimeType: String, name: String): DocumentNode {
        val uri = DocumentsContract.createDocument(
            resolver,
            documentUri(parentDocumentId),
            mimeType,
            name,
        ) ?: throw IOException("Unable to create Android destination document")
        return stat(DocumentsContract.getDocumentId(uri))
    }

    private fun documentUri(documentId: String): Uri =
        DocumentsContract.buildDocumentUriUsingTree(treeUri, documentId)

    private fun Cursor.node(): DocumentNode {
        val mimeType = getString(getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_MIME_TYPE))
        return DocumentNode(
            documentId = getString(
                getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_DOCUMENT_ID),
            ),
            displayName = getString(
                getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_DISPLAY_NAME),
            ),
            isDirectory = mimeType == DocumentsContract.Document.MIME_TYPE_DIR,
            size = if (isNull(getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_SIZE))) {
                0
            } else {
                getLong(getColumnIndexOrThrow(DocumentsContract.Document.COLUMN_SIZE))
            },
        )
    }

    private companion object {
        val PROJECTION = arrayOf(
            DocumentsContract.Document.COLUMN_DOCUMENT_ID,
            DocumentsContract.Document.COLUMN_DISPLAY_NAME,
            DocumentsContract.Document.COLUMN_MIME_TYPE,
            DocumentsContract.Document.COLUMN_SIZE,
        )
    }
}

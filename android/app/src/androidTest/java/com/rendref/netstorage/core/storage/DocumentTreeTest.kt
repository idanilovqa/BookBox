package com.rendref.netstorage.core.storage

import android.content.Intent
import android.net.Uri
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.InputStream
import java.io.OutputStream
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class DocumentTreeTest {
    @Test
    fun destinationPermissionAndUriSurviveAStoreRecreation() = runBlocking {
        var savedUri: String? = null
        val access = RecordingDestinationAccess()
        val first = DestinationStore(
            readDestination = { savedUri },
            writeDestination = { savedUri = it },
            access = access,
        )
        val uri = Uri.parse("content://documents/tree/audiobooks")

        first.persist(uri)
        val recreated = DestinationStore(
            readDestination = { savedUri },
            writeDestination = { savedUri = it },
            access = access,
        )

        assertEquals(uri, recreated.requireTree().treeUri)
        assertEquals(
            Intent.FLAG_GRANT_READ_URI_PERMISSION or
                Intent.FLAG_GRANT_WRITE_URI_PERMISSION,
            access.persistedFlags,
        )
        assertTrue(access.writable)
    }

    @Test
    fun revokedPersistedPermissionIsReportedBeforeDestinationMutation() = runBlocking {
        val uri = "content://documents/tree/audiobooks"
        val access = RecordingDestinationAccess().apply {
            persistedUri = uri
            writable = false
        }
        val store = DestinationStore(
            readDestination = { uri },
            writeDestination = {},
            access = access,
        )

        assertThrows(DestinationPermissionRevoked::class.java) {
            runBlocking { store.requireTree() }
        }
        assertEquals(0, access.openCalls)
    }

    @Test
    fun documentTreeCreatesSegmentsAndRejectsUnsafeOrCaseFoldedDuplicates() {
        val backend = RecordingDocumentBackend()
        val tree = DocumentTree(
            treeUri = Uri.parse("content://documents/tree/audiobooks"),
            rootDocumentId = "root",
            backend = backend,
        )

        tree.ensureParent("Frank Herbert/Dune/Dune.m4b")
        tree.ensureParent("Frank Herbert/Dune/Dune Messiah.m4b")

        assertEquals(listOf("Frank Herbert", "Dune"), backend.createdDirectories)
        listOf(
            "",
            ".",
            "..",
            "Dune//book.m4b",
            "Dune/../book.m4b",
            "Dune\\book.m4b",
            "Dune/bad\u0000name.m4b",
        ).forEach { unsafe ->
            assertThrows(InvalidDestinationPath::class.java) {
                tree.ensureParent(unsafe)
            }
        }

        backend.addChild("root", "DUNE", "directory-existing", isDirectory = true)
        assertThrows(DuplicateDestinationPath::class.java) {
            tree.ensureParent("dune/book.m4b")
        }
    }

    @Test
    fun providerCannotSilentlySuffixManifestDirectoriesTemporaryFilesOrFinalNames() {
        val directoryBackend = RecordingDocumentBackend().apply {
            suffixNextCreate = true
        }
        val directoryTree = DocumentTree(
            Uri.parse("content://documents/tree/audiobooks"),
            "root",
            directoryBackend,
        )
        assertThrows(InvalidDestinationPath::class.java) {
            directoryTree.ensureParent("Dune/book.m4b")
        }

        val fileBackend = RecordingDocumentBackend()
        val fileTree = DocumentTree(
            Uri.parse("content://documents/tree/audiobooks"),
            "root",
            fileBackend,
        )
        fileBackend.suffixNextCreate = true
        assertThrows(DuplicateDestinationPath::class.java) {
            fileTree.createTemporary("book.m4b", "transfer-1", "signed-file")
        }

        val temporary = fileTree.createTemporary(
            "book.m4b",
            "transfer-1",
            "signed-file",
        )
        fileBackend.suffixNextRename = true
        assertThrows(DuplicateDestinationPath::class.java) {
            fileTree.finalize(temporary.documentId, "book.m4b")
        }
    }
}

private class RecordingDestinationAccess : DestinationAccess {
    var persistedUri: String? = null
    var persistedFlags = 0
    var writable = true
    var openCalls = 0

    override fun persist(uri: Uri, flags: Int) {
        persistedUri = uri.toString()
        persistedFlags = flags
    }

    override fun hasReadWritePermission(uri: Uri): Boolean =
        persistedUri == uri.toString() && writable

    override fun open(uri: Uri): DocumentTree {
        openCalls += 1
        return DocumentTree(uri, "root", RecordingDocumentBackend())
    }
}

private class RecordingDocumentBackend : DocumentBackend {
    private val children = mutableMapOf<String, MutableList<DocumentNode>>()
    val createdDirectories = mutableListOf<String>()
    var suffixNextCreate = false
    var suffixNextRename = false

    override fun children(parentDocumentId: String): List<DocumentNode> =
        children[parentDocumentId].orEmpty()

    override fun createDirectory(parentDocumentId: String, name: String): DocumentNode {
        createdDirectories += name
        val returnedName = providerName(name)
        val node = DocumentNode("$parentDocumentId/$returnedName", returnedName, true, 0)
        children.getOrPut(parentDocumentId) { mutableListOf() } += node
        return node
    }

    override fun createFile(parentDocumentId: String, name: String): DocumentNode {
        val returnedName = providerName(name)
        val node = DocumentNode("$parentDocumentId/$returnedName", returnedName, false, 0)
        children.getOrPut(parentDocumentId) { mutableListOf() } += node
        return node
    }

    override fun stat(documentId: String): DocumentNode =
        children.values.flatten().first { it.documentId == documentId }

    override fun openInput(documentId: String): InputStream =
        ByteArrayInputStream(byteArrayOf())

    override fun openOutput(documentId: String, offset: Long): OutputStream =
        ByteArrayOutputStream()

    override fun delete(documentId: String) = Unit

    override fun rename(documentId: String, displayName: String): DocumentNode {
        val returnedName = if (suffixNextRename) {
            suffixNextRename = false
            "$displayName (1)"
        } else {
            displayName
        }
        return stat(documentId).copy(displayName = returnedName)
    }

    fun addChild(parent: String, name: String, id: String, isDirectory: Boolean) {
        children.getOrPut(parent) { mutableListOf() } +=
            DocumentNode(id, name, isDirectory, 0)
    }

    private fun providerName(requested: String): String =
        if (suffixNextCreate) {
            suffixNextCreate = false
            "$requested (1)"
        } else {
            requested
        }
}

package com.rendref.bookbox.e2e

import android.net.Uri
import android.content.pm.PackageManager
import android.provider.DocumentsContract
import androidx.test.platform.app.InstrumentationRegistry
import com.rendref.bookbox.core.storage.ContentResolverDocumentBackend
import com.rendref.bookbox.core.storage.DocumentTree
import java.security.MessageDigest
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TestDocumentsProviderContractTest {
    @Test
    fun providerProcessControlResetIsRepeatable() {
        val context = InstrumentationRegistry.getInstrumentation().context
        TestDocumentsControlProvider.reset(context)
        TestDocumentsControlProvider.reset(context)
        assertTrue(TestDocumentsControlProvider.operations(context).isEmpty())
    }

    @Test
    fun targetApplicationResolverCanUseOnlyTheTestDocumentTree() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        TestDocumentsControlProvider.reset(instrumentation.context)
        val treeUri = TestDocumentsControlProvider.grantTreeAccess(
            instrumentation.context,
            instrumentation.targetContext.packageName,
        )
        val tree = DocumentTree(
            treeUri = treeUri,
            rootDocumentId = TestDocumentsProvider.ROOT_DOCUMENT_ID,
            backend = ContentResolverDocumentBackend(
                instrumentation.targetContext.contentResolver,
                treeUri,
            ),
        )

        val temporary = tree.createTemporary(
            destinationPath = "Frank Herbert/Dune/Dune.m4b",
            transferId = "11111111-1111-4111-8111-111111111111",
            fileId = "signed-file-id",
        )
        val rootDocumentUri = DocumentsContract.buildDocumentUriUsingTree(
            treeUri,
            TestDocumentsProvider.ROOT_DOCUMENT_ID,
        )
        val temporaryDocumentUri = DocumentsContract.buildDocumentUriUsingTree(
            treeUri,
            temporary.documentId,
        )
        assertTrue(
            DocumentsContract.isChildDocument(
                instrumentation.targetContext.contentResolver,
                rootDocumentUri,
                temporaryDocumentUri,
            ),
        )
        assertFalse(
            DocumentsContract.isChildDocument(
                instrumentation.targetContext.contentResolver,
                temporaryDocumentUri,
                rootDocumentUri,
            ),
        )
        val expected = "fixture-audio".toByteArray()
        val split = 7
        tree.openOutput(temporary.documentId, 0).use {
            it.write(expected, 0, split)
        }
        tree.openOutput(temporary.documentId, split.toLong()).use {
            it.write(expected, split, expected.size - split)
        }
        val finished = tree.finalize(
            temporary.documentId,
            "Frank Herbert/Dune/Dune.m4b",
        )

        val actual = tree.openInput(finished.documentId).use { it.readBytes() }
        assertArrayEquals(expected, actual)
        assertEquals(
            MessageDigest.getInstance("SHA-256")
                .digest(expected)
                .joinToString("") { "%02x".format(it) },
            TestDocumentsControlProvider.sha256(
                instrumentation.context,
                "Frank Herbert/Dune/Dune.m4b",
            ),
        )
        assertTrue(
            TestDocumentsControlProvider.operations(instrumentation.context).any {
                it.kind == "rename" &&
                it.relativePath == "Frank Herbert/Dune/Dune.m4b"
            },
        )

        val disposable = tree.createTemporary(
            destinationPath = "delete-me.bin",
            transferId = "22222222-2222-4222-8222-222222222222",
            fileId = "delete-fixture",
        )
        tree.delete(disposable.documentId)
        assertTrue(
            TestDocumentsControlProvider.operations(instrumentation.context).any {
                it.kind == "delete" && it.relativePath.endsWith(".part")
            },
        )
    }

    @Test
    fun providerAuthorityExistsOnlyInTheInstrumentationManifest() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val testProvider = instrumentation.context.packageManager
            .resolveContentProvider(TestDocumentsProvider.AUTHORITY, 0)
        val controlProvider = instrumentation.context.packageManager
            .resolveContentProvider(TestDocumentsControlProvider.AUTHORITY, 0)
        val productionPackage = instrumentation.targetContext.packageManager
            .getPackageInfo(
                instrumentation.targetContext.packageName,
                PackageManager.PackageInfoFlags.of(
                    PackageManager.GET_PROVIDERS.toLong(),
                ),
            )

        assertEquals(
            instrumentation.context.packageName,
            requireNotNull(testProvider).packageName,
        )
        assertTrue(testProvider.processName.endsWith(":documents"))
        requireNotNull(controlProvider)
        assertTrue(controlProvider.processName.endsWith(":documents"))
        assertTrue(controlProvider.exported)
        assertEquals(null, controlProvider.readPermission)
        assertEquals(null, controlProvider.writePermission)
        assertEquals("com.rendref.bookbox", productionPackage.packageName)
        assertTrue(
            productionPackage.providers.orEmpty().none {
                it.authority in setOf(
                    TestDocumentsProvider.AUTHORITY,
                    TestDocumentsControlProvider.AUTHORITY,
                )
            },
        )
        assertTrue(
            Uri.parse(
                "content://${TestDocumentsProvider.AUTHORITY}/tree/" +
                    TestDocumentsProvider.ROOT_DOCUMENT_ID,
            ).authority == TestDocumentsProvider.AUTHORITY,
        )
    }
}

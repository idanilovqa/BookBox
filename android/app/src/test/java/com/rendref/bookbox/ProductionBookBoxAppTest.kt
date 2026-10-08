package com.rendref.bookbox

import com.rendref.bookbox.core.network.TransferSelection
import com.rendref.bookbox.core.storage.DestinationPermissionRevoked
import com.rendref.bookbox.core.storage.SavedConnection
import com.rendref.bookbox.core.transfer.TransferStartRequest
import com.rendref.bookbox.core.transfer.TransferStartResult
import com.rendref.bookbox.core.transfer.TransferStartSource
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.ObjectInputStream
import java.io.ObjectOutputStream
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class ProductionBookBoxAppTest {
    @Test
    fun `destination change keeps connected runtime identity`() {
        val before = SavedConnection(
            baseUrl = "http://bookbox.local:8787/",
            username = "listener",
            destinationUri = "content://documents/tree/old",
        )

        assertEquals(
            connectedRuntimeKey(before),
            connectedRuntimeKey(
                before.copy(destinationUri = "content://documents/tree/new"),
            ),
        )
    }

    @Test
    fun `book selection survives saved state serialization`() {
        val selection = TransferSelection.Book(
            "11111111-1111-4111-8111-111111111111",
        )
        val bytes = ByteArrayOutputStream().use { buffer ->
            ObjectOutputStream(buffer).use { it.writeObject(selection) }
            buffer.toByteArray()
        }

        val restored = ObjectInputStream(ByteArrayInputStream(bytes)).use {
            it.readObject()
        }

        assertEquals(selection, restored)
    }

    @Test
    fun `copy preparation distinguishes destination and server failures`() = runTest {
        val request = TransferStartRequest(
            selections = listOf(
                TransferSelection.Book("11111111-1111-4111-8111-111111111111"),
            ),
            displayName = "One audiobook",
        )

        val destination = prepareCopy(
            TransferStartSource { throw DestinationPermissionRevoked() },
            request,
        )
        val server = prepareCopy(
            TransferStartSource { throw IOException("manifest unavailable") },
            request,
        )

        assertTrue(destination is CopyStartOutcome.Failed)
        assertEquals(
            "Choose an Android destination before copying",
            (destination as CopyStartOutcome.Failed).message,
        )
        assertEquals(
            "Unable to prepare the copy. Check the server and try again.",
            (server as CopyStartOutcome.Failed).message,
        )
    }

    @Test
    fun `copy preparation returns scheduler result`() = runTest {
        val request = TransferStartRequest(
            selections = listOf(
                TransferSelection.Book("11111111-1111-4111-8111-111111111111"),
            ),
            displayName = "One audiobook",
        )

        val outcome = prepareCopy(
            TransferStartSource { TransferStartResult.Scheduled("transfer-id") },
            request,
        )

        assertEquals("Copy scheduled", (outcome as CopyStartOutcome.Started).message)
    }
}

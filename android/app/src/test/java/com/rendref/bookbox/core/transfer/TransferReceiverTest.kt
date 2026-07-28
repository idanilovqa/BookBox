package com.rendref.bookbox.core.transfer

import com.rendref.bookbox.core.model.ManifestDto
import com.rendref.bookbox.core.model.ManifestFileDto
import java.util.UUID
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class TransferReceiverTest {
    @get:Rule
    val temporary = TemporaryFolder()

    @Test
    fun `pause records state before cancelling the Android job`() {
        val journal = TransferJournal(temporary.newFolder("pause"))
        val record = receiverRecord(TransferState.Running)
        journal.write(record)
        var stateWhenCancelled: TransferState? = null
        val handler = TransferCommandHandler(
            journal = journal,
            cancelJob = {
                stateWhenCancelled = journal.read(record.id)?.state
            },
            hasPendingJob = { false },
        )

        handler.handle(TransferCommand.Pause, record.id)

        assertEquals(TransferState.Paused, stateWhenCancelled)
        assertEquals(TransferState.Paused, journal.read(record.id)?.state)
    }

    @Test
    fun `cancel retains the partial document for explicit discard`() {
        val journal = TransferJournal(temporary.newFolder("cancel"))
        val record = receiverRecord(
            state = TransferState.Running,
            temporaryDocument = "android-partial",
        )
        journal.write(record)
        val handler = TransferCommandHandler(
            journal = journal,
            cancelJob = {},
            hasPendingJob = { false },
        )

        handler.handle(TransferCommand.Cancel, record.id)

        val cancelled = journal.read(record.id)
        assertEquals(TransferState.Cancelled, cancelled?.state)
        assertEquals("android-partial", cancelled?.files?.single()?.temporaryDocument)
        assertEquals(4L, cancelled?.files?.single()?.bytesWritten)
    }

    @Test
    fun `boot leaves persisted jobs and reconciles only retryable orphans`() {
        val journal = TransferJournal(temporary.newFolder("boot"))
        val persisted = receiverRecord(TransferState.Running)
        val orphan = receiverRecord(TransferState.Pending)
        val cancelled = receiverRecord(TransferState.Cancelled)
        listOf(persisted, orphan, cancelled).forEach(journal::write)
        val handler = TransferCommandHandler(
            journal = journal,
            cancelJob = {},
            hasPendingJob = { transferId -> transferId == persisted.id },
        )

        handler.handle(TransferCommand.ReconcileBoot, null)

        assertEquals(TransferState.Running, journal.read(persisted.id)?.state)
        assertEquals(TransferState.Paused, journal.read(orphan.id)?.state)
        assertNotNull(journal.read(orphan.id)?.stopReason)
        assertEquals(TransferState.Cancelled, journal.read(cancelled.id)?.state)
        assertNull(journal.read(cancelled.id)?.stopReason)
    }
}

private fun receiverRecord(
    state: TransferState,
    temporaryDocument: String? = null,
) = TransferRecord(
    id = UUID.randomUUID().toString(),
    manifest = ManifestDto(
        id = "manifest-1",
        files = listOf(
            ManifestFileDto(
                fileId = "signed-file-id",
                sourcePath = "Dune/Dune.m4b",
                destinationPath = "Dune/Dune.m4b",
                size = 10,
                modifiedAt = "2026-07-26T12:00:00Z",
                etag = "\"source-etag\"",
                sha256 = "72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0",
            ),
        ),
        totalBytes = 10,
    ),
    files = listOf(
        FileProgress(
            fileId = "signed-file-id",
            temporaryDocument = temporaryDocument,
            bytesWritten = if (temporaryDocument == null) 0 else 4,
            state = if (temporaryDocument == null) FileState.Pending else FileState.Downloading,
        ),
    ),
    destinationTree = "content://documents/tree/audiobooks",
    state = state,
)

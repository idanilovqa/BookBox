package com.rendref.netstorage.feature.transfers

import com.rendref.netstorage.core.model.ManifestDto
import com.rendref.netstorage.core.model.ManifestFileDto
import com.rendref.netstorage.core.transfer.DestinationDocument
import com.rendref.netstorage.core.transfer.FileProgress
import com.rendref.netstorage.core.transfer.FileState
import com.rendref.netstorage.core.transfer.PlatformScheduleResult
import com.rendref.netstorage.core.transfer.TransferDestination
import com.rendref.netstorage.core.transfer.TransferJournal
import com.rendref.netstorage.core.transfer.TransferRecord
import com.rendref.netstorage.core.transfer.TransferScheduler
import com.rendref.netstorage.core.transfer.TransferState
import java.io.InputStream
import java.io.OutputStream
import java.util.UUID
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class TransferActionControllerTest {
    @get:Rule
    val temporary = TemporaryFolder()

    @Test
    fun `discard partial deletes only journaled Android temporary documents`() = runTest {
        val journal = TransferJournal(temporary.newFolder("discard"))
        val record = actionRecord(
            state = TransferState.Cancelled,
            temporaryDocument = "android-partial",
        )
        journal.write(record)
        val destination = RecordingDestination()
        val controller = TransferActionController(
            journal = journal,
            scheduler = TransferScheduler { PlatformScheduleResult.Success },
            destination = { destination },
            cancelJob = {},
            openConflict = {},
        )

        val result = controller.perform(record.id, TransferAction.DiscardPartialData)

        assertEquals(TransferActionResult.Accepted, result)
        assertEquals(listOf("android-partial"), destination.deleted)
        val updated = journal.read(record.id)!!
        assertEquals(TransferState.Cancelled, updated.state)
        assertNull(updated.files.single().temporaryDocument)
        assertEquals(0, updated.files.single().bytesWritten)
    }

    @Test
    fun `discard partial is rejected for a running transfer`() = runTest {
        val journal = TransferJournal(temporary.newFolder("running"))
        val record = actionRecord(
            state = TransferState.Running,
            temporaryDocument = "android-partial",
        )
        journal.write(record)
        val destination = RecordingDestination()
        val controller = TransferActionController(
            journal = journal,
            scheduler = TransferScheduler { PlatformScheduleResult.Success },
            destination = { destination },
            cancelJob = {},
            openConflict = {},
        )

        val result = controller.perform(record.id, TransferAction.DiscardPartialData)

        assertEquals(TransferActionResult.Rejected, result)
        assertTrue(destination.deleted.isEmpty())
    }

    @Test
    fun `visible resume restores paused journal when Android rejects scheduling`() =
        runTest {
            val journal = TransferJournal(temporary.newFolder("resume"))
            val record = actionRecord(state = TransferState.Paused)
            journal.write(record)
            val controller = TransferActionController(
                journal = journal,
                scheduler = TransferScheduler { PlatformScheduleResult.Failure },
                destination = { RecordingDestination() },
                cancelJob = {},
                openConflict = {},
            )

            val result = controller.perform(record.id, TransferAction.Resume)

            assertEquals(TransferActionResult.Rejected, result)
            assertEquals(TransferState.Paused, journal.read(record.id)?.state)
        }

    @Test
    fun `pause and cancel target only the Android job for this transfer`() = runTest {
        val journal = TransferJournal(temporary.newFolder("commands"))
        val paused = actionRecord(state = TransferState.Running)
        journal.write(paused)
        val cancelledJobs = mutableListOf<Int>()
        val controller = TransferActionController(
            journal = journal,
            scheduler = TransferScheduler { PlatformScheduleResult.Success },
            destination = { RecordingDestination() },
            cancelJob = { cancelledJobs += it },
            openConflict = {},
        )

        val result = controller.perform(paused.id, TransferAction.Pause)

        assertEquals(TransferActionResult.Accepted, result)
        assertEquals(TransferState.Paused, journal.read(paused.id)?.state)
        assertEquals(1, cancelledJobs.size)
    }

    @Test
    fun `discard checkpoints each Android temporary deletion before the next`() = runTest {
        val journal = TransferJournal(temporary.newFolder("checkpoint-discard"))
        val record = actionRecord(
            state = TransferState.Cancelled,
            temporaryDocument = "android-partial-one",
        ).let { first ->
            first.copy(
                manifest = first.manifest.copy(
                    files = listOf(
                        first.manifest.files.single(),
                        first.manifest.files.single().copy(
                            fileId = "signed-file-id-two",
                            destinationPath = "Dune/Part Two.m4b",
                        ),
                    ),
                    totalBytes = 20,
                ),
                files = listOf(
                    first.files.single(),
                    first.files.single().copy(
                        fileId = "signed-file-id-two",
                        temporaryDocument = "android-partial-two",
                    ),
                ),
            )
        }
        journal.write(record)
        val destination = RecordingDestination(failOn = "android-partial-two")
        val controller = TransferActionController(
            journal = journal,
            scheduler = TransferScheduler { PlatformScheduleResult.Success },
            destination = { destination },
            cancelJob = {},
            openConflict = {},
        )

        val result = controller.perform(record.id, TransferAction.DiscardPartialData)

        assertEquals(TransferActionResult.Rejected, result)
        val checkpoint = journal.read(record.id)!!
        assertNull(checkpoint.files[0].temporaryDocument)
        assertEquals(0L, checkpoint.files[0].bytesWritten)
        assertEquals("android-partial-two", checkpoint.files[1].temporaryDocument)
        assertEquals(
            listOf("android-partial-one", "android-partial-two"),
            destination.attemptedDeletes,
        )
    }
}

private class RecordingDestination(
    private val failOn: String? = null,
) : TransferDestination {
    val deleted = mutableListOf<String>()
    val attemptedDeletes = mutableListOf<String>()

    override fun find(relativePath: String): DestinationDocument? = null

    override fun createTemporary(
        destinationPath: String,
        transferId: String,
        fileId: String,
    ): DestinationDocument = error("Not used")

    override fun stat(documentId: String): DestinationDocument = error("Not used")

    override fun openInput(documentId: String): InputStream = error("Not used")

    override fun openOutput(documentId: String, offset: Long): OutputStream =
        error("Not used")

    override fun delete(documentId: String) {
        attemptedDeletes += documentId
        if (documentId == failOn) {
            error("simulated Android provider failure")
        }
        deleted += documentId
    }

    override fun finalize(
        documentId: String,
        destinationPath: String,
    ): DestinationDocument = error("Not used")
}

private fun actionRecord(
    state: TransferState,
    temporaryDocument: String? = null,
) = TransferRecord(
    id = UUID.randomUUID().toString(),
    manifest = ManifestDto(
        id = "Dune",
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

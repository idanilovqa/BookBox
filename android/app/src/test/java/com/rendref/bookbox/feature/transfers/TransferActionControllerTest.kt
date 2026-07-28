package com.rendref.bookbox.feature.transfers

import com.rendref.bookbox.core.model.ManifestDto
import com.rendref.bookbox.core.model.ManifestFileDto
import com.rendref.bookbox.core.transfer.DestinationDocument
import com.rendref.bookbox.core.transfer.ConflictChoice
import com.rendref.bookbox.core.transfer.FileProgress
import com.rendref.bookbox.core.transfer.FileState
import com.rendref.bookbox.core.transfer.JournalTemporaryWriter
import com.rendref.bookbox.core.transfer.PlatformScheduleResult
import com.rendref.bookbox.core.transfer.TransferDestination
import com.rendref.bookbox.core.transfer.TransferJournal
import com.rendref.bookbox.core.transfer.TransferRecord
import com.rendref.bookbox.core.transfer.TransferScheduler
import com.rendref.bookbox.core.transfer.TransferState
import java.io.FileOutputStream
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.util.UUID
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
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
    fun `conflict choice restores complete waiting journal when Android rejects scheduling`() {
        val journal = TransferJournal(temporary.newFolder("conflict-schedule"))
        val record = actionRecord(state = TransferState.WaitingForConflict).let {
            it.copy(
                files = it.files.map { file ->
                    file.copy(
                        state = FileState.WaitingForConflict,
                        existingBytes = 9,
                    )
                },
            )
        }
        journal.write(record)
        val controller = TransferActionController(
            journal = journal,
            scheduler = TransferScheduler { PlatformScheduleResult.Failure },
            destination = { RecordingDestination() },
            cancelJob = {},
            openConflict = {},
        )

        val result = controller.chooseConflict(record.id, ConflictChoice.Replace)

        assertEquals(TransferActionResult.Rejected, result)
        assertEquals(record, journal.read(record.id))
    }

    @Test
    fun `conflict choice restores waiting journal when Android scheduling throws`() {
        val journal = TransferJournal(temporary.newFolder("conflict-schedule-throw"))
        val record = waitingConflictRecord()
        journal.write(record)
        val controller = TransferActionController(
            journal = journal,
            scheduler = TransferScheduler { error("simulated scheduler failure") },
            destination = { RecordingDestination() },
            cancelJob = {},
            openConflict = {},
        )

        val result = controller.chooseConflict(record.id, ConflictChoice.KeepBoth)

        assertEquals(TransferActionResult.Rejected, result)
        assertEquals(record, journal.read(record.id))
    }

    @Test
    fun `conflict choice rejects journal write failure without changing waiting state`() {
        val filesDir = temporary.newFolder("conflict-journal-failure")
        val readableJournal = TransferJournal(filesDir)
        val record = waitingConflictRecord()
        readableJournal.write(record)
        val failingJournal = TransferJournal(
            filesDir = filesDir,
            temporaryWriter = JournalTemporaryWriter { _, _ ->
                throw IOException("simulated journal failure")
            },
        )
        val controller = TransferActionController(
            journal = failingJournal,
            scheduler = TransferScheduler { PlatformScheduleResult.Success },
            destination = { RecordingDestination() },
            cancelJob = {},
            openConflict = {},
        )

        val result = controller.chooseConflict(record.id, ConflictChoice.Skip)

        assertEquals(TransferActionResult.Rejected, result)
        assertEquals(record, readableJournal.read(record.id))
    }

    @Test
    fun `conflict choice rescues pending journal when rollback persistence fails`() {
        val filesDir = temporary.newFolder("conflict-rollback-failure")
        val readableJournal = TransferJournal(filesDir)
        val record = waitingConflictRecord()
        readableJournal.write(record)
        var writes = 0
        val rollbackFailingJournal = TransferJournal(
            filesDir = filesDir,
            temporaryWriter = JournalTemporaryWriter { temporaryFile, bytes ->
                writes += 1
                if (writes == 2) {
                    throw IOException("simulated rollback failure")
                }
                FileOutputStream(temporaryFile).use { stream ->
                    stream.write(bytes)
                    stream.fd.sync()
                }
            },
        )
        var scheduleCalls = 0
        val controller = TransferActionController(
            journal = rollbackFailingJournal,
            scheduler = TransferScheduler {
                scheduleCalls += 1
                if (scheduleCalls == 1) {
                    PlatformScheduleResult.Failure
                } else {
                    PlatformScheduleResult.Success
                }
            },
            destination = { RecordingDestination() },
            cancelJob = {},
            openConflict = {},
        )

        val result = controller.chooseConflict(record.id, ConflictChoice.Replace)

        assertEquals(TransferActionResult.Accepted, result)
        assertEquals(2, scheduleCalls)
        assertEquals(TransferState.Pending, readableJournal.read(record.id)?.state)
    }

    @Test
    fun `conflict choice surfaces rollback failure when rescue scheduling also fails`() {
        val filesDir = temporary.newFolder("conflict-unrecoverable")
        val readableJournal = TransferJournal(filesDir)
        val record = waitingConflictRecord()
        readableJournal.write(record)
        var writes = 0
        val rollbackFailingJournal = TransferJournal(
            filesDir = filesDir,
            temporaryWriter = JournalTemporaryWriter { temporaryFile, bytes ->
                writes += 1
                if (writes == 2) {
                    throw IOException("simulated rollback failure")
                }
                FileOutputStream(temporaryFile).use { stream ->
                    stream.write(bytes)
                    stream.fd.sync()
                }
            },
        )
        val controller = TransferActionController(
            journal = rollbackFailingJournal,
            scheduler = TransferScheduler { PlatformScheduleResult.Failure },
            destination = { RecordingDestination() },
            cancelJob = {},
            openConflict = {},
        )

        val failure = assertThrows(IllegalStateException::class.java) {
            controller.chooseConflict(record.id, ConflictChoice.Replace)
        }

        assertTrue(failure.message!!.contains("journal rollback both failed"))
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

private fun waitingConflictRecord() =
    actionRecord(state = TransferState.WaitingForConflict).let { record ->
        record.copy(
            files = record.files.map { file ->
                file.copy(
                    state = FileState.WaitingForConflict,
                    existingBytes = 9,
                )
            },
        )
    }

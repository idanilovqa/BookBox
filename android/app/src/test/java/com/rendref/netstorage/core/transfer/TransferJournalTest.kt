package com.rendref.netstorage.core.transfer

import com.rendref.netstorage.core.model.ManifestDto
import com.rendref.netstorage.core.model.ManifestFileDto
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import kotlinx.serialization.json.Json
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class TransferJournalTest {
    @get:Rule
    val temporaryFolder = TemporaryFolder()

    @Test
    fun `partial replacement cannot corrupt the last valid journal`() {
        val filesDir = temporaryFolder.newFolder("files")
        val journal = TransferJournal(filesDir)
        val original = transferRecord()
        journal.write(original)
        val changed = original.copy(
            state = TransferState.Running,
            files = original.files.map {
                it.copy(bytesWritten = 4, state = FileState.Downloading)
            },
        )
        val failingJournal = TransferJournal(
            filesDir = filesDir,
            temporaryWriter = JournalTemporaryWriter { temporary, bytes ->
                FileOutputStream(temporary).use { stream ->
                    stream.write(bytes, 0, bytes.size / 2)
                    stream.fd.sync()
                }
                throw IOException("simulated interrupted journal write")
            },
        )

        assertThrows(IOException::class.java) {
            failingJournal.write(changed)
        }

        assertEquals(original, TransferJournal(filesDir).read(original.id))
        assertTrue(File(filesDir, "transfers/${original.id}.json").isFile)
        assertTrue(File(filesDir, "transfers/${original.id}.json.tmp").isFile)
        assertFalse(filesDir.walkTopDown().any { it.extension.equals("db", ignoreCase = true) })
    }

    @Test
    fun `journal round trips conflict decisions as bounded JSON`() {
        val filesDir = temporaryFolder.newFolder("round-trip")
        val record = transferRecord().copy(
            state = TransferState.WaitingForConflict,
            files = listOf(
                transferRecord().files.single().copy(
                    state = FileState.WaitingForConflict,
                    conflictChoice = ConflictChoice.KeepBoth,
                ),
            ),
        )

        TransferJournal(filesDir).write(record)

        assertEquals(record, TransferJournal(filesDir).read(record.id))
        val encoded = File(filesDir, "transfers/${record.id}.json").readText()
        assertEquals(record, Json.decodeFromString<TransferRecord>(encoded))
    }

    @Test
    fun `unsafe transfer identifiers never escape the transfer directory`() {
        val journal = TransferJournal(temporaryFolder.newFolder("unsafe"))

        listOf("", ".", "..", "../outside", "folder/id", "folder\\id", "bad\u0000id")
            .forEach { id ->
                assertThrows(IllegalArgumentException::class.java) {
                    journal.read(id)
                }
            }
    }

    @Test
    fun `history lists only committed journals in stable identifier order`() {
        val filesDir = temporaryFolder.newFolder("history")
        val journal = TransferJournal(filesDir)
        journal.write(transferRecord().copy(id = "transfer-b"))
        journal.write(transferRecord().copy(id = "transfer-a"))
        File(filesDir, "transfers/transfer-c.json.tmp").writeText("partial")
        File(filesDir, "transfers/not-a-journal.txt").writeText("ignored")

        assertEquals(
            listOf("transfer-a", "transfer-b"),
            journal.list().map(TransferRecord::id),
        )
    }

    @Test
    fun `atomic update persists paused state and system stop reason`() {
        val filesDir = temporaryFolder.newFolder("update")
        val journal = TransferJournal(filesDir)
        journal.write(transferRecord())

        val updated = journal.update("transfer-1") {
            it.copy(
                state = TransferState.Paused,
                stopReason = 7,
            )
        }

        assertEquals(TransferState.Paused, updated.state)
        assertEquals(7, updated.stopReason)
        assertEquals(updated, TransferJournal(filesDir).read("transfer-1"))
    }

    @Test
    fun `atomic update rejects a changed transfer identifier`() {
        val filesDir = temporaryFolder.newFolder("changed-id")
        val journal = TransferJournal(filesDir)
        journal.write(transferRecord())

        assertThrows(IllegalArgumentException::class.java) {
            journal.update("transfer-1") { it.copy(id = "different-transfer") }
        }

        assertEquals("transfer-1", journal.read("transfer-1")?.id)
    }

    @Test
    fun `display name is persisted without exposing a source path`() {
        val filesDir = temporaryFolder.newFolder("display-name")
        val journal = TransferJournal(filesDir)
        val record = transferRecord().copy(displayName = "Dune")

        journal.write(record)

        assertEquals("Dune", journal.read(record.id)?.displayName)
        val encoded = File(filesDir, "transfers/${record.id}.json").readText()
        assertFalse(encoded.contains("H:\\"))
    }

    @Test
    fun `legacy journal without display name remains readable`() {
        val filesDir = temporaryFolder.newFolder("legacy-display")
        val directory = File(filesDir, "transfers").apply { mkdirs() }
        val legacy = """
            {
              "id":"transfer-1",
              "manifest":{
                "id":"opaque-manifest-id",
                "files":[{
                  "fileId":"signed-file-id",
                  "sourcePath":"Dune/Dune.m4b",
                  "destinationPath":"Dune/Dune.m4b",
                  "size":10,
                  "modifiedAt":"2026-07-26T12:00:00Z",
                  "etag":"\"source-etag\"",
                  "sha256":"72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0"
                }],
                "totalBytes":10
              },
              "files":[{
                "fileId":"signed-file-id",
                "temporaryDocument":null,
                "bytesWritten":0,
                "state":"Pending"
              }],
              "destinationTree":"content://documents/tree/audiobooks",
              "state":"Pending"
            }
        """.trimIndent()
        File(directory, "transfer-1.json").writeText(legacy)

        assertNull(TransferJournal(filesDir).read("transfer-1")?.displayName)
    }

    @Test
    fun `display name rejects paths controls and URI shaped values`() {
        val journal = TransferJournal(temporaryFolder.newFolder("unsafe-display"))

        listOf(
            "",
            "Author/Book",
            "Author\\Book",
            "H:\\Audiobooks",
            "content://documents/tree/audiobooks",
            "bad\u0000name",
        ).forEach { unsafe ->
            assertThrows(IllegalArgumentException::class.java) {
                journal.write(transferRecord().copy(displayName = unsafe))
            }
        }
    }
}

private fun transferRecord() = TransferRecord(
    id = "transfer-1",
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
            temporaryDocument = null,
            bytesWritten = 0,
            state = FileState.Pending,
        ),
    ),
    destinationTree = "content://documents/tree/audiobooks",
    state = TransferState.Pending,
)

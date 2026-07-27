package com.rendref.netstorage.core.transfer

import com.rendref.netstorage.core.model.ManifestDto
import com.rendref.netstorage.core.model.ManifestFileDto
import com.rendref.netstorage.core.network.TransferManifestSource
import com.rendref.netstorage.core.network.TransferSelection
import java.io.IOException
import java.util.UUID
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class TransferStarterTest {
    @get:Rule
    val temporaryFolder = TemporaryFolder()

    @Test
    fun `visible copy validates destination journals initial progress then schedules`() =
        runTest {
            val journal = TransferJournal(temporaryFolder.newFolder("visible-copy"))
            val events = mutableListOf<String>()
            val manifest = validStarterManifest()
            val source = TransferManifestSource {
                events += "manifest"
                manifest
            }
            val scheduler = TransferScheduler { requirements ->
                events += "schedule"
                assertTrue(journal.read(requirements.transferId) != null)
                PlatformScheduleResult.Success
            }
            val starter = TransferStarter(
                manifests = source,
                destination = TransferDestinationSource {
                    events += "destination"
                    "content://com.android.externalstorage.documents/tree/primary%3AAudiobooks"
                },
                journal = journal,
                scheduler = scheduler,
                transferId = { TRANSFER_ID },
            )

            val result = starter.startFromVisibleCopyAction(
                TransferStartRequest(
                    selections = listOf(
                        TransferSelection.Book(BOOK_ID),
                    ),
                    displayName = "Dune",
                ),
            )

            assertEquals(TransferStartResult.Scheduled(TRANSFER_ID), result)
            assertEquals(listOf("destination", "manifest", "schedule"), events)
            val record = checkNotNull(journal.read(TRANSFER_ID))
            assertEquals(manifest, record.manifest)
            assertEquals("Dune", record.displayName)
            assertEquals(
                "content://com.android.externalstorage.documents/tree/primary%3AAudiobooks",
                record.destinationTree,
            )
            assertEquals(TransferState.Pending, record.state)
            assertEquals(
                manifest.files.map {
                    FileProgress(
                        fileId = it.fileId,
                        temporaryDocument = null,
                        bytesWritten = 0,
                        state = FileState.Pending,
                    )
                },
                record.files,
            )
        }

    @Test
    fun `invalid manifest never journals or schedules`() = runTest {
        val files = temporaryFolder.newFolder("invalid")
        val journal = TransferJournal(files)
        var schedules = 0
        val starter = TransferStarter(
            manifests = TransferManifestSource {
                validStarterManifest().copy(totalBytes = 99)
            },
            destination = TransferDestinationSource { "content://provider/tree/audiobooks" },
            journal = journal,
            scheduler = TransferScheduler {
                schedules += 1
                PlatformScheduleResult.Success
            },
            transferId = { TRANSFER_ID },
        )

        val failure = assertThrows(IOException::class.java) {
            kotlinx.coroutines.runBlocking {
                starter.startFromVisibleCopyAction(
                    TransferStartRequest(
                        selections = listOf(TransferSelection.Book(BOOK_ID)),
                        displayName = "Dune",
                    ),
                )
            }
        }

        assertEquals("Server returned an invalid transfer manifest", failure.message)
        assertNull(journal.read(TRANSFER_ID))
        assertEquals(0, schedules)
    }

    @Test
    fun `unsafe display names destinations and transfer ids stop before scheduling`() =
        runTest {
            val journal = TransferJournal(temporaryFolder.newFolder("unsafe"))
            var schedules = 0

            suspend fun start(
                displayName: String = "Dune",
                destination: String = "content://provider/tree/audiobooks",
                id: String = TRANSFER_ID,
            ) {
                TransferStarter(
                    manifests = TransferManifestSource { validStarterManifest() },
                    destination = TransferDestinationSource { destination },
                    journal = journal,
                    scheduler = TransferScheduler {
                        schedules += 1
                        PlatformScheduleResult.Success
                    },
                    transferId = { id },
                ).startFromVisibleCopyAction(
                    TransferStartRequest(
                        selections = listOf(TransferSelection.Book(BOOK_ID)),
                        displayName = displayName,
                    ),
                )
            }

            listOf("H:/Audiobooks/Dune", "content://provider/private", "bad\nname")
                .forEach { displayName ->
                    assertThrows(IllegalArgumentException::class.java) {
                        kotlinx.coroutines.runBlocking { start(displayName = displayName) }
                    }
                }
            assertThrows(IllegalArgumentException::class.java) {
                kotlinx.coroutines.runBlocking { start(destination = "H:/Audiobooks") }
            }
            assertThrows(IllegalArgumentException::class.java) {
                kotlinx.coroutines.runBlocking { start(id = "not-a-uuid") }
            }
            assertEquals(0, schedules)
        }

    @Test
    fun `platform rejection is reported with pending journal available for visible retry`() =
        runTest {
            val journal = TransferJournal(temporaryFolder.newFolder("rejected"))
            val starter = TransferStarter(
                manifests = TransferManifestSource { validStarterManifest() },
                destination = TransferDestinationSource { "content://provider/tree/audiobooks" },
                journal = journal,
                scheduler = TransferScheduler { PlatformScheduleResult.Failure },
                transferId = { TRANSFER_ID },
            )

            val result = starter.startFromVisibleCopyAction(
                TransferStartRequest(
                    selections = listOf(TransferSelection.Book(BOOK_ID)),
                    displayName = "Dune",
                ),
            )

            assertEquals(TransferStartResult.ScheduleRejected(TRANSFER_ID), result)
            assertEquals(TransferState.Pending, journal.read(TRANSFER_ID)?.state)
        }
}

private fun validStarterManifest() = ManifestDto(
    id = "AQIDBAUGBwgJCgsMDQ4PEA",
    files = listOf(
        ManifestFileDto(
            fileId = "signed-file-id",
            sourcePath = "Dune/Dune.m4b",
            destinationPath = "Dune/Dune.m4b",
            size = 4,
            modifiedAt = "2026-07-26T12:00:00Z",
            etag = "\"source-etag\"",
            sha256 = STARTER_SHA256,
        ),
    ),
    totalBytes = 4,
)

private const val BOOK_ID = "f8287348-42e7-4f78-a58d-bc067819dffd"
private const val TRANSFER_ID = "bc433612-0e16-4c48-8266-93965f2b706f"
private const val STARTER_SHA256 =
    "72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0"

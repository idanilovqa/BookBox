package com.rendref.netstorage.feature.transfers

import com.rendref.netstorage.core.model.ManifestDto
import com.rendref.netstorage.core.model.ManifestFileDto
import com.rendref.netstorage.core.transfer.FileProgress
import com.rendref.netstorage.core.transfer.FileState
import com.rendref.netstorage.core.transfer.TransferRecord
import com.rendref.netstorage.core.transfer.TransferState
import java.util.UUID
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class TransfersViewModelTest {
    private val dispatcher = StandardTestDispatcher()

    @Before
    fun setUp() {
        Dispatchers.setMain(dispatcher)
    }

    @After
    fun tearDown() {
        Dispatchers.resetMain()
    }

    @Test
    fun `history calculates bounded overall and file progress`() = runTest(dispatcher) {
        val record = historyRecord(
            state = TransferState.Running,
            bytesWritten = 4,
        )
        val viewModel = TransfersViewModel(
            history = { listOf(record) },
            action = { _, _ -> TransferActionResult.Accepted },
            dispatcher = dispatcher,
        )

        dispatcher.scheduler.advanceUntilIdle()

        val row = viewModel.state.value.transfers.single()
        assertEquals(4, row.bytesWritten)
        assertEquals(10, row.totalBytes)
        assertEquals(0.4f, row.progress)
        assertEquals("Dune/Dune.m4b", row.currentFile)
        assertEquals(0, row.completedFiles)
        assertEquals(1, row.totalFiles)
    }

    @Test
    fun `cancelled history offers discard without deleting automatically`() =
        runTest(dispatcher) {
            val actions = mutableListOf<TransferAction>()
            val viewModel = TransfersViewModel(
                history = {
                    listOf(
                        historyRecord(
                            state = TransferState.Cancelled,
                            bytesWritten = 4,
                            temporaryDocument = "android-partial-document",
                        ),
                    )
                },
                action = { _, action ->
                    actions += action
                    TransferActionResult.Accepted
                },
                dispatcher = dispatcher,
            )
            dispatcher.scheduler.advanceUntilIdle()

            val row = viewModel.state.value.transfers.single()
            assertTrue(TransferAction.DiscardPartialData in row.availableActions)
            assertTrue(actions.isEmpty())

            viewModel.perform(row.id, TransferAction.DiscardPartialData)
            dispatcher.scheduler.advanceUntilIdle()

            assertEquals(listOf(TransferAction.DiscardPartialData), actions)
        }

    @Test
    fun `completed and skipped bytes never exceed manifest total`() =
        runTest(dispatcher) {
            val malformedProgress = historyRecord(
                state = TransferState.Completed,
                bytesWritten = 20,
            )
            val viewModel = TransfersViewModel(
                history = { listOf(malformedProgress) },
                action = { _, _ -> TransferActionResult.Accepted },
                dispatcher = dispatcher,
            )

            dispatcher.scheduler.advanceUntilIdle()

            val row = viewModel.state.value.transfers.single()
            assertEquals(10, row.bytesWritten)
            assertEquals(1f, row.progress)
            assertFalse(row.progress > 1f)
        }

    @Test
    fun `legacy history derives a safe title from destination without showing opaque ids`() =
        runTest(dispatcher) {
            val legacy = historyRecord(
                state = TransferState.Paused,
                bytesWritten = 0,
            ).copy(
                displayName = null,
                manifest = historyRecord(
                    state = TransferState.Paused,
                    bytesWritten = 0,
                ).manifest.copy(id = "e3403f27-opaque-manifest-id"),
            )
            val viewModel = TransfersViewModel(
                history = { listOf(legacy) },
                action = { _, _ -> TransferActionResult.Accepted },
                dispatcher = dispatcher,
            )

            dispatcher.scheduler.advanceUntilIdle()

            val row = viewModel.state.value.transfers.single()
            assertEquals("Dune", row.title)
            assertFalse(row.title.contains("opaque"))
            assertFalse(row.title.contains("content://"))
        }
}

private fun historyRecord(
    state: TransferState,
    bytesWritten: Long,
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
            bytesWritten = bytesWritten,
            state = if (state == TransferState.Completed) {
                FileState.Completed
            } else {
                FileState.Downloading
            },
        ),
    ),
    destinationTree = "content://documents/tree/audiobooks",
    state = state,
    displayName = "Dune",
)

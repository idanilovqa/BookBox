package com.rendref.netstorage.feature.transfers

import com.rendref.netstorage.core.transfer.FileState
import com.rendref.netstorage.core.transfer.TransferCommand
import com.rendref.netstorage.core.transfer.TransferCommandHandler
import com.rendref.netstorage.core.transfer.TransferDestination
import com.rendref.netstorage.core.transfer.TransferJournal
import com.rendref.netstorage.core.transfer.TransferRecord
import com.rendref.netstorage.core.transfer.TransferScheduleResult
import com.rendref.netstorage.core.transfer.TransferScheduler
import com.rendref.netstorage.core.transfer.TransferState

class TransferActionController(
    private val journal: TransferJournal,
    private val scheduler: TransferScheduler,
    private val destination: suspend (TransferRecord) -> TransferDestination,
    private val cancelJob: (Int) -> Unit,
    private val openConflict: (String) -> Unit,
) {
    suspend fun perform(
        transferId: String,
        action: TransferAction,
    ): TransferActionResult = runCatching {
        when (action) {
            TransferAction.Pause -> command(transferId, TransferCommand.Pause)
            TransferAction.Cancel -> command(transferId, TransferCommand.Cancel)
            TransferAction.Resume -> resume(transferId)
            TransferAction.ResolveConflict -> resolveConflict(transferId)
            TransferAction.DiscardPartialData -> discardPartial(transferId)
        }
    }.getOrDefault(TransferActionResult.Rejected)

    private fun command(
        transferId: String,
        command: TransferCommand,
    ): TransferActionResult {
        TransferCommandHandler(
            journal = journal,
            cancelJob = cancelJob,
            hasPendingJob = { false },
        ).handle(command, transferId)
        return TransferActionResult.Accepted
    }

    private fun resume(transferId: String): TransferActionResult {
        val previous = journal.read(transferId)
            ?: return TransferActionResult.Rejected
        if (previous.state !in setOf(TransferState.Paused, TransferState.Failed)) {
            return TransferActionResult.Rejected
        }
        val pending = journal.update(transferId) {
            it.copy(
                state = TransferState.Pending,
                stopReason = null,
                files = it.files.map { file ->
                    if (file.state == FileState.Failed) {
                        file.copy(state = FileState.Pending)
                    } else {
                        file
                    }
                },
            )
        }
        return if (
            scheduler.scheduleFromVisibleAction(pending) ==
            TransferScheduleResult.Scheduled
        ) {
            TransferActionResult.Accepted
        } else {
            journal.update(transferId) { current ->
                if (current.state == TransferState.Pending) previous else current
            }
            TransferActionResult.Rejected
        }
    }

    private fun resolveConflict(transferId: String): TransferActionResult {
        val record = journal.read(transferId)
            ?: return TransferActionResult.Rejected
        if (record.state != TransferState.WaitingForConflict) {
            return TransferActionResult.Rejected
        }
        openConflict(transferId)
        return TransferActionResult.Accepted
    }

    private suspend fun discardPartial(
        transferId: String,
    ): TransferActionResult {
        val record = journal.read(transferId)
            ?: return TransferActionResult.Rejected
        if (record.state != TransferState.Cancelled) {
            return TransferActionResult.Rejected
        }
        val temporaryDocuments = record.files
            .mapNotNull { it.temporaryDocument }
            .distinct()
        if (temporaryDocuments.isEmpty()) {
            return TransferActionResult.Accepted
        }
        val tree = destination(record)
        temporaryDocuments.forEach { temporaryDocument ->
            tree.delete(temporaryDocument)
            journal.update(transferId) { current ->
                if (current.state != TransferState.Cancelled) {
                    current
                } else {
                    current.copy(
                        files = current.files.map { file ->
                            if (file.temporaryDocument == temporaryDocument) {
                                file.copy(
                                    temporaryDocument = null,
                                    bytesWritten = 0,
                                    state = FileState.Failed,
                                )
                            } else {
                                file
                            }
                        },
                    )
                }
            }
        }
        return TransferActionResult.Accepted
    }
}

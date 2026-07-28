package com.rendref.bookbox.feature.transfers

import com.rendref.bookbox.core.transfer.FileState
import com.rendref.bookbox.core.transfer.ConflictChoice
import com.rendref.bookbox.core.transfer.TransferCommand
import com.rendref.bookbox.core.transfer.TransferCommandHandler
import com.rendref.bookbox.core.transfer.TransferDestination
import com.rendref.bookbox.core.transfer.TransferJournal
import com.rendref.bookbox.core.transfer.TransferRecord
import com.rendref.bookbox.core.transfer.TransferScheduleResult
import com.rendref.bookbox.core.transfer.TransferScheduler
import com.rendref.bookbox.core.transfer.TransferState

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

    fun chooseConflict(
        transferId: String,
        choice: ConflictChoice,
    ): TransferActionResult = try {
        chooseConflictSafely(transferId, choice)
    } catch (failure: ConflictRecoveryException) {
        throw failure
    } catch (_: Exception) {
        TransferActionResult.Rejected
    }

    private fun chooseConflictSafely(
        transferId: String,
        choice: ConflictChoice,
    ): TransferActionResult {
        var previous: TransferRecord? = null
        val pending = journal.update(transferId) { record ->
            if (
                record.state != TransferState.WaitingForConflict ||
                record.files.none { it.state == FileState.WaitingForConflict }
            ) {
                return@update record
            }
            previous = record
            record.copy(
                state = TransferState.Pending,
                files = record.files.map { file ->
                    if (file.state == FileState.WaitingForConflict) {
                        file.copy(
                            state = FileState.Pending,
                            conflictChoice = choice,
                            existingBytes = null,
                        )
                    } else {
                        file
                    }
                },
            )
        }
        val waiting = previous
            ?: return TransferActionResult.Rejected
        val scheduleResult = runCatching {
            scheduler.scheduleFromVisibleAction(pending)
        }.getOrElse {
            return recoverConflict(transferId, pending, waiting)
        }
        return if (scheduleResult == TransferScheduleResult.Scheduled) {
            TransferActionResult.Accepted
        } else {
            recoverConflict(transferId, pending, waiting)
        }
    }

    private fun recoverConflict(
        transferId: String,
        pending: TransferRecord,
        waiting: TransferRecord,
    ): TransferActionResult {
        try {
            journal.update(transferId) { current ->
                if (current == pending) waiting else current
            }
            return TransferActionResult.Rejected
        } catch (rollbackFailure: Exception) {
            val rescue = runCatching {
                scheduler.scheduleFromVisibleAction(pending)
            }.getOrDefault(TransferScheduleResult.Failure)
            if (rescue == TransferScheduleResult.Scheduled) {
                return TransferActionResult.Accepted
            }
            throw ConflictRecoveryException(rollbackFailure)
        }
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

private class ConflictRecoveryException(
    cause: Throwable,
) : IllegalStateException(
    "Conflict scheduling and journal rollback both failed",
    cause,
)

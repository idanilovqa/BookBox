package com.rendref.bookbox.core.transfer

import android.app.job.JobParameters
import android.app.job.JobScheduler
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

enum class TransferCommand {
    Pause,
    Cancel,
    ReconcileBoot,
}

class TransferCommandHandler(
    private val journal: TransferJournal,
    private val cancelJob: (Int) -> Unit,
    private val hasPendingJob: (String) -> Boolean,
) {
    fun handle(command: TransferCommand, transferId: String?) {
        when (command) {
            TransferCommand.Pause -> changeStateAndCancel(
                requireNotNull(transferId),
                TransferState.Paused,
            )
            TransferCommand.Cancel -> changeStateAndCancel(
                requireNotNull(transferId),
                TransferState.Cancelled,
            )
            TransferCommand.ReconcileBoot -> reconcileBoot()
        }
    }

    private fun changeStateAndCancel(
        transferId: String,
        state: TransferState,
    ) {
        journal.update(transferId) { current ->
            if (current.state == TransferState.Completed) {
                current
            } else {
                current.copy(
                    state = state,
                    stopReason = JobParameters.STOP_REASON_CANCELLED_BY_APP,
                )
            }
        }.let { updated ->
            if (updated.state == state) {
                cancelJob(transferJobId(transferId))
            }
        }
    }

    private fun reconcileBoot() {
        journal.list().forEach { record ->
            if (record.state !in setOf(TransferState.Running, TransferState.Pending)) {
                return@forEach
            }
            val stillPersisted = runCatching {
                hasPendingJob(record.id)
            }.getOrDefault(false)
            if (!stillPersisted) {
                journal.update(record.id) {
                    it.copy(
                        state = TransferState.Paused,
                        stopReason = JobParameters.STOP_REASON_UNDEFINED,
                    )
                }
            }
        }
    }
}

class TransferReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val command = when (intent.action) {
            ACTION_PAUSE_TRANSFER -> TransferCommand.Pause
            ACTION_CANCEL_TRANSFER -> TransferCommand.Cancel
            Intent.ACTION_BOOT_COMPLETED -> TransferCommand.ReconcileBoot
            else -> return
        }
        val transferId = intent.getStringExtra(TRANSFER_ID_EXTRA)
        if (command != TransferCommand.ReconcileBoot &&
            (transferId == null || !TRANSFER_UUID.matches(transferId))
        ) {
            return
        }
        val jobs = context.getSystemService(JobScheduler::class.java)
        val handler = TransferCommandHandler(
            journal = TransferJournal(context.filesDir),
            cancelJob = jobs::cancel,
            hasPendingJob = { id -> jobs.getPendingJob(transferJobId(id)) != null },
        )
        runCatching {
            handler.handle(command, transferId)
        }
    }

    companion object {
        const val ACTION_PAUSE_TRANSFER =
            "com.rendref.bookbox.action.PAUSE_TRANSFER"
        const val ACTION_CANCEL_TRANSFER =
            "com.rendref.bookbox.action.CANCEL_TRANSFER"
    }
}

private val TRANSFER_UUID =
    Regex("^[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$")

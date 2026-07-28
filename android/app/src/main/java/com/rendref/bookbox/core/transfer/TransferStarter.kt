package com.rendref.bookbox.core.transfer

import com.rendref.bookbox.core.network.TransferManifestSource
import com.rendref.bookbox.core.network.TransferSelection
import com.rendref.bookbox.core.network.validateTransferManifest
import java.util.UUID

data class TransferStartRequest(
    val selections: List<TransferSelection>,
    val displayName: String,
)

sealed interface TransferStartResult {
    data class Scheduled(val transferId: String) : TransferStartResult

    data class ScheduleRejected(val transferId: String) : TransferStartResult
}

fun interface TransferDestinationSource {
    suspend fun requirePersistedTreeUri(): String
}

class TransferStarter(
    private val manifests: TransferManifestSource,
    private val destination: TransferDestinationSource,
    private val journal: TransferJournal,
    private val scheduler: TransferScheduler,
    private val transferId: () -> String = { UUID.randomUUID().toString() },
) : TransferStartSource {
    override suspend fun startFromVisibleCopyAction(
        request: TransferStartRequest,
    ): TransferStartResult {
        requireSafeDisplayName(request.displayName)
        val destinationTree = destination.requirePersistedTreeUri()
        requireSafeDestinationTree(destinationTree)
        val manifest = validateTransferManifest(
            manifests.create(request.selections),
        )
        val id = transferId()
        require(runCatching { UUID.fromString(id) }.getOrNull()?.let { it != ZERO_UUID } == true)
        val record = TransferRecord(
            id = id,
            manifest = manifest,
            files = manifest.files.map { file ->
                FileProgress(
                    fileId = file.fileId,
                    temporaryDocument = null,
                    bytesWritten = 0,
                    state = FileState.Pending,
                )
            },
            destinationTree = destinationTree,
            state = TransferState.Pending,
            displayName = request.displayName,
        )
        journal.write(record)
        return when (scheduler.scheduleFromVisibleAction(record)) {
            TransferScheduleResult.Scheduled -> TransferStartResult.Scheduled(id)
            TransferScheduleResult.Failure -> TransferStartResult.ScheduleRejected(id)
        }
    }
}

fun interface TransferStartSource {
    suspend fun startFromVisibleCopyAction(
        request: TransferStartRequest,
    ): TransferStartResult
}

private fun requireSafeDisplayName(displayName: String) {
    require(
        displayName.isNotBlank() &&
            displayName.length <= MAX_DISPLAY_NAME_CHARS &&
            !displayName.startsWith("/") &&
            !displayName.startsWith("\\") &&
            !displayName.contains('/') &&
            !displayName.contains('\\') &&
            !displayName.contains("://") &&
            !WINDOWS_VOLUME.containsMatchIn(displayName) &&
            displayName.none(Char::isISOControl),
    ) {
        "Transfer display name is invalid"
    }
}

private fun requireSafeDestinationTree(destinationTree: String) {
    require(
        destinationTree.length in 1..MAX_DESTINATION_URI_CHARS &&
            destinationTree.startsWith("content://") &&
            destinationTree.none(Char::isISOControl),
    ) {
        "Persisted Android destination is invalid"
    }
}

private const val MAX_DISPLAY_NAME_CHARS = 200
private const val MAX_DESTINATION_URI_CHARS = 4_096
private val WINDOWS_VOLUME = Regex("^[A-Za-z]:")
private val ZERO_UUID = UUID(0L, 0L)

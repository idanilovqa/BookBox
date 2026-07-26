package com.rendref.netstorage.feature.transfers

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.netstorage.core.transfer.FileState
import com.rendref.netstorage.core.transfer.TransferRecord
import com.rendref.netstorage.core.transfer.TransferState
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

enum class TransferAction {
    Pause,
    Resume,
    Cancel,
    ResolveConflict,
    DiscardPartialData,
}

enum class TransferActionResult {
    Accepted,
    Rejected,
}

data class TransferRowState(
    val id: String,
    val title: String,
    val state: TransferState,
    val bytesWritten: Long,
    val totalBytes: Long,
    val completedFiles: Int,
    val totalFiles: Int,
    val currentFile: String?,
    val availableActions: Set<TransferAction>,
) {
    val progress: Float
        get() = if (totalBytes <= 0) {
            0f
        } else {
            (bytesWritten.toDouble() / totalBytes.toDouble())
                .coerceIn(0.0, 1.0)
                .toFloat()
        }
}

data class TransfersState(
    val transfers: List<TransferRowState> = emptyList(),
    val isLoading: Boolean = false,
    val errorMessage: String? = null,
)

class TransfersViewModel(
    private val history: suspend () -> List<TransferRecord>,
    private val action: suspend (String, TransferAction) -> TransferActionResult,
    private val dispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {
    private val mutableState = MutableStateFlow(TransfersState(isLoading = true))
    val state: StateFlow<TransfersState> = mutableState.asStateFlow()

    init {
        refresh()
    }

    fun refresh() {
        viewModelScope.launch {
            mutableState.value = mutableState.value.copy(
                isLoading = true,
                errorMessage = null,
            )
            val result = runCatching {
                withContext(dispatcher) {
                    history().map(TransferRecord::toRow)
                }
            }
            mutableState.value = result.fold(
                onSuccess = {
                    TransfersState(
                        transfers = it,
                        isLoading = false,
                    )
                },
                onFailure = {
                    mutableState.value.copy(
                        isLoading = false,
                        errorMessage = "Unable to read transfer history",
                    )
                },
            )
        }
    }

    fun perform(
        transferId: String,
        transferAction: TransferAction,
    ) {
        val row = mutableState.value.transfers
            .firstOrNull { it.id == transferId }
            ?: return
        if (transferAction !in row.availableActions) return
        viewModelScope.launch {
            val result = runCatching {
                withContext(dispatcher) {
                    action(transferId, transferAction)
                }
            }.getOrDefault(TransferActionResult.Rejected)
            if (result == TransferActionResult.Accepted) {
                refresh()
            } else {
                mutableState.value = mutableState.value.copy(
                    errorMessage = "Unable to update this transfer",
                )
            }
        }
    }
}

private fun TransferRecord.toRow(): TransferRowState {
    val boundedBytes = files.zip(manifest.files).fold(0L) { total, (progress, file) ->
        val next = progress.bytesWritten.coerceIn(0, file.size)
        if (total > Long.MAX_VALUE - next) Long.MAX_VALUE else total + next
    }.coerceAtMost(manifest.totalBytes)
    val currentIndex = files.indexOfFirst {
        it.state !in setOf(FileState.Completed, FileState.Skipped)
    }
    return TransferRowState(
        id = id,
        title = safeDisplayName(),
        state = state,
        bytesWritten = boundedBytes,
        totalBytes = manifest.totalBytes,
        completedFiles = files.count {
            it.state == FileState.Completed || it.state == FileState.Skipped
        },
        totalFiles = files.size,
        currentFile = currentIndex.takeIf { it >= 0 }
            ?.let { manifest.files[it].destinationPath }
            ?.takeIf(::safeRelativeDestinationPath),
        availableActions = actions(),
    )
}

private fun TransferRecord.safeDisplayName(): String {
    displayName?.takeIf(::safeSingleDisplayName)?.let { return it }
    return manifest.files
        .mapNotNull { file ->
            file.destinationPath
                .takeIf(::safeRelativeDestinationPath)
                ?.substringBefore('/')
                ?.takeIf(::safeSingleDisplayName)
        }
        .distinct()
        .singleOrNull()
        ?: "Audiobook transfer"
}

private fun safeSingleDisplayName(value: String): Boolean =
    value.isNotBlank() &&
        value.length <= 200 &&
        !value.contains('/') &&
        !value.contains('\\') &&
        !value.contains("://") &&
        !WINDOWS_VOLUME.matches(value) &&
        value.none(Char::isISOControl)

private fun safeRelativeDestinationPath(value: String): Boolean =
    value.isNotBlank() &&
        !value.startsWith('/') &&
        !value.contains('\\') &&
        !value.contains("://") &&
        !WINDOWS_VOLUME.matches(value) &&
        value.none(Char::isISOControl) &&
        value.split('/').none { it.isEmpty() || it == "." || it == ".." }

private fun TransferRecord.actions(): Set<TransferAction> = when (state) {
    TransferState.Pending -> setOf(
        TransferAction.Pause,
        TransferAction.Cancel,
    )
    TransferState.Running -> setOf(
        TransferAction.Pause,
        TransferAction.Cancel,
    )
    TransferState.Paused,
    TransferState.Failed,
    -> setOf(
        TransferAction.Resume,
        TransferAction.Cancel,
    )
    TransferState.WaitingForConflict -> setOf(
        TransferAction.ResolveConflict,
        TransferAction.Cancel,
    )
    TransferState.Cancelled -> if (files.any { it.temporaryDocument != null }) {
        setOf(TransferAction.DiscardPartialData)
    } else {
        emptySet()
    }
    TransferState.Completed -> emptySet()
}

private val WINDOWS_VOLUME = Regex("^[A-Za-z]:.*")

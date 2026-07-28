package com.rendref.bookbox.feature.transfers

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.rendref.bookbox.core.transfer.TransferState
import java.util.Locale

@Composable
fun TransfersScreen(
    state: TransfersState,
    onAction: (String, TransferAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text(
            text = "Transfers",
            style = MaterialTheme.typography.headlineMedium,
            modifier = Modifier.padding(top = 20.dp),
        )
        Text(
            text = "Copies write only to the Android folder you selected.",
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        state.errorMessage?.let {
            Text(it, color = MaterialTheme.colorScheme.error)
        }
        if (state.isLoading && state.transfers.isEmpty()) {
            LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
        } else if (state.transfers.isEmpty()) {
            Text("No transfers yet")
        } else {
            LazyColumn(
                verticalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                items(
                    items = state.transfers,
                    key = TransferRowState::id,
                ) { transfer ->
                    TransferRow(
                        transfer = transfer,
                        onAction = { action ->
                            onAction(transfer.id, action)
                        },
                    )
                }
            }
        }
    }
}

@Composable
private fun TransferRow(
    transfer: TransferRowState,
    onAction: (TransferAction) -> Unit,
) {
    Card(modifier = Modifier.fillMaxWidth()) {
        Column(
            modifier = Modifier.padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Text(transfer.title, style = MaterialTheme.typography.titleLarge)
            Text(
                transfer.state.displayName(),
                color = MaterialTheme.colorScheme.primary,
                style = MaterialTheme.typography.labelLarge,
            )
            transfer.currentFile?.let {
                Text(it, style = MaterialTheme.typography.bodyMedium)
            }
            LinearProgressIndicator(
                progress = { transfer.progress },
                modifier = Modifier.fillMaxWidth(),
            )
            Text(
                "${formatHistoryBytes(transfer.bytesWritten)} of " +
                    "${formatHistoryBytes(transfer.totalBytes)} \u00b7 " +
                    "${transfer.completedFiles}/${transfer.totalFiles} files",
                style = MaterialTheme.typography.bodySmall,
            )
            if (TransferAction.DiscardPartialData in transfer.availableActions) {
                Text(
                    "Discard partial data removes only unfinished files on this " +
                        "Android device.",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    style = MaterialTheme.typography.bodySmall,
                )
            }
            Row(
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                transfer.availableActions.forEach { action ->
                    TransferActionButton(action, onAction)
                }
            }
        }
    }
}

@Composable
private fun TransferActionButton(
    action: TransferAction,
    onAction: (TransferAction) -> Unit,
) {
    when (action) {
        TransferAction.Pause,
        TransferAction.Resume,
        -> OutlinedButton(onClick = { onAction(action) }) {
            Text(action.label())
        }
        TransferAction.Cancel,
        TransferAction.DiscardPartialData,
        -> TextButton(onClick = { onAction(action) }) {
            Text(action.label())
        }
        TransferAction.ResolveConflict -> Button(
            onClick = { onAction(action) },
        ) {
            Text(action.label())
        }
    }
}

private fun TransferAction.label(): String = when (this) {
    TransferAction.Pause -> "Pause"
    TransferAction.Resume -> "Resume"
    TransferAction.Cancel -> "Cancel"
    TransferAction.ResolveConflict -> "Resolve conflict"
    TransferAction.DiscardPartialData -> "Discard partial data"
}

private fun TransferState.displayName(): String = when (this) {
    TransferState.Pending -> "Waiting"
    TransferState.Running -> "Running"
    TransferState.Paused -> "Paused"
    TransferState.WaitingForConflict -> "Needs a conflict choice"
    TransferState.Completed -> "Complete"
    TransferState.Failed -> "Failed"
    TransferState.Cancelled -> "Cancelled"
}

private fun formatHistoryBytes(bytes: Long): String = when {
    bytes < 1_024 -> "$bytes B"
    bytes < 1_048_576 -> String.format(Locale.ROOT, "%.1f KB", bytes / 1_024.0)
    bytes < 1_073_741_824 -> String.format(
        Locale.ROOT,
        "%.1f MB",
        bytes / 1_048_576.0,
    )
    else -> String.format(Locale.ROOT, "%.1f GB", bytes / 1_073_741_824.0)
}

package com.rendref.netstorage.feature.transfers

import android.app.Activity
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.DocumentsContract
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContract
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import java.util.Locale
import com.rendref.netstorage.core.storage.DestinationStore

data class TransferSetupState(
    val destinationLabel: String?,
    val fileCount: Int,
    val totalBytes: Long,
    val isStarting: Boolean = false,
    val errorMessage: String? = null,
)

@Composable
fun TransferSetupScreen(
    state: TransferSetupState,
    onDestinationSelected: (Uri) -> Unit,
    onStart: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val destinationPicker = rememberLauncherForActivityResult(
        contract = PersistableOpenDocumentTree(),
    ) { uri ->
        uri?.let(onDestinationSelected)
    }
    TransferSetupContent(
        state = state,
        onChooseDestination = { destinationPicker.launch(null) },
        onStart = onStart,
        modifier = modifier,
    )
}

class PersistableOpenDocumentTree : ActivityResultContract<Uri?, Uri?>() {
    override fun createIntent(context: Context, input: Uri?): Intent =
        Intent(Intent.ACTION_OPEN_DOCUMENT_TREE)
            .addFlags(DestinationStore.DOCUMENT_TREE_INTENT_FLAGS)
            .apply {
                input?.let {
                    putExtra(DocumentsContract.EXTRA_INITIAL_URI, it)
                }
            }

    override fun parseResult(resultCode: Int, intent: Intent?): Uri? =
        if (resultCode == Activity.RESULT_OK) intent?.data else null
}

@Composable
internal fun TransferSetupContent(
    state: TransferSetupState,
    onChooseDestination: () -> Unit,
    onStart: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier.padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text("Copy to Android", style = MaterialTheme.typography.headlineMedium)
        Text(
            "${state.fileCount} files \u00b7 ${formatTransferBytes(state.totalBytes)}",
            style = MaterialTheme.typography.bodyLarge,
        )
        Text("Windows source files stay read-only.")
        state.destinationLabel?.let { destination ->
            Text(
                "Destination: $destination",
                color = MaterialTheme.colorScheme.primary,
            )
        }
        state.errorMessage?.let { error ->
            Text(error, color = MaterialTheme.colorScheme.error)
        }
        OutlinedButton(
            onClick = onChooseDestination,
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text(
                if (state.destinationLabel == null) {
                    "Choose Android folder"
                } else {
                    "Change Android folder"
                },
            )
        }
        Button(
            onClick = onStart,
            enabled = state.destinationLabel != null && !state.isStarting,
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text(if (state.isStarting) "Starting\u2026" else "Copy ${state.fileCount} files")
        }
    }
}

private fun formatTransferBytes(bytes: Long): String = when {
    bytes < 1_024 -> "$bytes B"
    bytes < 1_048_576 -> String.format(Locale.ROOT, "%.1f KB", bytes / 1_024.0)
    bytes < 1_073_741_824 -> String.format(
        Locale.ROOT,
        "%.1f MB",
        bytes / 1_048_576.0,
    )
    else -> String.format(Locale.ROOT, "%.1f GB", bytes / 1_073_741_824.0)
}

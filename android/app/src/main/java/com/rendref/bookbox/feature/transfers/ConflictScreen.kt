package com.rendref.bookbox.feature.transfers

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.rendref.bookbox.core.transfer.ConflictChoice

data class ConflictState(
    val destinationPath: String,
    val existingBytes: Long?,
    val incomingBytes: Long,
)

@Composable
fun ConflictScreen(
    state: ConflictState,
    onChoice: (ConflictChoice) -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier.padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        Text("File already exists", style = MaterialTheme.typography.headlineMedium)
        Text(state.destinationPath, style = MaterialTheme.typography.bodyLarge)
        Text(
            "The Android file differs from the selected copy " +
                "(${state.existingBytes?.let { "$it B" } ?: "unknown size"} existing, " +
                "${state.incomingBytes} B incoming).",
        )
        Text(
            "Replace removes only this Android copy. The Windows source is never changed.",
            color = MaterialTheme.colorScheme.primary,
        )
        Button(
            onClick = { onChoice(ConflictChoice.Replace) },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text("Replace")
        }
        OutlinedButton(
            onClick = { onChoice(ConflictChoice.KeepBoth) },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text("Keep both")
        }
        TextButton(
            onClick = { onChoice(ConflictChoice.Skip) },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text("Skip")
        }
    }
}

package com.rendref.bookbox.feature.settings

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp

sealed interface SettingsAction {
    data object Rescan : SettingsAction

    data object ChooseDestination : SettingsAction

    data object SignOut : SettingsAction

    data object RequestServerChange : SettingsAction

    data object ResetSavedConnection : SettingsAction

    data object ConfirmServerChange : SettingsAction

    data object CancelServerChange : SettingsAction
}

@Composable
fun SettingsScreen(
    state: SettingsUiState,
    onAction: (SettingsAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    LazyColumn(
        modifier = modifier
            .fillMaxSize()
            .padding(horizontal = 20.dp),
        contentPadding = PaddingValues(bottom = 64.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp),
    ) {
        item {
            Text(
                text = "Settings",
                modifier = Modifier.padding(top = 20.dp),
                style = MaterialTheme.typography.headlineMedium,
            )
        }

        if (state.isLoading) {
            item {
                CircularProgressIndicator()
            }
        } else {
            state.host?.let { host ->
                item { Diagnostic("Saved server", host) }
            }
            state.username?.let { username ->
                item { Diagnostic("Username", username) }
            }
            state.destinationLabel?.let { destination ->
                item { Diagnostic("Android destination", destination) }
            }
            state.appVersion?.let { appVersion ->
                item { Text("App version $appVersion") }
            }
            state.serverVersion?.let { serverVersion ->
                item { Text("Server version $serverVersion") }
            }
            state.scanStatus?.let { scanStatus ->
                item { Text("Scan ${scanStatus.displayName()}") }
            }
            state.error?.let { error ->
                item {
                    Text(
                        text = error.message,
                        color = MaterialTheme.colorScheme.error,
                    )
                }
            }
            state.message?.let { message ->
                item { Text(message) }
            }
            item {
                Button(
                    onClick = { onAction(SettingsAction.Rescan) },
                    enabled = !state.isBusy,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("Rescan")
                }
            }
            item {
                OutlinedButton(
                    onClick = { onAction(SettingsAction.ChooseDestination) },
                    enabled = !state.isBusy,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("Choose destination")
                }
            }
            item {
                OutlinedButton(
                    onClick = { onAction(SettingsAction.SignOut) },
                    enabled = !state.isBusy,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("Sign in again")
                }
            }
            item {
                OutlinedButton(
                    onClick = { onAction(SettingsAction.RequestServerChange) },
                    enabled = !state.isBusy,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("Change server")
                }
            }
            item {
                OutlinedButton(
                    onClick = { onAction(SettingsAction.ResetSavedConnection) },
                    enabled = !state.isBusy,
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("Reset saved connection")
                }
            }
        }

        item {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(bottom = 20.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                if (state.isBusy) {
                    CircularProgressIndicator()
                }
            }
        }
    }

    if (state.confirmServerChange) {
        AlertDialog(
            onDismissRequest = {
                onAction(SettingsAction.CancelServerChange)
            },
            title = { Text("Reset saved connection?") },
            text = {
                Text(
                    "This clears tokens and the saved server. Android downloads and " +
                        "transfer history stay on this device.",
                )
            },
            confirmButton = {
                Button(
                    onClick = {
                        onAction(SettingsAction.ConfirmServerChange)
                    },
                ) {
                    Text("Reset saved connection")
                }
            },
            dismissButton = {
                TextButton(
                    onClick = {
                        onAction(SettingsAction.CancelServerChange)
                    },
                ) {
                    Text("Keep server")
                }
            },
        )
    }
}

@Composable
private fun Diagnostic(
    label: String,
    value: String,
) {
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Text(label, style = MaterialTheme.typography.labelLarge)
        Text(value, style = MaterialTheme.typography.bodyLarge)
    }
}

private fun ScanStatus.displayName(): String = when (this) {
    ScanStatus.NotScanned -> "not started"
    ScanStatus.Queued -> "queued"
    ScanStatus.Running -> "running"
    ScanStatus.Complete -> "complete"
    ScanStatus.Failed -> "failed"
}

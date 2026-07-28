package com.rendref.bookbox.feature.connect

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp

@Composable
fun ConnectScreen(
    state: ConnectState,
    onConnect: (String, String, String) -> Unit,
    onRetry: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier.padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text("Connect to BookBox", style = MaterialTheme.typography.headlineMedium)
        when (state) {
            is ConnectState.EnterCredentials -> CredentialsContent(state, onConnect)
            ConnectState.Connecting -> {
                CircularProgressIndicator()
                Text("Connecting…")
            }
            ConnectState.Ready -> Text("Connected. Opening Audiobooks…")
            is ConnectState.Error -> {
                Text(state.message, color = MaterialTheme.colorScheme.error)
                Button(onClick = onRetry) {
                    Text(
                        if (state.recovery == ConnectRecovery.RetryRefresh) {
                            "Try again"
                        } else {
                            "Edit connection"
                        },
                    )
                }
            }
        }
    }
}

@Composable
private fun CredentialsContent(
    state: ConnectState.EnterCredentials,
    onConnect: (String, String, String) -> Unit,
) {
    var server by rememberSaveable { mutableStateOf(state.server) }
    var username by rememberSaveable { mutableStateOf(state.username) }
    var password by rememberSaveable { mutableStateOf("") }
    LaunchedEffect(state.server, state.username) {
        server = state.server
        username = state.username
        password = ""
    }
    Text("Enter the BookBox hostname or private IP address and your account.")
    OutlinedTextField(
        value = server,
        onValueChange = { server = it },
        modifier = Modifier.fillMaxWidth(),
        label = { Text("Server") },
        singleLine = true,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
    )
    OutlinedTextField(
        value = username,
        onValueChange = { username = it },
        modifier = Modifier.fillMaxWidth(),
        label = { Text("Username") },
        singleLine = true,
    )
    OutlinedTextField(
        value = password,
        onValueChange = { password = it },
        modifier = Modifier.fillMaxWidth(),
        label = { Text("Password") },
        singleLine = true,
        visualTransformation = PasswordVisualTransformation(),
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
    )
    Button(
        onClick = {
            val submittedPassword = password
            password = ""
            onConnect(server, username, submittedPassword)
        },
        enabled = server.isNotBlank() && username.isNotBlank() && password.isNotEmpty(),
    ) {
        Text("Connect")
    }
}

package com.rendref.bookbox.feature.connect

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
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
    onInspectServer: (String) -> Unit,
    onApproveFingerprint: (String) -> Unit,
    onSignIn: (String, String) -> Unit,
    onRetry: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier.padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text("Connect to BookBox", style = MaterialTheme.typography.headlineMedium)
        when (state) {
            ConnectState.EnterServer -> EnterServerContent(onInspectServer)
            is ConnectState.ReviewCertificate -> CertificateContent(
                state = state,
                onApproveFingerprint = onApproveFingerprint,
            )
            is ConnectState.SignIn -> SignInContent(state, onSignIn)
            ConnectState.Ready -> Text("Connected. Opening Audiobooks…")
            is ConnectState.Error -> {
                Text(state.message, color = MaterialTheme.colorScheme.error)
                Button(onClick = onRetry) {
                    Text("Try again")
                }
            }
        }
    }
}

@Composable
private fun EnterServerContent(onInspectServer: (String) -> Unit) {
    var address by rememberSaveable { mutableStateOf("") }
    Text("Enter the HTTPS address shown by your BookBox server.")
    OutlinedTextField(
        value = address,
        onValueChange = { address = it },
        modifier = Modifier.fillMaxWidth(),
        label = { Text("Server address") },
        singleLine = true,
        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
    )
    Button(
        onClick = { onInspectServer(address) },
        enabled = address.isNotBlank(),
    ) {
        Text("Inspect certificate")
    }
}

@Composable
private fun CertificateContent(
    state: ConnectState.ReviewCertificate,
    onApproveFingerprint: (String) -> Unit,
) {
    var typedFingerprint by rememberSaveable(state.observed.fingerprint) {
        mutableStateOf("")
    }
    Text("Compare this fingerprint with the one printed by the Windows server:")
    Text(
        state.observed.fingerprint,
        style = MaterialTheme.typography.bodyLarge,
    )
    OutlinedTextField(
        value = typedFingerprint,
        onValueChange = { typedFingerprint = it },
        modifier = Modifier.fillMaxWidth(),
        label = { Text("Type the full fingerprint") },
        supportingText = { Text("Use uppercase letters and include every colon.") },
    )
    Button(
        onClick = { onApproveFingerprint(typedFingerprint) },
        enabled = typedFingerprint == state.observed.fingerprint,
    ) {
        Text("Approve certificate")
    }
}

@Composable
private fun SignInContent(
    state: ConnectState.SignIn,
    onSignIn: (String, String) -> Unit,
) {
    var username by rememberSaveable(state.connection.baseUrl) {
        mutableStateOf(state.connection.username)
    }
    var password by remember(state.connection.baseUrl) { mutableStateOf("") }
    Text("Approved server: ${state.connection.baseUrl}")
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
            onSignIn(username, submittedPassword)
        },
        enabled = username.isNotBlank() && password.isNotEmpty(),
    ) {
        Text("Sign in")
    }
}

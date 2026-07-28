package com.rendref.bookbox.feature.connect

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.bookbox.core.network.SessionException
import com.rendref.bookbox.core.network.SessionService
import com.rendref.bookbox.core.storage.ConnectionRepository
import com.rendref.bookbox.core.storage.SavedConnection
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl

sealed interface ConnectState {
    data class EnterCredentials(
        val server: String = DEFAULT_SERVER,
        val username: String = "",
    ) : ConnectState

    data object Connecting : ConnectState

    data object Ready : ConnectState

    data class Error(
        val message: String,
        val recovery: ConnectRecovery,
    ) : ConnectState
}

enum class ConnectRecovery {
    EditCredentials,
    ChangeServer,
    RetryRefresh,
}

class ConnectViewModel(
    private val connections: ConnectionRepository,
    private val sessionFactory: (SavedConnection) -> SessionService,
    private val dispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {
    private val mutableState = MutableStateFlow<ConnectState>(
        ConnectState.EnterCredentials(),
    )
    val state: StateFlow<ConnectState> = mutableState.asStateFlow()

    private var pendingCredentials = ConnectState.EnterCredentials()
    private var savedForRefresh: SavedConnection? = null

    init {
        viewModelScope.launch(dispatcher) {
            val saved = connections.state.first()
            if (saved != null) refreshSavedConnection(saved)
        }
    }

    fun connect(serverInput: String, usernameInput: String, password: String) {
        val username = usernameInput.trim()
        pendingCredentials = ConnectState.EnterCredentials(
            server = serverInput.trim().ifEmpty { DEFAULT_SERVER },
            username = username,
        )
        if (serverInput.isBlank() || username.isEmpty() || password.isEmpty()) {
            mutableState.value = ConnectState.Error(
                message = "Server, username, and password are required",
                recovery = ConnectRecovery.EditCredentials,
            )
            return
        }
        val server = try {
            normalizeServerOrigin(serverInput)
        } catch (_: IllegalArgumentException) {
            mutableState.value = ConnectState.Error(
                message = "Enter a clean HTTP server hostname or private IP address",
                recovery = ConnectRecovery.ChangeServer,
            )
            return
        }
        val connection = SavedConnection(server.toString(), username)
        mutableState.value = ConnectState.Connecting
        viewModelScope.launch(dispatcher) {
            try {
                sessionFactory(connection).login(username, password)
                connections.save(connection)
                mutableState.value = ConnectState.Ready
            } catch (failure: Exception) {
                mutableState.value = failure.toConnectError()
            }
        }
    }

    fun retry() {
        when ((mutableState.value as? ConnectState.Error)?.recovery) {
            ConnectRecovery.EditCredentials,
            ConnectRecovery.ChangeServer,
            -> mutableState.value = pendingCredentials
            ConnectRecovery.RetryRefresh -> savedForRefresh?.let { connection ->
                viewModelScope.launch(dispatcher) { refreshSavedConnection(connection) }
            }
            null -> Unit
        }
    }

    private suspend fun refreshSavedConnection(connection: SavedConnection) {
        savedForRefresh = connection
        pendingCredentials = ConnectState.EnterCredentials(
            server = connection.displayServer(),
            username = connection.username,
        )
        mutableState.value = ConnectState.Connecting
        try {
            sessionFactory(connection).refresh()
            mutableState.value = ConnectState.Ready
        } catch (failure: SessionException) {
            mutableState.value = if (failure.statusCode == 401) {
                pendingCredentials
            } else {
                ConnectState.Error(
                    message = "Unable to refresh the saved session",
                    recovery = ConnectRecovery.RetryRefresh,
                )
            }
        } catch (_: Exception) {
            mutableState.value = ConnectState.Error(
                message = "Unable to refresh the saved session",
                recovery = ConnectRecovery.RetryRefresh,
            )
        }
    }
}

internal fun normalizeServerOrigin(input: String): HttpUrl {
    val trimmed = input.trim()
    require(trimmed.isNotEmpty())
    val candidate = if ("://" in trimmed) trimmed else "http://$trimmed"
    val url = candidate.toHttpUrl()
    val authority = candidate
        .substringAfter("://")
        .substringBefore('/')
        .substringBefore('?')
        .substringBefore('#')
        .substringAfterLast('@')
    val hasExplicitPort = EXPLICIT_PORT.matches(authority)
    require(url.scheme == "http")
    require(url.encodedUsername.isEmpty() && url.encodedPassword.isEmpty())
    require(url.query == null && url.fragment == null)
    require(url.encodedPath == "/")
    return url.newBuilder()
        .port(if (hasExplicitPort) url.port else DEFAULT_PORT)
        .encodedPath("/")
        .query(null)
        .fragment(null)
        .build()
}

private fun SavedConnection.displayServer(): String {
    val url = baseUrl.toHttpUrl()
    val host = if (':' in url.host) "[${url.host}]" else url.host
    return if (url.port == DEFAULT_PORT) host else "$host:${url.port}"
}

private fun Exception.toConnectError(): ConnectState.Error {
    val causes = generateSequence<Throwable>(this) { it.cause }.toList()
    return when {
        causes.any { it is UnknownHostException } -> ConnectState.Error(
            message = "BookBox could not find that server hostname",
            recovery = ConnectRecovery.ChangeServer,
        )
        causes.any { it is ConnectException || it is SocketTimeoutException } ->
            ConnectState.Error(
                message = "BookBox could not reach that server",
                recovery = ConnectRecovery.ChangeServer,
            )
        this is SessionException && statusCode == 401 -> ConnectState.Error(
            message = "The username or password is incorrect",
            recovery = ConnectRecovery.EditCredentials,
        )
        this is SessionException && statusCode in setOf(404, 405) -> ConnectState.Error(
            message = "The server does not provide a compatible BookBox API",
            recovery = ConnectRecovery.ChangeServer,
        )
        this is SessionException && message.orEmpty().contains("invalid", ignoreCase = true) ->
            ConnectState.Error(
                message = "The server does not provide a compatible BookBox API",
                recovery = ConnectRecovery.ChangeServer,
            )
        else -> ConnectState.Error(
            message = "Sign-in failed",
            recovery = ConnectRecovery.EditCredentials,
        )
    }
}

private const val DEFAULT_SERVER = "bookbox.local"
private const val DEFAULT_PORT = 8787
private val EXPLICIT_PORT = Regex("""^(?:\[[^]]+]|[^:]+):\d+$""")

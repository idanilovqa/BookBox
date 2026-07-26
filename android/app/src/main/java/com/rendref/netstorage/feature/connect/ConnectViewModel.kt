package com.rendref.netstorage.feature.connect

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.netstorage.core.network.CertificateInspector
import com.rendref.netstorage.core.network.ObservedCertificate
import com.rendref.netstorage.core.network.SessionException
import com.rendref.netstorage.core.network.SessionService
import com.rendref.netstorage.core.storage.ConnectionRepository
import com.rendref.netstorage.core.storage.SavedConnection
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl

sealed interface ConnectState {
    data object EnterServer : ConnectState

    data class ReviewCertificate(
        val observed: ObservedCertificate,
    ) : ConnectState

    data class SignIn(
        val connection: SignInTarget,
    ) : ConnectState

    data object Ready : ConnectState

    data class Error(
        val message: String,
        val retry: ConnectAction,
    ) : ConnectState
}

data class SignInTarget(
    val baseUrl: String,
    val certificateSha256: String,
    val username: String = "",
    val destinationUri: String? = null,
)

enum class ConnectAction {
    EnterServer,
    SignIn,
    Refresh,
}

class ConnectViewModel(
    private val connections: ConnectionRepository,
    private val certificateInspector: CertificateInspector,
    private val sessionFactory: (SavedConnection) -> SessionService,
    private val probeDispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {
    private val mutableState = MutableStateFlow<ConnectState>(ConnectState.EnterServer)
    val state: StateFlow<ConnectState> = mutableState.asStateFlow()

    private var pendingBaseUrl: HttpUrl? = null
    private var pendingCertificate: ObservedCertificate? = null
    private var pendingSignIn: SignInTarget? = null
    private var savedForRefresh: SavedConnection? = null
    private var inspectionGeneration = 0L

    init {
        viewModelScope.launch {
            connections.state.first()?.let { saved ->
                refreshSavedConnection(saved)
            }
        }
    }

    fun inspectServer(serverInput: String) {
        val generation = ++inspectionGeneration
        pendingBaseUrl = null
        pendingCertificate = null
        pendingSignIn = null
        savedForRefresh = null
        val server = try {
            normalizeServerOrigin(serverInput)
        } catch (_: IllegalArgumentException) {
            mutableState.value = ConnectState.Error(
                message = "Enter a clean HTTPS server address",
                retry = ConnectAction.EnterServer,
            )
            return
        }
        viewModelScope.launch {
            try {
                val observed = withContext(probeDispatcher) {
                    certificateInspector.inspect(server)
                }
                if (generation != inspectionGeneration) return@launch
                pendingBaseUrl = server
                pendingCertificate = observed
                mutableState.value = ConnectState.ReviewCertificate(observed)
            } catch (_: Exception) {
                if (generation != inspectionGeneration) return@launch
                mutableState.value = ConnectState.Error(
                    message = "Unable to inspect the server certificate",
                    retry = ConnectAction.EnterServer,
                )
            }
        }
    }

    fun approveFingerprint(typedFingerprint: String) {
        val reviewState = mutableState.value as? ConnectState.ReviewCertificate ?: return
        val observed = pendingCertificate ?: return
        val server = pendingBaseUrl ?: return
        if (
            reviewState.observed.fingerprint != observed.fingerprint ||
            typedFingerprint != observed.fingerprint
        ) {
            return
        }
        pendingSignIn = SignInTarget(
            baseUrl = server.toString(),
            certificateSha256 = observed.fingerprint,
        )
        mutableState.value = ConnectState.SignIn(checkNotNull(pendingSignIn))
    }

    fun signIn(username: String, password: String) {
        val current = mutableState.value as? ConnectState.SignIn ?: return
        val signInTarget = pendingSignIn ?: return
        if (current.connection != signInTarget) return
        val normalizedUsername = username.trim()
        if (normalizedUsername.isEmpty() || password.isEmpty()) {
            mutableState.value = ConnectState.Error(
                message = "Username and password are required",
                retry = ConnectAction.SignIn,
            )
            return
        }
        pendingSignIn = signInTarget.copy(username = normalizedUsername)
        val connection = SavedConnection(
            baseUrl = signInTarget.baseUrl,
            certificateSha256 = signInTarget.certificateSha256,
            username = normalizedUsername,
            destinationUri = signInTarget.destinationUri,
        )
        viewModelScope.launch {
            try {
                sessionFactory(connection).login(normalizedUsername, password)
                connections.save(connection)
                mutableState.value = ConnectState.Ready
            } catch (_: Exception) {
                mutableState.value = ConnectState.Error(
                    message = "Sign-in failed",
                    retry = ConnectAction.SignIn,
                )
            }
        }
    }

    fun retry() {
        val error = mutableState.value as? ConnectState.Error ?: return
        when (error.retry) {
            ConnectAction.EnterServer -> mutableState.value = ConnectState.EnterServer
            ConnectAction.SignIn -> pendingSignIn?.let {
                mutableState.value = ConnectState.SignIn(it)
            }
            ConnectAction.Refresh -> savedForRefresh?.let { saved ->
                viewModelScope.launch { refreshSavedConnection(saved) }
            }
        }
    }

    private suspend fun refreshSavedConnection(connection: SavedConnection) {
        savedForRefresh = connection
        pendingSignIn = SignInTarget(
            baseUrl = connection.baseUrl,
            certificateSha256 = connection.certificateSha256,
            username = connection.username,
            destinationUri = connection.destinationUri,
        )
        try {
            sessionFactory(connection).refresh()
            mutableState.value = ConnectState.Ready
        } catch (failure: SessionException) {
            mutableState.value = if (failure.statusCode == 401) {
                ConnectState.SignIn(checkNotNull(pendingSignIn))
            } else {
                ConnectState.Error(
                    message = "Unable to refresh the saved session",
                    retry = ConnectAction.Refresh,
                )
            }
        } catch (_: Exception) {
            mutableState.value = ConnectState.Error(
                message = "Unable to refresh the saved session",
                retry = ConnectAction.Refresh,
            )
        }
    }
}

internal fun normalizeServerOrigin(input: String): HttpUrl {
    val url = input.trim().toHttpUrl()
    require(url.isHttps)
    require(url.encodedUsername.isEmpty() && url.encodedPassword.isEmpty())
    require(url.query == null && url.fragment == null)
    require(url.encodedPath == "/")
    return url.newBuilder()
        .encodedPath("/")
        .query(null)
        .fragment(null)
        .build()
}

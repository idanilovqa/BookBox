package com.rendref.bookbox.feature.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.bookbox.core.error.UserError
import java.util.Locale
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull

enum class ScanStatus {
    NotScanned,
    Queued,
    Running,
    Complete,
    Failed,
}

data class SettingsSnapshot(
    val baseUrl: String,
    val certificateSha256: String,
    val username: String,
    val destinationLabel: String?,
    val appVersion: String,
    val serverVersion: String?,
    val scanStatus: ScanStatus,
)

interface SettingsSource {
    suspend fun load(): SettingsSnapshot

    suspend fun rescan(): ScanStatus

    suspend fun signOut()

    suspend fun clearServerCredentials()
}

data class SettingsUiState(
    val isLoading: Boolean = false,
    val isBusy: Boolean = false,
    val host: String? = null,
    val fingerprintAbbreviation: String? = null,
    val fingerprintForCopy: String? = null,
    val username: String? = null,
    val destinationLabel: String? = null,
    val appVersion: String? = null,
    val serverVersion: String? = null,
    val scanStatus: ScanStatus? = null,
    val confirmServerChange: Boolean = false,
    val signedOut: Boolean = false,
    val serverChanged: Boolean = false,
    val message: String? = null,
    val error: UserError? = null,
)

class SettingsViewModel(
    private val source: SettingsSource,
) : ViewModel() {
    private val mutableState = MutableStateFlow(SettingsUiState(isLoading = true))
    val state: StateFlow<SettingsUiState> = mutableState.asStateFlow()

    init {
        viewModelScope.launch {
            try {
                mutableState.value = source.load().toSafeUiState()
            } catch (_: Exception) {
                mutableState.value = SettingsUiState(
                    isLoading = false,
                    error = UserError.ServerUnavailable,
                )
            }
        }
    }

    fun rescan() {
        if (!beginOperation()) return
        viewModelScope.launch {
            try {
                val status = source.rescan()
                mutableState.value = mutableState.value.copy(
                    isBusy = false,
                    scanStatus = status,
                )
            } catch (_: Exception) {
                failOperation(UserError.ServerUnavailable)
            }
        }
    }

    fun signOut() {
        if (!beginOperation()) return
        viewModelScope.launch {
            try {
                source.signOut()
                mutableState.value = mutableState.value.copy(
                    isBusy = false,
                    signedOut = true,
                )
            } catch (_: Exception) {
                failOperation(UserError.ServerUnavailable)
            }
        }
    }

    fun requestServerChange() {
        if (mutableState.value.isBusy) return
        mutableState.value = mutableState.value.copy(confirmServerChange = true)
    }

    fun cancelServerChange() {
        mutableState.value = mutableState.value.copy(confirmServerChange = false)
    }

    fun confirmServerChange() {
        if (!mutableState.value.confirmServerChange || !beginOperation()) return
        mutableState.value = mutableState.value.copy(confirmServerChange = false)
        viewModelScope.launch {
            try {
                source.clearServerCredentials()
                mutableState.value = mutableState.value.copy(
                    isBusy = false,
                    serverChanged = true,
                    message = SERVER_CHANGED_MESSAGE,
                )
            } catch (_: Exception) {
                failOperation(UserError.ServerUnavailable)
            }
        }
    }

    private fun beginOperation(): Boolean {
        if (mutableState.value.isLoading || mutableState.value.isBusy) return false
        mutableState.value = mutableState.value.copy(
            isBusy = true,
            message = null,
            error = null,
        )
        return true
    }

    private fun failOperation(error: UserError) {
        mutableState.value = mutableState.value.copy(
            isBusy = false,
            error = error,
        )
    }
}

private fun SettingsSnapshot.toSafeUiState(): SettingsUiState {
    val fingerprint = certificateSha256.normalizedFingerprint()
    return SettingsUiState(
        isLoading = false,
        host = baseUrl.safeHost(),
        fingerprintAbbreviation = fingerprint?.abbreviateFingerprint(),
        fingerprintForCopy = fingerprint,
        username = username.safeLabel() ?: "Saved user",
        destinationLabel = destinationLabel?.safeLabel(),
        appVersion = appVersion.safeLabel(),
        serverVersion = serverVersion?.safeLabel(),
        scanStatus = scanStatus,
    )
}

private fun String.safeHost(): String? {
    val url = toHttpUrlOrNull() ?: return null
    if (
        !url.isHttps ||
        url.encodedUsername.isNotEmpty() ||
        url.encodedPassword.isNotEmpty() ||
        url.encodedPath != "/" ||
        url.query != null ||
        url.fragment != null
    ) {
        return null
    }
    val safeHost = if (':' in url.host) "[${url.host}]" else url.host
    return if (url.port == 443) safeHost else "$safeHost:${url.port}"
}

private fun String.safeLabel(): String? {
    val clean = trim()
    if (
        clean.isEmpty() ||
        clean.length > MAX_DIAGNOSTIC_LENGTH ||
        clean.any(Char::isISOControl) ||
        clean.startsWith("/") ||
        clean.startsWith("\\") ||
        clean.contains("://", ignoreCase = true) ||
        WINDOWS_ABSOLUTE_PATH.containsMatchIn(clean) ||
        SENSITIVE_LABEL.containsMatchIn(clean)
    ) {
        return null
    }
    return clean
}

private fun String.normalizedFingerprint(): String? {
    if (!CERTIFICATE_FINGERPRINT.matches(this)) return null
    return uppercase(Locale.ROOT)
}

private fun String.abbreviateFingerprint(): String {
    val bytes = split(':')
    return bytes.take(4).joinToString(":") +
        ":\u2026:" +
        bytes.takeLast(2).joinToString(":")
}

private const val MAX_DIAGNOSTIC_LENGTH = 100
private const val SERVER_CHANGED_MESSAGE =
    "Saved server and session cleared. Android downloads and transfer history were kept."
private val CERTIFICATE_FINGERPRINT =
    Regex("(?i)^(?:[0-9a-f]{2}:){31}[0-9a-f]{2}$")
private val WINDOWS_ABSOLUTE_PATH = Regex("(?i)^[a-z]:[\\\\/]")
private val SENSITIVE_LABEL = Regex("(?i)(?:password|token)")

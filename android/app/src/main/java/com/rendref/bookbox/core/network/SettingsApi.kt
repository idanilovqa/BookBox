package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.model.ApiJson
import java.io.IOException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.encodeToString
import okhttp3.Call
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

class SettingsApi(
    private val calls: Call.Factory,
    private val baseUrl: HttpUrl,
) {
    init {
        require(baseUrl.scheme == "http")
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty())
        require(baseUrl.query == null && baseUrl.fragment == null && baseUrl.encodedPath == "/")
    }

    suspend fun serverVersion(): String? = withContext(Dispatchers.IO) {
        val request = Request.Builder()
            .url(baseUrl.newBuilder().addPathSegments("v1/health").build())
            .get()
            .build()
        calls.newCall(request).execute().use { response ->
            if (!response.isSuccessful) return@withContext null
            runCatching {
                ApiJson.decodeFromString<HealthDto>(response.body.string()).version
                    .takeIf(::safeVersion)
            }.getOrNull()
        }
    }

    suspend fun rescan(rootIds: List<String>) = withContext(Dispatchers.IO) {
        require(rootIds.isNotEmpty())
        rootIds.forEach { rootId ->
            require(ROOT_ID.matches(rootId))
            val request = Request.Builder()
                .url(baseUrl.newBuilder().addPathSegments("v1/catalog/rescan").build())
                .post(
                    ApiJson.encodeToString(RescanDto(rootId))
                        .toRequestBody(JSON),
                )
                .build()
            calls.newCall(request).execute().use { response ->
                if (response.code != 202) {
                    throw IOException("Server did not accept the catalog rescan")
                }
            }
        }
    }
}

@Serializable
private data class HealthDto(
    val status: String,
    val version: String,
)

@Serializable
private data class RescanDto(val rootId: String)

private fun safeVersion(value: String): Boolean =
    value.isNotBlank() &&
        value.length <= 64 &&
        value.none(Char::isISOControl) &&
        !value.contains("://") &&
        !WINDOWS_VOLUME.containsMatchIn(value)

private val JSON = "application/json; charset=utf-8".toMediaType()
private val ROOT_ID = Regex("^[a-z0-9][a-z0-9_-]{0,63}$")
private val WINDOWS_VOLUME = Regex("^[A-Za-z]:")

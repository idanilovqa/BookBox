package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.storage.RefreshTokenStore
import java.io.IOException
import java.time.Instant
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

@Serializable
data class SessionTokens(
    val accessToken: String,
    val refreshToken: String,
    val accessExpiry: String,
)

interface SessionService {
    suspend fun login(username: String, password: String): SessionTokens

    suspend fun refresh(): SessionTokens
}

interface TokenSession {
    val accessToken: String?

    suspend fun refresh(): SessionTokens
}

class SessionApi(
    private val client: OkHttpClient,
    private val baseUrl: HttpUrl,
    private val tokenStore: RefreshTokenStore,
    private val json: Json = Json { ignoreUnknownKeys = true },
) : SessionService, TokenSession {
    @Volatile
    override var accessToken: String? = null
        private set

    init {
        require(baseUrl.isHttps) { "Session API requires HTTPS" }
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty()) {
            "Session API URL must not contain credentials"
        }
        require(baseUrl.query == null && baseUrl.fragment == null) {
            "Session API URL must not contain a query or fragment"
        }
    }

    override suspend fun login(username: String, password: String): SessionTokens =
        postSession(
            path = "/v1/session",
            encodedBody = json.encodeToString(LoginRequest(username, password)),
        )

    override suspend fun refresh(): SessionTokens {
        val refreshToken = try {
            tokenStore.read()
        } catch (_: Exception) {
            throw SessionException("Unable to read the encrypted session")
        }
            ?: throw SessionException("Sign-in has expired", statusCode = 401)
        return postSession(
            path = "/v1/session/refresh",
            encodedBody = json.encodeToString(RefreshRequest(refreshToken)),
        )
    }

    private suspend fun postSession(path: String, encodedBody: String): SessionTokens =
        withContext(Dispatchers.IO) {
            val endpoint = checkNotNull(baseUrl.resolve(path))
            val request = Request.Builder()
                .url(endpoint)
                .post(encodedBody.toRequestBody(JSON_MEDIA_TYPE))
                .build()
            val response = try {
                client.newCall(request).execute()
            } catch (_: IOException) {
                throw SessionException("Unable to contact the saved server")
            }
            response.use {
                if (it.code == 401) {
                    accessToken = null
                    try {
                        tokenStore.clear()
                    } catch (_: Exception) {
                        throw SessionException(
                            "Unable to clear the expired session",
                            statusCode = 401,
                        )
                    }
                    throw SessionException("Sign-in has expired", statusCode = 401)
                }
                if (!it.isSuccessful) {
                    throw SessionException(
                        message = "Server rejected the sign-in request",
                        statusCode = it.code,
                    )
                }
                val tokens = try {
                    val source = it.body.source()
                    if (source.request(MAX_SESSION_RESPONSE_BYTES + 1L)) {
                        throw IllegalArgumentException("Session response is too large")
                    }
                    val responseBytes = source.readByteArray()
                    json.decodeFromString<SessionTokens>(
                        responseBytes.toString(Charsets.UTF_8),
                    ).also(::validateTokens)
                } catch (_: Exception) {
                    throw SessionException("Server returned an invalid sign-in response")
                }
                try {
                    tokenStore.write(tokens.refreshToken)
                } catch (_: Exception) {
                    throw SessionException("Unable to store the encrypted session")
                }
                accessToken = tokens.accessToken
                tokens
            }
        }

    private fun validateTokens(tokens: SessionTokens) {
        require(tokens.accessToken.isNotBlank() && tokens.accessToken.length <= MAX_TOKEN_CHARS)
        require(tokens.refreshToken.isNotBlank() && tokens.refreshToken.length <= MAX_TOKEN_CHARS)
        Instant.parse(tokens.accessExpiry)
    }
}

class SessionException(
    message: String,
    val statusCode: Int? = null,
) : IOException(message) {
    override fun toString(): String = "${this::class.java.name}: $message"
}

@Serializable
private data class LoginRequest(
    val username: String,
    val password: String,
)

@Serializable
private data class RefreshRequest(
    val refreshToken: String,
)

private val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()
private const val MAX_SESSION_RESPONSE_BYTES = 64 * 1024
private const val MAX_TOKEN_CHARS = 16 * 1024

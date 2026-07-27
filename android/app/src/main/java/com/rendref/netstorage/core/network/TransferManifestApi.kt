package com.rendref.netstorage.core.network

import com.rendref.netstorage.core.model.ApiJson
import com.rendref.netstorage.core.model.ManifestDto
import java.io.IOException
import java.time.Instant
import java.util.Locale
import java.util.UUID
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.Call
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

sealed interface TransferSelection : java.io.Serializable {
    data class Book(val bookId: String) : TransferSelection

    data class Folder(
        val rootId: String,
        val relativePath: String,
    ) : TransferSelection
}

fun interface TransferManifestSource {
    suspend fun create(selections: List<TransferSelection>): ManifestDto
}

class TransferManifestApi(
    private val calls: Call.Factory,
    private val baseUrl: HttpUrl,
) : TransferManifestSource {
    init {
        require(baseUrl.isHttps)
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty())
        require(baseUrl.query == null && baseUrl.fragment == null && baseUrl.encodedPath == "/")
    }

    override suspend fun create(selections: List<TransferSelection>): ManifestDto {
        validateSelections(selections)
        val endpoint = baseUrl.newBuilder()
            .addPathSegments("v1/transfers/manifest")
            .build()
        val body = buildJsonObject {
            put(
                "selections",
                JsonArray(selections.map(TransferSelection::toWireJson)),
            )
        }
        val request = Request.Builder()
            .url(endpoint)
            .post(
                ApiJson.encodeToString(JsonObject.serializer(), body)
                    .toRequestBody(JSON_MEDIA_TYPE),
            )
            .build()
        return withContext(Dispatchers.IO) {
            calls.newCall(request).execute().use { response ->
                if (!response.isSuccessful) {
                    throw IOException("Transfer manifest request failed with HTTP ${response.code}")
                }
                if (response.request.url != endpoint) {
                    throw IOException("Transfer manifest request was redirected")
                }
                val source = response.body.source()
                if (source.request(MAX_MANIFEST_RESPONSE_BYTES + 1L)) {
                    throw IOException("Transfer manifest response is too large")
                }
                val decoded = try {
                    ApiJson.decodeFromString<ManifestDto>(
                        source.readByteArray().toString(Charsets.UTF_8),
                    )
                } catch (_: Exception) {
                    throw invalidManifest()
                }
                validateTransferManifest(decoded)
            }
        }
    }
}

internal fun validateTransferManifest(manifest: ManifestDto): ManifestDto {
    try {
        require(MANIFEST_ID.matches(manifest.id))
        require(manifest.files.isNotEmpty() && manifest.files.size <= MAX_MANIFEST_FILES)
        require(manifest.totalBytes >= 0)
        require(manifest.files.map { it.fileId }.distinct().size == manifest.files.size)
        require(
            manifest.files
                .map { it.destinationPath.lowercase(Locale.ROOT) }
                .distinct()
                .size == manifest.files.size,
        )
        var total = 0L
        manifest.files.forEach { file ->
            require(file.fileId.length in 1..MAX_FILE_ID_CHARS)
            require(file.fileId.all(::isSafeOpaqueCharacter))
            require(validManifestPath(file.sourcePath))
            require(validManifestPath(file.destinationPath))
            require(file.size >= 0)
            total = Math.addExact(total, file.size)
            Instant.parse(file.modifiedAt)
            require(
                file.etag.isNotBlank() &&
                    file.etag.length <= MAX_ETAG_CHARS &&
                    file.etag.none(Char::isISOControl),
            )
            require(SHA256.matches(file.sha256))
        }
        require(total == manifest.totalBytes)
    } catch (_: Exception) {
        throw invalidManifest()
    }
    return manifest
}

private fun validateSelections(selections: List<TransferSelection>) {
    require(selections.size in 1..MAX_SELECTIONS)
    val bookCount = selections.count { it is TransferSelection.Book }
    require(bookCount == 0 || bookCount == selections.size) {
        "Book and folder selections cannot be mixed"
    }
    when {
        bookCount > 0 -> {
            val ids = selections.map { (it as TransferSelection.Book).bookId }
            require(ids.distinct().size == ids.size)
            ids.forEach { raw ->
                val parsed = runCatching { UUID.fromString(raw) }.getOrNull()
                require(parsed != null && parsed != ZERO_UUID)
            }
        }

        else -> {
            val folders = selections.map { it as TransferSelection.Folder }
            require(
                folders.map { it.rootId to it.relativePath }.distinct().size ==
                    folders.size,
            )
            folders.forEach { folder ->
                require(ROOT_ID.matches(folder.rootId))
                require(validSelectionPath(folder.relativePath))
            }
        }
    }
}

private fun TransferSelection.toWireJson(): JsonObject = buildJsonObject {
    when (this@toWireJson) {
        is TransferSelection.Book -> {
            put("kind", JsonPrimitive("book"))
            put("bookId", JsonPrimitive(bookId))
        }

        is TransferSelection.Folder -> {
            put("rootId", JsonPrimitive(rootId))
            put("relativePath", JsonPrimitive(relativePath))
            put("kind", JsonPrimitive("folder"))
        }
    }
}

private fun validSelectionPath(path: String): Boolean =
    path.isEmpty() || validRelativePath(path)

private fun validManifestPath(path: String): Boolean =
    path.isNotEmpty() && validRelativePath(path)

private fun validRelativePath(path: String): Boolean {
    if (
        path.length > MAX_RELATIVE_PATH_CHARS ||
        path.startsWith("/") ||
        path.startsWith("\\") ||
        WINDOWS_VOLUME.containsMatchIn(path) ||
        '\\' in path ||
        path.any(Char::isISOControl)
    ) {
        return false
    }
    val segments = path.split('/')
    return segments.all { it.isNotEmpty() && it != "." && it != ".." }
}

private fun isSafeOpaqueCharacter(character: Char): Boolean =
    character.isLetterOrDigit() || character in "._~-"

private fun invalidManifest() =
    IOException("Server returned an invalid transfer manifest")

private const val MAX_SELECTIONS = 100
private const val MAX_MANIFEST_FILES = 100_000
private const val MAX_MANIFEST_RESPONSE_BYTES = 16L * 1024 * 1024
private const val MAX_RELATIVE_PATH_CHARS = 4_096
private const val MAX_FILE_ID_CHARS = 16 * 1024
private const val MAX_ETAG_CHARS = 512
private val JSON_MEDIA_TYPE = "application/json; charset=utf-8".toMediaType()
private val ROOT_ID = Regex("^[a-z0-9][a-z0-9_-]{0,63}$")
private val WINDOWS_VOLUME = Regex("^[A-Za-z]:")
private val MANIFEST_ID = Regex("^[A-Za-z0-9_-]{22}$")
private val SHA256 = Regex("^[0-9a-f]{64}$")
private val ZERO_UUID = UUID(0L, 0L)

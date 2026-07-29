package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.model.ApiJson
import com.rendref.bookbox.core.model.BookFilesDto
import com.rendref.bookbox.core.model.BrowseDto
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.FolderSearchDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.model.RootsDto
import java.io.IOException
import java.time.Instant
import java.time.format.DateTimeParseException
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.serialization.SerializationException
import kotlinx.serialization.decodeFromString
import okhttp3.Call
import okhttp3.Callback
import okhttp3.HttpUrl
import okhttp3.Request
import okhttp3.Response

class BrowseApi(
    private val calls: Call.Factory,
    private val baseUrl: HttpUrl,
) {
    init {
        require(baseUrl.scheme == "http")
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty())
        require(baseUrl.query == null && baseUrl.fragment == null && baseUrl.encodedPath == "/")
    }

    suspend fun roots(): List<RootDto> {
        val url = baseUrl.newBuilder().addPathSegments("v1/roots").build()
        return execute<RootsDto>(url).roots.also { roots ->
            if (
                roots.distinctBy(RootDto::id).size != roots.size ||
                roots.any { !validRootId(it.id) || !validDisplayName(it.name) }
            ) {
                throw IOException("Server returned invalid library roots")
            }
        }
    }

    suspend fun list(rootId: String, path: String): BrowseDto {
        require(validRootId(rootId)) { "Invalid root identifier" }
        require(validRelativePath(path, allowEmpty = true)) { "Invalid relative path" }
        val url = baseUrl.newBuilder()
            .addPathSegments("v1/browse")
            .addQueryParameter("root", rootId)
            .apply {
                path.takeIf(String::isNotEmpty)?.let {
                    addQueryParameter("path", it)
                }
            }
            .build()
        return execute<BrowseDto>(url).also { directory ->
            if (
                directory.rootId != rootId ||
                directory.path != path ||
                directory.entries.any { !validEntry(path, it) }
            ) {
                throw IOException("Server returned an invalid directory listing")
            }
        }
    }

    suspend fun searchFolders(rootId: String, query: String): FolderSearchDto {
        require(validRootId(rootId)) { "Invalid root identifier" }
        require(query.isNotBlank()) {
            "Invalid folder search query"
        }
        val url = baseUrl.newBuilder()
            .addPathSegments("v1/browse/search")
            .addQueryParameter("root", rootId)
            .addQueryParameter("q", query)
            .build()
        return execute<FolderSearchDto>(url).also { result ->
            if (
                result.rootId != rootId ||
                result.query != query ||
                result.entries.size > MAX_FOLDER_SEARCH_RESULTS ||
                result.entries.distinctBy(BrowseEntryDto::relativePath).size !=
                result.entries.size ||
                result.entries.any { !validSearchFolder(it) }
            ) {
                throw IOException("Server returned invalid folder search results")
            }
        }
    }

    suspend fun bookFiles(bookId: String): BookFilesDto {
        require(bookId.isNotBlank() && bookId.length <= MAX_IDENTIFIER_LENGTH)
        val url = baseUrl.newBuilder()
            .addPathSegments("v1/books")
            .addPathSegment(bookId)
            .addPathSegment("files")
            .build()
        return execute<BookFilesDto>(url).also { response ->
            if (
                response.bookId != bookId ||
                !validRootId(response.rootId) ||
                !validRelativePath(response.sourceFolder, allowEmpty = true) ||
                response.files.distinctBy { it.id }.size != response.files.size ||
                response.files.any { file ->
                    file.id.isBlank() ||
                        file.id.length > MAX_IDENTIFIER_LENGTH ||
                        file.size < 0 ||
                        !validInstant(file.modifiedAt) ||
                        !validDisplayName(file.name) ||
                        !validRelativePath(file.relativePath, allowEmpty = false) ||
                        file.relativePath != childPath(response.sourceFolder, file.name)
                }
            ) {
                throw IOException("Server returned invalid book files")
            }
        }
    }

    private suspend inline fun <reified T> execute(url: HttpUrl): T {
        val payload = executePayload(url)
        return try {
            ApiJson.decodeFromString<T>(payload.toString(Charsets.UTF_8))
        } catch (_: SerializationException) {
            throw IOException("Browse response is invalid")
        } catch (_: IllegalArgumentException) {
            throw IOException("Browse response is invalid")
        }
    }

    private suspend fun executePayload(url: HttpUrl): ByteArray =
        suspendCancellableCoroutine { continuation ->
            val call = calls.newCall(Request.Builder().url(url).get().build())
            continuation.invokeOnCancellation {
                call.cancel()
            }
            call.enqueue(
                object : Callback {
                    override fun onFailure(call: Call, e: IOException) {
                        if (continuation.isActive) {
                            continuation.resumeWithException(e)
                        }
                    }

                    override fun onResponse(call: Call, response: Response) {
                        try {
                            response.use {
                                if (!it.isSuccessful) {
                                    throw IOException(
                                        "Browse request failed with HTTP ${it.code}",
                                    )
                                }
                                val source = it.body.source()
                                if (source.request(MAX_BROWSE_RESPONSE_BYTES + 1L)) {
                                    throw IOException("Browse response is too large")
                                }
                                val payload = source.readByteArray()
                                if (continuation.isActive) {
                                    continuation.resume(payload)
                                }
                            }
                        } catch (failure: Exception) {
                            if (continuation.isActive) {
                                continuation.resumeWithException(failure)
                            }
                        }
                    }
                },
            )
        }
}

private fun validEntry(parent: String, entry: BrowseEntryDto): Boolean =
    entry.kind in ENTRY_KINDS &&
        validDisplayName(entry.name) &&
        validInstant(entry.modifiedAt) &&
        validRelativePath(entry.relativePath, allowEmpty = false) &&
        entry.relativePath == childPath(parent, entry.name) &&
        (entry.size == null || entry.size >= 0)

private fun validSearchFolder(entry: BrowseEntryDto): Boolean =
    entry.kind == "folder" &&
        entry.size == null &&
        validDisplayName(entry.name) &&
        validInstant(entry.modifiedAt) &&
        validRelativePath(entry.relativePath, allowEmpty = false) &&
        entry.relativePath.substringAfterLast('/') == entry.name

private fun childPath(parent: String, name: String): String =
    if (parent.isEmpty()) name else "$parent/$name"

private fun validDisplayName(value: String): Boolean =
    value.isNotBlank() &&
        value.length <= MAX_NAME_LENGTH &&
        value != "." &&
        value != ".." &&
        '/' !in value &&
        '\\' !in value &&
        value.none(Char::isISOControl) &&
        !WINDOWS_VOLUME.containsMatchIn(value)

internal fun validRelativePath(value: String, allowEmpty: Boolean): Boolean {
    if (value.isEmpty()) return allowEmpty
    if (
        value.length > MAX_RELATIVE_PATH_LENGTH ||
        value.startsWith('/') ||
        value.startsWith('\\') ||
        '\\' in value ||
        WINDOWS_VOLUME.containsMatchIn(value)
    ) {
        return false
    }
    val segments = value.split('/')
    return segments.all { segment ->
        segment.isNotEmpty() &&
            segment != "." &&
            segment != ".." &&
            validDisplayName(segment)
    }
}

private fun validRootId(value: String): Boolean =
    value.length in 1..MAX_IDENTIFIER_LENGTH && ROOT_ID.matches(value)

private fun validInstant(value: String): Boolean {
    if (value.isEmpty() || value.length > MAX_INSTANT_LENGTH) return false
    return try {
        Instant.parse(value)
        true
    } catch (_: DateTimeParseException) {
        false
    }
}

private val ENTRY_KINDS = setOf("folder", "file")
private val ROOT_ID = Regex("^[a-z0-9][a-z0-9_-]*$")
private val WINDOWS_VOLUME = Regex("^[A-Za-z]:")
private const val MAX_IDENTIFIER_LENGTH = 128
private const val MAX_NAME_LENGTH = 255
private const val MAX_RELATIVE_PATH_LENGTH = 4096
private const val MAX_INSTANT_LENGTH = 64
private const val MAX_BROWSE_RESPONSE_BYTES = 2 * 1024 * 1024
private const val MAX_FOLDER_SEARCH_RESULTS = 100

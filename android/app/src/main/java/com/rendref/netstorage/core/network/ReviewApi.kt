package com.rendref.netstorage.core.network

import com.rendref.netstorage.core.model.AcceptedJobDto
import com.rendref.netstorage.core.model.ApiJson
import com.rendref.netstorage.core.model.BookDetailDto
import com.rendref.netstorage.core.model.BookFilesDto
import com.rendref.netstorage.core.model.GroupingContextDto
import com.rendref.netstorage.core.model.MatchSearchDto
import com.rendref.netstorage.core.model.MetadataCandidateDto
import java.io.IOException
import java.time.Instant
import java.time.format.DateTimeParseException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import kotlinx.serialization.decodeFromString
import kotlinx.serialization.encodeToString
import okhttp3.Call
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

class ReviewApi(
    private val calls: Call.Factory,
    private val baseUrl: HttpUrl,
) {
    init {
        require(baseUrl.isHttps)
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty())
        require(baseUrl.query == null && baseUrl.fragment == null && baseUrl.encodedPath == "/")
    }

    suspend fun bookDetail(bookId: String): BookDetailDto {
        requireUuid(bookId)
        val url = bookUrl(bookId)
        return executeJson<BookDetailDto>(Request.Builder().url(url).get().build())
            .let { detail ->
                if (
                    detail.id != bookId ||
                    !ROOT_ID.matches(detail.rootId) ||
                    !validRelativePath(detail.sourceFolder, allowEmpty = true) ||
                    detail.title.isBlank() ||
                    !detail.title.isSafeDisplayText() ||
                    detail.authors.any { !it.isSafeDisplayText() } ||
                    listOfNotNull(
                        detail.subtitle,
                        detail.series,
                        detail.seriesPosition,
                        detail.publisher,
                        detail.publishedDate,
                        detail.metadataSource,
                    ).any { !it.isSafeDisplayText() } ||
                    detail.narrators.any { !it.isSafeDisplayText() } ||
                    detail.genres.any { !it.isSafeDisplayText() } ||
                    detail.providerSubjects.any { !it.isSafeDisplayText() } ||
                    detail.identifiers.any {
                        !it.key.isSafeDisplayText() || it.value.hasUnsafeText()
                    } ||
                    detail.confidence?.let {
                        !it.isFinite() || it !in 0.0..1.0
                    } == true ||
                    detail.attribution?.let { attribution ->
                        !attribution.provider.isSafeDisplayText() ||
                            attribution.displayName?.isSafeDisplayText() == false ||
                            attribution.sourceUrl?.let(::validPublicHttpsUrl) == false
                    } == true
                ) {
                    throw IOException("Server returned invalid book details")
                }
                val coverUrl = detail.coverUrl?.let {
                    normalizeServerCover(bookId, it)
                }
                detail.copy(coverUrl = coverUrl)
            }
    }

    suspend fun bookFiles(bookId: String): BookFilesDto {
        requireUuid(bookId)
        val request = Request.Builder()
            .url(bookUrl(bookId).newBuilder().addPathSegment("files").build())
            .get()
            .build()
        return executeJson<BookFilesDto>(request).also { response ->
            if (
                response.bookId != bookId ||
                !ROOT_ID.matches(response.rootId) ||
                !validRelativePath(response.sourceFolder, allowEmpty = true) ||
                response.files.distinctBy { it.id }.size != response.files.size ||
                response.files.any { file ->
                    !UUID.matches(file.id) ||
                        file.size < 0 ||
                        file.name.isBlank() ||
                        file.name.hasUnsafeText() ||
                        '/' in file.name ||
                        '\\' in file.name ||
                        !validRelativePath(file.relativePath, allowEmpty = false) ||
                        file.relativePath != childPath(response.sourceFolder, file.name)
                        || !validInstant(file.modifiedAt)
                }
            ) {
                throw IOException("Server returned invalid assigned files")
            }
        }
    }

    suspend fun groupingContext(bookId: String): GroupingContextDto {
        requireUuid(bookId)
        val request = Request.Builder()
            .url(bookUrl(bookId).newBuilder().addPathSegment("grouping").build())
            .get()
            .build()
        return executeJson<GroupingContextDto>(request).also { response ->
            val availableFileIds = response.files.map { it.id }.toSet()
            val assignedFileIds = response.books.flatMap { it.fileIds }
            if (
                response.bookId != bookId ||
                !ROOT_ID.matches(response.rootId) ||
                !validRelativePath(response.sourceFolder, allowEmpty = true) ||
                response.files.distinctBy { it.id }.size != response.files.size ||
                response.files.any { file ->
                    !UUID.matches(file.id) ||
                        file.size < 0 ||
                        file.name.isBlank() ||
                        file.name.hasUnsafeText() ||
                        '/' in file.name ||
                        '\\' in file.name ||
                        !validRelativePath(file.relativePath, allowEmpty = false) ||
                        file.relativePath != childPath(response.sourceFolder, file.name) ||
                        !validInstant(file.modifiedAt) ||
                        AUDIO_SUFFIXES.none {
                            file.name.endsWith(it, ignoreCase = true)
                        }
                } ||
                response.books.size !in 1..MAX_GROUPS ||
                response.books.distinctBy { it.bookId }.size != response.books.size ||
                response.books.none { it.bookId == bookId } ||
                response.books.any { book ->
                    !UUID.matches(book.bookId) ||
                        book.title.isBlank() ||
                        !book.title.isSafeDisplayText() ||
                        book.fileIds.distinct().size != book.fileIds.size ||
                        book.fileIds.any {
                            !UUID.matches(it) || it !in availableFileIds
                        }
                } ||
                assignedFileIds.distinct().size != assignedFileIds.size
            ) {
                throw IOException("Server returned invalid grouping context")
            }
        }
    }

    suspend fun searchMatches(
        bookId: String,
        title: String,
        author: String,
        isbn: String,
    ): MatchSearchDto {
        requireUuid(bookId)
        val input = MatchSearchRequest(
            bookId = bookId,
            title = title.safeSearchTerm(MAX_SEARCH_LENGTH),
            author = author.safeSearchTerm(MAX_SEARCH_LENGTH),
            isbn = isbn.safeSearchTerm(MAX_ISBN_LENGTH),
        )
        val request = jsonRequest(
            baseUrl.newBuilder().addPathSegments("v1/catalog/matches/search").build(),
            "POST",
            ApiJson.encodeToString(input),
        )
        return executeJson<MatchSearchDto>(request).also { response ->
            if (
                response.bookId != bookId ||
                response.candidates.size > MAX_CANDIDATES ||
                response.candidates.any { !validCandidate(it) }
            ) {
                throw IOException("Server returned invalid metadata candidates")
            }
        }
    }

    suspend fun confirmMatch(bookId: String, matchId: String): AcceptedJobDto {
        requireUuid(bookId)
        requireUuid(matchId)
        val request = jsonRequest(
            bookUrl(bookId).newBuilder().addPathSegment("match").build(),
            "PUT",
            ApiJson.encodeToString(ConfirmMatchRequest(matchId)),
        )
        return executeJson<AcceptedJobDto>(request).also {
            if (!UUID.matches(it.jobId) || it.status.isBlank() || it.status.hasUnsafeText()) {
                throw IOException("Server returned an invalid cover job")
            }
        }
    }

    suspend fun saveGrouping(
        bookId: String,
        groups: List<ReviewGroupingRequest>,
    ) {
        requireUuid(bookId)
        require(groups.size in 2..MAX_GROUPS)
        val allFiles = groups.flatMap(ReviewGroupingRequest::fileIds)
        val existingBookIds = groups.mapNotNull(ReviewGroupingRequest::bookId)
        require(
            groups.all { group ->
                group.bookId == null || UUID.matches(group.bookId)
            } &&
                groups.all { it.fileIds.isNotEmpty() && it.fileIds.all(UUID::matches) } &&
                allFiles.distinct().size == allFiles.size &&
                existingBookIds.distinct().size == existingBookIds.size,
        )
        val request = jsonRequest(
            bookUrl(bookId).newBuilder().addPathSegment("grouping").build(),
            "PUT",
            ApiJson.encodeToString(GroupingRequest(groups)),
        )
        executeNoContent(request)
    }

    private fun bookUrl(bookId: String): HttpUrl = baseUrl.newBuilder()
        .addPathSegments("v1/books")
        .addPathSegment(bookId)
        .build()

    private fun normalizeServerCover(bookId: String, raw: String): String {
        val resolved = baseUrl.resolve(raw)
            ?: throw IOException("Server returned an invalid cover URL")
        val expected = bookUrl(bookId).newBuilder().addPathSegment("cover").build()
        if (
            !resolved.sameOrigin(expected) ||
            resolved.encodedUsername.isNotEmpty() ||
            resolved.encodedPassword.isNotEmpty() ||
            resolved.encodedPath != expected.encodedPath ||
            resolved.query != null ||
            resolved.fragment != null
        ) {
            throw IOException("Server returned an untrusted cover URL")
        }
        return resolved.toString()
    }

    private suspend inline fun <reified T> executeJson(request: Request): T =
        withContext(Dispatchers.IO) {
            val response = calls.newCall(request).execute()
            response.use {
                if (!it.isSuccessful) {
                    throw IOException("Review request failed with HTTP ${it.code}")
                }
                val source = it.body.source()
                if (source.request(MAX_RESPONSE_BYTES + 1L)) {
                    throw IOException("Review response is too large")
                }
                try {
                    ApiJson.decodeFromString<T>(
                        source.readByteArray().toString(Charsets.UTF_8),
                    )
                } catch (_: Exception) {
                    throw IOException("Review response is invalid")
                }
            }
        }

    private suspend fun executeNoContent(request: Request) = withContext(Dispatchers.IO) {
        val response = calls.newCall(request).execute()
        response.use {
            if (it.code != 204) {
                throw IOException("Grouping request failed with HTTP ${it.code}")
            }
        }
    }
}

@Serializable
private data class MatchSearchRequest(
    val bookId: String,
    val title: String,
    val author: String,
    val isbn: String,
)

@Serializable
private data class ConfirmMatchRequest(val matchId: String)

@Serializable
data class ReviewGroupingRequest(
    val bookId: String? = null,
    val fileIds: List<String>,
)

@Serializable
private data class GroupingRequest(val groups: List<ReviewGroupingRequest>)

private fun jsonRequest(url: HttpUrl, method: String, body: String): Request =
    Request.Builder()
        .url(url)
        .method(method, body.toRequestBody(JSON))
        .build()

private fun validCandidate(candidate: MetadataCandidateDto): Boolean {
    if (
        !UUID.matches(candidate.matchId) ||
        candidate.provider.isBlank() ||
        !candidate.provider.isSafeDisplayText() ||
        candidate.providerId.isBlank() ||
        candidate.providerId.hasUnsafeText() ||
        candidate.title.isBlank() ||
        !candidate.title.isSafeDisplayText() ||
        candidate.authors.any { !it.isSafeDisplayText() } ||
        listOfNotNull(
            candidate.subtitle,
            candidate.publisher,
            candidate.publishedDate,
            candidate.series,
            candidate.seriesPosition,
            candidate.isbn,
        ).any { !it.isSafeDisplayText() } ||
        candidate.genres.any { !it.isSafeDisplayText() } ||
        candidate.subjects.any { !it.isSafeDisplayText() } ||
        !candidate.score.isFinite() ||
        candidate.score !in 0.0..1.0
    ) {
        return false
    }
    val cover = candidate.coverUrl ?: return true
    return validPublicHttpsUrl(cover)
}

private fun String.safeSearchTerm(maxLength: Int): String {
    val normalized = trim()
    require(normalized.length <= maxLength && normalized.isSafeDisplayText())
    return normalized
}

private fun String.hasUnsafeText(): Boolean =
    any(Char::isISOControl) || length > MAX_TEXT_LENGTH

private fun String.isSafeDisplayText(): Boolean =
    !hasUnsafeText() &&
        !startsWith('/') &&
        !startsWith('\\') &&
        !WINDOWS_ABSOLUTE.containsMatchIn(this)

private fun validPublicHttpsUrl(value: String): Boolean {
    val url = value.toHttpUrlOrNull() ?: return false
    return url.isHttps &&
        url.encodedUsername.isEmpty() &&
        url.encodedPassword.isEmpty() &&
        url.fragment == null
}

private fun validInstant(value: String): Boolean = try {
    value.length <= 64 && Instant.parse(value).let { true }
} catch (_: DateTimeParseException) {
    false
}

private fun requireUuid(value: String) {
    require(UUID.matches(value))
}

private fun childPath(parent: String, name: String): String =
    if (parent.isEmpty()) name else "$parent/$name"

private fun HttpUrl.sameOrigin(other: HttpUrl): Boolean =
    scheme == other.scheme && host == other.host && port == other.port

private val JSON = "application/json; charset=utf-8".toMediaType()
private val UUID = Regex(
    "^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$",
)
private val ROOT_ID = Regex("^[a-z0-9][a-z0-9_-]*$")
private val WINDOWS_ABSOLUTE = Regex("^[A-Za-z]:[/\\\\]")
private const val MAX_SEARCH_LENGTH = 500
private const val MAX_ISBN_LENGTH = 64
private const val MAX_TEXT_LENGTH = 1_000
private const val MAX_CANDIDATES = 100
private const val MAX_GROUPS = 100
private const val MAX_RESPONSE_BYTES = 2 * 1024 * 1024
private val AUDIO_SUFFIXES = setOf(
    ".m4b",
    ".m4a",
    ".mp3",
    ".aac",
    ".flac",
    ".ogg",
    ".opus",
    ".wav",
)

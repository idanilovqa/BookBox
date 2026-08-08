package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.model.ApiJson
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.model.BookSearchGroupDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.model.ApiErrorDto
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.repository.CatalogFiltersDto
import com.rendref.bookbox.core.repository.PagedBooks
import java.io.IOException
import java.time.Instant
import java.util.UUID
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.Serializable
import okhttp3.Call
import okhttp3.HttpUrl
import okhttp3.Request

class CatalogApi(
    private val calls: Call.Factory,
    private val baseUrl: HttpUrl,
) {
    init {
        require(baseUrl.scheme == "http")
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty())
        require(baseUrl.query == null && baseUrl.fragment == null && baseUrl.encodedPath == "/")
    }

    suspend fun books(query: BookQuery): PagedBooks = withContext(Dispatchers.IO) {
        val url = baseUrl.newBuilder()
            .addPathSegments("v1/books")
            .apply {
                query.text.takeIf(String::isNotBlank)?.let { addQueryParameter("q", it) }
                query.author?.takeIf(String::isNotBlank)
                    ?.let { addQueryParameter("author", it) }
                query.genre?.takeIf(String::isNotBlank)
                    ?.let { addQueryParameter("genre", it) }
                query.year?.let { addQueryParameter("year", it.toString()) }
                val roots = query.rootIds.ifEmpty { listOfNotNull(query.rootId) }
                    .map(String::trim)
                    .filter(String::isNotEmpty)
                    .distinct()
                    .sorted()
                roots.forEach { addQueryParameter("rootId", it) }
                query.reviewState?.takeIf(String::isNotBlank)
                    ?.let { addQueryParameter("reviewState", it) }
                addQueryParameter("sort", query.sort.wireValue)
                query.cursor?.takeIf(String::isNotBlank)
                    ?.let { addQueryParameter("cursor", it) }
            }
            .build()
        val response = calls.newCall(Request.Builder().url(url).get().build()).execute()
        response.use {
            val source = it.body.source()
            if (source.request(MAX_CATALOG_RESPONSE_BYTES + 1L)) {
                throw IOException("Catalog response is too large")
            }
            val payload = source.readByteArray().toString(Charsets.UTF_8)
            if (!it.isSuccessful) {
                val code = runCatching {
                    ApiJson.decodeFromString<ApiErrorDto>(payload).error.code
                }.getOrNull()
                if (it.code == 503 && code == "search_index_building") {
                    throw SearchIndexBuilding()
                }
                throw IOException("Catalog request failed with HTTP ${it.code}")
            }
            val decoded = try {
                ApiJson.decodeFromString<BooksPageDto>(
                    payload,
                )
            } catch (_: Exception) {
                throw IOException("Catalog response is invalid")
            }
            validatePage(decoded, query)
            val groups = decoded.groups.map { group ->
                group.copy(books = group.books.map(::normalizeCoverUrl))
            }
            PagedBooks(
                groups = groups,
                snapshots = decoded.snapshots,
                nextCursor = decoded.nextCursor,
                filters = decoded.filters,
            )
        }
    }

    private fun normalizeCoverUrl(book: BookCardDto): BookCardDto {
        val rawCoverUrl = book.coverUrl ?: return book
        val resolved = baseUrl.resolve(rawCoverUrl)
            ?: throw IOException("Catalog response contains an invalid cover URL")
        val expectedPath = baseUrl.newBuilder()
            .addPathSegments("v1/books")
            .addPathSegment(book.id)
            .addPathSegment("cover")
            .build()
            .encodedPath
        if (
            !resolved.hasSameOriginAs(baseUrl) ||
            resolved.encodedUsername.isNotEmpty() ||
            resolved.encodedPassword.isNotEmpty() ||
            resolved.encodedPath != expectedPath ||
            resolved.query != null ||
            resolved.fragment != null
        ) {
            throw IOException("Catalog response contains an untrusted cover URL")
        }
        return book.copy(coverUrl = resolved.toString())
    }

    private fun validatePage(page: BooksPageDto, query: BookQuery) {
        val requested = query.rootIds.ifEmpty { listOfNotNull(query.rootId) }
            .map(String::trim)
            .filter(String::isNotEmpty)
            .toSet()
        val snapshotRoots = page.snapshots.map { it.rootId }
        val childIds = page.groups.flatMap { it.books }.map { it.id }
        val groupIds = page.groups.map { it.groupId }
        val invalidSnapshots =
            page.snapshots.isEmpty() ||
                snapshotRoots != snapshotRoots.sorted() ||
                snapshotRoots.distinct().size != snapshotRoots.size ||
                snapshotRoots.any { !VALID_ROOT.matches(it) } ||
                (requested.isNotEmpty() && snapshotRoots.toSet() != requested) ||
                page.snapshots.any {
                    !validUuid(it.scanRunId) || !validInstant(it.completedAt)
                }
        val invalidGroups =
            childIds.distinct().size != childIds.size ||
                groupIds.distinct().size != groupIds.size ||
                page.groups.any { group -> invalidGroup(group, snapshotRoots) }
        if (invalidSnapshots || invalidGroups) {
            throw IOException("Catalog response is invalid")
        }
    }

    private fun invalidGroup(
        group: BookSearchGroupDto,
        snapshotRoots: List<String>,
    ): Boolean {
        val roots = group.books.map { it.rootId }.toSet()
        return !validUuid(group.groupId) ||
            group.books.isEmpty() ||
            group.duplicate != (group.books.size > 1) ||
            roots.size != 1 ||
            roots.singleOrNull() !in snapshotRoots ||
            group.books.any { book ->
                !validUuid(book.id) ||
                    book.title.isBlank() ||
                    !validRelativePath(book.sourceFolder, true) ||
                    book.durationMs?.let { it < 0 } == true
            }
    }
}

@Serializable
private data class BooksPageDto(
    val groups: List<BookSearchGroupDto> = emptyList(),
    val snapshots: List<SearchSnapshotDto> = emptyList(),
    val nextCursor: String? = null,
    val filters: CatalogFiltersDto = CatalogFiltersDto(),
)

class SearchIndexBuilding : IOException("Search index is building")

private fun validUuid(value: String): Boolean = runCatching {
    val id = UUID.fromString(value)
    id != UUID(0, 0) && id.toString() == value
}.getOrDefault(false)

private fun validInstant(value: String): Boolean = runCatching {
    Instant.parse(value) != Instant.EPOCH
}.getOrDefault(false)
private val VALID_ROOT = Regex("^[a-z0-9][a-z0-9_-]*$")

private const val MAX_CATALOG_RESPONSE_BYTES = 2 * 1024 * 1024

private fun HttpUrl.hasSameOriginAs(other: HttpUrl): Boolean =
    scheme == other.scheme && host == other.host && port == other.port

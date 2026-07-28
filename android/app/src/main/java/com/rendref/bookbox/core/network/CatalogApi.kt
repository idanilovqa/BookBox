package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.model.ApiJson
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.repository.CatalogFiltersDto
import com.rendref.bookbox.core.repository.PagedBooks
import java.io.IOException
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
        require(baseUrl.isHttps)
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
                query.rootId?.takeIf(String::isNotBlank)
                    ?.let { addQueryParameter("rootId", it) }
                query.reviewState?.takeIf(String::isNotBlank)
                    ?.let { addQueryParameter("reviewState", it) }
                addQueryParameter("sort", query.sort.wireValue)
                query.cursor?.takeIf(String::isNotBlank)
                    ?.let { addQueryParameter("cursor", it) }
            }
            .build()
        val response = calls.newCall(Request.Builder().url(url).get().build()).execute()
        response.use {
            if (!it.isSuccessful) {
                throw IOException("Catalog request failed with HTTP ${it.code}")
            }
            val source = it.body.source()
            if (source.request(MAX_CATALOG_RESPONSE_BYTES + 1L)) {
                throw IOException("Catalog response is too large")
            }
            val decoded = try {
                ApiJson.decodeFromString<BooksPageDto>(
                    source.readByteArray().toString(Charsets.UTF_8),
                )
            } catch (_: Exception) {
                throw IOException("Catalog response is invalid")
            }
            PagedBooks(
                decoded.items.map(::normalizeCoverUrl),
                decoded.nextCursor,
                decoded.filters,
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
}

@Serializable
private data class BooksPageDto(
    val items: List<BookCardDto>,
    val nextCursor: String? = null,
    val filters: CatalogFiltersDto = CatalogFiltersDto(),
)

private const val MAX_CATALOG_RESPONSE_BYTES = 2 * 1024 * 1024

private fun HttpUrl.hasSameOriginAs(other: HttpUrl): Boolean =
    scheme == other.scheme && host == other.host && port == other.port

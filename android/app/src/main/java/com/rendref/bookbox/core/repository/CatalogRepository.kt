package com.rendref.bookbox.core.repository

import com.rendref.bookbox.core.model.BookSearchGroupDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.network.CatalogApi
import java.io.Serializable
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.serialization.Serializable as KotlinSerializable

enum class BookSort(
    val wireValue: String,
) {
    TITLE("title"),
    SERIES("series"),
    AUTHOR("author"),
    YEAR("year"),
    RECENTLY_ADDED("recently_added"),
    RECENTLY_SCANNED("recently_scanned"),
}

data class BookQuery(
    val text: String = "",
    val author: String? = null,
    val series: String? = null,
    val genre: String? = null,
    val year: Int? = null,
    val rootId: String? = null,
    val rootIds: List<String> = emptyList(),
    val reviewState: String? = null,
    val sort: BookSort = BookSort.TITLE,
    val cursor: String? = null,
) : Serializable

@KotlinSerializable
data class CatalogFiltersDto(
    val authors: List<String> = emptyList(),
    val genres: List<String> = emptyList(),
    val years: List<Int> = emptyList(),
    val roots: List<String> = emptyList(),
    val reviewStates: List<String> = emptyList(),
)

data class PagedBooks(
    val groups: List<BookSearchGroupDto>,
    val snapshots: List<SearchSnapshotDto>,
    val nextCursor: String?,
    val filters: CatalogFiltersDto,
)

fun interface CatalogSource {
    fun books(query: BookQuery): Flow<PagedBooks>
}

class CatalogRepository(
    private val api: CatalogApi,
) : CatalogSource {
    override fun books(query: BookQuery): Flow<PagedBooks> = flow {
        emit(api.books(query))
    }
}

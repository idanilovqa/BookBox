package com.rendref.bookbox.feature.library

import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.repository.BookSort
import com.rendref.bookbox.core.repository.CatalogFiltersDto
import com.rendref.bookbox.core.repository.CatalogSource
import com.rendref.bookbox.core.repository.PagedBooks
import java.io.IOException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.catch
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

data class LibraryState(
    val query: BookQuery = BookQuery(),
    val books: List<BookCardDto> = emptyList(),
    val filters: CatalogFiltersDto = CatalogFiltersDto(),
    val selectedBookIds: Set<String> = emptySet(),
    val nextCursor: String? = null,
    val isLoading: Boolean = false,
    val isLoadingMore: Boolean = false,
    val errorMessage: String? = null,
    val scrollIndex: Int = 0,
    val scrollOffset: Int = 0,
)

@OptIn(ExperimentalCoroutinesApi::class, FlowPreview::class)
class LibraryViewModel(
    private val savedState: SavedStateHandle,
    private val catalog: CatalogSource,
) : ViewModel() {
    private val initialQuery = savedState.get<BookQuery>(QUERY_KEY) ?: BookQuery()
    private val query = MutableStateFlow(initialQuery.withoutCursor())
    private val mutableState = MutableStateFlow(
        LibraryState(
            query = initialQuery.withoutCursor(),
            selectedBookIds = savedState.get<ArrayList<String>>(SELECTION_KEY)
                ?.toSet()
                .orEmpty(),
            scrollIndex = savedState[SCROLL_INDEX_KEY] ?: 0,
            scrollOffset = savedState[SCROLL_OFFSET_KEY] ?: 0,
        ),
    )
    val state: StateFlow<LibraryState> = mutableState.asStateFlow()

    private val consumedCursors = mutableSetOf<String>()
    private var loadingCursor: String? = null

    init {
        viewModelScope.launch {
            query
                .debounce(SEARCH_DEBOUNCE_MILLIS)
                .flatMapLatest { requested ->
                    flow<LibraryLoad> {
                        emit(LibraryLoad.Loading(requested))
                        catalog.books(requested).collect { page ->
                            emit(LibraryLoad.Loaded(requested, page))
                        }
                    }.catch { failure ->
                        if (failure is CancellationException) throw failure
                        emit(LibraryLoad.Failed(requested))
                    }
                }
                .collect(::accept)
        }
    }

    fun setSearchText(text: String) = updateQuery { copy(text = text) }

    fun setAuthor(author: String?) = updateQuery { copy(author = author.cleanFilter()) }

    fun setGenre(genre: String?) = updateQuery { copy(genre = genre.cleanFilter()) }

    fun setYear(year: Int?) = updateQuery { copy(year = year) }

    fun setRoot(rootId: String?) = updateQuery { copy(rootId = rootId.cleanFilter()) }

    fun setReviewState(reviewState: String?) =
        updateQuery { copy(reviewState = reviewState.cleanFilter()) }

    fun setSort(sort: BookSort) = updateQuery { copy(sort = sort) }

    fun toggleSelection(bookId: String) {
        val updated = mutableState.value.selectedBookIds.toMutableSet().apply {
            if (!add(bookId)) remove(bookId)
        }.toSet()
        savedState[SELECTION_KEY] = ArrayList(updated)
        mutableState.value = mutableState.value.copy(selectedBookIds = updated)
    }

    fun saveGridPosition(index: Int, offset: Int) {
        val safeIndex = index.coerceAtLeast(0)
        val safeOffset = offset.coerceAtLeast(0)
        savedState[SCROLL_INDEX_KEY] = safeIndex
        savedState[SCROLL_OFFSET_KEY] = safeOffset
        mutableState.value = mutableState.value.copy(
            scrollIndex = safeIndex,
            scrollOffset = safeOffset,
        )
    }

    fun loadNextPage() {
        val cursor = mutableState.value.nextCursor ?: return
        if (
            loadingCursor != null ||
            cursor in consumedCursors ||
            mutableState.value.isLoading
        ) {
            return
        }
        loadingCursor = cursor
        mutableState.value = mutableState.value.copy(
            isLoadingMore = true,
            errorMessage = null,
        )
        val baseQuery = mutableState.value.query.withoutCursor()
        viewModelScope.launch {
            try {
                val page = catalog.books(baseQuery.copy(cursor = cursor)).first()
                if (loadingCursor != cursor || mutableState.value.query != baseQuery) return@launch
                consumedCursors += cursor
                val knownIds = mutableState.value.books.mapTo(mutableSetOf(), BookCardDto::id)
                val additions = page.items.filter { knownIds.add(it.id) }
                mutableState.value = mutableState.value.copy(
                    books = mutableState.value.books + additions,
                    filters = page.filters,
                    nextCursor = page.nextCursor,
                    isLoadingMore = false,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: IOException) {
                mutableState.value = mutableState.value.copy(
                    isLoadingMore = false,
                    errorMessage = "Unable to load more audiobooks",
                )
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(
                    isLoadingMore = false,
                    errorMessage = "Unable to load more audiobooks",
                )
            } finally {
                if (loadingCursor == cursor) loadingCursor = null
            }
        }
    }

    private fun updateQuery(transform: BookQuery.() -> BookQuery) {
        val updated = query.value.transform().withoutCursor()
        if (updated == query.value) return
        savedState[QUERY_KEY] = updated
        query.value = updated
        mutableState.value = mutableState.value.copy(query = updated, errorMessage = null)
    }

    private fun accept(load: LibraryLoad) {
        when (load) {
            is LibraryLoad.Loading -> {
                consumedCursors.clear()
                loadingCursor = null
                mutableState.value = mutableState.value.copy(
                    query = load.query,
                    isLoading = true,
                    isLoadingMore = false,
                    errorMessage = null,
                )
            }

            is LibraryLoad.Loaded -> {
                mutableState.value = mutableState.value.copy(
                    query = load.query,
                    books = load.page.items.distinctBy(BookCardDto::id),
                    filters = load.page.filters,
                    nextCursor = load.page.nextCursor,
                    isLoading = false,
                    isLoadingMore = false,
                    errorMessage = null,
                )
            }

            is LibraryLoad.Failed -> {
                mutableState.value = mutableState.value.copy(
                    query = load.query,
                    isLoading = false,
                    isLoadingMore = false,
                    errorMessage = "Unable to load audiobooks",
                )
            }
        }
    }
}

private sealed interface LibraryLoad {
    val query: BookQuery

    data class Loading(override val query: BookQuery) : LibraryLoad

    data class Loaded(
        override val query: BookQuery,
        val page: PagedBooks,
    ) : LibraryLoad

    data class Failed(override val query: BookQuery) : LibraryLoad
}

private fun BookQuery.withoutCursor() = copy(cursor = null)

private fun String?.cleanFilter(): String? = this?.trim()?.takeIf(String::isNotEmpty)

private const val SEARCH_DEBOUNCE_MILLIS = 300L
private const val QUERY_KEY = "library.query"
private const val SELECTION_KEY = "library.selection"
private const val SCROLL_INDEX_KEY = "library.scroll.index"
private const val SCROLL_OFFSET_KEY = "library.scroll.offset"

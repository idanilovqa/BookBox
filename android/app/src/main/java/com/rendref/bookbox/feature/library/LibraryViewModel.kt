package com.rendref.bookbox.feature.library

import androidx.lifecycle.SavedStateHandle
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.model.BookSearchGroupDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.network.SearchIndexBuilding
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.repository.BookSort
import com.rendref.bookbox.core.repository.CatalogFiltersDto
import com.rendref.bookbox.core.repository.CatalogSource
import com.rendref.bookbox.core.repository.PagedBooks
import com.rendref.bookbox.core.search.SearchUpdate
import com.rendref.bookbox.core.search.STALE_CATALOG_STATUS
import com.rendref.bookbox.core.search.catalogIsStale
import com.rendref.bookbox.core.search.debouncedSearchUpdates
import com.rendref.bookbox.core.search.isOlderCatalogGeneration
import java.io.IOException
import java.time.Clock
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

enum class LibraryLayout { GRID, COMPACT }

data class LibraryState(
    val query: BookQuery = BookQuery(),
    val books: List<BookCardDto> = emptyList(),
    val groups: List<BookSearchGroupDto> = emptyList(),
    val snapshots: List<SearchSnapshotDto> = emptyList(),
    val filters: CatalogFiltersDto = CatalogFiltersDto(),
    val selectedBookIds: Set<String> = emptySet(),
    val nextCursor: String? = null,
    val isLoading: Boolean = false,
    val isLoadingMore: Boolean = false,
    val paginationError: String? = null,
    val isIndexBuilding: Boolean = false,
    val isCatalogStale: Boolean = false,
    val catalogStatusMessage: String? = null,
    val errorMessage: String? = null,
    val scrollIndex: Int = 0,
    val scrollOffset: Int = 0,
    val compactScrollIndex: Int = 0,
    val compactScrollOffset: Int = 0,
    val layout: LibraryLayout = LibraryLayout.GRID,
) {
    val canLoadNextPage: Boolean
        get() = nextCursor != null && paginationError == null
}

class LibraryViewModel(
    private val savedState: SavedStateHandle,
    private val catalog: CatalogSource,
    private val clock: Clock = Clock.systemUTC(),
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
            compactScrollIndex = savedState[COMPACT_SCROLL_INDEX_KEY] ?: 0,
            compactScrollOffset = savedState[COMPACT_SCROLL_OFFSET_KEY] ?: 0,
            layout = savedState.get<String>(LAYOUT_KEY)
                ?.let { runCatching { LibraryLayout.valueOf(it) }.getOrNull() }
                ?: LibraryLayout.GRID,
        ),
    )
    val state: StateFlow<LibraryState> = mutableState.asStateFlow()

    private val consumedCursors = mutableSetOf<String>()
    private var failedPaginationCursor: String? = null
    private var paginationJob: Job? = null

    init {
        viewModelScope.launch {
            query
                .debouncedSearchUpdates(
                    debounceMillis = SEARCH_DEBOUNCE_MILLIS,
                    allowBlank = true,
                    text = BookQuery::text,
                    isIndexBuilding = { it is SearchIndexBuilding },
                    load = { catalog.books(it).first() },
                )
                .collect(::accept)
        }
    }

    fun setSearchText(text: String) = updateQuery { copy(text = text) }

    fun setAuthor(author: String?) = updateQuery { copy(author = author.cleanFilter()) }

    fun setGenre(genre: String?) = updateQuery { copy(genre = genre.cleanFilter()) }

    fun setYear(year: Int?) = updateQuery { copy(year = year) }

    fun setRoot(rootId: String?) = updateQuery {
        copy(rootId = rootId.cleanFilter(), rootIds = emptyList())
    }

    fun setRoots(rootIds: List<String>) = updateQuery {
        copy(
            rootId = null,
            rootIds = rootIds.map(String::trim).filter(String::isNotEmpty).distinct().sorted(),
        )
    }

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
            paginationJob?.isActive == true ||
            cursor in consumedCursors ||
            cursor == failedPaginationCursor ||
            mutableState.value.isLoading
        ) {
            return
        }
        mutableState.value = mutableState.value.copy(
            isLoadingMore = true,
            isIndexBuilding = false,
            errorMessage = null,
            paginationError = null,
        )
        val baseQuery = mutableState.value.query.withoutCursor()
        lateinit var launchedJob: Job
        launchedJob = viewModelScope.launch(start = CoroutineStart.LAZY) {
            try {
                val page = catalog.books(baseQuery.copy(cursor = cursor)).first()
                if (mutableState.value.query != baseQuery) return@launch
                val current = mutableState.value
                val appendedGroups = appendGroups(current, page)
                val appendedBooks = appendedGroups.flatMap(BookSearchGroupDto::books)
                consumedCursors += cursor
                mutableState.value = mutableState.value.copy(
                    books = appendedBooks,
                    groups = appendedGroups,
                    snapshots = page.snapshots,
                    filters = page.filters,
                    nextCursor = page.nextCursor,
                    isLoadingMore = false,
                    paginationError = null,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: SearchIndexBuilding) {
                recordPaginationFailure(cursor)
                mutableState.value = mutableState.value.copy(isIndexBuilding = true)
            } catch (_: IOException) {
                recordPaginationFailure(cursor)
            } catch (_: Exception) {
                recordPaginationFailure(cursor)
            } finally {
                if (paginationJob === launchedJob) paginationJob = null
            }
        }
        paginationJob = launchedJob
        launchedJob.start()
    }

    fun retryNextPage() {
        failedPaginationCursor = null
        mutableState.value = mutableState.value.copy(paginationError = null)
        loadNextPage()
    }

    private fun recordPaginationFailure(cursor: String) {
        failedPaginationCursor = cursor
        mutableState.value = mutableState.value.copy(
            isLoadingMore = false,
            errorMessage = null,
            paginationError = "Couldn't load more",
        )
    }

    private fun updateQuery(transform: BookQuery.() -> BookQuery) {
        val updated = query.value.transform().withoutCursor()
        if (updated == query.value) return
        savedState[QUERY_KEY] = updated
        consumedCursors.clear()
        failedPaginationCursor = null
        paginationJob?.cancel()
        paginationJob = null
        query.value = updated
        mutableState.value = mutableState.value.copy(
            query = updated,
            nextCursor = null,
            isLoadingMore = false,
            isIndexBuilding = false,
            errorMessage = null,
            paginationError = null,
        )
    }

    private fun accept(update: SearchUpdate<BookQuery, PagedBooks>) {
        when (update) {
            is SearchUpdate.Loading -> {
                consumedCursors.clear()
                failedPaginationCursor = null
                paginationJob?.cancel()
                paginationJob = null
                mutableState.value = mutableState.value.copy(
                    query = update.query,
                    isLoading = true,
                    isLoadingMore = false,
                    isIndexBuilding = false,
                    errorMessage = null,
                    paginationError = null,
                )
            }

            is SearchUpdate.Loaded -> {
                val current = mutableState.value
                if (isOlderCatalogGeneration(update.page.snapshots, current.snapshots)) {
                    mutableState.value = current.copy(
                        query = update.query,
                        isLoading = false,
                        isLoadingMore = false,
                        isIndexBuilding = false,
                        errorMessage = null,
                    )
                    return
                }
                val groups = update.page.groups
                val catalogStale = catalogIsStale(update.page.snapshots, clock)
                mutableState.value = mutableState.value.copy(
                    query = update.query,
                    books = groups.flatMap(BookSearchGroupDto::books),
                    groups = groups,
                    snapshots = update.page.snapshots,
                    filters = update.page.filters,
                    nextCursor = update.page.nextCursor,
                    isLoading = false,
                    isLoadingMore = false,
                    isIndexBuilding = false,
                    isCatalogStale = catalogStale,
                    catalogStatusMessage = STALE_CATALOG_STATUS.takeIf { catalogStale },
                    errorMessage = null,
                    paginationError = null,
                )
            }

            is SearchUpdate.Failed -> {
                mutableState.value = mutableState.value.copy(
                    query = update.query,
                    isLoading = false,
                    isLoadingMore = false,
                    isIndexBuilding = false,
                    errorMessage = "Unable to load audiobooks",
                    paginationError = null,
                )
            }

            is SearchUpdate.IndexBuilding -> mutableState.value = mutableState.value.copy(
                query = update.query,
                isLoading = false,
                isLoadingMore = false,
                isIndexBuilding = true,
                errorMessage = null,
                paginationError = null,
            )

            is SearchUpdate.Inactive -> mutableState.value = mutableState.value.copy(
                query = update.query,
                isLoading = false,
                isLoadingMore = false,
                isIndexBuilding = false,
                errorMessage = null,
                paginationError = null,
            )
        }
    }

    fun saveCompactPosition(index: Int, offset: Int) {
        val safeIndex = index.coerceAtLeast(0)
        val safeOffset = offset.coerceAtLeast(0)
        savedState[COMPACT_SCROLL_INDEX_KEY] = safeIndex
        savedState[COMPACT_SCROLL_OFFSET_KEY] = safeOffset
        mutableState.value = mutableState.value.copy(
            compactScrollIndex = safeIndex,
            compactScrollOffset = safeOffset,
        )
    }

    fun setLayout(layout: LibraryLayout) {
        if (mutableState.value.layout == layout) return
        savedState[LAYOUT_KEY] = layout.name
        mutableState.value = mutableState.value.copy(layout = layout)
    }
}

private fun appendGroups(current: LibraryState, page: PagedBooks): List<BookSearchGroupDto> {
    require(page.snapshots == current.snapshots) { "Search snapshot changed during pagination" }
    val knownGroups = current.groups.mapTo(mutableSetOf(), BookSearchGroupDto::groupId)
    val additions = page.groups.filter { knownGroups.add(it.groupId) }
    val knownBooks = current.groups
        .flatMap(BookSearchGroupDto::books)
        .mapTo(mutableSetOf(), BookCardDto::id)
    require(additions.flatMap(BookSearchGroupDto::books).all { knownBooks.add(it.id) }) {
        "Search page repeats an audiobook in another group"
    }
    return current.groups + additions
}

private fun BookQuery.withoutCursor() = copy(cursor = null)

private fun String?.cleanFilter(): String? = this?.trim()?.takeIf(String::isNotEmpty)

private const val SEARCH_DEBOUNCE_MILLIS = 150L
private const val QUERY_KEY = "library.query"
private const val SELECTION_KEY = "library.selection"
private const val SCROLL_INDEX_KEY = "library.scroll.index"
private const val SCROLL_OFFSET_KEY = "library.scroll.offset"
private const val COMPACT_SCROLL_INDEX_KEY = "library.compact.scroll.index"
private const val COMPACT_SCROLL_OFFSET_KEY = "library.compact.scroll.offset"
private const val LAYOUT_KEY = "library.layout"

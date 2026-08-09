package com.rendref.bookbox.feature.folders

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.RawSearchEntryDto
import com.rendref.bookbox.core.model.RawSearchPageDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.network.SearchIndexBuilding
import com.rendref.bookbox.core.repository.BrowseSource
import com.rendref.bookbox.core.search.SearchUpdate
import com.rendref.bookbox.core.search.STALE_CATALOG_STATUS
import com.rendref.bookbox.core.search.catalogIsStale
import com.rendref.bookbox.core.search.debouncedSearchUpdates
import com.rendref.bookbox.core.search.isOlderCatalogGeneration
import java.time.Clock
import java.util.Locale
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

data class Breadcrumb(
    val label: String,
    val path: String,
)

data class FoldersState(
    val roots: List<RootDto> = emptyList(),
    val selectedRootId: String? = null,
    val path: String = "",
    val breadcrumbs: List<Breadcrumb> = emptyList(),
    val entries: List<BrowseEntryDto> = emptyList(),
    val selectedFolderPaths: Set<String> = emptySet(),
    val highlightedFileIds: Set<String> = emptySet(),
    val assignedRelativePaths: Set<String> = emptySet(),
    val originBookId: String? = null,
    val searchQuery: String = "",
    val searchResults: List<RawSearchEntryDto> = emptyList(),
    val searchSnapshot: SearchSnapshotDto? = null,
    val searchNextCursor: String? = null,
    val isSearchIndexBuilding: Boolean = false,
    val isSearchCatalogStale: Boolean = false,
    val searchCatalogStatusMessage: String? = null,
    val searchHighlightedRelativePath: String? = null,
    val isSearching: Boolean = false,
    val isSearchLoadingMore: Boolean = false,
    val searchErrorMessage: String? = null,
    val isLoading: Boolean = false,
    val isRefreshing: Boolean = false,
    val errorMessage: String? = null,
)

class FoldersViewModel(
    private val browse: BrowseSource,
    private val clock: Clock = Clock.systemUTC(),
) : ViewModel() {
    private val mutableState = MutableStateFlow(FoldersState(isLoading = true))
    val state: StateFlow<FoldersState> = mutableState.asStateFlow()
    private var navigationGeneration = 0L
    private val searchRequests = MutableStateFlow(RawSearchRequest(null, ""))
    private var searchPaginationJob: Job? = null
    private val rootsReady = CompletableDeferred<List<RootDto>>()

    init {
        viewModelScope.launch {
            searchRequests.debouncedSearchUpdates(
                debounceMillis = SEARCH_DEBOUNCE_MILLIS,
                allowBlank = false,
                text = RawSearchRequest::text,
                eligible = { it.rootId != null && hasAtLeastTwoSearchCharacters(it.text) },
                isIndexBuilding = { it is SearchIndexBuilding },
                load = { browse.searchRaw(checkNotNull(it.rootId), it.text).first() },
            ).collect(::acceptSearchUpdate)
        }
        viewModelScope.launch {
            try {
                val roots = browse.roots().first()
                rootsReady.complete(roots)
                val firstRoot = roots.firstOrNull()
                mutableState.value = mutableState.value.copy(
                    roots = roots,
                    selectedRootId = firstRoot?.id,
                    breadcrumbs = firstRoot?.let { breadcrumbs(it, "") }.orEmpty(),
                    isLoading = firstRoot != null,
                    errorMessage = null,
                )
                if (navigationGeneration == 0L) {
                    firstRoot?.let { loadDirectory(it.id, "", refreshing = false) }
                }
            } catch (failure: CancellationException) {
                throw failure
            } catch (failure: Exception) {
                rootsReady.completeExceptionally(failure)
                mutableState.value = mutableState.value.copy(
                    isLoading = false,
                    errorMessage = "Unable to load library folders",
                )
            }
        }
    }

    fun selectRoot(rootId: String) {
        if (mutableState.value.roots.none { it.id == rootId }) return
        cancelSearch()
        mutableState.value = mutableState.value.copy(
            selectedRootId = rootId,
            path = "",
            entries = emptyList(),
            selectedFolderPaths = emptySet(),
            highlightedFileIds = emptySet(),
            assignedRelativePaths = emptySet(),
            originBookId = null,
            searchQuery = "",
            searchResults = emptyList(),
            searchSnapshot = null,
            searchNextCursor = null,
            isSearching = false,
            isSearchLoadingMore = false,
            isSearchIndexBuilding = false,
            isSearchCatalogStale = false,
            searchCatalogStatusMessage = null,
            searchHighlightedRelativePath = null,
            searchErrorMessage = null,
        )
        loadDirectory(rootId, "", refreshing = false)
    }

    fun openFolder(relativePath: String) {
        val rootId = mutableState.value.selectedRootId ?: return
        if (
            relativePath.isNotEmpty() &&
            mutableState.value.entries.none {
                it.kind == "folder" && it.relativePath == relativePath
            } &&
            relativePath != parentPath(mutableState.value.path)
        ) {
            return
        }
        loadDirectory(rootId, relativePath, refreshing = false)
    }

    fun openBreadcrumb(path: String) {
        val rootId = mutableState.value.selectedRootId ?: return
        if (mutableState.value.breadcrumbs.none { it.path == path }) return
        clearSearchState()
        loadDirectory(rootId, path, refreshing = false)
    }

    fun navigateBack(): Boolean {
        if (hasSearchCharacters(mutableState.value.searchQuery)) {
            clearSearchState()
            return true
        }
        val current = mutableState.value.path
        if (current.isEmpty()) return false
        val rootId = mutableState.value.selectedRootId ?: return false
        loadDirectory(rootId, parentPath(current), refreshing = false)
        return true
    }

    fun toggleFolderSelection(relativePath: String) {
        if (
            mutableState.value.entries.none {
                it.kind == "folder" && it.relativePath == relativePath
            }
        ) {
            return
        }
        val updated = mutableState.value.selectedFolderPaths.toMutableSet().apply {
            if (!add(relativePath)) remove(relativePath)
        }.toSet()
        mutableState.value = mutableState.value.copy(selectedFolderPaths = updated)
    }

    fun refresh() {
        val rootId = mutableState.value.selectedRootId ?: return
        loadDirectory(rootId, mutableState.value.path, refreshing = true)
    }

    fun updateSearchQuery(query: String) {
        val rootId = mutableState.value.selectedRootId
        val active = hasAtLeastTwoSearchCharacters(query) && rootId != null
        cancelSearchPagination()
        mutableState.value = mutableState.value.copy(
            searchQuery = query,
            isSearching = active,
            isSearchLoadingMore = false,
            searchNextCursor = null,
            isSearchIndexBuilding = false,
            searchErrorMessage = null,
        )
        if (!active) {
            mutableState.value = mutableState.value.copy(
                searchResults = emptyList(),
                searchSnapshot = null,
                isSearchCatalogStale = false,
                searchCatalogStatusMessage = null,
                searchHighlightedRelativePath = null,
            )
        }
        searchRequests.value = RawSearchRequest(rootId, query)
    }

    fun openSearchResult(relativePath: String) {
        val current = mutableState.value
        val rootId = current.selectedRootId ?: return
        val result = current.searchResults.firstOrNull { it.relativePath == relativePath } ?: return
        clearSearchState()
        val targetPath = if (result.kind == "file") parentPath(relativePath) else relativePath
        loadDirectory(
            rootId,
            targetPath,
            refreshing = false,
            searchHighlight = relativePath.takeIf { result.kind == "file" },
        )
    }

    fun loadNextSearchPage() {
        val current = mutableState.value
        val rootId = current.selectedRootId ?: return
        val cursor = current.searchNextCursor ?: return
        val snapshot = current.searchSnapshot ?: return
        if (searchPaginationJob?.isActive == true || current.isSearching || current.isSearchLoadingMore) return
        mutableState.value = current.copy(isSearchLoadingMore = true, searchErrorMessage = null)
        lateinit var launchedJob: Job
        launchedJob = viewModelScope.launch(start = CoroutineStart.LAZY) {
            try {
                val page = browse.searchRaw(rootId, current.searchQuery, cursor).first()
                val latest = mutableState.value
                if (latest.selectedRootId != rootId || latest.searchQuery != current.searchQuery) return@launch
                require(page.snapshot == snapshot) { "Search snapshot changed during pagination" }
                val knownIDs = latest.searchResults.mapTo(mutableSetOf(), RawSearchEntryDto::id)
                val knownPaths = latest.searchResults.mapTo(mutableSetOf(), RawSearchEntryDto::relativePath)
                require(page.entries.all { knownIDs.add(it.id) && knownPaths.add(it.relativePath) }) {
                    "Search page repeats an entry"
                }
                mutableState.value = latest.copy(
                    searchResults = latest.searchResults + page.entries,
                    searchNextCursor = page.nextCursor,
                    isSearchLoadingMore = false,
                    isSearchIndexBuilding = false,
                    searchErrorMessage = null,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: SearchIndexBuilding) {
                mutableState.value = mutableState.value.copy(
                    isSearchLoadingMore = false,
                    isSearchIndexBuilding = true,
                    searchErrorMessage = null,
                )
            } catch (_: Exception) {
                mutableState.value = mutableState.value.copy(
                    isSearchLoadingMore = false,
                    searchErrorMessage = "Unable to load more search results",
                )
            } finally {
                if (searchPaginationJob === launchedJob) searchPaginationJob = null
            }
        }
        searchPaginationJob = launchedJob
        launchedJob.start()
    }

    fun viewRawFiles(bookId: String) {
        clearSearchState()
        val generation = ++navigationGeneration
        mutableState.value = mutableState.value.copy(
            isLoading = true,
            isRefreshing = false,
            errorMessage = null,
        )
        viewModelScope.launch {
            try {
                val bookFiles = browse.bookFiles(bookId).first()
                val roots = rootsReady.await()
                check(roots.any { it.id == bookFiles.rootId })
                val directory = browse.list(bookFiles.rootId, bookFiles.sourceFolder).first()
                if (generation != navigationGeneration) return@launch
                mutableState.value = mutableState.value.copy(
                    roots = roots,
                    selectedRootId = bookFiles.rootId,
                    path = directory.path,
                    breadcrumbs = breadcrumbsFor(bookFiles.rootId, directory.path),
                    entries = sortEntries(directory.entries),
                    highlightedFileIds = bookFiles.files.mapTo(
                        linkedSetOf(),
                    ) { it.id },
                    assignedRelativePaths = bookFiles.files.mapTo(
                        linkedSetOf(),
                    ) { it.relativePath },
                    originBookId = bookId,
                    isLoading = false,
                    isRefreshing = false,
                    errorMessage = null,
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                if (generation != navigationGeneration) return@launch
                mutableState.value = mutableState.value.copy(
                    isLoading = false,
                    isRefreshing = false,
                    errorMessage = "Unable to open this audiobook's files",
                )
            }
        }
    }

    private fun loadDirectory(
        rootId: String,
        path: String,
        refreshing: Boolean,
        searchHighlight: String? = null,
    ) {
        val generation = ++navigationGeneration
        mutableState.value = mutableState.value.copy(
            isLoading = !refreshing,
            isRefreshing = refreshing,
            errorMessage = null,
        )
        viewModelScope.launch {
            try {
                val directory = browse.list(rootId, path).first()
                if (generation != navigationGeneration) return@launch
                mutableState.value = mutableState.value.copy(
                    selectedRootId = rootId,
                    path = directory.path,
                    breadcrumbs = breadcrumbsFor(rootId, directory.path),
                    entries = sortEntries(directory.entries),
                    isLoading = false,
                    isRefreshing = false,
                    errorMessage = null,
                    searchHighlightedRelativePath = searchHighlight?.takeIf { highlighted ->
                        directory.entries.any {
                            it.kind == "file" && it.relativePath == highlighted
                        }
                    },
                )
            } catch (failure: CancellationException) {
                throw failure
            } catch (_: Exception) {
                if (generation != navigationGeneration) return@launch
                mutableState.value = mutableState.value.copy(
                    isLoading = false,
                    isRefreshing = false,
                    errorMessage = "Unable to load this folder",
                )
            }
        }
    }

    private fun breadcrumbsFor(rootId: String, path: String): List<Breadcrumb> {
        val root = mutableState.value.roots.firstOrNull { it.id == rootId }
            ?: RootDto(rootId, rootId)
        return breadcrumbs(root, path)
    }

    private fun clearSearchState() {
        cancelSearchPagination()
        searchRequests.value = RawSearchRequest(mutableState.value.selectedRootId, "")
        mutableState.value = mutableState.value.copy(
            searchQuery = "",
            searchResults = emptyList(),
            isSearching = false,
            isSearchLoadingMore = false,
            searchSnapshot = null,
            isSearchCatalogStale = false,
            searchCatalogStatusMessage = null,
            searchNextCursor = null,
            isSearchIndexBuilding = false,
            searchHighlightedRelativePath = null,
            searchErrorMessage = null,
        )
    }

    private fun cancelSearch() {
        cancelSearchPagination()
        searchRequests.value = RawSearchRequest(mutableState.value.selectedRootId, "")
    }

    private fun cancelSearchPagination() {
        searchPaginationJob?.cancel()
        searchPaginationJob = null
    }

    private fun acceptSearchUpdate(update: SearchUpdate<RawSearchRequest, RawSearchPageDto>) {
        when (update) {
            is SearchUpdate.Loading -> mutableState.value = mutableState.value.copy(
                isSearching = true,
                isSearchLoadingMore = false,
                isSearchIndexBuilding = false,
                searchErrorMessage = null,
            )
            is SearchUpdate.Loaded -> {
                val current = mutableState.value
                if (
                    isOlderCatalogGeneration(
                        listOf(update.page.snapshot),
                        listOfNotNull(current.searchSnapshot),
                    )
                ) {
                    mutableState.value = current.copy(
                        isSearching = false,
                        isSearchLoadingMore = false,
                        isSearchIndexBuilding = false,
                        searchErrorMessage = null,
                    )
                    return
                }
                val catalogStale = catalogIsStale(listOf(update.page.snapshot), clock)
                mutableState.value = current.copy(
                    searchResults = update.page.entries,
                    searchSnapshot = update.page.snapshot,
                    searchNextCursor = update.page.nextCursor,
                    isSearching = false,
                    isSearchLoadingMore = false,
                    isSearchIndexBuilding = false,
                    isSearchCatalogStale = catalogStale,
                    searchCatalogStatusMessage = STALE_CATALOG_STATUS.takeIf { catalogStale },
                    searchErrorMessage = null,
                )
            }
            is SearchUpdate.IndexBuilding -> mutableState.value = mutableState.value.copy(
                isSearching = false,
                isSearchLoadingMore = false,
                isSearchIndexBuilding = true,
                searchErrorMessage = null,
            )
            is SearchUpdate.Failed -> mutableState.value = mutableState.value.copy(
                isSearching = false,
                isSearchLoadingMore = false,
                isSearchIndexBuilding = false,
                searchErrorMessage = "Unable to search library folders",
            )
            is SearchUpdate.Inactive -> mutableState.value = mutableState.value.copy(
                isSearching = false,
                isSearchLoadingMore = false,
                isSearchIndexBuilding = false,
                searchErrorMessage = null,
            )
        }
    }
}

private data class RawSearchRequest(val rootId: String?, val text: String)

private fun hasAtLeastTwoSearchCharacters(query: CharSequence): Boolean {
    var count = 0
    var index = 0
    while (index < query.length) {
        val codePoint = Character.codePointAt(query, index)
        if (Character.isLetterOrDigit(codePoint) && ++count >= 2) return true
        index += Character.charCount(codePoint)
    }
    return false
}

private fun breadcrumbs(root: RootDto, path: String): List<Breadcrumb> {
    val result = mutableListOf(Breadcrumb(root.name, ""))
    var current = ""
    path.split('/').filter(String::isNotEmpty).forEach { segment ->
        current = if (current.isEmpty()) segment else "$current/$segment"
        result += Breadcrumb(segment, current)
    }
    return result
}

private fun sortEntries(entries: List<BrowseEntryDto>): List<BrowseEntryDto> =
    entries.withIndex()
        .sortedWith(
            compareBy<IndexedValue<BrowseEntryDto>>(
                { if (it.value.kind == "folder") 0 else 1 },
                { it.value.name.lowercase(Locale.ROOT) },
                { it.index },
            ),
        )
        .map(IndexedValue<BrowseEntryDto>::value)

private fun parentPath(path: String): String = path.substringBeforeLast('/', "")

private const val SEARCH_DEBOUNCE_MILLIS = 150L

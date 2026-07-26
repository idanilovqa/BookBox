package com.rendref.netstorage.feature.folders

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rendref.netstorage.core.model.BrowseEntryDto
import com.rendref.netstorage.core.model.RootDto
import com.rendref.netstorage.core.repository.BrowseSource
import java.util.Locale
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
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
    val isLoading: Boolean = false,
    val isRefreshing: Boolean = false,
    val errorMessage: String? = null,
)

class FoldersViewModel(
    private val browse: BrowseSource,
) : ViewModel() {
    private val mutableState = MutableStateFlow(FoldersState(isLoading = true))
    val state: StateFlow<FoldersState> = mutableState.asStateFlow()
    private var navigationGeneration = 0L
    private val rootsReady = CompletableDeferred<List<RootDto>>()

    init {
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
        mutableState.value = mutableState.value.copy(
            selectedRootId = rootId,
            path = "",
            entries = emptyList(),
            selectedFolderPaths = emptySet(),
            highlightedFileIds = emptySet(),
            assignedRelativePaths = emptySet(),
            originBookId = null,
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
        loadDirectory(rootId, path, refreshing = false)
    }

    fun navigateBack(): Boolean {
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

    fun viewRawFiles(bookId: String) {
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

package com.rendref.bookbox.feature.folders

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.RawSearchEntryDto
import com.rendref.bookbox.core.search.LibrarySearchField
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import java.util.Locale

sealed interface FolderAction {
    data class SelectRoot(val rootId: String) : FolderAction

    data class OpenBreadcrumb(val path: String) : FolderAction

    data class OpenFolder(val relativePath: String) : FolderAction

    data class ToggleFolder(val relativePath: String) : FolderAction

    data class UpdateSearchQuery(val query: String) : FolderAction

    data class OpenSearchResult(val relativePath: String) : FolderAction
	data object LoadMoreSearchResults : FolderAction

    data object NavigateBack : FolderAction

    data object Refresh : FolderAction

    data class ReturnToBook(val bookId: String) : FolderAction
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FoldersScreen(
    state: FoldersState,
    onAction: (FolderAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    var rootsExpanded by remember { mutableStateOf(false) }
	val searchActive = state.searchQuery.isNotEmpty()
    BackHandler(enabled = searchActive || state.path.isNotEmpty()) {
        onAction(FolderAction.NavigateBack)
    }

    Column(
        modifier = modifier
            .fillMaxSize()
            .padding(horizontal = 16.dp),
    ) {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(top = 14.dp, bottom = 8.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column {
                Text(
                    text = "RAW FOLDERS",
                    color = MaterialTheme.colorScheme.primary,
                    style = MaterialTheme.typography.labelLarge,
                    fontWeight = FontWeight.Bold,
                )
                Text(
                    text = "Browse source files",
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.SemiBold,
                )
            }
            TextButton(
                onClick = { onAction(FolderAction.Refresh) },
                modifier = Modifier.semantics {
                    contentDescription = "Refresh folders"
                },
            ) {
                Text("Refresh")
            }
        }

        Row(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column {
                OutlinedButton(
                    onClick = { rootsExpanded = true },
                    enabled = state.roots.isNotEmpty(),
                ) {
                    Text(
                        state.roots.firstOrNull {
                            it.id == state.selectedRootId
                        }?.name ?: "Choose library",
                    )
                }
                DropdownMenu(
                    expanded = rootsExpanded,
                    onDismissRequest = { rootsExpanded = false },
                ) {
                    state.roots.forEach { root ->
                        DropdownMenuItem(
                            text = { Text(root.name) },
                            onClick = {
                                rootsExpanded = false
                                onAction(FolderAction.SelectRoot(root.id))
                            },
                        )
                    }
                }
            }
            state.originBookId?.let { bookId ->
                TextButton(
                    onClick = { onAction(FolderAction.ReturnToBook(bookId)) },
                ) {
                    Text("Return to audiobook")
                }
            }
        }

		LibrarySearchField(
            value = state.searchQuery,
			label = "Search raw folders",
			isLoading = state.isSearching || state.isSearchLoadingMore,
			onValueChange = { onAction(FolderAction.UpdateSearchQuery(it)) },
            modifier = Modifier
                .fillMaxWidth()
                .padding(top = 8.dp),
        )

        Breadcrumbs(
            breadcrumbs = state.breadcrumbs,
            onOpen = { onAction(FolderAction.OpenBreadcrumb(it)) },
        )
        state.errorMessage?.let { error ->
            Text(
                text = error,
                modifier = Modifier.padding(vertical = 8.dp),
                color = MaterialTheme.colorScheme.error,
            )
        }

        if (searchActive) {
            FolderSearchResults(
                state = state,
                onAction = onAction,
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(1f),
            )
        } else {
            PullToRefreshBox(
                isRefreshing = state.isRefreshing,
                onRefresh = { onAction(FolderAction.Refresh) },
                modifier = Modifier
                    .fillMaxWidth()
                    .weight(1f),
            ) {
                when {
                    state.isLoading && state.entries.isEmpty() -> {
                        CircularProgressIndicator(
                            modifier = Modifier
                                .align(Alignment.TopCenter)
                                .padding(32.dp),
                        )
                    }

                    state.entries.isEmpty() -> {
                        Text(
                            text = "This folder is empty.",
                            modifier = Modifier
                                .align(Alignment.TopCenter)
                                .padding(32.dp),
                            color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.68f),
                        )
                    }

                    else -> {
                        LazyColumn(
                            verticalArrangement = Arrangement.spacedBy(8.dp),
                        ) {
                            items(
                                items = state.entries,
                                key = BrowseEntryDto::relativePath,
                            ) { entry ->
                                FolderEntryRow(
                                    entry = entry,
                                    selected = entry.relativePath in state.selectedFolderPaths,
                                    assigned = entry.relativePath in state.assignedRelativePaths,
									highlighted = entry.relativePath == state.searchHighlightedRelativePath,
                                    onAction = onAction,
                                )
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun FolderSearchResults(
    state: FoldersState,
    onAction: (FolderAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier,
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
		if (state.searchResults.isNotEmpty()) {
			when {
				state.isSearchIndexBuilding -> Text("Search index is still building.", color = MaterialTheme.colorScheme.primary)
				state.searchErrorMessage != null -> Text(state.searchErrorMessage, color = MaterialTheme.colorScheme.error)
				state.isSearching -> Text("Updating search results...", color = MaterialTheme.colorScheme.onSurfaceVariant)
			}
		}
        when {
			!hasAtLeastTwoSearchCharacters(state.searchQuery) && state.searchResults.isEmpty() -> Text(
				text = "Enter at least 2 letters or numbers",
				modifier = Modifier.padding(24.dp),
			)

			state.isSearchIndexBuilding && state.searchResults.isEmpty() -> Text(
				text = "Search index is still building.",
				modifier = Modifier.padding(24.dp),
				color = MaterialTheme.colorScheme.primary,
			)

			state.isSearching && state.searchResults.isEmpty() -> {
                CircularProgressIndicator(
                    modifier = Modifier
                        .align(Alignment.CenterHorizontally)
                        .padding(24.dp)
                        .semantics {
                            contentDescription = "Searching folders"
                        },
                )
            }

			state.searchErrorMessage != null && state.searchResults.isEmpty() -> {
                Text(
                    text = state.searchErrorMessage,
                    modifier = Modifier.padding(24.dp),
                    color = MaterialTheme.colorScheme.error,
                )
            }

			state.searchResults.isEmpty() -> {
                Text(
				text = "No matching folders or files.",
                    modifier = Modifier
                        .align(Alignment.CenterHorizontally)
                        .padding(24.dp),
                    color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.68f),
                )
            }

            else -> {
                LazyColumn(
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    items(
                        items = state.searchResults,
						key = RawSearchEntryDto::id,
                    ) { entry ->
                        FolderSearchResultRow(
                            entry = entry,
						highlighted = entry.relativePath == state.searchHighlightedRelativePath,
                            onOpen = {
                                onAction(
                                    FolderAction.OpenSearchResult(entry.relativePath),
                                )
                            },
                        )
                    }
					if (state.searchNextCursor != null) {
						item {
							TextButton(onClick = { onAction(FolderAction.LoadMoreSearchResults) }, enabled = !state.isSearchLoadingMore) {
								Text(if (state.isSearchLoadingMore) "Loading..." else "Load more")
							}
						}
					}
                }
            }
        }
    }
}

@Composable
private fun FolderSearchResultRow(
	entry: RawSearchEntryDto,
	highlighted: Boolean,
    onOpen: () -> Unit,
) {
    Surface(
        modifier = Modifier
            .fillMaxWidth()
			.clickable(onClick = onOpen)
			.semantics { if (highlighted) contentDescription = "Highlighted search result" },
		shape = RoundedCornerShape(14.dp),
		color = if (highlighted) MaterialTheme.colorScheme.primaryContainer else MaterialTheme.colorScheme.surface,
        tonalElevation = 1.dp,
    ) {
        Column(
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(3.dp),
        ) {
			Text(if (entry.kind == "folder") "FOLDER" else "AUDIO FILE", style = MaterialTheme.typography.labelSmall, color = MaterialTheme.colorScheme.primary)
            Text(
                text = entry.name,
                style = MaterialTheme.typography.bodyLarge,
                fontWeight = FontWeight.Medium,
            )
            Text(
                text = entry.relativePath,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.62f),
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
			if (entry.kind == "file" && entry.size != null) {
				Text(formatBytes(entry.size), style = MaterialTheme.typography.bodySmall)
			}
			if (entry.duplicateBook) {
				Text("Duplicate book", style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.primary)
			}
        }
    }
}

@Composable
private fun Breadcrumbs(
    breadcrumbs: List<Breadcrumb>,
    onOpen: (String) -> Unit,
) {
    LazyRow(
        modifier = Modifier
            .fillMaxWidth()
            .padding(vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        items(
            items = breadcrumbs,
            key = Breadcrumb::path,
        ) { breadcrumb ->
            TextButton(onClick = { onOpen(breadcrumb.path) }) {
                Text(
                    text = breadcrumb.label,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
            if (breadcrumb != breadcrumbs.lastOrNull()) {
                Text("›", color = MaterialTheme.colorScheme.outline)
            }
        }
    }
}

@Composable
private fun FolderEntryRow(
    entry: BrowseEntryDto,
    selected: Boolean,
    assigned: Boolean,
	highlighted: Boolean,
    onAction: (FolderAction) -> Unit,
) {
    val isFolder = entry.kind == "folder"
    Surface(
        modifier = Modifier
            .fillMaxWidth()
			.semantics { if (highlighted) contentDescription = "Highlighted search result" }
            .then(
                if (isFolder) {
                    Modifier.clickable {
                        onAction(FolderAction.OpenFolder(entry.relativePath))
                    }
                } else {
                    Modifier
                },
            ),
        shape = RoundedCornerShape(14.dp),
        color = if (assigned || highlighted) {
            MaterialTheme.colorScheme.primaryContainer
        } else {
            MaterialTheme.colorScheme.surface
        },
        tonalElevation = 1.dp,
    ) {
        Row(
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(
                text = if (isFolder) "FOLDER" else "FILE",
                color = MaterialTheme.colorScheme.primary,
                style = MaterialTheme.typography.labelSmall,
                fontWeight = FontWeight.Bold,
            )
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    text = entry.name,
                    style = MaterialTheme.typography.bodyLarge,
                    fontWeight = FontWeight.Medium,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
					if (!isFolder && entry.size != null) {
                        Text(
							text = formatBytes(entry.size),
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.62f),
                        )
                    }
                    Text(
                        text = formatModified(entry.modifiedAt),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.62f),
                    )
                }
            }
            if (assigned) {
                Text(
                    text = "This book",
                    color = MaterialTheme.colorScheme.primary,
                    style = MaterialTheme.typography.labelLarge,
                    fontWeight = FontWeight.Bold,
                )
            }
			if (highlighted) {
				Text("Opened file", color = MaterialTheme.colorScheme.primary, style = MaterialTheme.typography.labelLarge)
			}
            if (isFolder) {
                Checkbox(
                    checked = selected,
                    onCheckedChange = {
                        onAction(FolderAction.ToggleFolder(entry.relativePath))
                    },
                    modifier = Modifier.semantics {
                        contentDescription = "Select folder ${entry.name}"
                    },
                )
            }
        }
    }
}

private fun formatBytes(bytes: Long): String = when {
    bytes < 1_024 -> "$bytes B"
    bytes < 1_048_576 -> String.format(Locale.ROOT, "%.1f KB", bytes / 1_024.0)
    bytes < 1_073_741_824 -> String.format(
        Locale.ROOT,
        "%.1f MB",
        bytes / 1_048_576.0,
    )
    else -> String.format(Locale.ROOT, "%.1f GB", bytes / 1_073_741_824.0)
}

private fun formatModified(value: String): String =
    runCatching {
        DateTimeFormatter
            .ofLocalizedDate(FormatStyle.MEDIUM)
            .withLocale(Locale.getDefault())
            .withZone(ZoneId.systemDefault())
            .format(Instant.parse(value))
    }.getOrDefault("Date unavailable")

private fun hasAtLeastTwoSearchCharacters(value: String): Boolean =
	value.codePoints().filter { Character.isLetterOrDigit(it) }.limit(2).count() >= 2

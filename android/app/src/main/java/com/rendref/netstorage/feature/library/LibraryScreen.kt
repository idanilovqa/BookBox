package com.rendref.netstorage.feature.library

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.rememberLazyGridState
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.rendref.netstorage.core.repository.BookSort
import kotlinx.coroutines.flow.distinctUntilChanged

@Composable
@OptIn(ExperimentalMaterial3Api::class)
fun LibraryScreen(
    state: LibraryState,
    onSearchTextChange: (String) -> Unit,
    onAction: (LibraryAction) -> Unit,
    onAuthorChange: (String?) -> Unit,
    onGenreChange: (String?) -> Unit,
    onYearChange: (Int?) -> Unit,
    onRootChange: (String?) -> Unit,
    onReviewStateChange: (String?) -> Unit,
    onSortChange: (BookSort) -> Unit,
    onLoadNextPage: () -> Unit,
    onGridPositionChange: (index: Int, offset: Int) -> Unit,
    modifier: Modifier = Modifier,
) {
    var filtersVisible by remember { mutableStateOf(false) }
    var sortVisible by remember { mutableStateOf(false) }
    val gridState = rememberLazyGridState(
        initialFirstVisibleItemIndex = state.scrollIndex,
        initialFirstVisibleItemScrollOffset = state.scrollOffset,
    )
    val nearEnd by remember {
        derivedStateOf {
            val lastVisible = gridState.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: 0
            val total = gridState.layoutInfo.totalItemsCount
            total > 0 && lastVisible >= total - 3
        }
    }

    LaunchedEffect(gridState) {
        snapshotFlow {
            gridState.firstVisibleItemIndex to gridState.firstVisibleItemScrollOffset
        }.distinctUntilChanged().collect { (index, offset) ->
            onGridPositionChange(index, offset)
        }
    }
    LaunchedEffect(nearEnd, state.nextCursor, state.isLoadingMore) {
        if (nearEnd && state.nextCursor != null && !state.isLoadingMore) {
            onLoadNextPage()
        }
    }

    Column(
        modifier = modifier
            .fillMaxSize()
            .padding(horizontal = 16.dp),
    ) {
        Text(
            text = "AUDIOBOOK CATALOG",
            modifier = Modifier.padding(top = 16.dp),
            color = MaterialTheme.colorScheme.primary,
            style = MaterialTheme.typography.labelLarge,
            fontWeight = FontWeight.Bold,
        )
        Text(
            text = "Choose your next listen",
            modifier = Modifier.padding(top = 4.dp, bottom = 12.dp),
            style = MaterialTheme.typography.headlineMedium,
            fontWeight = FontWeight.SemiBold,
        )
        OutlinedTextField(
            value = state.query.text,
            onValueChange = onSearchTextChange,
            modifier = Modifier.fillMaxWidth(),
            label = { Text("Search audiobooks") },
            singleLine = true,
        )
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .padding(vertical = 10.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            OutlinedButton(onClick = { filtersVisible = true }) {
                Text("Filters")
            }
            Column {
                OutlinedButton(onClick = { sortVisible = true }) {
                    Text("Sort")
                }
                DropdownMenu(
                    expanded = sortVisible,
                    onDismissRequest = { sortVisible = false },
                ) {
                    BookSort.entries.forEach { sort ->
                        DropdownMenuItem(
                            text = { Text(sort.displayLabel()) },
                            onClick = {
                                sortVisible = false
                                onSortChange(sort)
                            },
                        )
                    }
                }
            }
            if (state.selectedBookIds.isNotEmpty()) {
                Text(
                    text = "${state.selectedBookIds.size} selected",
                    color = MaterialTheme.colorScheme.primary,
                    style = MaterialTheme.typography.labelLarge,
                )
            }
        }

        state.errorMessage?.let { message ->
            Text(
                text = message,
                modifier = Modifier.padding(bottom = 8.dp),
                color = MaterialTheme.colorScheme.error,
                style = MaterialTheme.typography.bodyMedium,
            )
        }

        LazyVerticalGrid(
            columns = GridCells.Adaptive(160.dp),
            modifier = Modifier.weight(1f),
            state = gridState,
            contentPadding = PaddingValues(bottom = 24.dp),
            horizontalArrangement = Arrangement.spacedBy(12.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            if (state.isLoading && state.books.isEmpty()) {
                item(span = { GridItemSpan(maxLineSpan) }) {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(32.dp),
                        horizontalArrangement = Arrangement.Center,
                    ) {
                        CircularProgressIndicator()
                    }
                }
            } else if (state.books.isEmpty()) {
                item(span = { GridItemSpan(maxLineSpan) }) {
                    Text(
                        text = "No audiobooks match these filters.",
                        modifier = Modifier.padding(vertical = 32.dp),
                        color = MaterialTheme.colorScheme.onBackground.copy(alpha = 0.7f),
                    )
                }
            }
            items(
                count = state.books.size,
                key = { index -> state.books[index].id },
            ) { index ->
                val book = state.books[index]
                AudiobookCard(
                    book = book,
                    selected = book.id in state.selectedBookIds,
                    onAction = onAction,
                )
            }
            if (state.isLoadingMore) {
                item(span = { GridItemSpan(maxLineSpan) }) {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(20.dp),
                        horizontalArrangement = Arrangement.Center,
                    ) {
                        CircularProgressIndicator()
                    }
                }
            }
        }
    }

    if (filtersVisible) {
        FilterSheet(
            state = state,
            onAuthorChange = onAuthorChange,
            onGenreChange = onGenreChange,
            onYearChange = onYearChange,
            onRootChange = onRootChange,
            onReviewStateChange = onReviewStateChange,
            onDismiss = { filtersVisible = false },
        )
    }
}

@Composable
@OptIn(ExperimentalMaterial3Api::class)
private fun FilterSheet(
    state: LibraryState,
    onAuthorChange: (String?) -> Unit,
    onGenreChange: (String?) -> Unit,
    onYearChange: (Int?) -> Unit,
    onRootChange: (String?) -> Unit,
    onReviewStateChange: (String?) -> Unit,
    onDismiss: () -> Unit,
) {
    ModalBottomSheet(onDismissRequest = onDismiss) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(horizontal = 20.dp, vertical = 8.dp),
            verticalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            Text(
                text = "Refine your library",
                style = MaterialTheme.typography.headlineSmall,
                fontWeight = FontWeight.SemiBold,
            )
            FilterSection(
                label = "Author",
                choices = state.filters.authors,
                selected = state.query.author,
                onChoice = onAuthorChange,
            )
            FilterSection(
                label = "Genre",
                choices = state.filters.genres,
                selected = state.query.genre,
                onChoice = onGenreChange,
            )
            FilterSection(
                label = "Year",
                choices = state.filters.years.map(Int::toString),
                selected = state.query.year?.toString(),
                onChoice = { onYearChange(it?.toIntOrNull()) },
            )
            FilterSection(
                label = "Library root",
                choices = state.filters.roots,
                selected = state.query.rootId,
                onChoice = onRootChange,
            )
            FilterSection(
                label = "Review state",
                choices = state.filters.reviewStates,
                selected = state.query.reviewState,
                choiceLabel = String::displayLabel,
                onChoice = onReviewStateChange,
            )
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.End,
            ) {
                TextButton(onClick = {
                    onAuthorChange(null)
                    onGenreChange(null)
                    onYearChange(null)
                    onRootChange(null)
                    onReviewStateChange(null)
                }) {
                    Text("Clear filters")
                }
                Button(onClick = onDismiss) {
                    Text("Done")
                }
            }
        }
    }
}

@Composable
private fun FilterSection(
    label: String,
    choices: List<String>,
    selected: String?,
    choiceLabel: (String) -> String = { it },
    onChoice: (String?) -> Unit,
) {
    if (choices.isEmpty()) return
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(
            text = label,
            style = MaterialTheme.typography.labelLarge,
            fontWeight = FontWeight.Bold,
        )
        LazyRow(
            modifier = Modifier.fillMaxWidth(),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            items(choices, key = { it }) { choice ->
                FilterChip(
                    selected = selected == choice,
                    onClick = {
                        onChoice(choice.takeUnless { selected == choice })
                    },
                    label = { Text(choiceLabel(choice)) },
                )
            }
        }
    }
}

private fun BookSort.displayLabel(): String = when (this) {
    BookSort.TITLE -> "Title"
    BookSort.AUTHOR -> "Author"
    BookSort.YEAR -> "Year"
    BookSort.RECENTLY_ADDED -> "Recently added"
    BookSort.RECENTLY_SCANNED -> "Recently scanned"
}

private fun String.displayLabel(): String =
    replace('_', ' ')
        .replaceFirstChar { character ->
            if (character.isLowerCase()) character.titlecase() else character.toString()
        }

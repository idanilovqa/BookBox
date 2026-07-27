package com.rendref.netstorage.feature.book

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import coil3.compose.AsyncImage

@Composable
fun MatchPickerScreen(
    state: BookReviewState,
    onAction: (BookReviewAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    val providerCoverImageLoader = rememberProviderCoverImageLoader()
    LazyColumn(
        modifier = modifier
            .testTag("match-picker-list")
            .fillMaxSize()
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        item {
            Text(
                "Choose metadata match",
                modifier = Modifier.padding(top = 16.dp),
                style = MaterialTheme.typography.headlineSmall,
                fontWeight = FontWeight.SemiBold,
            )
        }
        item {
            OutlinedTextField(
                value = state.searchTitle,
                onValueChange = {
                    onAction(
                        BookReviewAction.UpdateSearch(
                            it,
                            state.searchAuthor,
                            state.searchIsbn,
                        ),
                    )
                },
                label = { Text("Title") },
                modifier = Modifier.fillMaxWidth(),
            )
        }
        item {
            OutlinedTextField(
                value = state.searchAuthor,
                onValueChange = {
                    onAction(
                        BookReviewAction.UpdateSearch(
                            state.searchTitle,
                            it,
                            state.searchIsbn,
                        ),
                    )
                },
                label = { Text("Author") },
                modifier = Modifier.fillMaxWidth(),
            )
        }
        item {
            Button(
                onClick = { onAction(BookReviewAction.SearchMatches) },
                enabled = !state.isLoading,
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text("Search providers")
            }
        }
        item {
            ReviewStatusMessages(
                state = state,
                loadingLabel = "Searching metadata providers...",
                savingLabel = "Confirming metadata and cover...",
            )
        }
        items(state.matches, key = { it.matchId }) { candidate ->
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(
                    modifier = Modifier.padding(14.dp),
                    verticalArrangement = Arrangement.spacedBy(5.dp),
                ) {
                    AsyncImage(
                        model = candidate.coverUrl,
                        imageLoader = providerCoverImageLoader,
                        contentDescription = "Candidate cover for ${candidate.title}",
                        modifier = Modifier
                            .fillMaxWidth()
                            .height(120.dp),
                    )
                    Text(
                        candidate.provider.displayLabel(),
                        color = MaterialTheme.colorScheme.primary,
                        fontWeight = FontWeight.Bold,
                    )
                    Text(candidate.title, style = MaterialTheme.typography.titleLarge)
                    Text(candidate.authors.joinToString())
                    candidate.publishedDate?.let { Text(it) }
                    candidate.subjects.forEach { Text(it) }
                    Text("${(candidate.score * 100).toInt()}% match")
                    Button(
                        onClick = {
                            onAction(BookReviewAction.RequestMatch(candidate.matchId))
                        },
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Text("Use this match")
                    }
                }
            }
        }
    }

    if (state.pendingMatchId != null) {
        AlertDialog(
            onDismissRequest = {
                onAction(BookReviewAction.CancelMatchConfirmation)
            },
            title = { Text("Confirm metadata match") },
            text = {
                Text(
                    "The server may publish this cover in the Windows book folder. " +
                        "Audio files and tags will not change.",
                )
            },
            confirmButton = {
                Button(
                    onClick = {
                        onAction(BookReviewAction.ConfirmCoverPublication)
                    },
                    enabled = !state.isSaving,
                ) {
                    Text("Confirm metadata and cover")
                }
            },
            dismissButton = {
                TextButton(
                    onClick = {
                        onAction(BookReviewAction.CancelMatchConfirmation)
                    },
                ) {
                    Text("Cancel")
                }
            },
        )
    }
}

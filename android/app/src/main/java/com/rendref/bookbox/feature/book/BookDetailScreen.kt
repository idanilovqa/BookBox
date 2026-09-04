package com.rendref.bookbox.feature.book

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import coil3.compose.AsyncImage

sealed interface BookReviewAction {
    data class ViewRawFiles(val bookId: String) : BookReviewAction

    data class OpenSeries(val series: String) : BookReviewAction

    data class OpenAuthor(val author: String) : BookReviewAction

    data object ChooseMetadata : BookReviewAction

    data object ReviewGrouping : BookReviewAction

    data object ShowDetail : BookReviewAction

    data class UpdateSearch(
        val title: String,
        val author: String,
        val isbn: String,
    ) : BookReviewAction

    data object SearchMatches : BookReviewAction

    data class RequestMatch(val matchId: String) : BookReviewAction

    data object CancelMatchConfirmation : BookReviewAction

    data object ConfirmCoverPublication : BookReviewAction

    data object AddGroup : BookReviewAction

    data class AssignFile(val fileId: String, val groupId: Int) : BookReviewAction

    data class ToggleCompanion(val fileId: String, val groupId: Int) : BookReviewAction

    data class MergeGroups(val sourceId: Int, val targetId: Int) : BookReviewAction

    data class UpdateTargetFolder(val groupId: Int, val targetFolder: String) : BookReviewAction

    data object PreviewReorganization : BookReviewAction

    data object ConfirmReorganization : BookReviewAction

    data object CancelReorganization : BookReviewAction

    data object SaveGrouping : BookReviewAction
}

@Composable
fun BookDetailScreen(
    state: BookReviewState,
    onAction: (BookReviewAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    val detail = state.detail
    if (detail == null) {
        Column(
            modifier = modifier
                .fillMaxSize()
                .padding(18.dp),
        ) {
            ReviewStatusMessages(
                state = state,
                loadingLabel = "Loading audiobook details...",
                savingLabel = "Saving audiobook changes...",
            )
        }
        return
    }
    LazyColumn(
        modifier = modifier
            .fillMaxSize()
            .padding(horizontal = 18.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        item {
            ReviewStatusMessages(
                state = state,
                loadingLabel = "Loading audiobook details...",
                savingLabel = "Saving audiobook changes...",
            )
        }
        item {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(top = 16.dp),
                horizontalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                AsyncImage(
                    model = detail.coverUrl,
                    contentDescription = "Cover of ${detail.title}",
                    modifier = Modifier
                        .width(112.dp)
                        .height(164.dp),
                )
                Column(verticalArrangement = Arrangement.spacedBy(5.dp)) {
                    Text(
                        detail.reviewState.displayLabel(),
                        color = MaterialTheme.colorScheme.primary,
                        style = MaterialTheme.typography.labelLarge,
                        fontWeight = FontWeight.Bold,
                    )
                    Text(
                        detail.title,
                        style = MaterialTheme.typography.headlineMedium,
                        fontWeight = FontWeight.SemiBold,
                    )
                    detail.subtitle?.let { Text(it) }
                    if (detail.authors.isEmpty()) {
                        Text("Unknown author")
                    } else {
                        FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            detail.authors.forEach { author ->
                                TextButton(
                                    onClick = { onAction(BookReviewAction.OpenAuthor(author)) },
                                ) {
                                    Text(
                                        text = author,
                                        textDecoration = TextDecoration.Underline,
                                    )
                                }
                            }
                        }
                    }
                }
            }
        }
        detail.series?.let { series ->
            item {
                TextButton(onClick = { onAction(BookReviewAction.OpenSeries(series)) }) {
                    Text(
                        listOfNotNull(series, detail.seriesPosition).joinToString(" · "),
                        fontWeight = FontWeight.Medium,
                    )
                }
            }
        }
        if (detail.narrators.isNotEmpty()) {
            item { Text("Narrated by ${detail.narrators.joinToString()}") }
        }
        detail.publisher?.let { item { Text(it) } }
        (detail.publishedDate ?: detail.publicationYear?.toString())?.let {
            item { Text(it) }
        }
        items(detail.genres) { Text(it) }
        items(detail.identifiers.toList()) { (key, value) ->
            Text("$key: $value")
        }
        detail.attribution?.let { attribution ->
            item {
                Text(
                    attribution.displayName ?: attribution.provider.displayLabel(),
                    color = MaterialTheme.colorScheme.primary,
                )
            }
        }
        item {
            Text(
                "Assigned files",
                style = MaterialTheme.typography.titleMedium,
                fontWeight = FontWeight.Bold,
            )
        }
        items(state.files, key = { it.id }) { file ->
            Card(modifier = Modifier.fillMaxWidth()) {
                Text(file.name, modifier = Modifier.padding(12.dp))
            }
        }
        item {
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(bottom = 24.dp),
            ) {
                Button(
                    onClick = {
                        onAction(BookReviewAction.ViewRawFiles(detail.id))
                    },
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("View raw files")
                }
                TextButton(
                    onClick = { onAction(BookReviewAction.ChooseMetadata) },
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("Choose metadata match")
                }
                TextButton(
                    onClick = { onAction(BookReviewAction.ReviewGrouping) },
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text("Review grouping")
                }
            }
        }
    }
}

@Composable
internal fun ReviewStatusMessages(
    state: BookReviewState,
    loadingLabel: String,
    savingLabel: String,
) {
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        if (state.isLoading) {
            Text(loadingLabel, color = MaterialTheme.colorScheme.primary)
        }
        if (state.isSaving) {
            Text(savingLabel, color = MaterialTheme.colorScheme.primary)
        }
        state.errorMessage?.let {
            Text(it, color = MaterialTheme.colorScheme.error)
        }
        state.message?.let {
            Text(it, color = MaterialTheme.colorScheme.primary)
        }
    }
}

internal fun String.displayLabel(): String = replace('_', ' ')
    .split(' ')
    .joinToString(" ") { word ->
        word.replaceFirstChar {
            if (it.isLowerCase()) it.titlecase() else it.toString()
        }
    }

package com.rendref.netstorage.feature.book

import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.RadioButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp

@Composable
fun GroupingScreen(
    state: BookReviewState,
    onAction: (BookReviewAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    var mergeSourceId by remember { mutableStateOf<Int?>(null) }
    val audioFiles = state.groupingFiles.filter { file ->
        AUDIO_SUFFIXES.any { file.name.endsWith(it, ignoreCase = true) }
    }
    LazyColumn(
        modifier = modifier
            .testTag("grouping-list")
            .fillMaxSize()
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        item {
            Text(
                "Review grouping",
                modifier = Modifier.padding(top = 16.dp),
                style = MaterialTheme.typography.headlineSmall,
                fontWeight = FontWeight.SemiBold,
            )
            Text("Each audio file must belong to exactly one logical book.")
        }
        item {
            ReviewStatusMessages(
                state = state,
                loadingLabel = "Loading grouping...",
                savingLabel = "Saving catalog grouping...",
            )
        }
        itemsIndexed(state.groups, key = { _, group -> group.id }) { index, group ->
            val groupNumber = index + 1
            Card(modifier = Modifier.fillMaxWidth()) {
                Column(
                    modifier = Modifier.padding(12.dp),
                    verticalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    Text(
                        "Logical book $groupNumber",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                    )
                    group.title?.let {
                        Text(it, style = MaterialTheme.typography.bodyMedium)
                    }
                    audioFiles.forEach { file ->
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .selectable(
                                    selected = file.id in group.fileIds,
                                    role = Role.RadioButton,
                                    onClick = {
                                        onAction(
                                            BookReviewAction.AssignFile(file.id, group.id),
                                        )
                                    },
                                )
                                .semantics {
                                    contentDescription =
                                        "Assign ${file.name} to logical book $groupNumber"
                                },
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            RadioButton(
                                selected = file.id in group.fileIds,
                                onClick = null,
                            )
                            Text(file.name)
                        }
                    }
                    if (state.groups.size > 1) {
                        Box {
                            TextButton(
                                onClick = { mergeSourceId = group.id },
                            ) {
                                Text("Combine with another logical book")
                            }
                            DropdownMenu(
                                expanded = mergeSourceId == group.id,
                                onDismissRequest = { mergeSourceId = null },
                            ) {
                                state.groups.forEachIndexed { targetIndex, target ->
                                    if (target.id != group.id) {
                                        DropdownMenuItem(
                                            text = {
                                                Text(
                                                    "Combine into logical book " +
                                                        "${targetIndex + 1}",
                                                )
                                            },
                                            onClick = {
                                                mergeSourceId = null
                                                onAction(
                                                    BookReviewAction.MergeGroups(
                                                        group.id,
                                                        target.id,
                                                    ),
                                                )
                                            },
                                        )
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
        item {
            TextButton(
                onClick = { onAction(BookReviewAction.AddGroup) },
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text("Add logical book")
            }
            Button(
                onClick = { onAction(BookReviewAction.SaveGrouping) },
                enabled = state.canSaveGrouping && !state.isSaving,
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(bottom = 24.dp),
            ) {
                Text("Save catalog grouping")
            }
        }
    }
}

private val AUDIO_SUFFIXES = listOf(
    ".m4b",
    ".m4a",
    ".mp3",
    ".aac",
    ".flac",
    ".ogg",
    ".opus",
    ".wav",
)

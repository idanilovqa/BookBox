package com.rendref.bookbox.feature.book

import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.toggleable
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
import androidx.compose.material3.Checkbox
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
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
    val audioFiles = state.groupingFiles.filter { it.role == "audio" }
    val companionFiles = state.groupingFiles.filter { it.role != "audio" }
    LazyColumn(
        modifier = modifier
            .testTag("grouping-list")
            .fillMaxSize()
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        item {
            Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                Text(
                    "Review grouping",
                    modifier = Modifier.padding(top = 16.dp),
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.SemiBold,
                )
                Text("Each audio file must belong to exactly one logical book.")
                state.groupingReviewReason?.let { reason ->
                    Text("Review reason: ${reason.displayLabel()}")
                }
                state.groupingEvidence.toSortedMap().forEach { (role, paths) ->
                    paths.forEach { path ->
                        Text("${role.displayLabel()} evidence: $path")
                    }
                }
            }
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
                    if (state.canReorganize) {
                        OutlinedTextField(
                            value = group.targetFolder,
                            onValueChange = {
                                onAction(
                                    BookReviewAction.UpdateTargetFolder(group.id, it),
                                )
                            },
                            label = { Text("Target folder") },
                            singleLine = true,
                            modifier = Modifier
                                .fillMaxWidth()
                                .semantics {
                                    contentDescription =
                                        "Target folder for logical book $groupNumber"
                                },
                        )
                    }
                    audioFiles.forEach { file ->
                        val displayPath = if (
                            state.groupingScopePath.isNotEmpty() ||
                            state.groupingReviewReason != null
                        ) {
                            file.relativePath
                        } else {
                            file.name
                        }
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
                                        "Assign $displayPath to logical book $groupNumber"
                                },
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            RadioButton(
                                selected = file.id in group.fileIds,
                                onClick = null,
                            )
                            Text(displayPath)
                        }
                    }
                    companionFiles.forEach { file ->
                        val displayPath = file.relativePath
                        val selected = file.id in group.companionFileIds
                        val verb = if (file.shared) "Share" else "Assign"
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .toggleable(
                                    value = selected,
                                    role = Role.Checkbox,
                                    onValueChange = {
                                        onAction(
                                            BookReviewAction.ToggleCompanion(file.id, group.id),
                                        )
                                    },
                                )
                                .semantics {
                                    contentDescription = if (file.shared) {
                                        "$verb $displayPath with logical book $groupNumber"
                                    } else {
                                        "$verb $displayPath to logical book $groupNumber"
                                    }
                                },
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Checkbox(
                                checked = selected,
                                onCheckedChange = null,
                            )
                            Text(displayPath)
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
        state.reorganizationPreview?.let { preview ->
            item {
                Card(modifier = Modifier.fillMaxWidth()) {
                    Column(
                        modifier = Modifier.padding(12.dp),
                        verticalArrangement = Arrangement.spacedBy(6.dp),
                    ) {
                        Text(
                            "Reorganization preview",
                            style = MaterialTheme.typography.titleMedium,
                            fontWeight = FontWeight.Bold,
                        )
                        Text("Review every source-relative path before confirming.")
                        preview.moves.sortedBy { it.order }.forEach { move ->
                            Text("${move.role.displayLabel()}: ${move.sourcePath}")
                            Text("Destination: ${move.destinationPath}")
                        }
                        Button(
                            onClick = {
                                onAction(BookReviewAction.ConfirmReorganization)
                            },
                            enabled = !state.isSaving,
                            modifier = Modifier.fillMaxWidth(),
                        ) {
                            Text("Confirm file reorganization")
                        }
                        TextButton(
                            onClick = {
                                onAction(BookReviewAction.CancelReorganization)
                            },
                            enabled = !state.isSaving,
                            modifier = Modifier.fillMaxWidth(),
                        ) {
                            Text("Cancel reorganization")
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
            if (state.canReorganize) {
                if (state.reorganizationPreview == null) {
                    Button(
                        onClick = {
                            onAction(BookReviewAction.PreviewReorganization)
                        },
                        enabled = state.canSaveGrouping &&
                            state.groups.all {
                                validReorganizationTargetFolder(it.targetFolder)
                            } &&
                            !state.isSaving,
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(bottom = 24.dp),
                    ) {
                        Text("Confirm and reorganize files")
                    }
                }
            } else {
                Button(
                    onClick = { onAction(BookReviewAction.SaveGrouping) },
                    enabled = state.canSaveGrouping && !state.isSaving,
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(bottom = 24.dp),
                ) {
                    Text("Confirm grouping")
                }
            }
        }
    }
}

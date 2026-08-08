package com.rendref.bookbox.feature.library

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.role
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.model.BookSearchGroupDto

@Composable
fun DuplicateBookGroupCard(
    group: BookSearchGroupDto,
    expanded: Boolean,
    selectedBookIds: Set<String>,
    onExpandedChange: (Boolean) -> Unit,
    onAction: (LibraryAction) -> Unit,
    modifier: Modifier = Modifier,
) {
    val representative = group.books.firstOrNull() ?: return
    val copyLabel = "${group.books.size} copies"
    val roots = group.books.map(BookCardDto::rootId)
        .filter(String::isNotBlank)
        .distinct()
    val rootLabel = roots.joinToString(" • ")
    val selectedCount = group.books.count { it.id in selectedBookIds }
    val selectionLabel = "$selectedCount selected"
    Column(
        modifier = modifier.fillMaxWidth(),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Surface(
            modifier = Modifier
                .fillMaxWidth()
                .clickable { onExpandedChange(!expanded) }
                .semantics {
                    role = Role.Button
                    contentDescription = listOf(
                        representative.title,
                        copyLabel,
                        rootLabel,
                        selectionLabel,
                    ).filter(String::isNotBlank).joinToString(", ")
                    stateDescription = if (expanded) "Expanded" else "Collapsed"
                },
            shape = RoundedCornerShape(16.dp),
            color = MaterialTheme.colorScheme.secondaryContainer,
            tonalElevation = 2.dp,
        ) {
            Row(
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 14.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        text = representative.title,
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.SemiBold,
                    )
                    Text(
                        text = copyLabel,
                        color = MaterialTheme.colorScheme.onSecondaryContainer,
                        style = MaterialTheme.typography.labelLarge,
                    )
                    if (rootLabel.isNotEmpty()) {
                        Text(
                            text = rootLabel,
                            color = MaterialTheme.colorScheme.onSecondaryContainer,
                            style = MaterialTheme.typography.bodySmall,
                        )
                    }
                    Text(
                        text = selectionLabel,
                        color = MaterialTheme.colorScheme.onSecondaryContainer,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
                Text(
                    text = if (expanded) "Hide copies" else "Show copies",
                    color = MaterialTheme.colorScheme.primary,
                    style = MaterialTheme.typography.labelLarge,
                    fontWeight = FontWeight.Bold,
                )
            }
        }
        if (expanded) {
            group.books.forEach { book ->
                AudiobookCard(
                    book = book,
                    selected = book.id in selectedBookIds,
                    onAction = onAction,
                    duplicate = true,
                    showRoot = true,
                    modifier = Modifier.padding(start = 20.dp),
                )
            }
        }
    }
}

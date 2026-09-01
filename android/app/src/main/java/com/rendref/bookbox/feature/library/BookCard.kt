package com.rendref.bookbox.feature.library

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.Checkbox
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.painter.ColorPainter
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import coil3.compose.AsyncImage
import com.rendref.bookbox.core.model.BookCardDto

sealed interface LibraryAction {
    val bookId: String

    data class OpenBook(override val bookId: String) : LibraryAction

    data class ToggleSelected(override val bookId: String) : LibraryAction

    data class ViewRawFiles(override val bookId: String) : LibraryAction

    data class ReviewGrouping(override val bookId: String) : LibraryAction
}

@Composable
fun AudiobookCard(
    book: BookCardDto,
    selected: Boolean,
    onAction: (LibraryAction) -> Unit,
    modifier: Modifier = Modifier,
    duplicate: Boolean = false,
    showRoot: Boolean = false,
) {
    val actionContext = if (showRoot) {
        listOf(book.rootId, book.sourceFolder)
            .filter(String::isNotBlank)
            .joinToString(", ")
            .takeIf(String::isNotEmpty)
            ?.let { " from $it" }
            .orEmpty()
    } else {
        ""
    }
    Card(
        modifier = modifier
            .fillMaxWidth()
            .clickable(
                onClickLabel = "Open ${book.title}$actionContext",
            ) {
                onAction(LibraryAction.OpenBook(book.id))
            },
        shape = RoundedCornerShape(20.dp),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surface,
        ),
        elevation = CardDefaults.cardElevation(defaultElevation = 2.dp),
    ) {
        Column {
            Box {
                AsyncImage(
                    model = book.coverUrl,
                    contentDescription = "Cover of ${book.title}$actionContext",
                    modifier = Modifier
                        .fillMaxWidth()
                        .aspectRatio(0.68f)
                        .background(MaterialTheme.colorScheme.primaryContainer),
                    placeholder = ColorPainter(MaterialTheme.colorScheme.primaryContainer),
                    error = ColorPainter(MaterialTheme.colorScheme.primaryContainer),
                )
                Text(
                    text = book.reviewState.displayLabel(),
                    modifier = Modifier
                        .align(Alignment.TopStart)
                        .padding(10.dp)
                        .clip(RoundedCornerShape(999.dp))
                        .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.94f))
                        .padding(horizontal = 10.dp, vertical = 5.dp),
                    color = MaterialTheme.colorScheme.primary,
                    style = MaterialTheme.typography.labelMedium,
                    fontWeight = FontWeight.Bold,
                )
                Checkbox(
                    checked = selected,
                    onCheckedChange = { onAction(LibraryAction.ToggleSelected(book.id)) },
                    modifier = Modifier
                        .align(Alignment.TopEnd)
                        .padding(4.dp)
                        .semantics {
                            contentDescription = "Select ${book.title}$actionContext"
                        },
                )
            }

            Column(
                modifier = Modifier.padding(start = 14.dp, end = 14.dp, top = 14.dp),
                verticalArrangement = Arrangement.spacedBy(5.dp),
            ) {
                Text(
                    text = book.title,
                    style = MaterialTheme.typography.titleLarge,
                    fontWeight = FontWeight.SemiBold,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(
                    text = book.authors.joinToString().ifBlank { "Unknown author" },
                    color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.72f),
                    style = MaterialTheme.typography.bodyMedium,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
                book.publicationYear?.let { year ->
                    Text(
                        text = year.toString(),
                        color = MaterialTheme.colorScheme.primary,
                        style = MaterialTheme.typography.labelLarge,
                    )
                }
                book.genres.firstOrNull()?.let { genre ->
                    Text(
                        text = genre,
                        modifier = Modifier
                            .clip(RoundedCornerShape(8.dp))
                            .background(MaterialTheme.colorScheme.primaryContainer)
                            .padding(horizontal = 8.dp, vertical = 4.dp),
                        color = MaterialTheme.colorScheme.onPrimaryContainer,
                        style = MaterialTheme.typography.labelMedium,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                book.series?.takeIf(String::isNotBlank)?.let { series ->
                    MetadataLine("Series: $series")
                }
                book.narrators.takeIf(List<String>::isNotEmpty)?.let { narrators ->
                    MetadataLine("Narrated by ${narrators.joinToString()}")
                }
                if (duplicate) {
                    Text(
                        text = "Duplicate book",
                        color = MaterialTheme.colorScheme.primary,
                        style = MaterialTheme.typography.labelLarge,
                        fontWeight = FontWeight.Bold,
                    )
                }
                if (showRoot && book.rootId.isNotBlank()) {
                    MetadataLine("Library: ${book.rootId}")
                }
            }

            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 6.dp, vertical = 4.dp),
                horizontalArrangement = Arrangement.End,
            ) {
                if (book.reviewState == "needs_review") {
                    TextButton(
                        onClick = { onAction(LibraryAction.ReviewGrouping(book.id)) },
                        colors = ButtonDefaults.textButtonColors(
                            contentColor = MaterialTheme.colorScheme.error,
                        ),
                        modifier = Modifier.semantics {
                            contentDescription = "Review audiobook grouping"
                        },
                    ) {
                        Text("!")
                    }
                }
                TextButton(
                    onClick = { onAction(LibraryAction.ViewRawFiles(book.id)) },
                    modifier = Modifier.semantics {
                        contentDescription = "View raw files for ${book.title}$actionContext"
                    },
                ) {
                    Text("View raw files")
                }
            }
        }
    }
}

@Composable
fun CompactAudiobookCard(
    book: BookCardDto,
    selected: Boolean,
    onAction: (LibraryAction) -> Unit,
    modifier: Modifier = Modifier,
    duplicate: Boolean = false,
    showRoot: Boolean = false,
) {
    val context = if (showRoot && book.rootId.isNotBlank()) " from ${book.rootId}" else ""
    Card(
        modifier = modifier
            .fillMaxWidth()
            .height(80.dp)
            .clickable(onClickLabel = "Open ${book.title}$context") {
                onAction(LibraryAction.OpenBook(book.id))
            },
        shape = RoundedCornerShape(18.dp),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
        elevation = CardDefaults.cardElevation(defaultElevation = 1.dp),
    ) {
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            AsyncImage(
                model = book.coverUrl,
                contentDescription = "Cover of ${book.title}$context",
                modifier = Modifier
                    .width(54.dp)
                    .height(80.dp)
                    .background(MaterialTheme.colorScheme.primaryContainer),
                contentScale = ContentScale.Crop,
                placeholder = ColorPainter(MaterialTheme.colorScheme.primaryContainer),
                error = ColorPainter(MaterialTheme.colorScheme.primaryContainer),
            )
            Column(
                modifier = Modifier
                    .weight(1f)
                    .padding(horizontal = 10.dp, vertical = 7.dp),
                verticalArrangement = Arrangement.spacedBy(1.dp),
            ) {
                Text(
                    text = book.title,
                    style = MaterialTheme.typography.titleSmall,
                    fontWeight = FontWeight.SemiBold,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(
                    text = book.authors.joinToString().ifBlank { "Unknown author" },
                    color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.68f),
                    style = MaterialTheme.typography.bodySmall,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                book.series?.takeIf(String::isNotBlank)?.let { series ->
                    Text(
                        text = listOfNotNull(
                            series,
                            book.seriesPosition?.takeIf(String::isNotBlank),
                        ).joinToString(" · "),
                        color = MaterialTheme.colorScheme.primary.copy(alpha = 0.82f),
                        style = MaterialTheme.typography.labelSmall,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                if (duplicate) {
                    Text(
                        text = "duplicate",
                        color = MaterialTheme.colorScheme.error,
                        style = MaterialTheme.typography.labelSmall,
                    )
                }
            }
            TextButton(onClick = { onAction(LibraryAction.ViewRawFiles(book.id)) }) {
                Text("Raw", style = MaterialTheme.typography.labelSmall)
            }
            Checkbox(
                checked = selected,
                onCheckedChange = { onAction(LibraryAction.ToggleSelected(book.id)) },
                modifier = Modifier.semantics {
                    contentDescription = "Select ${book.title}$context"
                },
            )
        }
    }
}

@Composable
private fun MetadataLine(text: String) {
    Text(
        text = text,
        color = MaterialTheme.colorScheme.onSurface.copy(alpha = 0.64f),
        style = MaterialTheme.typography.bodySmall,
        maxLines = 2,
        overflow = TextOverflow.Ellipsis,
    )
}

private fun String.displayLabel(): String =
    replace('_', ' ')
        .replaceFirstChar { character ->
            if (character.isLowerCase()) character.titlecase() else character.toString()
        }

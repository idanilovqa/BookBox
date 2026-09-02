package com.rendref.bookbox.feature.library

import androidx.compose.foundation.layout.width
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.unit.dp
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.model.BookSearchGroupDto
import com.rendref.bookbox.core.model.SearchSnapshotDto
import com.rendref.bookbox.core.search.STALE_CATALOG_STATUS
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

class LibraryScreenTest {
    @get:Rule
    val composeRule = createComposeRule()

    @Test
    fun audiobookCardShowsCatalogMetadataSelectionAndRawFilesOnly() {
        val actions = mutableListOf<LibraryAction>()
        composeRule.setContent {
            MaterialTheme {
                AudiobookCard(
                    book = dune,
                    selected = false,
                    onAction = actions::add,
                    modifier = Modifier.width(180.dp),
                )
            }
        }

        composeRule.onNodeWithContentDescription("Cover of Dune").assertIsDisplayed()
        composeRule.onNodeWithText("Dune").assertIsDisplayed()
        composeRule.onNodeWithText("Frank Herbert").assertIsDisplayed()
        composeRule.onNodeWithText("1965").assertIsDisplayed()
        composeRule.onNodeWithText("Science Fiction").assertIsDisplayed()
        composeRule.onNodeWithText("Series: Dune").assertIsDisplayed()
        composeRule.onNodeWithText("Narrated by Simon Vance").assertIsDisplayed()
        composeRule.onNodeWithText("Confirmed").assertIsDisplayed()
        composeRule.onNodeWithContentDescription("Select Dune")
            .assertIsDisplayed()
            .performClick()
        composeRule.onNodeWithText("View raw files")
            .assertIsDisplayed()
            .performClick()

        assertEquals(
            listOf(
                LibraryAction.ToggleSelected(dune.id),
                LibraryAction.ViewRawFiles(dune.id),
            ),
            actions,
        )
        forbiddenLabels.forEach { label ->
            composeRule.onAllNodesWithText(label, substring = true)
                .assertCountEquals(0)
        }
    }

    @Test
    fun libraryKeepsSearchVisibleAndRoutesCardActions() {
        val searches = mutableListOf<String>()
        val actions = mutableListOf<LibraryAction>()
        var state by mutableStateOf(
            LibraryState(
                books = listOf(dune),
                groups = listOf(BookSearchGroupDto(dune.id, false, listOf(dune))),
            ),
        )
        composeRule.setContent {
            MaterialTheme {
                LibraryScreen(
                    state = state,
                    onSearchTextChange = { value ->
                        searches.add(value)
                        state = state.copy(query = state.query.copy(text = value))
                    },
                    onAction = actions::add,
                    onAuthorChange = {},
                    onGenreChange = {},
                    onYearChange = {},
                    onRootChange = {},
                    onReviewStateChange = {},
                    onSortChange = {},
                    onLoadNextPage = {},
                    onGridPositionChange = { _, _ -> },
                )
            }
        }

        composeRule.onNodeWithText("Search audiobooks")
            .assertIsDisplayed()
            .performTextReplacement("dune")
        composeRule.onNodeWithText("Filters").assertIsDisplayed()
        composeRule.onNodeWithText("Sort").assertIsDisplayed()
        composeRule.onNodeWithText("Dune").performClick()

        assertEquals("dune", searches.last())
        assertEquals(LibraryAction.OpenBook(dune.id), actions.last())
    }

    @Test
    fun libraryRendersStaleCatalogStatusWithRetainedCards() {
        composeRule.setContent {
            MaterialTheme {
                LibraryScreen(
                    state = LibraryState(
                        groups = listOf(BookSearchGroupDto(dune.id, false, listOf(dune))),
                        isCatalogStale = true,
                        catalogStatusMessage = STALE_CATALOG_STATUS,
                    ),
                    onSearchTextChange = {},
                    onAction = {},
                    onAuthorChange = {},
                    onGenreChange = {},
                    onYearChange = {},
                    onRootChange = {},
                    onReviewStateChange = {},
                    onSortChange = {},
                    onLoadNextPage = {},
                    onGridPositionChange = { _, _ -> },
                )
            }
        }

        composeRule.onNodeWithText(STALE_CATALOG_STATUS).assertIsDisplayed()
        composeRule.onNodeWithText("Dune").assertIsDisplayed()
    }

    @Test
    fun needsReviewCardExposesAccessibleGroupingReviewAction() {
        val actions = mutableListOf<LibraryAction>()
        val provisional = dune.copy(
            reviewState = "needs_review",
            reviewReason = "probe_failed",
        )
        composeRule.setContent {
            MaterialTheme {
                AudiobookCard(
                    book = provisional,
                    selected = false,
                    onAction = actions::add,
                    modifier = Modifier.width(180.dp),
                )
            }
        }

        composeRule.onNodeWithContentDescription("Review audiobook grouping")
            .assertIsDisplayed()
            .performClick()

        assertEquals(LibraryAction.ReviewGrouping(dune.id), actions.single())
    }

    @Test
    fun duplicateGroupStartsCollapsedExpandsAcrossRootsAndRoutesEachBook() {
        val actions = mutableListOf<LibraryAction>()
        composeRule.setContent {
            MaterialTheme {
                LibraryScreen(
                    state = LibraryState(
                        groups = listOf(
                            BookSearchGroupDto(
                                groupId = "00000000-0000-4000-8000-000000000010",
                                duplicate = true,
                                books = listOf(dune, alternateDune),
                            ),
                        ),
                        selectedBookIds = setOf(dune.id),
                    ),
                    onSearchTextChange = {},
                    onAction = actions::add,
                    onAuthorChange = {},
                    onGenreChange = {},
                    onYearChange = {},
                    onRootChange = {},
                    onReviewStateChange = {},
                    onSortChange = {},
                    onLoadNextPage = {},
                    onGridPositionChange = { _, _ -> },
                )
            }
        }

        composeRule.onNodeWithText("2 copies", substring = true).assertIsDisplayed()
        composeRule.onNodeWithText("primary").assertIsDisplayed()
        composeRule.onNodeWithContentDescription(
            "Dune, 2 copies, primary, 1 selected",
        ).assertIsDisplayed()
        composeRule.onAllNodesWithContentDescription(
            "Select Dune from primary, Frank Herbert/Dune",
        )
            .assertCountEquals(0)
        composeRule.onAllNodesWithText("Duplicate book").assertCountEquals(0)

        composeRule.onNodeWithText("2 copies", substring = true).performClick()

        composeRule.onAllNodesWithText("Duplicate book").assertCountEquals(2)
        composeRule.onAllNodesWithText("Library: primary").assertCountEquals(2)
        composeRule.onNodeWithContentDescription(
            "Select Dune from primary, Frank Herbert/Dune",
        )
            .performScrollTo()
            .assertIsDisplayed()
            .performClick()
        composeRule.onNodeWithContentDescription(
            "Select Dune from primary, Alternate/Dune",
        )
            .performScrollTo()
            .assertIsDisplayed()
            .performClick()

        assertEquals(
            listOf(
                LibraryAction.ToggleSelected(dune.id),
                LibraryAction.ToggleSelected(alternateDune.id),
            ),
            actions,
        )
    }

    @Test
    fun sameTitleSingletonsAcrossRootsShowRootIdentity() {
        composeRule.setContent {
            MaterialTheme {
                LibraryScreen(
                    state = LibraryState(
                        groups = listOf(
                            BookSearchGroupDto(dune.id, false, listOf(dune)),
                            BookSearchGroupDto(archiveDune.id, false, listOf(archiveDune)),
                        ),
                        snapshots = listOf(archiveSnapshot, primarySnapshot),
                    ),
                    onSearchTextChange = {},
                    onAction = {},
                    onAuthorChange = {},
                    onGenreChange = {},
                    onYearChange = {},
                    onRootChange = {},
                    onReviewStateChange = {},
                    onSortChange = {},
                    onLoadNextPage = {},
                    onGridPositionChange = { _, _ -> },
                )
            }
        }

        val grid = composeRule.onNode(hasScrollToIndexAction())
        grid.performScrollToNode(hasText("Library: primary"))
        composeRule.onNodeWithText("Library: primary").assertIsDisplayed()
        grid.performScrollToNode(hasText("Library: archive"))
        composeRule.onNodeWithText("Library: archive").assertIsDisplayed()
        grid.performScrollToNode(
            hasContentDescription("Select Dune from primary, Frank Herbert/Dune"),
        )
        composeRule.onNodeWithContentDescription(
            "Select Dune from primary, Frank Herbert/Dune",
        ).assertIsDisplayed()
        grid.performScrollToNode(
            hasContentDescription("Select Dune from archive, Archive/Dune"),
        )
        composeRule.onNodeWithContentDescription(
            "Select Dune from archive, Archive/Dune",
        ).assertIsDisplayed()
    }

    @Test
    fun paginationFailureShowsStableManualRetryInGridAndCompactLayouts() {
        var state by mutableStateOf(
            LibraryState(
                groups = listOf(BookSearchGroupDto(dune.id, false, listOf(dune))),
                nextCursor = "failed-cursor",
                paginationError = "Couldn't load more",
            ),
        )
        var automaticLoads = 0
        var retries = 0
        composeRule.setContent {
            MaterialTheme {
                LibraryScreen(
                    state = state,
                    onSearchTextChange = {},
                    onAction = {},
                    onAuthorChange = {},
                    onGenreChange = {},
                    onYearChange = {},
                    onRootChange = {},
                    onReviewStateChange = {},
                    onSortChange = {},
                    onLoadNextPage = { automaticLoads += 1 },
                    onGridPositionChange = { _, _ -> },
                    onRetryNextPage = { retries += 1 },
                )
            }
        }

        composeRule.onNodeWithText("Couldn't load more").assertIsDisplayed()
        composeRule.onNodeWithText("Retry").performClick()
        composeRule.runOnIdle {
            assertEquals(0, automaticLoads)
            assertEquals(1, retries)
            state = state.copy(layout = LibraryLayout.COMPACT)
        }
        composeRule.onNodeWithText("Couldn't load more").assertIsDisplayed()
        composeRule.onNodeWithText("Retry").performClick()
        composeRule.runOnIdle {
            assertEquals(0, automaticLoads)
            assertEquals(2, retries)
        }
    }
}

private val dune = BookCardDto(
    id = "00000000-0000-4000-8000-000000000030",
    title = "Dune",
    authors = listOf("Frank Herbert"),
    publicationYear = 1965,
    genres = listOf("Science Fiction"),
    series = "Dune",
    narrators = listOf("Simon Vance"),
    coverUrl = null,
    reviewState = "confirmed",
    sourceFolder = "Frank Herbert/Dune",
    rootId = "primary",
)

private val archiveDune = BookCardDto(
    id = "00000000-0000-4000-8000-000000000031",
    title = "Dune",
    authors = listOf("Frank Herbert"),
    publicationYear = 1965,
    genres = listOf("Science Fiction"),
    series = "Dune",
    narrators = listOf("George Guidall"),
    coverUrl = null,
    reviewState = "confirmed",
    sourceFolder = "Archive/Dune",
    rootId = "archive",
)

private val alternateDune = archiveDune.copy(
    id = "00000000-0000-4000-8000-000000000032",
    rootId = "primary",
    sourceFolder = "Alternate/Dune",
)

private val primarySnapshot = SearchSnapshotDto(
    rootId = "primary",
    scanRunId = "00000000-0000-4000-8000-000000000020",
    completedAt = "2026-08-08T12:00:00Z",
)

private val archiveSnapshot = SearchSnapshotDto(
    rootId = "archive",
    scanRunId = "00000000-0000-4000-8000-000000000021",
    completedAt = "2026-08-08T12:00:00Z",
)

private val forbiddenLabels = listOf(
    "Rename",
    "Move",
    "Delete",
    "Upload",
    "Edit tags",
    "New folder",
)

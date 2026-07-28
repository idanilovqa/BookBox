package com.rendref.bookbox.feature.library

import androidx.compose.foundation.layout.width
import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.unit.dp
import com.rendref.bookbox.core.model.BookCardDto
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
                LibraryAction.ToggleSelected("b1"),
                LibraryAction.ViewRawFiles("b1"),
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
        composeRule.setContent {
            MaterialTheme {
                LibraryScreen(
                    state = LibraryState(books = listOf(dune)),
                    onSearchTextChange = searches::add,
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
            .performTextInput("dune")
        composeRule.onNodeWithText("Filters").assertIsDisplayed()
        composeRule.onNodeWithText("Sort").assertIsDisplayed()
        composeRule.onNodeWithText("Dune").performClick()

        assertTrue(searches.last().contains("dune"))
        assertEquals(LibraryAction.OpenBook("b1"), actions.last())
    }
}

private val dune = BookCardDto(
    id = "b1",
    title = "Dune",
    authors = listOf("Frank Herbert"),
    publicationYear = 1965,
    genres = listOf("Science Fiction"),
    series = "Dune",
    narrators = listOf("Simon Vance"),
    coverUrl = null,
    reviewState = "confirmed",
    sourceFolder = "Frank Herbert/Dune",
)

private val forbiddenLabels = listOf(
    "Rename",
    "Move",
    "Delete",
    "Upload",
    "Edit tags",
    "New folder",
)

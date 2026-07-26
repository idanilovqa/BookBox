package com.rendref.netstorage.feature.folders

import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import com.rendref.netstorage.core.model.BrowseEntryDto
import com.rendref.netstorage.core.model.RootDto
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test

class FoldersScreenTest {
    @get:Rule
    val composeRule = createComposeRule()

    @Test
    fun rawFolderScreenShowsAllSiblingsHighlightsBookAndHasNoMutationActions() {
        val actions = mutableListOf<FolderAction>()
        composeRule.setContent {
            MaterialTheme {
                FoldersScreen(
                    state = folderState,
                    onAction = actions::add,
                )
            }
        }

        composeRule.onAllNodesWithText("Audiobooks")
            .assertCountEquals(2)[0]
            .assertIsDisplayed()
        composeRule.onNodeWithText("Author One").assertIsDisplayed()
        composeRule.onNodeWithText("Book").assertIsDisplayed()
        composeRule.onNodeWithText("Extras").assertIsDisplayed()
        composeRule.onNodeWithText("chapter.m4b").assertIsDisplayed()
        composeRule.onNodeWithText("notes.txt").assertIsDisplayed()
        composeRule.onNodeWithText("This book").assertIsDisplayed()
        composeRule.onNodeWithContentDescription("Select folder Extras")
            .assertIsDisplayed()
            .performClick()
        composeRule.onNodeWithText("Return to audiobook")
            .assertIsDisplayed()
            .performClick()

        assertEquals(
            listOf(
                FolderAction.ToggleFolder("Author One/Book/Extras"),
                FolderAction.ReturnToBook("book-1"),
            ),
            actions,
        )
        forbiddenLabels.forEach { label ->
            composeRule.onAllNodesWithText(label, substring = true)
                .assertCountEquals(0)
        }
    }

    @Test
    fun folderAndRefreshControlsRouteReadOnlyNavigation() {
        val actions = mutableListOf<FolderAction>()
        composeRule.setContent {
            MaterialTheme {
                FoldersScreen(
                    state = folderState.copy(originBookId = null),
                    onAction = actions::add,
                )
            }
        }

        composeRule.onNodeWithText("Extras").performClick()
        composeRule.onNodeWithContentDescription("Refresh folders").performClick()

        assertEquals(
            listOf(
                FolderAction.OpenFolder("Author One/Book/Extras"),
                FolderAction.Refresh,
            ),
            actions,
        )
    }
}

private val folderState = FoldersState(
    roots = listOf(RootDto("audiobooks", "Audiobooks")),
    selectedRootId = "audiobooks",
    path = "Author One/Book",
    breadcrumbs = listOf(
        Breadcrumb("Audiobooks", ""),
        Breadcrumb("Author One", "Author One"),
        Breadcrumb("Book", "Author One/Book"),
    ),
    entries = listOf(
        BrowseEntryDto(
            name = "Extras",
            relativePath = "Author One/Book/Extras",
            kind = "folder",
            modifiedAt = "2026-07-26T00:00:00Z",
        ),
        BrowseEntryDto(
            name = "chapter.m4b",
            relativePath = "Author One/Book/chapter.m4b",
            kind = "file",
            size = 1_536,
            modifiedAt = "2026-07-26T00:00:00Z",
        ),
        BrowseEntryDto(
            name = "notes.txt",
            relativePath = "Author One/Book/notes.txt",
            kind = "file",
            size = 24,
            modifiedAt = "2026-07-26T00:00:00Z",
        ),
    ),
    highlightedFileIds = setOf("file-1"),
    assignedRelativePaths = setOf("Author One/Book/chapter.m4b"),
    originBookId = "book-1",
)

private val forbiddenLabels = listOf(
    "Rename",
    "Move",
    "Delete",
    "Upload",
    "Edit tags",
    "New folder",
)

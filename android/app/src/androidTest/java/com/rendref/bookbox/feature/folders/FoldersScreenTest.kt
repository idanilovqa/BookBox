package com.rendref.bookbox.feature.folders

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.RawSearchEntryDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.search.STALE_CATALOG_STATUS
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

    @Test
    fun folderSearchRoutesTypingAndOpensPathResult() {
        val actions = mutableListOf<FolderAction>()
        var state by mutableStateOf(
            folderState.copy(
                searchQuery = "black box",
                searchResults = listOf(
                    RawSearchEntryDto(
                        id = "00000000-0000-4000-8000-000000000001",
                        name = "Black.Box",
                        relativePath = "Author/Black.Box",
                        kind = "folder",
                    ),
                ),
            ),
        )
        composeRule.setContent {
            MaterialTheme {
                FoldersScreen(
                    state = state,
                    onAction = { action ->
                        actions += action
                        if (action is FolderAction.UpdateSearchQuery) {
                            state = state.copy(searchQuery = action.query)
                        }
                    },
                )
            }
        }

        composeRule.onNodeWithContentDescription("Search raw folders")
            .performTextReplacement("black box!")
        composeRule.onNodeWithText("Author/Black.Box")
            .assertIsDisplayed()
            .performClick()

        assertEquals(
            listOf(
                FolderAction.UpdateSearchQuery("black box!"),
                FolderAction.OpenSearchResult("Author/Black.Box"),
            ),
            actions,
        )
    }

    @Test
    fun rawSearchRendersFolderAndFileMetadataAndRoutesBothResults() {
        val actions = mutableListOf<FolderAction>()
        composeRule.setContent {
            MaterialTheme {
                FoldersScreen(
                    state = folderState.copy(
                        searchQuery = "dune",
						searchHighlightedRelativePath = "Loose Files/Dune.m4b",
                        searchResults = listOf(
                            RawSearchEntryDto(
                                id = "00000000-0000-4000-8000-000000000010",
                                name = "Dune",
                                relativePath = "Frank Herbert/Dune",
                                kind = "folder",
                                duplicateGroupId =
                                    "00000000-0000-4000-8000-000000000012",
                                duplicateBook = true,
                            ),
                            RawSearchEntryDto(
                                id = "00000000-0000-4000-8000-000000000011",
                                name = "Dune.m4b",
                                relativePath = "Loose Files/Dune.m4b",
                                kind = "file",
                                size = 1_536,
                                modifiedAt = "2026-08-08T12:00:00Z",
                                bookId = "00000000-0000-4000-8000-000000000013",
                                duplicateGroupId =
                                    "00000000-0000-4000-8000-000000000012",
                                duplicateBook = true,
                            ),
                        ),
                    ),
                    onAction = actions::add,
                )
            }
        }

        composeRule.onNodeWithText("FOLDER").assertIsDisplayed()
		composeRule.onNodeWithText("AUDIO FILE").assertIsDisplayed()
		composeRule.onNodeWithContentDescription("Highlighted search result").assertIsDisplayed()
        composeRule.onNodeWithText("Frank Herbert/Dune").assertIsDisplayed()
        composeRule.onNodeWithText("Loose Files/Dune.m4b").assertIsDisplayed()
        composeRule.onNodeWithText("1.5 KB").assertIsDisplayed()
        composeRule.onAllNodesWithText("Duplicate book").assertCountEquals(2)

        composeRule.onNodeWithText("Dune").performClick()
        composeRule.onNodeWithText("Dune.m4b").performClick()

        assertEquals(
            listOf(
                FolderAction.OpenSearchResult("Frank Herbert/Dune"),
                FolderAction.OpenSearchResult("Loose Files/Dune.m4b"),
            ),
            actions,
        )
    }

    @Test
    fun folderSearchShowsProgressEmptyAndErrorStates() {
        var state by mutableStateOf(
            folderState.copy(
				searchQuery = "\uD801\uDC00\uD801\uDC01",
                isSearching = true,
            ),
        )
        composeRule.setContent {
            MaterialTheme {
                FoldersScreen(
                    state = state,
                    onAction = {},
                )
            }
        }
        composeRule.onNodeWithContentDescription("Searching folders")
            .assertIsDisplayed()

        composeRule.runOnIdle {
            state = folderState.copy(searchQuery = "missing")
        }
		composeRule.onNodeWithText("No matching folders or files.").assertIsDisplayed()

        composeRule.runOnIdle {
            state = folderState.copy(
                searchQuery = "missing",
                searchErrorMessage = "Unable to search library folders",
            )
        }
        composeRule.onNodeWithText("Unable to search library folders")
            .assertIsDisplayed()
    }

    @Test
    fun folderSearchRendersStaleCatalogStatusWithRetainedResults() {
        composeRule.setContent {
            MaterialTheme {
                FoldersScreen(
                    state = folderState.copy(
                        searchQuery = "dune",
                        searchResults = listOf(
                            RawSearchEntryDto(
                                id = "00000000-0000-4000-8000-000000000011",
                                name = "Dune.m4b",
                                relativePath = "Loose Files/Dune.m4b",
                                kind = "file",
                            ),
                        ),
                        isSearchCatalogStale = true,
                        searchCatalogStatusMessage = STALE_CATALOG_STATUS,
                    ),
                    onAction = {},
                )
            }
        }

        composeRule.onNodeWithText(STALE_CATALOG_STATUS).assertIsDisplayed()
        composeRule.onNodeWithText("Dune.m4b").assertIsDisplayed()
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

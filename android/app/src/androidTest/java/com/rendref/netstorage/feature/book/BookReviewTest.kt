package com.rendref.netstorage.feature.book

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToIndex
import com.rendref.netstorage.core.model.BookDetailDto
import com.rendref.netstorage.core.model.BookFileDto
import com.rendref.netstorage.core.model.MetadataAttributionDto
import com.rendref.netstorage.core.model.MetadataCandidateDto
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test

class BookReviewTest {
    @get:Rule
    val composeRule = createComposeRule()

    @Test
    fun detailShowsMetadataAssignedFilesAndOnlyApprovedActions() {
        composeRule.setContent {
            MaterialTheme {
                BookDetailScreen(reviewState, onAction = {})
            }
        }

        listOf(
            "Dune",
            "Frank Herbert",
            "Dune · 1",
            "Narrated by Simon Vance",
            "Ace",
            "1965",
            "Science Fiction",
            "isbn: 9780441172719",
            "Open Library",
            "part-1.m4b",
            "View raw files",
            "Choose metadata match",
            "Review grouping",
        ).forEach {
            composeRule.onNodeWithText(it).performScrollTo().assertIsDisplayed()
        }
        assertNoMutationActions()
    }

    @Test
    fun metadataConfirmationExplainsCoverPublicationBeforeAction() {
        val actions = mutableListOf<BookReviewAction>()
        var renderedState by mutableStateOf(reviewState)
        composeRule.setContent {
            MaterialTheme {
                MatchPickerScreen(renderedState, onAction = actions::add)
            }
        }

        composeRule.onNodeWithTag("match-picker-list").performScrollToIndex(5)
        composeRule.onNodeWithText("Open Library").performScrollTo().assertIsDisplayed()
        composeRule.onNodeWithText("1965").performScrollTo().assertIsDisplayed()
        composeRule.onNodeWithText("Science Fiction").performScrollTo().assertIsDisplayed()
        composeRule.onNodeWithText("Space opera").performScrollTo().assertIsDisplayed()
        composeRule.onNodeWithText("96% match").performScrollTo().assertIsDisplayed()
        composeRule.onNodeWithText("Use this match").performScrollTo().performClick()
        assertEquals(
            BookReviewAction.RequestMatch(MATCH_ID),
            actions.single(),
        )

        composeRule.runOnIdle {
            renderedState = reviewState.copy(pendingMatchId = MATCH_ID)
        }
        composeRule.onNodeWithText(
            "The server may publish this cover in the Windows book folder. " +
                "Audio files and tags will not change.",
        ).assertIsDisplayed()
        composeRule.onNodeWithText("Confirm metadata and cover").performClick()
        assertEquals(BookReviewAction.ConfirmCoverPublication, actions.last())
        assertNoMutationActions()
    }

    @Test
    fun reviewScreensShowProgressErrorsAndSuccessAndDisableSearchWhileLoading() {
        var renderedScreen by mutableStateOf("detail")
        var renderedState by mutableStateOf(BookReviewState(isLoading = true))
        composeRule.setContent {
            MaterialTheme {
                when (renderedScreen) {
                    "detail" -> BookDetailScreen(renderedState, onAction = {})
                    "matches" -> MatchPickerScreen(renderedState, onAction = {})
                    else -> GroupingScreen(renderedState, onAction = {})
                }
            }
        }
        composeRule.onNodeWithText("Loading audiobook details...")
            .assertIsDisplayed()

        composeRule.runOnIdle {
            renderedScreen = "matches"
            renderedState = reviewState.copy(isLoading = true)
        }
        composeRule.onNodeWithTag("match-picker-list").performScrollToIndex(4)
        composeRule.onNodeWithText("Searching metadata providers...")
            .performScrollTo()
            .assertIsDisplayed()
        composeRule.onNodeWithText("Search providers").assertIsNotEnabled()

        composeRule.runOnIdle {
            renderedState = reviewState.copy(
                isLoading = false,
                errorMessage = "Unable to search metadata providers",
            )
        }
        composeRule.onNodeWithTag("match-picker-list").performScrollToIndex(4)
        composeRule.onNodeWithText("Unable to search metadata providers")
            .performScrollTo()
            .assertIsDisplayed()

        composeRule.runOnIdle {
            renderedScreen = "grouping"
            renderedState = reviewState.copy(
                pane = ReviewPane.GROUPING,
                message = "Grouping saved in the catalog.",
            )
        }
        composeRule.onNodeWithText("Grouping saved in the catalog.")
            .performScrollTo()
            .assertIsDisplayed()
    }

    @Test
    fun groupingShowsEveryAudioAssignmentWithoutRenameOrMoveControls() {
        val actions = mutableListOf<BookReviewAction>()
        composeRule.setContent {
            MaterialTheme {
                GroupingScreen(
                    reviewState.copy(
                        pane = ReviewPane.GROUPING,
                        groups = listOf(
                            GroupingDraft(
                                1,
                                BOOK_ID,
                                setOf(FILE_ONE_ID),
                                title = "Dune",
                            ),
                            GroupingDraft(
                                2,
                                null,
                                setOf(FILE_TWO_ID),
                                title = "Dune Messiah",
                            ),
                        ),
                        canSaveGrouping = true,
                    ),
                    onAction = actions::add,
                )
            }
        }

        composeRule.onNodeWithText("Logical book 1").assertIsDisplayed()
        composeRule.onNodeWithText("Logical book 2").assertIsDisplayed()
        composeRule.onNodeWithText("Dune Messiah").assertIsDisplayed()
        composeRule.onNodeWithContentDescription(
            "Assign part-1.m4b to logical book 1",
        ).assertIsDisplayed()
        composeRule.onNodeWithContentDescription(
            "Assign part-2.m4b to logical book 2",
        ).assertIsDisplayed()
        composeRule.onAllNodesWithText("part-1.m4b").assertCountEquals(2)
        composeRule.onAllNodesWithText("part-2.m4b").assertCountEquals(2)
        composeRule.onNodeWithText("Add logical book").assertIsDisplayed()
        composeRule.onNodeWithText("Save catalog grouping").performClick()
        assertEquals(BookReviewAction.SaveGrouping, actions.last())
        assertNoMutationActions()
    }

    @Test
    fun groupingCanCombineAnyLogicalBookIntoAnyOtherLogicalBook() {
        val actions = mutableListOf<BookReviewAction>()
        composeRule.setContent {
            MaterialTheme {
                GroupingScreen(
                    reviewState.copy(
                        pane = ReviewPane.GROUPING,
                        groups = listOf(
                            GroupingDraft(1, BOOK_ID, setOf(FILE_ONE_ID)),
                            GroupingDraft(2, null, setOf(FILE_TWO_ID)),
                            GroupingDraft(3, null, emptySet()),
                        ),
                    ),
                    onAction = actions::add,
                )
            }
        }

        composeRule.onAllNodesWithText("Combine with another logical book")[0]
            .performScrollTo()
            .performClick()
        composeRule.onNodeWithText("Combine into logical book 3").performClick()

        assertEquals(BookReviewAction.MergeGroups(1, 3), actions.last())
        assertNoMutationActions()
    }

    private fun assertNoMutationActions() {
        listOf("Rename", "Move", "Delete", "Upload", "Edit tags", "New folder")
            .forEach {
                composeRule.onAllNodesWithText(it, substring = true)
                    .assertCountEquals(0)
            }
    }
}

private val reviewState = BookReviewState(
    detail = BookDetailDto(
        id = BOOK_ID,
        rootId = "audiobooks",
        title = "Dune",
        subtitle = "A Novel",
        authors = listOf("Frank Herbert"),
        reviewState = "confirmed",
        sourceFolder = "Frank Herbert/Dune",
        series = "Dune",
        seriesPosition = "1",
        narrators = listOf("Simon Vance"),
        publisher = "Ace",
        publishedDate = "1965",
        publicationYear = 1965,
        genres = listOf("Science Fiction"),
        identifiers = mapOf("isbn" to "9780441172719"),
        attribution = MetadataAttributionDto("open_library", "Open Library"),
    ),
    files = listOf(
        bookFile(FILE_ONE_ID, "part-1.m4b"),
        bookFile(FILE_TWO_ID, "part-2.m4b"),
    ),
    groupingFiles = listOf(
        bookFile(FILE_ONE_ID, "part-1.m4b"),
        bookFile(FILE_TWO_ID, "part-2.m4b"),
    ),
    matches = listOf(
        MetadataCandidateDto(
            matchId = MATCH_ID,
            provider = "open_library",
            providerId = "OL1",
            title = "Dune",
            authors = listOf("Frank Herbert"),
            publishedDate = "1965",
            subjects = listOf("Science Fiction", "Space opera"),
            score = 0.96,
        ),
    ),
    searchTitle = "Dune",
    searchAuthor = "Frank Herbert",
    pane = ReviewPane.MATCHES,
    isLoading = false,
)

private fun bookFile(id: String, name: String) = BookFileDto(
    id,
    "Frank Herbert/Dune/$name",
    name,
    100,
    "2026-07-26T00:00:00Z",
)

private const val BOOK_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
private const val MATCH_ID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
private const val FILE_ONE_ID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
private const val FILE_TWO_ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"

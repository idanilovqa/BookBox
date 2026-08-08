package com.rendref.bookbox

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.SnackbarHostState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.test.performTextReplacement
import com.rendref.bookbox.core.model.BookCardDto
import com.rendref.bookbox.core.model.BookSearchGroupDto
import com.rendref.bookbox.core.model.BookDetailDto
import com.rendref.bookbox.core.model.BookFileDto
import com.rendref.bookbox.core.model.BrowseEntryDto
import com.rendref.bookbox.core.model.RootDto
import com.rendref.bookbox.core.model.RawSearchEntryDto
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.feature.book.BookDetailScreen
import com.rendref.bookbox.feature.book.BookReviewAction
import com.rendref.bookbox.feature.book.BookReviewState
import com.rendref.bookbox.feature.book.MatchPickerScreen
import com.rendref.bookbox.feature.book.ReviewPane
import com.rendref.bookbox.feature.connect.ConnectScreen
import com.rendref.bookbox.feature.connect.ConnectState
import com.rendref.bookbox.feature.folders.FolderAction
import com.rendref.bookbox.feature.folders.FoldersScreen
import com.rendref.bookbox.feature.folders.FoldersState
import com.rendref.bookbox.feature.library.LibraryAction
import com.rendref.bookbox.feature.library.LibraryScreen
import com.rendref.bookbox.feature.library.LibraryState
import com.rendref.bookbox.feature.settings.ScanStatus
import com.rendref.bookbox.feature.settings.SettingsAction
import com.rendref.bookbox.feature.settings.SettingsScreen
import com.rendref.bookbox.feature.settings.SettingsUiState
import com.rendref.bookbox.feature.transfers.ConflictScreen
import com.rendref.bookbox.feature.transfers.ConflictState
import com.rendref.bookbox.feature.transfers.TransferRowState
import com.rendref.bookbox.feature.transfers.TransferSetupContent
import com.rendref.bookbox.feature.transfers.TransferSetupState
import com.rendref.bookbox.feature.transfers.TransfersScreen
import com.rendref.bookbox.feature.transfers.TransfersState
import com.rendref.bookbox.core.transfer.ConflictChoice
import com.rendref.bookbox.core.transfer.TransferState
import androidx.test.espresso.Espresso.closeSoftKeyboard
import org.junit.Rule
import org.junit.Test

class AppShellJourneyTest {
    @get:Rule
    val compose = createComposeRule()

    @Test
    fun firstRunToTransferSettingsSignOutAndRememberedServerReopen() {
        compose.setContent {
            MaterialTheme {
                JourneyHarness()
            }
        }

        compose.onNode(hasSetTextAction() and androidx.compose.ui.test.hasText("Server"))
            .performTextReplacement("192.168.1.25")
        compose.onNode(hasSetTextAction() and androidx.compose.ui.test.hasText("Username"))
            .performTextInput("listener")
        compose.onNode(hasSetTextAction() and androidx.compose.ui.test.hasText("Password"))
            .performTextInput("correct horse battery staple")
        compose.onNodeWithText("Connect").performClick()

        compose.onNodeWithText("Audiobooks").assertIsDisplayed()
        compose.onNode(hasSetTextAction() and androidx.compose.ui.test.hasText("Search audiobooks"))
            .performTextInput("Dune")
        closeSoftKeyboard()
        compose.onNodeWithText("View raw files")
            .performScrollTo()
            .assertIsDisplayed()
            .performClick()
        compose.onAllNodesWithText("RAW FOLDERS").assertCountEquals(1)
        compose.onAllNodesWithText("Dune.m4b").assertCountEquals(1)
        compose.onNodeWithText("Return to audiobook").performClick()
        compose.onNodeWithText("Choose metadata match").performClick()
        compose.onNodeWithText("Choose metadata match").assertIsDisplayed()
        compose.onNodeWithText("Search providers").performClick()

        compose.onNodeWithText("Audiobooks").performClick()
        compose.onNodeWithContentDescription("Select Dune")
            .performScrollTo()
            .assertIsDisplayed()
            .performClick()
        compose.onNodeWithText("Copy 1 selected").assertIsDisplayed()
        compose.onNodeWithText("Copy 1 selected").performClick()
        compose.onNodeWithText("Copy to Android").assertIsDisplayed()
        compose.onNodeWithText("Choose Android folder").performClick()
        compose.onNodeWithText("Destination: Audiobooks").assertIsDisplayed()
        compose.onNodeWithText("Copy 1 files").performClick()

        compose.onNodeWithText("File already exists").assertIsDisplayed()
        compose.onNodeWithText("Keep both").performClick()
        compose.onNodeWithText("Dune").assertIsDisplayed()
        compose.onNodeWithText(
            "512 B of 1.0 KB",
            substring = true,
        ).assertIsDisplayed()

        compose.onNodeWithText("Settings").performClick()
        compose.onNodeWithText("Saved server").assertIsDisplayed()
        compose.onNodeWithText("Android destination").assertIsDisplayed()
        compose.onNode(hasScrollToIndexAction())
            .performScrollToNode(hasText("Sign in again"))
        compose.onNodeWithText("Sign in again").performClick()
        compose.onNodeWithText("192.168.1.25").assertIsDisplayed()
        compose.onNode(hasSetTextAction() and androidx.compose.ui.test.hasText("Password"))
            .performTextInput("new session")
        compose.onNodeWithText("Connect").performClick()
        compose.onNodeWithText("Audiobooks").assertIsDisplayed()
    }

	@Test
	fun duplicateChildOpensItsRawFileParentAndHighlightsTheFile() {
		compose.setContent { MaterialTheme { DuplicateRawJourneyHarness() } }

		compose.onNodeWithText("2 copies").performClick()
		compose.onNodeWithContentDescription(
			"Select Dune from archive, Alternate/Dune",
		).performClick()
		compose.onNodeWithText("1 selected").assertIsDisplayed()
		compose.onNodeWithContentDescription(
			"View raw files for Dune from archive, Alternate/Dune",
		).performClick()
		compose.onNodeWithText("Loose/Dune-alt.m4b").performClick()
		compose.onNodeWithContentDescription("Highlighted search result").assertIsDisplayed()
		compose.onNodeWithText("Opened file").assertIsDisplayed()
	}
}

private enum class JourneyStage {
    Connect,
    Main,
    Setup,
    Conflict,
}

@Composable
private fun JourneyHarness() {
    var stage by remember { mutableStateOf(JourneyStage.Connect) }
    var connectState by remember {
        mutableStateOf<ConnectState>(ConnectState.EnterCredentials())
    }
    var route by remember { mutableStateOf(AppRoute.Library) }
    var selected by remember { mutableStateOf(false) }
    var search by remember { mutableStateOf("") }
    var reviewPane by remember { mutableStateOf(ReviewPane.DETAIL) }
    var destination by remember { mutableStateOf<String?>(null) }
    val snackbar = remember { SnackbarHostState() }

    when (stage) {
        JourneyStage.Connect -> ConnectScreen(
            state = connectState,
            onConnect = { _, _, _ ->
                stage = JourneyStage.Main
                route = AppRoute.Library
            },
            onRetry = {},
        )
        JourneyStage.Setup -> TransferSetupContent(
            state = TransferSetupState(
                destinationLabel = destination,
                fileCount = 1,
                totalBytes = 1_024,
            ),
            onChooseDestination = { destination = "Audiobooks" },
            onStart = { stage = JourneyStage.Conflict },
        )
        JourneyStage.Conflict -> ConflictScreen(
            state = ConflictState(
                destinationPath = "Dune/Dune.m4b",
                existingBytes = 900,
                incomingBytes = 1_024,
            ),
            onChoice = {
                check(it in ConflictChoice.entries)
                stage = JourneyStage.Main
                route = AppRoute.Transfers
            },
        )
        JourneyStage.Main -> AppScaffold(
            route = route,
            onRoute = { route = it },
            snackbarHostState = snackbar,
            copyLabel = if (route == AppRoute.Library && selected) {
                "Copy 1 selected"
            } else {
                null
            },
            onCopy = { stage = JourneyStage.Setup },
        ) { modifier ->
            when (route) {
                AppRoute.Library -> LibraryScreen(
                    state = LibraryState(
                        query = BookQuery(text = search),
                        books = listOf(BOOK),
						groups = listOf(BookSearchGroupDto(BOOK.id, false, listOf(BOOK))),
                        selectedBookIds = if (selected) setOf(BOOK.id) else emptySet(),
                    ),
                    onSearchTextChange = { search = it },
                    onAction = { action ->
                        when (action) {
                            is LibraryAction.ToggleSelected -> selected = !selected
                            is LibraryAction.ViewRawFiles -> route = AppRoute.Folders
                            is LibraryAction.OpenBook -> route = AppRoute.BookDetail
                        }
                    },
                    onAuthorChange = {},
                    onGenreChange = {},
                    onYearChange = {},
                    onRootChange = {},
                    onReviewStateChange = {},
                    onSortChange = {},
                    onLoadNextPage = {},
                    onGridPositionChange = { _, _ -> },
                    modifier = modifier,
                )
                AppRoute.Folders -> FoldersScreen(
                    state = FOLDERS,
                    onAction = { action ->
                        if (action is FolderAction.ReturnToBook) {
                            route = AppRoute.BookDetail
                        }
                    },
                    modifier = modifier,
                )
                AppRoute.BookDetail -> {
                    val state = DETAIL.copy(pane = reviewPane)
                    val action: (BookReviewAction) -> Unit = {
                        when (it) {
                            BookReviewAction.ChooseMetadata -> reviewPane = ReviewPane.MATCHES
                            BookReviewAction.ShowDetail -> reviewPane = ReviewPane.DETAIL
                            is BookReviewAction.ViewRawFiles -> route = AppRoute.Folders
                            else -> Unit
                        }
                    }
                    if (reviewPane == ReviewPane.MATCHES) {
                        MatchPickerScreen(state, action, modifier)
                    } else {
                        BookDetailScreen(state, action, modifier)
                    }
                }
                AppRoute.Transfers -> TransfersScreen(
                    state = TRANSFERS,
                    onAction = { _, _ -> },
                    modifier = modifier,
                )
                AppRoute.Settings -> SettingsScreen(
                    state = SETTINGS,
                    onAction = {
                        if (it == SettingsAction.SignOut) {
                            stage = JourneyStage.Connect
                            connectState = ConnectState.EnterCredentials(
                                server = "192.168.1.25",
                                username = "listener",
                            )
                        }
                    },
                    modifier = modifier,
                )
                AppRoute.TransferSetup -> error("Setup has its own journey stage")
                AppRoute.Conflict -> error("Conflict has its own journey stage")
            }
        }
    }
}

@Composable
private fun DuplicateRawJourneyHarness() {
	var route by remember { mutableStateOf(AppRoute.Library) }
	var selected by remember { mutableStateOf(emptySet<String>()) }
	var folders by remember {
		mutableStateOf(
			FoldersState(
				roots = listOf(RootDto("archive", "Archive")),
				selectedRootId = "archive",
				searchQuery = "dune",
				searchResults = listOf(RAW_ALTERNATE),
			),
		)
	}
	when (route) {
		AppRoute.Library -> LibraryScreen(
			state = LibraryState(
				groups = listOf(
					BookSearchGroupDto(
						DUPLICATE_GROUP_ID,
						true,
						listOf(ARCHIVE_BOOK, ALTERNATE_BOOK),
					),
				),
				selectedBookIds = selected,
			),
			onSearchTextChange = {},
			onAction = { action ->
				when (action) {
					is LibraryAction.ToggleSelected -> selected = selected + action.bookId
					is LibraryAction.ViewRawFiles -> route = AppRoute.Folders
					is LibraryAction.OpenBook -> Unit
				}
			},
			onAuthorChange = {}, onGenreChange = {}, onYearChange = {}, onRootChange = {},
			onReviewStateChange = {}, onSortChange = {}, onLoadNextPage = {},
			onGridPositionChange = { _, _ -> },
		)
		AppRoute.Folders -> FoldersScreen(
			state = folders,
			onAction = { action ->
				if (action is FolderAction.OpenSearchResult) {
					folders = folders.copy(
						searchQuery = "",
						searchResults = emptyList(),
						path = "Loose",
						entries = listOf(
							BrowseEntryDto("Dune-alt.m4b", action.relativePath, "file", 2_048, "2026-08-08T12:00:00Z"),
						),
						searchHighlightedRelativePath = action.relativePath,
					)
				}
			},
		)
		else -> error("Unexpected route")
	}
}

private val BOOK = BookCardDto(
    id = "11111111-1111-4111-8111-111111111111",
    title = "Dune",
    authors = listOf("Frank Herbert"),
    publicationYear = 1965,
    genres = listOf("Science fiction"),
    reviewState = "confirmed",
    sourceFolder = "Frank Herbert/Dune",
)

private const val DUPLICATE_GROUP_ID = "55555555-5555-4555-8555-555555555555"

private val ARCHIVE_BOOK = BOOK.copy(rootId = "archive")

private val ALTERNATE_BOOK = ARCHIVE_BOOK.copy(
	id = "66666666-6666-4666-8666-666666666666",
	rootId = "archive",
	sourceFolder = "Alternate/Dune",
)

private val RAW_ALTERNATE = RawSearchEntryDto(
	id = "77777777-7777-4777-8777-777777777777",
	name = "Dune-alt.m4b",
	relativePath = "Loose/Dune-alt.m4b",
	kind = "file",
	size = 2_048,
	modifiedAt = "2026-08-08T12:00:00Z",
	bookId = ALTERNATE_BOOK.id,
	duplicateBook = true,
)

private val FOLDERS = FoldersState(
    roots = listOf(RootDto("audiobooks", "Audiobooks")),
    selectedRootId = "audiobooks",
    path = "Frank Herbert/Dune",
    entries = listOf(
        BrowseEntryDto(
            name = "Dune.m4b",
            relativePath = "Frank Herbert/Dune/Dune.m4b",
            kind = "file",
            size = 1_024,
            modifiedAt = "2026-07-26T00:00:00Z",
        ),
    ),
    highlightedFileIds = setOf("22222222-2222-4222-8222-222222222222"),
    assignedRelativePaths = setOf("Frank Herbert/Dune/Dune.m4b"),
    originBookId = BOOK.id,
)

private val DETAIL = BookReviewState(
    pane = ReviewPane.DETAIL,
    detail = BookDetailDto(
        id = BOOK.id,
        rootId = "audiobooks",
        title = BOOK.title,
        authors = BOOK.authors,
        reviewState = BOOK.reviewState,
        sourceFolder = BOOK.sourceFolder,
        publicationYear = BOOK.publicationYear,
        genres = BOOK.genres,
    ),
    files = listOf(
        BookFileDto(
            id = "22222222-2222-4222-8222-222222222222",
            relativePath = "Frank Herbert/Dune/Dune.m4b",
            name = "Dune.m4b",
            size = 1_024,
            modifiedAt = "2026-07-26T00:00:00Z",
        ),
    ),
    searchTitle = "Dune",
    searchAuthor = "Frank Herbert",
    isLoading = false,
)

private val TRANSFERS = TransfersState(
    transfers = listOf(
        TransferRowState(
            id = "33333333-3333-4333-8333-333333333333",
            title = "Dune",
            state = TransferState.Running,
            bytesWritten = 512,
            totalBytes = 1_024,
            completedFiles = 0,
            totalFiles = 1,
            currentFile = "Dune.m4b",
            availableActions = emptySet(),
        ),
    ),
)

private val SETTINGS = SettingsUiState(
    host = "192.168.1.25",
    username = "listener",
    destinationLabel = "Audiobooks",
    appVersion = "1.0.0",
    serverVersion = "1.0.0",
    scanStatus = ScanStatus.Complete,
)

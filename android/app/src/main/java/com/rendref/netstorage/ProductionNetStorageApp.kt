package com.rendref.netstorage

import android.Manifest
import android.app.job.JobScheduler
import android.content.ClipData
import android.content.ClipboardManager
import android.content.pm.PackageManager
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.createSavedStateHandle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.lifecycle.viewmodel.initializer
import androidx.lifecycle.viewmodel.viewModelFactory
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.rememberNavBackStack
import com.rendref.netstorage.core.network.TransferSelection
import com.rendref.netstorage.core.transfer.FileState
import com.rendref.netstorage.core.transfer.TransferStartRequest
import com.rendref.netstorage.core.transfer.TransferStartResult
import com.rendref.netstorage.core.transfer.TransferState
import com.rendref.netstorage.feature.book.BookDetailScreen
import com.rendref.netstorage.feature.book.BookDetailViewModel
import com.rendref.netstorage.feature.book.BookReviewAction
import com.rendref.netstorage.feature.book.GroupingScreen
import com.rendref.netstorage.feature.book.MatchPickerScreen
import com.rendref.netstorage.feature.book.ReviewPane
import com.rendref.netstorage.feature.connect.ConnectScreen
import com.rendref.netstorage.feature.connect.ConnectState
import com.rendref.netstorage.feature.folders.FolderAction
import com.rendref.netstorage.feature.folders.FoldersScreen
import com.rendref.netstorage.feature.folders.FoldersViewModel
import com.rendref.netstorage.feature.library.LibraryAction
import com.rendref.netstorage.feature.library.LibraryScreen
import com.rendref.netstorage.feature.library.LibraryViewModel
import com.rendref.netstorage.feature.settings.SettingsAction
import com.rendref.netstorage.feature.settings.SettingsScreen
import com.rendref.netstorage.feature.settings.SettingsViewModel
import com.rendref.netstorage.feature.transfers.ConflictScreen
import com.rendref.netstorage.feature.transfers.ConflictState
import com.rendref.netstorage.feature.transfers.PersistableOpenDocumentTree
import com.rendref.netstorage.feature.transfers.TransferActionController
import com.rendref.netstorage.feature.transfers.TransferActionResult
import com.rendref.netstorage.feature.transfers.TransferSetupContent
import com.rendref.netstorage.feature.transfers.TransferSetupState
import com.rendref.netstorage.feature.transfers.TransfersScreen
import com.rendref.netstorage.feature.transfers.TransfersViewModel
import kotlinx.coroutines.launch
import kotlinx.serialization.Serializable

@Serializable
internal enum class AppRoute(
    val label: String,
    val shortLabel: String,
) : NavKey {
    Library("Audiobooks", "A"),
    Folders("Raw folders", "F"),
    Transfers("Transfers", "T"),
    Settings("Settings", "S"),
    BookDetail("Book", "B"),
    TransferSetup("Copy", "C"),
    Conflict("Conflict", "!"),
}

private data class CopyDraft(
    val selections: List<TransferSelection>,
    val displayName: String,
) : java.io.Serializable

internal fun interface NotificationPermissionRequester {
    fun request(onResult: (Boolean) -> Unit)
}

internal fun startAfterNotificationDecision(
    requester: NotificationPermissionRequester,
    onDenied: () -> Unit,
    start: () -> Unit,
) {
    var handled = false
    requester.request { granted ->
        if (handled) return@request
        handled = true
        if (!granted) onDenied()
        start()
    }
}

@Composable
internal fun ProductionNetStorageApp() {
    val context = LocalContext.current
    val runtime = remember(context) { NetStorageRuntime.get(context) }
    var connectionGeneration by remember { mutableIntStateOf(0) }
    val connect = remember(runtime, connectionGeneration) {
        runtime.connectViewModel()
    }
    val connectState by connect.state.collectAsState()
    val saved by runtime.connections.state.collectAsState(initial = null)

    MaterialTheme(
        colorScheme = if (isSystemInDarkTheme()) DarkColors else LightColors,
    ) {
        val connected = saved
        if (connectState == ConnectState.Ready && connected != null) {
            ConnectedApp(
                runtime = remember(connected) { runtime.connected(connected) },
                onConnectionReset = { connectionGeneration += 1 },
            )
        } else {
            ConnectScreen(
                state = connectState,
                onInspectServer = connect::inspectServer,
                onApproveFingerprint = connect::approveFingerprint,
                onSignIn = connect::signIn,
                onRetry = connect::retry,
                modifier = Modifier.fillMaxSize(),
            )
        }
    }
}

@Composable
internal fun ConnectedApp(
    runtime: ConnectedRuntime,
    onConnectionReset: () -> Unit,
    notificationPermissionRequester: NotificationPermissionRequester? = null,
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val snackbar = remember { SnackbarHostState() }
    val backStack = rememberNavBackStack(AppRoute.Library)
    val route = backStack.last() as AppRoute
    var currentBookId by rememberSaveable { mutableStateOf<String?>(null) }
    var conflictTransferId by rememberSaveable { mutableStateOf<String?>(null) }
    var copyDraft by rememberSaveable { mutableStateOf<CopyDraft?>(null) }
    var settingsGeneration by rememberSaveable { mutableIntStateOf(0) }
    var permissionRequestPending by rememberSaveable { mutableStateOf(false) }
    var permissionDecision by rememberSaveable { mutableStateOf<Boolean?>(null) }

    val library: LibraryViewModel = viewModel(
        key = "${runtime.instanceKey}-library",
        factory = viewModelFactory {
            initializer {
                LibraryViewModel(createSavedStateHandle(), runtime.catalog)
            }
        },
    )
    val folders: FoldersViewModel = viewModel(
        key = "${runtime.instanceKey}-folders",
        factory = viewModelFactory {
            initializer { FoldersViewModel(runtime.browse) }
        },
    )
    val transferController = remember(runtime, context) {
        TransferActionController(
            journal = runtime.journal,
            scheduler = runtime.scheduler,
            destination = { runtime.destination.requireTree() },
            cancelJob = { id ->
                context.getSystemService(JobScheduler::class.java).cancel(id)
            },
            openConflict = { id ->
                conflictTransferId = id
                if (backStack.lastOrNull() != AppRoute.Conflict) {
                    backStack.add(AppRoute.Conflict)
                }
            },
        )
    }
    val transfers: TransfersViewModel = viewModel(
        key = "${runtime.instanceKey}-transfers",
        factory = viewModelFactory {
            initializer {
                TransfersViewModel(
                    history = runtime.journal::list,
                    action = transferController::perform,
                )
            }
        },
    )
    val settings: SettingsViewModel = viewModel(
        key = "${runtime.instanceKey}-settings-$settingsGeneration",
        factory = viewModelFactory {
            initializer { SettingsViewModel(runtime.settings) }
        },
    )
    val detail = currentBookId?.let { bookId ->
        viewModel<BookDetailViewModel>(
            key = "${runtime.instanceKey}-book-$bookId",
            factory = viewModelFactory {
                initializer { BookDetailViewModel(bookId, runtime.reviews) }
            },
        )
    }

    val libraryState by library.state.collectAsState()
    val folderState by folders.state.collectAsState()
    val transferState by transfers.state.collectAsState()
    val settingsState by settings.state.collectAsState()
    val detailState = detail?.state?.collectAsState()?.value

    val destinationLauncher = rememberLauncherForActivityResult(
        contract = PersistableOpenDocumentTree(),
    ) { uri ->
        if (uri != null) {
            scope.launch {
                runCatching { runtime.destination.persist(uri) }
                    .onSuccess {
                        settingsGeneration += 1
                        snackbar.showSnackbar("Android destination saved")
                    }
                    .onFailure {
                        snackbar.showSnackbar("Unable to save that Android destination")
                    }
            }
        }
    }
    val notificationLauncher = rememberLauncherForActivityResult(
        contract = ActivityResultContracts.RequestPermission(),
    ) { granted ->
        permissionDecision = granted
    }

    LaunchedEffect(settingsState.signedOut, settingsState.serverChanged) {
        if (settingsState.signedOut || settingsState.serverChanged) {
            onConnectionReset()
        }
    }
    LaunchedEffect(route) {
        if (route == AppRoute.Transfers) transfers.refresh()
    }
    BackHandler(enabled = backStack.size > 1) {
        backStack.removeLastOrNull()
    }

    fun openBook(bookId: String) {
        currentBookId = bookId
        if (backStack.lastOrNull() != AppRoute.BookDetail) {
            backStack.add(AppRoute.BookDetail)
        }
    }

    fun openRawFiles(bookId: String) {
        folders.viewRawFiles(bookId)
        if (backStack.lastOrNull() != AppRoute.Folders) {
            backStack.add(AppRoute.Folders)
        }
    }

    fun startCopy(selections: List<TransferSelection>, displayName: String) {
        if (selections.isEmpty()) return
        scope.launch {
            val message = runCatching {
                runtime.starter.startFromVisibleCopyAction(
                    TransferStartRequest(selections, displayName),
                )
            }.fold(
                onSuccess = { result ->
                    when (result) {
                        is TransferStartResult.Scheduled -> "Copy scheduled"
                        is TransferStartResult.ScheduleRejected ->
                            "Copy saved but Android could not schedule it"
                    }
                },
                onFailure = { "Choose an Android destination before copying" },
            )
            snackbar.showSnackbar(message)
            transfers.refresh()
            backStack.clear()
            backStack.add(AppRoute.Transfers)
        }
    }

    fun startDraftAfterPermission(draft: CopyDraft, granted: Boolean) {
        if (!granted) {
            scope.launch {
                snackbar.showSnackbar(
                    "Notifications are off. Copy progress remains " +
                        "in netStorage and Android Task Manager.",
                )
            }
        }
        startCopy(draft.selections, draft.displayName)
        copyDraft = null
    }

    LaunchedEffect(permissionRequestPending, permissionDecision, copyDraft) {
        val decision = permissionDecision
        val draft = copyDraft
        if (permissionRequestPending && decision != null) {
            permissionRequestPending = false
            permissionDecision = null
            draft?.let { startDraftAfterPermission(it, decision) }
        }
    }

    val copyLabel = when (route) {
        AppRoute.Library -> libraryState.selectedBookIds
            .takeIf { it.isNotEmpty() }
            ?.let { "Copy ${it.size} selected" }
        AppRoute.Folders -> folderState.selectedFolderPaths
            .takeIf { it.isNotEmpty() }
            ?.let { "Copy ${it.size} selected" }
        else -> null
    }
    AppScaffold(
        route = route,
        onRoute = {
            backStack.clear()
            backStack.add(it)
        },
        snackbarHostState = snackbar,
        copyLabel = copyLabel,
        onCopy = {
            val draft = when (route) {
                AppRoute.Library -> CopyDraft(
                    selections = libraryState.selectedBookIds.sorted()
                        .map(TransferSelection::Book),
                    displayName = selectedDisplayName(
                        libraryState.selectedBookIds.size,
                        "audiobook",
                    ),
                )
                AppRoute.Folders -> {
                    val rootId = folderState.selectedRootId
                    if (rootId != null) {
                        CopyDraft(
                            selections = folderState.selectedFolderPaths.sorted().map { path ->
                                TransferSelection.Folder(rootId, path)
                            },
                            displayName = selectedDisplayName(
                                folderState.selectedFolderPaths.size,
                                "folder",
                            ),
                        )
                    } else {
                        null
                    }
                }
                else -> null
            }
            if (draft != null) {
                copyDraft = draft
                backStack.add(AppRoute.TransferSetup)
            }
        },
    ) { modifier ->
        when (route) {
            AppRoute.Library -> LibraryScreen(
                state = libraryState,
                onSearchTextChange = library::setSearchText,
                onAction = { action ->
                    when (action) {
                        is LibraryAction.OpenBook -> openBook(action.bookId)
                        is LibraryAction.ToggleSelected ->
                            library.toggleSelection(action.bookId)
                        is LibraryAction.ViewRawFiles -> openRawFiles(action.bookId)
                    }
                },
                onAuthorChange = library::setAuthor,
                onGenreChange = library::setGenre,
                onYearChange = library::setYear,
                onRootChange = library::setRoot,
                onReviewStateChange = library::setReviewState,
                onSortChange = library::setSort,
                onLoadNextPage = library::loadNextPage,
                onGridPositionChange = library::saveGridPosition,
                modifier = modifier,
            )
            AppRoute.Folders -> FoldersScreen(
                state = folderState,
                onAction = { action ->
                    when (action) {
                        is FolderAction.SelectRoot -> folders.selectRoot(action.rootId)
                        is FolderAction.OpenBreadcrumb ->
                            folders.openBreadcrumb(action.path)
                        is FolderAction.OpenFolder -> folders.openFolder(action.relativePath)
                        is FolderAction.ToggleFolder ->
                            folders.toggleFolderSelection(action.relativePath)
                        FolderAction.NavigateBack -> folders.navigateBack()
                        FolderAction.Refresh -> folders.refresh()
                        is FolderAction.ReturnToBook -> {
                            if (
                                backStack.size > 1 &&
                                backStack[backStack.lastIndex - 1] == AppRoute.BookDetail
                            ) {
                                backStack.removeLastOrNull()
                            } else {
                                openBook(action.bookId)
                            }
                        }
                    }
                },
                modifier = modifier,
            )
            AppRoute.Transfers -> TransfersScreen(
                state = transferState,
                onAction = transfers::perform,
                modifier = modifier,
            )
            AppRoute.Settings -> SettingsScreen(
                state = settingsState,
                onAction = { action ->
                    when (action) {
                        SettingsAction.CopyFingerprint -> {
                            settingsState.fingerprintForCopy?.let { fingerprint ->
                                context.getSystemService(ClipboardManager::class.java)
                                    .setPrimaryClip(
                                        ClipData.newPlainText(
                                            "netStorage certificate fingerprint",
                                            fingerprint,
                                        ),
                                    )
                            }
                        }
                        SettingsAction.Rescan -> settings.rescan()
                        SettingsAction.ChooseDestination ->
                            destinationLauncher.launch(null)
                        SettingsAction.SignOut -> settings.signOut()
                        SettingsAction.RequestServerChange ->
                            settings.requestServerChange()
                        SettingsAction.ConfirmServerChange ->
                            settings.confirmServerChange()
                        SettingsAction.CancelServerChange ->
                            settings.cancelServerChange()
                    }
                },
                modifier = modifier,
            )
            AppRoute.BookDetail -> {
                if (detailState == null) {
                    Text("Opening audiobook…", modifier = modifier)
                } else {
                    val onReviewAction: (BookReviewAction) -> Unit = { action ->
                        handleBookReviewAction(
                            viewModel = checkNotNull(detail),
                            action = action,
                            openRawFiles = ::openRawFiles,
                        )
                    }
                    when (detailState.pane) {
                        ReviewPane.DETAIL ->
                            BookDetailScreen(detailState, onReviewAction, modifier)
                        ReviewPane.MATCHES ->
                            MatchPickerScreen(detailState, onReviewAction, modifier)
                        ReviewPane.GROUPING ->
                            GroupingScreen(detailState, onReviewAction, modifier)
                    }
                }
            }
            AppRoute.TransferSetup -> {
                val draft = copyDraft
                if (draft == null) {
                    Text("No copy selection is waiting.", modifier = modifier)
                } else {
                    TransferSetupContent(
                        state = TransferSetupState(
                            destinationLabel = settingsState.destinationLabel,
                            fileCount = null,
                            totalBytes = null,
                        ),
                        onChooseDestination = {
                            destinationLauncher.launch(null)
                        },
                        onStart = {
                            val injected = notificationPermissionRequester
                            when {
                                injected != null -> startAfterNotificationDecision(
                                    requester = injected,
                                    onDenied = {
                                        scope.launch {
                                            snackbar.showSnackbar(
                                                "Notifications are off. Copy progress remains " +
                                                    "in netStorage and Android Task Manager.",
                                            )
                                        }
                                    },
                                    start = {
                                        startCopy(draft.selections, draft.displayName)
                                        copyDraft = null
                                    },
                                )
                                context.checkSelfPermission(
                                    Manifest.permission.POST_NOTIFICATIONS,
                                ) == PackageManager.PERMISSION_GRANTED ->
                                    startDraftAfterPermission(draft, true)
                                else -> {
                                    permissionRequestPending = true
                                    permissionDecision = null
                                    notificationLauncher.launch(
                                        Manifest.permission.POST_NOTIFICATIONS,
                                    )
                                }
                            }
                        },
                        modifier = modifier,
                    )
                }
            }
            AppRoute.Conflict -> ConflictRoute(
                runtime = runtime,
                controller = transferController,
                transferId = conflictTransferId,
                modifier = modifier,
                onResolved = {
                    conflictTransferId = null
                    transfers.refresh()
                    backStack.clear()
                    backStack.add(AppRoute.Transfers)
                },
            )
        }
    }
}

@Composable
internal fun AppScaffold(
    route: AppRoute,
    onRoute: (AppRoute) -> Unit,
    snackbarHostState: SnackbarHostState,
    copyLabel: String?,
    onCopy: () -> Unit,
    content: @Composable (Modifier) -> Unit,
) {
    Scaffold(
        snackbarHost = { SnackbarHost(snackbarHostState) },
        bottomBar = {
            NavigationBar {
                AppRoute.entries
                    .filter { it in TOP_LEVEL_ROUTES }
                    .forEach { destination ->
                        NavigationBarItem(
                            selected = route == destination,
                            onClick = { onRoute(destination) },
                            icon = { Text(destination.shortLabel) },
                            label = { Text(destination.label) },
                        )
                    }
            }
        },
        floatingActionButton = {
            if (copyLabel != null) {
                Button(onClick = onCopy) {
                    Text(copyLabel)
                }
            }
        },
    ) { padding ->
        Box(
            modifier = Modifier
                .fillMaxSize()
                .padding(padding),
        ) {
            content(Modifier.fillMaxSize())
        }
    }
}

@Composable
private fun ConflictRoute(
    runtime: ConnectedRuntime,
    controller: TransferActionController,
    transferId: String?,
    modifier: Modifier,
    onResolved: () -> Unit,
) {
    val record = transferId?.let(runtime.journal::read)
    val waitingIndex = record?.files?.indexOfFirst {
        it.state == FileState.WaitingForConflict
    } ?: -1
    val incoming = record?.manifest?.files?.getOrNull(waitingIndex)
    if (record == null || incoming == null) {
        Text("This conflict is no longer waiting.", modifier = modifier)
    } else {
        ConflictScreen(
            state = ConflictState(
                destinationPath = incoming.destinationPath,
                existingBytes = record.files[waitingIndex].existingBytes,
                incomingBytes = incoming.size,
            ),
            onChoice = { choice ->
                if (
                    controller.chooseConflict(record.id, choice) ==
                    TransferActionResult.Accepted
                ) {
                    onResolved()
                }
            },
            modifier = modifier,
        )
    }
}

private fun handleBookReviewAction(
    viewModel: BookDetailViewModel,
    action: BookReviewAction,
    openRawFiles: (String) -> Unit,
) {
    when (action) {
        is BookReviewAction.ViewRawFiles -> openRawFiles(action.bookId)
        BookReviewAction.ChooseMetadata -> viewModel.openMatchPicker()
        BookReviewAction.ReviewGrouping -> viewModel.openGrouping()
        BookReviewAction.ShowDetail -> viewModel.showDetail()
        is BookReviewAction.UpdateSearch -> viewModel.updateMatchSearch(
            action.title,
            action.author,
            action.isbn,
        )
        BookReviewAction.SearchMatches -> viewModel.searchMatches()
        is BookReviewAction.RequestMatch ->
            viewModel.requestMatchConfirmation(action.matchId)
        BookReviewAction.CancelMatchConfirmation ->
            viewModel.cancelMatchConfirmation()
        BookReviewAction.ConfirmCoverPublication ->
            viewModel.confirmCoverPublication()
        BookReviewAction.AddGroup -> viewModel.addGroup()
        is BookReviewAction.AssignFile ->
            viewModel.assignFileToGroup(action.fileId, action.groupId)
        is BookReviewAction.MergeGroups ->
            viewModel.mergeGroups(action.sourceId, action.targetId)
        BookReviewAction.SaveGrouping -> viewModel.saveGrouping()
    }
}

private fun selectedDisplayName(count: Int, kind: String): String =
    "$count selected $kind${if (count == 1) "" else "s"}"

private val TOP_LEVEL_ROUTES = setOf(
    AppRoute.Library,
    AppRoute.Folders,
    AppRoute.Transfers,
    AppRoute.Settings,
)

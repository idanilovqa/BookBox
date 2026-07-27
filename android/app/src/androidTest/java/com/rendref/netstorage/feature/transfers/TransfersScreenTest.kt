package com.rendref.netstorage.feature.transfers

import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.printToString
import com.rendref.netstorage.core.transfer.TransferState
import org.junit.Rule
import org.junit.Test

class TransfersScreenTest {
    @get:Rule
    val compose = createComposeRule()

    @Test
    fun runningTransferShowsProgressAndAndroidSafeControls() {
        compose.setContent {
            MaterialTheme {
                TransfersScreen(
                    state = TransfersState(
                        transfers = listOf(
                            TransferRowState(
                                id = "4f48f34f-fdde-4dd2-8e41-c73f02224157",
                                title = "Dune",
                                state = TransferState.Running,
                                bytesWritten = 4,
                                totalBytes = 10,
                                completedFiles = 0,
                                totalFiles = 1,
                                currentFile = "Dune/Dune.m4b",
                                availableActions = setOf(
                                    TransferAction.Pause,
                                    TransferAction.Cancel,
                                ),
                            ),
                        ),
                    ),
                    onAction = { _, _ -> },
                )
            }
        }

        compose.onNodeWithText("Transfers").assertIsDisplayed()
        compose.onNodeWithText("Dune").assertIsDisplayed()
        compose.onNodeWithText("Dune/Dune.m4b").assertIsDisplayed()
        compose.onNodeWithText("Pause").assertIsDisplayed()
        compose.onNodeWithText("Cancel").assertIsDisplayed()
        compose.onNode(hasText("Delete Windows", substring = true)).assertDoesNotExist()
        compose.onNode(hasText("Move source", substring = true)).assertDoesNotExist()
        compose.onNode(hasText("Edit tags", substring = true)).assertDoesNotExist()
    }

    @Test
    fun cancelledTransferRequiresExplicitPartialDiscard() {
        compose.setContent {
            MaterialTheme {
                TransfersScreen(
                    state = TransfersState(
                        transfers = listOf(
                            TransferRowState(
                                id = "4f48f34f-fdde-4dd2-8e41-c73f02224157",
                                title = "Dune",
                                state = TransferState.Cancelled,
                                bytesWritten = 4,
                                totalBytes = 10,
                                completedFiles = 0,
                                totalFiles = 1,
                                currentFile = "Dune/Dune.m4b",
                                availableActions = setOf(
                                    TransferAction.DiscardPartialData,
                                ),
                            ),
                        ),
                    ),
                    onAction = { _, _ -> },
                )
            }
        }

        compose.onNodeWithText("Discard partial data").assertIsDisplayed()
        check(
            compose.onRoot().printToString().contains(
                "removes only unfinished files on this Android device",
            ),
        )
    }
}

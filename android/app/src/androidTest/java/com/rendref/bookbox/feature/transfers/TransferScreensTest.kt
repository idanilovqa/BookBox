package com.rendref.bookbox.feature.transfers

import android.content.Intent
import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import com.rendref.bookbox.core.transfer.ConflictChoice
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import androidx.test.platform.app.InstrumentationRegistry

class TransferScreensTest {
    @get:Rule
    val composeRule = createComposeRule()

    @Test
    fun destinationPickerRequestsReadWritePersistableAndPrefixGrants() {
        val intent = PersistableOpenDocumentTree().createIntent(
            InstrumentationRegistry.getInstrumentation().targetContext,
            null,
        )
        val required = Intent.FLAG_GRANT_READ_URI_PERMISSION or
            Intent.FLAG_GRANT_WRITE_URI_PERMISSION or
            Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION or
            Intent.FLAG_GRANT_PREFIX_URI_PERMISSION

        assertEquals(Intent.ACTION_OPEN_DOCUMENT_TREE, intent.action)
        assertTrue(intent.flags and required == required)
    }

    @Test
    fun setupRequiresAWritableAndroidDestinationBeforeStarting() {
        var chooseCalls = 0
        var startCalls = 0
        composeRule.setContent {
            MaterialTheme {
                TransferSetupContent(
                    state = TransferSetupState(
                        destinationLabel = null,
                        fileCount = 2,
                        totalBytes = 1_048_576,
                    ),
                    onChooseDestination = { chooseCalls += 1 },
                    onStart = { startCalls += 1 },
                )
            }
        }

        composeRule.onNodeWithText("Choose Android folder").performClick()
        composeRule.onNodeWithText("Copy 2 files").assertIsNotEnabled()
        composeRule.onNodeWithText("Windows source files stay read-only.").assertIsDisplayed()
        assertEquals(1, chooseCalls)
        assertEquals(0, startCalls)
    }

    @Test
    fun conflictOffersOnlyReplaceKeepBothAndSkipWithAndroidOnlyWarning() {
        val choices = mutableListOf<ConflictChoice>()
        composeRule.setContent {
            MaterialTheme {
                ConflictScreen(
                    state = ConflictState(
                        destinationPath = "Dune/Dune.m4b",
                        existingBytes = 10,
                        incomingBytes = 12,
                    ),
                    onChoice = choices::add,
                )
            }
        }

        composeRule.onNodeWithText(
            "Replace removes only this Android copy. The Windows source is never changed.",
        ).assertIsDisplayed()
        listOf(
            "Replace" to ConflictChoice.Replace,
            "Keep both" to ConflictChoice.KeepBoth,
            "Skip" to ConflictChoice.Skip,
        ).forEach { (label, expected) ->
            composeRule.onNodeWithText(label).performClick()
            assertEquals(expected, choices.last())
        }
    }

    @Test
    fun conflictShowsUnknownSizeForLegacyJournalEntries() {
        composeRule.setContent {
            MaterialTheme {
                ConflictScreen(
                    state = ConflictState(
                        destinationPath = "Dune/Dune.m4b",
                        existingBytes = null,
                        incomingBytes = 12,
                    ),
                    onChoice = {},
                )
            }
        }

        composeRule.onNodeWithText(
            "The Android file differs from the selected copy " +
                "(unknown size existing, 12 B incoming).",
        ).assertIsDisplayed()
    }
}

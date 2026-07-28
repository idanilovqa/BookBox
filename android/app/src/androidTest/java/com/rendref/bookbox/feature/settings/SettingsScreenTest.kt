package com.rendref.bookbox.feature.settings

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasScrollToIndexAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test

class SettingsScreenTest {
    @get:Rule
    val composeRule = createComposeRule()

    @Test
    fun settingsShowsDiagnosticsAndApprovedActionsWithoutSensitiveValues() {
        val actions = mutableListOf<SettingsAction>()
        var state by mutableStateOf(settingsState)
        composeRule.setContent {
            MaterialTheme {
                SettingsScreen(state, onAction = actions::add)
            }
        }
        fun scrollToDisplayed(text: String) {
            composeRule.onNode(hasScrollToIndexAction())
                .performScrollToNode(hasText(text))
            val node = composeRule.onNodeWithText(text)
            composeRule.waitUntil(timeoutMillis = 5_000) {
                node.isDisplayed()
            }
            node.assertIsDisplayed()
        }

        listOf(
            "Settings",
            "nas.home:9443",
            "owner",
            "Audiobooks",
            "App version 0.1.0",
            "Server version 1.0.0",
            "Scan complete",
            "Rescan",
            "Choose destination",
            "Sign in again",
            "Change server",
            "Reset saved connection",
        ).forEach {
            scrollToDisplayed(it)
        }
        listOf("content://", "H:/", "password", "token").forEach {
            composeRule.onAllNodesWithText(it, substring = true)
                .assertCountEquals(0)
        }

        listOf(
            "Rescan",
            "Choose destination",
            "Sign in again",
        ).forEach { label ->
            scrollToDisplayed(label)
            composeRule.onNodeWithText(label).performClick()
        }
        assertEquals(
            listOf(
                SettingsAction.Rescan,
                SettingsAction.ChooseDestination,
                SettingsAction.SignOut,
            ),
            actions,
        )

        scrollToDisplayed("Change server")
        composeRule.onNodeWithText("Change server").performClick()
        assertEquals(SettingsAction.RequestServerChange, actions.last())
        composeRule.runOnIdle {
            state = state.copy(confirmServerChange = true)
        }
        composeRule.onNodeWithText(
            "This clears tokens and the saved server. Android downloads and " +
                "transfer history stay on this device.",
        ).assertIsDisplayed()
        composeRule.onNode(
            hasText("Reset saved connection").and(hasAnyAncestor(isDialog())),
        ).performClick()
        assertEquals(SettingsAction.ConfirmServerChange, actions.last())
    }
}

private val settingsState = SettingsUiState(
    host = "nas.home:9443",
    username = "owner",
    destinationLabel = "Audiobooks",
    appVersion = "0.1.0",
    serverVersion = "1.0.0",
    scanStatus = ScanStatus.Complete,
)

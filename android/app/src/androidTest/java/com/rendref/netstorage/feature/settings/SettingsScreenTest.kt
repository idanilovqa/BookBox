package com.rendref.netstorage.feature.settings

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
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

        listOf(
            "Settings",
            "nas.home:9443",
            "AB:AB:AB:AB:\u2026:AB:AB",
            "owner",
            "Audiobooks",
            "App version 0.1.0",
            "Server version 1.0.0",
            "Scan complete",
            "Copy full fingerprint",
            "Rescan",
            "Choose destination",
            "Sign out",
            "Change server",
        ).forEach {
            composeRule.onNodeWithText(it).performScrollTo().assertIsDisplayed()
        }
        listOf(FINGERPRINT, "content://", "H:/", "password", "token").forEach {
            composeRule.onAllNodesWithText(it, substring = true)
                .assertCountEquals(0)
        }

        composeRule.onNodeWithText("Copy full fingerprint").performScrollTo().performClick()
        composeRule.onNodeWithText("Rescan").performScrollTo().performClick()
        composeRule.onNodeWithText("Choose destination").performScrollTo().performClick()
        composeRule.onNodeWithText("Sign out").performScrollTo().performClick()
        assertEquals(
            listOf(
                SettingsAction.CopyFingerprint,
                SettingsAction.Rescan,
                SettingsAction.ChooseDestination,
                SettingsAction.SignOut,
            ),
            actions,
        )

        composeRule.onNodeWithText("Change server").performScrollTo().performClick()
        assertEquals(SettingsAction.RequestServerChange, actions.last())
        composeRule.runOnIdle {
            state = state.copy(confirmServerChange = true)
        }
        composeRule.onNodeWithText(
            "This clears the saved server, certificate pin, and session. " +
                "Android downloads and transfer history stay on this device.",
        ).assertIsDisplayed()
        composeRule.onNodeWithText("Clear server and session").performClick()
        assertEquals(SettingsAction.ConfirmServerChange, actions.last())
    }
}

private val settingsState = SettingsUiState(
    host = "nas.home:9443",
    fingerprintAbbreviation = "AB:AB:AB:AB:\u2026:AB:AB",
    fingerprintForCopy = FINGERPRINT,
    username = "owner",
    destinationLabel = "Audiobooks",
    appVersion = "0.1.0",
    serverVersion = "1.0.0",
    scanStatus = ScanStatus.Complete,
)

private const val FINGERPRINT =
    "AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:" +
        "AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB:AB"

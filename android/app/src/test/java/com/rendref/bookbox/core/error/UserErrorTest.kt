package com.rendref.bookbox.core.error

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class UserErrorTest {
    @Test
    fun `approved failures have specific recovery actions without sensitive details`() {
        val expected = mapOf(
            UserError.ServerUnavailable to UserRecoveryAction.Retry,
            UserError.PinMismatch to UserRecoveryAction.ReviewCertificate,
            UserError.SessionExpired to UserRecoveryAction.SignIn,
            UserError.Throttled to UserRecoveryAction.RetryLater,
            UserError.SourceChanged to UserRecoveryAction.Rescan,
            UserError.SourceMissing to UserRecoveryAction.Rescan,
            UserError.DestinationPermissionRevoked to
                UserRecoveryAction.ChooseDestination,
            UserError.InsufficientSpace to UserRecoveryAction.FreeSpace,
            UserError.DigestMismatch to UserRecoveryAction.RetryTransfer,
            UserError.ProviderFailure to UserRecoveryAction.RetryMetadata,
            UserError.AmbiguousMetadata to UserRecoveryAction.ReviewMatches,
            UserError.CoverFailure to UserRecoveryAction.RetryCover,
        )

        expected.forEach { (error, recovery) ->
            assertEquals(recovery, error.recovery)
            assertFalse(error.message.isBlank())
            FORBIDDEN.forEach { forbidden ->
                assertFalse(
                    "${error::class.simpleName} leaked $forbidden",
                    error.message.contains(forbidden, ignoreCase = true),
                )
            }
        }
    }
}

private val FORBIDDEN = listOf(
    "token",
    "password",
    "content://",
    "H:/",
    "H:\\",
    "/storage/",
)

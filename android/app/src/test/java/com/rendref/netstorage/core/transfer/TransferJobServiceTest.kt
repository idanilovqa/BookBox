package com.rendref.netstorage.core.transfer

import android.app.job.JobParameters
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TransferJobServiceTest {
    @Test
    fun `network failure requests a resumable system retry`() {
        assertTrue(
            transferNeedsReschedule(
                TransferResult.Failed(
                    fileId = "file-id",
                    failure = TransferFailure.HttpFailure,
                ),
            ),
        )
    }

    @Test
    fun `terminal transfer outcomes do not request a retry`() {
        assertFalse(transferNeedsReschedule(TransferResult.Completed))
        assertFalse(
            transferNeedsReschedule(
                TransferResult.Failed(
                    fileId = "file-id",
                    failure = TransferFailure.DigestMismatch,
                ),
            ),
        )
        assertFalse(
            transferNeedsReschedule(
                TransferResult.Conflict(
                    transferId = "transfer-id",
                    fileId = "file-id",
                    destinationPath = "Dune/Dune.m4b",
                    existingBytes = 10,
                    incomingBytes = 10,
                ),
            ),
        )
    }

    @Test
    fun `user and app stops never reschedule`() {
        assertFalse(
            stopNeedsReschedule(
                JobParameters.STOP_REASON_USER,
                TransferState.Running,
            ),
        )
        assertFalse(
            stopNeedsReschedule(
                JobParameters.STOP_REASON_CANCELLED_BY_APP,
                TransferState.Running,
            ),
        )
        assertFalse(
            stopNeedsReschedule(
                JobParameters.STOP_REASON_CONSTRAINT_CONNECTIVITY,
                TransferState.Cancelled,
            ),
        )
        assertFalse(
            stopNeedsReschedule(
                JobParameters.STOP_REASON_CONSTRAINT_CONNECTIVITY,
                TransferState.Paused,
            ),
        )
    }

    @Test
    fun `system connectivity stop reschedules a running transfer`() {
        assertTrue(
            stopNeedsReschedule(
                JobParameters.STOP_REASON_CONSTRAINT_CONNECTIVITY,
                TransferState.Running,
            ),
        )
    }

    @Test
    fun `conflict waiting transfer does not run or reschedule until user resolves it`() {
        assertFalse(transferCanExecute(TransferState.WaitingForConflict))
        assertFalse(
            stopNeedsReschedule(
                JobParameters.STOP_REASON_CONSTRAINT_CONNECTIVITY,
                TransferState.WaitingForConflict,
            ),
        )
    }

    @Test
    fun `pending and running transfers remain executable`() {
        assertTrue(transferCanExecute(TransferState.Pending))
        assertTrue(transferCanExecute(TransferState.Running))
    }
}

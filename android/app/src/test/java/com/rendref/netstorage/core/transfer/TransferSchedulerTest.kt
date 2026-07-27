package com.rendref.netstorage.core.transfer

import android.net.NetworkCapabilities
import com.rendref.netstorage.core.model.ManifestDto
import com.rendref.netstorage.core.model.ManifestFileDto
import java.util.UUID
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class TransferSchedulerTest {
    @Test
    fun `job requirements describe a persisted user initiated LAN download`() {
        val record = schedulerRecord(totalBytes = 9_437_184)

        val requirements = TransferJobRequirements.from(record)

        assertTrue(requirements.userInitiated)
        assertTrue(requirements.persisted)
        assertTrue(requirements.requiresStorageNotLow)
        assertEquals(9_437_184, requirements.estimatedDownloadBytes)
        assertEquals(0, requirements.estimatedUploadBytes)
        assertTrue(
            NetworkCapabilities.NET_CAPABILITY_NOT_VPN in
                requirements.requiredNetworkCapabilities,
        )
        assertFalse(
            NetworkCapabilities.NET_CAPABILITY_INTERNET in
                requirements.requiredNetworkCapabilities,
        )
    }

    @Test
    fun `job identifier is positive stable and tied to one transfer UUID`() {
        val transferId = "4f48f34f-fdde-4dd2-8e41-c73f02224157"

        val first = transferJobId(transferId)
        val second = transferJobId(transferId)

        assertTrue(first > 0)
        assertEquals(first, second)
        assertTrue(first != transferJobId("123f2680-b29f-4772-921f-27bc2bb9d5cf"))
    }

    @Test
    fun `job identifier rejects non UUID transfer identifiers`() {
        assertThrows(IllegalArgumentException::class.java) {
            transferJobId("transfer-1")
        }
    }

    @Test
    fun `scheduler reports platform rejection instead of claiming start`() {
        val scheduled = mutableListOf<TransferJobRequirements>()
        val scheduler = TransferScheduler(
            submit = {
                scheduled += it
                PlatformScheduleResult.Failure
            },
        )

        val result = scheduler.scheduleFromVisibleAction(schedulerRecord())

        assertEquals(TransferScheduleResult.Failure, result)
        assertEquals(1, scheduled.size)
    }

    @Test
    fun `automatic recovery never submits a user initiated job`() {
        var submissions = 0
        val scheduler = TransferScheduler(
            submit = {
                submissions += 1
                PlatformScheduleResult.Success
            },
        )

        val result = scheduler.reconcileAfterBoot(
            record = schedulerRecord().copy(state = TransferState.Running),
            hasPersistedJob = false,
        )

        assertEquals(TransferRecoveryAction.RequireVisibleResume, result)
        assertEquals(0, submissions)
    }

    @Test
    fun `user cancelled transfer stays cancelled after reboot`() {
        val scheduler = TransferScheduler(
            submit = { PlatformScheduleResult.Success },
        )

        val result = scheduler.reconcileAfterBoot(
            record = schedulerRecord().copy(state = TransferState.Cancelled),
            hasPersistedJob = false,
        )

        assertEquals(TransferRecoveryAction.LeaveUnchanged, result)
    }
}

private fun schedulerRecord(
    totalBytes: Long = 10,
) = TransferRecord(
    id = UUID.randomUUID().toString(),
    manifest = ManifestDto(
        id = "manifest-1",
        files = listOf(
            ManifestFileDto(
                fileId = "signed-file-id",
                sourcePath = "Dune/Dune.m4b",
                destinationPath = "Dune/Dune.m4b",
                size = totalBytes,
                modifiedAt = "2026-07-26T12:00:00Z",
                etag = "\"source-etag\"",
                sha256 = "72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0",
            ),
        ),
        totalBytes = totalBytes,
    ),
    files = listOf(
        FileProgress(
            fileId = "signed-file-id",
            temporaryDocument = null,
            bytesWritten = 0,
            state = FileState.Pending,
        ),
    ),
    destinationTree = "content://documents/tree/audiobooks",
    state = TransferState.Pending,
)

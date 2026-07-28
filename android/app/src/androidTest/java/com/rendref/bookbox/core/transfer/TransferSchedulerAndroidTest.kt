package com.rendref.bookbox.core.transfer

import android.content.ComponentName
import android.content.Context
import android.net.NetworkCapabilities
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.rendref.bookbox.core.model.ManifestDto
import com.rendref.bookbox.core.model.ManifestFileDto
import java.util.UUID
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class TransferSchedulerAndroidTest {
    @Test
    fun jobInfo_isPersistedUserInitiatedLanDownload() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        val record = androidSchedulerRecord()

        val info = buildTransferJobInfo(
            service = ComponentName(context, TransferJobService::class.java),
            record = record,
        )

        assertTrue(info.isUserInitiated)
        assertTrue(info.isPersisted)
        assertTrue(info.isRequireStorageNotLow)
        assertNotNull(info.requiredNetwork)
        assertTrue(
            info.requiredNetwork!!.hasCapability(
                NetworkCapabilities.NET_CAPABILITY_NOT_VPN,
            ),
        )
        assertFalse(
            info.requiredNetwork!!.hasCapability(
                NetworkCapabilities.NET_CAPABILITY_INTERNET,
            ),
        )
        assertEquals(record.manifest.totalBytes, info.estimatedNetworkDownloadBytes)
        assertEquals(0, info.estimatedNetworkUploadBytes)
        assertEquals(record.id, info.extras.getString(TRANSFER_ID_EXTRA))
    }
}

private fun androidSchedulerRecord() = TransferRecord(
    id = UUID.randomUUID().toString(),
    manifest = ManifestDto(
        id = "manifest-1",
        files = listOf(
            ManifestFileDto(
                fileId = "signed-file-id",
                sourcePath = "Dune/Dune.m4b",
                destinationPath = "Dune/Dune.m4b",
                size = 10,
                modifiedAt = "2026-07-26T12:00:00Z",
                etag = "\"source-etag\"",
                sha256 = "72399361da6a7754fec986dca5b7cbaf1c810a28ded4abaf56b2106d06cb78b0",
            ),
        ),
        totalBytes = 10,
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

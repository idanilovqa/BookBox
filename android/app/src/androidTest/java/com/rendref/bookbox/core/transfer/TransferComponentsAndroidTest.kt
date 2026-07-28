package com.rendref.bookbox.core.transfer

import android.Manifest
import android.app.Notification
import android.app.NotificationManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.util.UUID
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class TransferComponentsAndroidTest {
    private val context = ApplicationProvider.getApplicationContext<Context>()

    @Test
    fun notificationChannelAndActionsAreAppScopedBroadcasts() {
        val transferId = UUID.randomUUID().toString()
        val notifications = TransferNotifications(context)
        val notification = notifications.initial(transferId)
        val channel = context.getSystemService(NotificationManager::class.java)
            .getNotificationChannel(TransferNotifications.CHANNEL_ID)

        assertNotNull(channel)
        assertEquals(2, notification.actions.size)
        assertTrue(notification.actions.all { it.actionIntent.isBroadcast })
        assertTrue(
            notification.actions.all {
                it.actionIntent.creatorPackage == context.packageName
            },
        )
        listOf(
            TransferReceiver.ACTION_PAUSE_TRANSFER,
            TransferReceiver.ACTION_CANCEL_TRANSFER,
        ).forEach { action ->
            val intent = buildTransferCommandIntent(context, transferId, action)
            assertEquals(context.packageName, intent.`package`)
            assertEquals(
                ComponentName(context, TransferReceiver::class.java),
                intent.component,
            )
            assertEquals(transferId, intent.getStringExtra(TRANSFER_ID_EXTRA))
        }
    }

    @Test
    fun notificationTextNeverContainsSourcePathOrDestinationTreeUri() {
        val transferId = UUID.randomUUID().toString()
        val notifications = TransferNotifications(context)
        val notification = notifications.progress(
            progress = TransferProgress(
                transferId = transferId,
                fileId = "signed-file-id",
                bytesWritten = 4,
                fileBytes = 10,
                completedFiles = 0,
                totalFiles = 1,
            ),
            currentFile = "Dune/Dune.m4b",
            totalBytes = 10,
            transferredBytes = 4,
        )
        val visible = listOfNotNull(
            notification.extras.getCharSequence(Notification.EXTRA_TITLE),
            notification.extras.getCharSequence(Notification.EXTRA_TEXT),
        ).joinToString(" ")

        assertTrue(visible.contains("Dune.m4b"))
        assertFalse(visible.contains("Dune/Dune.m4b"))
        assertFalse(visible.contains("H:\\Audiobooks"))
        assertFalse(visible.contains("content://"))
    }

    @Test
    fun manifestProtectsJobServiceAndBootReceiver() {
        val service = context.packageManager.getServiceInfo(
            ComponentName(context, TransferJobService::class.java),
            PackageManager.ComponentInfoFlags.of(0),
        )
        val receiver = context.packageManager.getReceiverInfo(
            ComponentName(context, TransferReceiver::class.java),
            PackageManager.ComponentInfoFlags.of(0),
        )

        assertFalse(service.exported)
        assertEquals("android.permission.BIND_JOB_SERVICE", service.permission)
        assertFalse(receiver.exported)
        assertTrue(receiver.enabled)
        assertEquals(
            PackageManager.PERMISSION_GRANTED,
            context.checkSelfPermission(Manifest.permission.RUN_USER_INITIATED_JOBS),
        )
    }
}

package com.rendref.bookbox.core.transfer

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import kotlin.math.roundToInt

class TransferNotifications(
    private val context: Context,
) {
    private val manager = context.getSystemService(NotificationManager::class.java)

    init {
        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                "Audiobook transfers",
                NotificationManager.IMPORTANCE_LOW,
            ).apply {
                description = "Progress for copies from BookBox to this Android device"
                setShowBadge(false)
            },
        )
    }

    fun initial(transferId: String): Notification =
        notification(
            transferId = transferId,
            title = "Preparing audiobook copy",
            text = "Connecting to the saved home server",
            bytesWritten = 0,
            totalBytes = 0,
            indeterminate = true,
        )

    fun progress(
        progress: TransferProgress,
        currentFile: String,
        totalBytes: Long,
        transferredBytes: Long,
    ): Notification {
        val remaining = (progress.totalFiles - progress.completedFiles).coerceAtLeast(0)
        return notification(
            transferId = progress.transferId,
            title = "Copying to Android",
            text = "${safeNotificationFileLabel(currentFile)} \u00b7 $remaining remaining",
            bytesWritten = transferredBytes,
            totalBytes = totalBytes,
            indeterminate = totalBytes <= 0,
        )
    }

    fun notify(
        transferId: String,
        notification: Notification,
    ) {
        manager.notify(notificationId(transferId), notification)
    }

    fun notificationId(transferId: String): Int =
        NOTIFICATION_ID_BASE + transferJobId(transferId) % NOTIFICATION_ID_RANGE

    private fun notification(
        transferId: String,
        title: String,
        text: String,
        bytesWritten: Long,
        totalBytes: Long,
        indeterminate: Boolean,
    ): Notification {
        val progress = scaledTransferProgress(bytesWritten, totalBytes)
        return Notification.Builder(context, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.stat_sys_download)
            .setContentTitle(title)
            .setContentText(text)
            .setCategory(Notification.CATEGORY_PROGRESS)
            .setOnlyAlertOnce(true)
            .setOngoing(true)
            .setProgress(PROGRESS_MAX, progress, indeterminate)
            .addAction(
                Notification.Action.Builder(
                    Icon.createWithResource(
                        context,
                        android.R.drawable.ic_media_pause,
                    ),
                    "Pause",
                    commandIntent(
                        transferId,
                        TransferReceiver.ACTION_PAUSE_TRANSFER,
                        PAUSE_REQUEST_OFFSET,
                    ),
                ).build(),
            )
            .addAction(
                Notification.Action.Builder(
                    Icon.createWithResource(
                        context,
                        android.R.drawable.ic_menu_close_clear_cancel,
                    ),
                    "Cancel",
                    commandIntent(
                        transferId,
                        TransferReceiver.ACTION_CANCEL_TRANSFER,
                        CANCEL_REQUEST_OFFSET,
                    ),
                ).build(),
            )
            .build()
    }

    private fun commandIntent(
        transferId: String,
        action: String,
        requestOffset: Int,
    ): PendingIntent {
        val intent = buildTransferCommandIntent(context, transferId, action)
        return PendingIntent.getBroadcast(
            context,
            transferJobId(transferId) % NOTIFICATION_ID_RANGE + requestOffset,
            intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    companion object {
        const val CHANNEL_ID = "bookbox_transfers"
        private const val NOTIFICATION_ID_BASE = 10_000
        private const val NOTIFICATION_ID_RANGE = 1_000_000
        private const val PAUSE_REQUEST_OFFSET = 1_100_000
        private const val CANCEL_REQUEST_OFFSET = 2_200_000
        private const val PROGRESS_MAX = 1_000
    }
}

internal fun safeNotificationFileLabel(value: String): String {
    val fallback = "Selected audiobook"
    if (
        value.isBlank() ||
        value.contains('\\') ||
        value.contains("://") ||
        value.any(Char::isISOControl) ||
        value.startsWith('/')
    ) {
        return fallback
    }

    val segments = value.split('/')
    if (segments.any { it.isBlank() || it == "." || it == ".." }) {
        return fallback
    }

    val leaf = segments.last()
    return if (leaf.length <= 200 && leaf.none(Char::isISOControl)) leaf else fallback
}

internal fun scaledTransferProgress(
    bytesWritten: Long,
    totalBytes: Long,
): Int {
    if (totalBytes <= 0L) return 0
    val boundedBytes = bytesWritten.coerceIn(0L, totalBytes)
    return ((boundedBytes.toDouble() / totalBytes.toDouble()) * 1_000.0)
        .roundToInt()
        .coerceIn(0, 1_000)
}

internal fun buildTransferCommandIntent(
    context: Context,
    transferId: String,
    action: String,
): Intent {
    require(
        action == TransferReceiver.ACTION_PAUSE_TRANSFER ||
            action == TransferReceiver.ACTION_CANCEL_TRANSFER,
    ) {
        "Unsupported transfer notification action"
    }
    transferJobId(transferId)
    return Intent(action)
        .setComponent(ComponentName(context, TransferReceiver::class.java))
        .setPackage(context.packageName)
        .putExtra(TRANSFER_ID_EXTRA, transferId)
}

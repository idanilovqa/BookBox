package com.rendref.netstorage.core.transfer

import android.app.job.JobParameters
import android.app.job.JobService
import android.content.Context
import android.net.Network
import com.rendref.netstorage.core.network.AuthenticatedCall
import com.rendref.netstorage.core.network.PinnedClientFactory
import com.rendref.netstorage.core.network.SessionApi
import com.rendref.netstorage.core.network.SessionException
import com.rendref.netstorage.core.storage.ConnectionStore
import com.rendref.netstorage.core.storage.DestinationPermissionRevoked
import com.rendref.netstorage.core.storage.DestinationStore
import com.rendref.netstorage.core.storage.KeystoreTokenStore
import com.rendref.netstorage.core.storage.netStorageConnectionDataStore
import java.io.IOException
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.first
import okhttp3.Dns
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient

data class TransferJobRuntime(
    val journal: TransferJournal,
    val engine: TransferEngine,
)

fun interface TransferJobRuntimeFactory {
    suspend fun create(
        context: Context,
        record: TransferRecord,
        network: Network?,
    ): TransferJobRuntime
}

class TransferJobService : JobService() {
    private val serviceScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val running = ConcurrentHashMap<Int, Job>()
    private val stopReasons = ConcurrentHashMap<Int, Int>()

    override fun onStartJob(params: JobParameters): Boolean {
        val transferId = params.extras.getString(TRANSFER_ID_EXTRA)
            ?.takeIf(TRANSFER_UUID::matches)
            ?: return false
        val notifications = TransferNotifications(this)
        val initial = notifications.initial(transferId)
        setNotification(
            params,
            notifications.notificationId(transferId),
            initial,
            JOB_END_NOTIFICATION_POLICY_REMOVE,
        )
        val job = serviceScope.launch {
            execute(params, transferId, notifications)
        }
        running[params.jobId] = job
        job.invokeOnCompletion {
            running.remove(params.jobId, job)
            val stopReason = stopReasons.remove(params.jobId)
            if (stopReason != null) {
                recordStoppedTransfer(transferId, stopReason)
            }
        }
        return true
    }

    override fun onStopJob(params: JobParameters): Boolean {
        val transferId = params.extras.getString(TRANSFER_ID_EXTRA)
        val currentState = transferId
            ?.let { runCatching { TransferJournal(filesDir).read(it)?.state }.getOrNull() }
        val wantsReschedule = stopNeedsReschedule(
            stopReason = params.stopReason,
            state = currentState,
        )
        stopReasons[params.jobId] = params.stopReason
        if (transferId != null) {
            recordStoppedTransfer(transferId, params.stopReason)
        }
        running.remove(params.jobId)?.cancel(
            CancellationException("Transfer job stopped by Android"),
        )
        return wantsReschedule
    }

    override fun onDestroy() {
        serviceScope.cancel()
        super.onDestroy()
    }

    private suspend fun execute(
        params: JobParameters,
        transferId: String,
        notifications: TransferNotifications,
    ) {
        val journal = TransferJournal(filesDir)
        val record = runCatching { journal.read(transferId) }.getOrNull()
        if (record == null || !transferCanExecute(record.state)) {
            finishUnlessStopped(params, wantsReschedule = false)
            return
        }
        val runtime = try {
            runtimeFactory.create(this, record, params.network)
        } catch (failure: Exception) {
            val wantsReschedule = runtimeFailureNeedsReschedule(failure)
            journal.updateIfPresent(transferId) {
                it.copy(
                    state = if (wantsReschedule) {
                        TransferState.Pending
                    } else {
                        TransferState.Failed
                    },
                    stopReason = null,
                )
            }
            finishUnlessStopped(params, wantsReschedule)
            return
        }
        val result = try {
            runtime.engine.run(transferId, params.network) { progress ->
                val latest = runtime.journal.read(transferId) ?: return@run
                val transferred = latest.transferredBytes()
                val fileName = latest.manifest.files
                    .firstOrNull { it.fileId == progress.fileId }
                    ?.destinationPath
                    ?: "Selected audiobook"
                val notification = notifications.progress(
                    progress = progress,
                    currentFile = fileName,
                    totalBytes = latest.manifest.totalBytes,
                    transferredBytes = transferred,
                )
                notifications.notify(transferId, notification)
                updateTransferredNetworkBytes(params, transferred, 0)
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            TransferResult.Failed(null, TransferFailure.DestinationFailure)
        }
        val wantsReschedule = transferNeedsReschedule(result)
        if (wantsReschedule) {
            runtime.journal.updateIfPresent(transferId) {
                it.copy(state = TransferState.Pending, stopReason = null)
            }
        }
        finishUnlessStopped(params, wantsReschedule)
    }

    private fun finishUnlessStopped(
        params: JobParameters,
        wantsReschedule: Boolean,
    ) {
        if (!stopReasons.containsKey(params.jobId)) {
            jobFinished(params, wantsReschedule)
        }
    }

    private fun recordStoppedTransfer(
        transferId: String,
        stopReason: Int,
    ) {
        TransferJournal(filesDir).updateIfPresent(transferId) { record ->
            when (record.state) {
                TransferState.Completed,
                TransferState.WaitingForConflict,
                TransferState.Cancelled,
                TransferState.Paused,
                -> record.copy(stopReason = stopReason)
                else -> record.copy(
                    state = if (stopReason == JobParameters.STOP_REASON_USER) {
                        TransferState.Cancelled
                    } else {
                        TransferState.Pending
                    },
                    stopReason = stopReason,
                )
            }
        }
    }

    companion object {
        @Volatile
        internal var runtimeFactory: TransferJobRuntimeFactory =
            DefaultTransferJobRuntimeFactory
    }
}

fun transferNeedsReschedule(result: TransferResult): Boolean =
    result is TransferResult.Failed &&
        result.failure == TransferFailure.HttpFailure

fun transferCanExecute(state: TransferState): Boolean =
    state == TransferState.Pending || state == TransferState.Running

fun stopNeedsReschedule(
    stopReason: Int,
    state: TransferState?,
): Boolean =
    state != TransferState.Cancelled &&
        state != TransferState.Paused &&
        state != TransferState.Completed &&
        state != TransferState.WaitingForConflict &&
        stopReason != JobParameters.STOP_REASON_USER &&
        stopReason != JobParameters.STOP_REASON_CANCELLED_BY_APP

private fun runtimeFailureNeedsReschedule(failure: Exception): Boolean = when (failure) {
    is DestinationPermissionRevoked -> false
    is SessionException -> failure.statusCode != 401
    is IOException -> true
    else -> false
}

private object DefaultTransferJobRuntimeFactory : TransferJobRuntimeFactory {
    override suspend fun create(
        context: Context,
        record: TransferRecord,
        network: Network?,
    ): TransferJobRuntime {
        val applicationContext = context.applicationContext
        val tokenStore = KeystoreTokenStore(applicationContext)
        val connections = ConnectionStore(
            applicationContext.netStorageConnectionDataStore,
            tokenStore,
        )
        val connection = connections.state.first()
            ?: throw SessionException("Sign-in has expired", statusCode = 401)
        val origin = connection.baseUrl.toHttpUrl()
        val pinned = PinnedClientFactory().create(connection)
        val sessionClient = pinned.boundTo(network)
        val session = SessionApi(
            client = sessionClient,
            baseUrl = origin,
            tokenStore = tokenStore,
        )
        session.refresh()
        val destination = DestinationStore(
            connections,
            applicationContext.contentResolver,
        ).requireTree()
        if (destination.treeUri.toString() != record.destinationTree) {
            throw DestinationPermissionRevoked(
                "The transfer destination no longer matches the selected Android folder",
            )
        }
        val journal = TransferJournal(applicationContext.filesDir)
        val engine = TransferEngine(
            baseUrl = origin,
            callFactory = { selectedNetwork ->
                AuthenticatedCall(
                    pinned.boundTo(selectedNetwork),
                    session,
                )
            },
            journal = journal,
            destination = { destinationTree ->
                if (destinationTree != record.destinationTree) {
                    throw DestinationPermissionRevoked()
                }
                destination
            },
        )
        return TransferJobRuntime(journal, engine)
    }
}

private fun OkHttpClient.boundTo(network: Network?): OkHttpClient {
    if (network == null) return this
    return newBuilder()
        .socketFactory(network.socketFactory)
        .dns(
            Dns { hostname ->
                network.getAllByName(hostname).toList()
            },
        )
        .build()
}

private fun TransferRecord.transferredBytes(): Long =
    files.zip(manifest.files).fold(0L) { total, (progress, file) ->
        val bounded = progress.bytesWritten.coerceIn(0, file.size)
        if (total > Long.MAX_VALUE - bounded) Long.MAX_VALUE else total + bounded
    }.coerceAtMost(manifest.totalBytes)

private fun TransferJournal.updateIfPresent(
    transferId: String,
    transform: (TransferRecord) -> TransferRecord,
) {
    runCatching {
        update(transferId, transform)
    }
}

private val TRANSFER_UUID =
    Regex("^[0-9a-fA-F]{8}-(?:[0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$")

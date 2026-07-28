package com.rendref.bookbox.core.transfer

import android.app.job.JobInfo
import android.app.job.JobScheduler
import android.content.ComponentName
import android.content.Context
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.PersistableBundle
import java.util.UUID

const val TRANSFER_ID_EXTRA = "transfer_id"

data class TransferJobRequirements(
    val transferId: String,
    val jobId: Int,
    val userInitiated: Boolean,
    val persisted: Boolean,
    val requiresStorageNotLow: Boolean,
    val requiredNetworkCapabilities: Set<Int>,
    val estimatedDownloadBytes: Long,
    val estimatedUploadBytes: Long,
) {
    companion object {
        fun from(record: TransferRecord): TransferJobRequirements {
            require(record.manifest.totalBytes >= 0) {
                "Estimated download size cannot be negative"
            }
            return TransferJobRequirements(
                transferId = record.id,
                jobId = transferJobId(record.id),
                userInitiated = true,
                persisted = true,
                requiresStorageNotLow = true,
                requiredNetworkCapabilities = setOf(
                    NetworkCapabilities.NET_CAPABILITY_NOT_VPN,
                ),
                estimatedDownloadBytes = record.manifest.totalBytes,
                estimatedUploadBytes = 0,
            )
        }
    }
}

enum class PlatformScheduleResult {
    Success,
    Failure,
}

enum class TransferScheduleResult {
    Scheduled,
    Failure,
}

enum class TransferRecoveryAction {
    PersistedJobOwnsRecovery,
    RequireVisibleResume,
    LeaveUnchanged,
}

class TransferScheduler(
    private val submit: (TransferJobRequirements) -> PlatformScheduleResult,
) {
    constructor(context: Context) : this(
        submit = platformSubmitter(context.applicationContext),
    )

    fun scheduleFromVisibleAction(record: TransferRecord): TransferScheduleResult {
        val requirements = TransferJobRequirements.from(record)
        return if (submit(requirements) == PlatformScheduleResult.Success) {
            TransferScheduleResult.Scheduled
        } else {
            TransferScheduleResult.Failure
        }
    }

    fun reconcileAfterBoot(
        record: TransferRecord,
        hasPersistedJob: Boolean,
    ): TransferRecoveryAction = when {
        hasPersistedJob -> TransferRecoveryAction.PersistedJobOwnsRecovery
        record.state == TransferState.Cancelled ||
            record.state == TransferState.Completed ||
            record.state == TransferState.WaitingForConflict ->
            TransferRecoveryAction.LeaveUnchanged
        record.state == TransferState.Running ||
            record.state == TransferState.Pending ->
            TransferRecoveryAction.RequireVisibleResume
        else -> TransferRecoveryAction.LeaveUnchanged
    }
}

fun buildTransferJobInfo(
    service: ComponentName,
    record: TransferRecord,
): JobInfo = buildTransferJobInfo(service, TransferJobRequirements.from(record))

internal fun buildTransferJobInfo(
    service: ComponentName,
    requirements: TransferJobRequirements,
): JobInfo {
    val network = NetworkRequest.Builder()
        .clearCapabilities()
        .apply {
            requirements.requiredNetworkCapabilities.forEach(::addCapability)
        }
        .build()
    return JobInfo.Builder(requirements.jobId, service)
        .setExtras(
            PersistableBundle().apply {
                putString(TRANSFER_ID_EXTRA, requirements.transferId)
            },
        )
        .setUserInitiated(requirements.userInitiated)
        .setPersisted(requirements.persisted)
        .setRequiresStorageNotLow(requirements.requiresStorageNotLow)
        .setRequiredNetwork(network)
        .setEstimatedNetworkBytes(
            requirements.estimatedDownloadBytes,
            requirements.estimatedUploadBytes,
        )
        .setMinimumNetworkChunkBytes(
            requirements.estimatedDownloadBytes.coerceIn(
                1,
                MAX_RESUMABLE_CHUNK_BYTES,
            ),
        )
        .setBackoffCriteria(
            INITIAL_BACKOFF_MILLIS,
            JobInfo.BACKOFF_POLICY_EXPONENTIAL,
        )
        .build()
}

fun transferJobId(transferId: String): Int {
    val uuid = UUID.fromString(transferId)
    return (uuid.hashCode() and Int.MAX_VALUE).takeIf { it != 0 } ?: 1
}

private fun platformSubmitter(
    context: Context,
): (TransferJobRequirements) -> PlatformScheduleResult {
    val jobs = context.getSystemService(JobScheduler::class.java)
    val service = ComponentName(context, TransferJobService::class.java)
    return { requirements ->
        val result = runCatching {
            jobs.schedule(buildTransferJobInfo(service, requirements))
        }.getOrDefault(JobScheduler.RESULT_FAILURE)
        if (result == JobScheduler.RESULT_SUCCESS) {
            PlatformScheduleResult.Success
        } else {
            PlatformScheduleResult.Failure
        }
    }
}

private const val INITIAL_BACKOFF_MILLIS = 30_000L
private const val MAX_RESUMABLE_CHUNK_BYTES = 4L * 1024 * 1024

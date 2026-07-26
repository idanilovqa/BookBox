package com.rendref.netstorage.core.transfer

import com.rendref.netstorage.core.model.ManifestDto
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json

@Serializable
data class TransferRecord(
    val id: String,
    val manifest: ManifestDto,
    val files: List<FileProgress>,
    val destinationTree: String,
    val state: TransferState,
    val stopReason: Int? = null,
    val displayName: String? = null,
)

@Serializable
data class FileProgress(
    val fileId: String,
    val temporaryDocument: String?,
    val bytesWritten: Long,
    val state: FileState,
    val conflictChoice: ConflictChoice? = null,
)

@Serializable
enum class TransferState {
    Pending,
    Running,
    Paused,
    WaitingForConflict,
    Completed,
    Failed,
    Cancelled,
}

@Serializable
enum class FileState {
    Pending,
    Downloading,
    WaitingForConflict,
    Completed,
    Skipped,
    Failed,
}

@Serializable
enum class ConflictChoice {
    Replace,
    KeepBoth,
    Skip,
}

fun interface JournalTemporaryWriter {
    @Throws(IOException::class)
    fun write(temporary: File, bytes: ByteArray)
}

class TransferJournal(
    filesDir: File,
    private val json: Json = Json,
    private val temporaryWriter: JournalTemporaryWriter = JournalTemporaryWriter(::writeAndSync),
) {
    private val directory = File(filesDir, "transfers")

    init {
        if (!directory.exists() && !directory.mkdirs()) {
            throw IOException("Unable to create transfer journal directory")
        }
        if (!directory.isDirectory) {
            throw IOException("Transfer journal location is not a directory")
        }
    }

    @Synchronized
    fun read(transferId: String): TransferRecord? {
        val target = targetFile(transferId)
        if (!target.exists()) {
            return null
        }
        if (!target.isFile || target.length() > MAX_JOURNAL_BYTES) {
            throw IOException("Transfer journal is invalid")
        }
        val bytes = target.readBytes()
        val record = try {
            json.decodeFromString<TransferRecord>(bytes.toString(Charsets.UTF_8))
        } catch (failure: Exception) {
            throw IOException("Transfer journal is invalid", failure)
        }
        if (record.id != transferId) {
            throw IOException("Transfer journal identifier does not match")
        }
        validateRecord(record)
        return record
    }

    @Synchronized
    fun write(record: TransferRecord) {
        validateRecord(record)
        val encoded = json.encodeToString(record).toByteArray(Charsets.UTF_8)
        if (encoded.size > MAX_JOURNAL_BYTES) {
            throw IOException("Transfer journal is too large")
        }
        val target = targetFile(record.id)
        val temporary = File(directory, "${record.id}.json.tmp")
        temporaryWriter.write(temporary, encoded)
        try {
            Files.move(
                temporary.toPath(),
                target.toPath(),
                StandardCopyOption.ATOMIC_MOVE,
                StandardCopyOption.REPLACE_EXISTING,
            )
        } catch (failure: Exception) {
            throw IOException("Unable to commit transfer journal", failure)
        }
    }

    @Synchronized
    fun list(): List<TransferRecord> =
        directory.listFiles { file ->
            file.isFile &&
                file.name.endsWith(JOURNAL_SUFFIX) &&
                !file.name.endsWith(TEMPORARY_SUFFIX)
        }
            ?.asSequence()
            ?.sortedBy(File::getName)
            ?.take(MAX_HISTORY_RECORDS)
            ?.map { file ->
                val transferId = file.name.removeSuffix(JOURNAL_SUFFIX)
                read(transferId)
                    ?: throw IOException("Transfer journal disappeared while reading history")
            }
            ?.toList()
            .orEmpty()

    @Synchronized
    fun update(
        transferId: String,
        transform: (TransferRecord) -> TransferRecord,
    ): TransferRecord {
        val current = read(transferId)
            ?: throw IOException("Transfer journal is missing")
        val updated = transform(current)
        require(updated.id == transferId) {
            "Transfer identifier cannot be changed"
        }
        write(updated)
        return updated
    }

    private fun targetFile(transferId: String): File {
        require(SAFE_TRANSFER_ID.matches(transferId)) {
            "Transfer identifier is invalid"
        }
        return File(directory, "$transferId.json")
    }

    private fun validateRecord(record: TransferRecord) {
        require(SAFE_TRANSFER_ID.matches(record.id)) {
            "Transfer identifier is invalid"
        }
        require(record.destinationTree.isNotBlank()) {
            "Destination tree is required"
        }
        require(record.files.size == record.manifest.files.size) {
            "Transfer progress must match the manifest"
        }
        val manifestIDs = record.manifest.files.map { it.fileId }
        require(manifestIDs.distinct().size == manifestIDs.size) {
            "Manifest file identifiers must be unique"
        }
        require(record.files.map { it.fileId } == manifestIDs) {
            "Transfer progress order must match the manifest"
        }
        record.files.forEach { progress ->
            require(progress.bytesWritten >= 0) {
                "Transfer progress cannot be negative"
            }
        }
        require(record.stopReason == null || record.stopReason >= 0) {
            "Transfer stop reason is invalid"
        }
        record.displayName?.let { displayName ->
            require(
                displayName.isNotBlank() &&
                    displayName.length <= MAX_DISPLAY_NAME_CHARS &&
                    !displayName.contains('/') &&
                    !displayName.contains('\\') &&
                    !displayName.contains("://") &&
                    !WINDOWS_VOLUME.matches(displayName) &&
                    displayName.none(Char::isISOControl),
            ) {
                "Transfer display name is invalid"
            }
        }
    }

    private companion object {
        const val MAX_JOURNAL_BYTES = 16 * 1024 * 1024L
        const val MAX_HISTORY_RECORDS = 10_000
        const val MAX_DISPLAY_NAME_CHARS = 200
        const val JOURNAL_SUFFIX = ".json"
        const val TEMPORARY_SUFFIX = ".json.tmp"
        val SAFE_TRANSFER_ID = Regex("^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
        val WINDOWS_VOLUME = Regex("^[A-Za-z]:.*")

        fun writeAndSync(temporary: File, bytes: ByteArray) {
            FileOutputStream(temporary).use { stream ->
                stream.write(bytes)
                stream.fd.sync()
            }
        }
    }
}

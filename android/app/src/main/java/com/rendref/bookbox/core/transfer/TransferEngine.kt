package com.rendref.bookbox.core.transfer

import android.net.Network
import com.rendref.bookbox.core.model.ManifestFileDto
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.security.MessageDigest
import kotlin.coroutines.coroutineContext
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import okhttp3.Call
import okhttp3.HttpUrl
import okhttp3.Request

data class DestinationDocument(val documentId: String, val size: Long)

class DestinationSeekUnsupported(cause: Throwable? = null) :
    IOException("Android document provider cannot resume this file", cause)

interface TransferDestination {
    fun find(relativePath: String): DestinationDocument?
    fun createTemporary(
        destinationPath: String,
        transferId: String,
        fileId: String,
    ): DestinationDocument
    fun stat(documentId: String): DestinationDocument
    fun openInput(documentId: String): InputStream
    fun openOutput(documentId: String, offset: Long): OutputStream
    fun delete(documentId: String)
    fun finalize(documentId: String, destinationPath: String): DestinationDocument
}

data class TransferProgress(
    val transferId: String,
    val fileId: String,
    val bytesWritten: Long,
    val fileBytes: Long,
    val completedFiles: Int,
    val totalFiles: Int,
)

enum class TransferFailure {
    JournalMissing,
    InvalidManifest,
    HttpFailure,
    SourceChanged,
    SizeMismatch,
    DigestMismatch,
    DestinationFailure,
}

sealed interface TransferResult {
    data object Completed : TransferResult

    data class Conflict(
        val transferId: String,
        val fileId: String,
        val destinationPath: String,
        val existingBytes: Long,
        val incomingBytes: Long,
    ) : TransferResult

    data class Failed(
        val fileId: String?,
        val failure: TransferFailure,
    ) : TransferResult
}

class TransferEngine(
    private val baseUrl: HttpUrl,
    private val callFactory: (Network?) -> Call.Factory,
    private val journal: TransferJournal,
    private val destination: (String) -> TransferDestination,
    private val nowMillis: () -> Long = System::currentTimeMillis,
) {
    init {
        require(
            baseUrl.isHttps &&
                baseUrl.encodedUsername.isEmpty() &&
                baseUrl.encodedPassword.isEmpty() &&
                baseUrl.encodedPath == "/" &&
                baseUrl.query == null &&
                baseUrl.fragment == null,
        ) { "Transfer origin must be a clean HTTPS origin" }
    }

    suspend fun run(
        transferId: String,
        network: Network?,
        onProgress: (TransferProgress) -> Unit,
    ): TransferResult = withContext(Dispatchers.IO) {
        var record = journal.read(transferId)
            ?: return@withContext TransferResult.Failed(
                null,
                TransferFailure.JournalMissing,
            )
        if (!validRecord(record)) {
            return@withContext TransferResult.Failed(null, TransferFailure.InvalidManifest)
        }
        val tree = try {
            destination(record.destinationTree)
        } catch (_: Exception) {
            return@withContext TransferResult.Failed(
                null,
                TransferFailure.DestinationFailure,
            )
        }
        journal.write(record.copy(state = TransferState.Running))
        for (index in record.files.indices) {
            record = checkNotNull(journal.read(transferId))
            if (record.files[index].state in
                setOf(FileState.Completed, FileState.Skipped)
            ) {
                continue
            }
            when (val result = runFile(record, index, tree, network, onProgress)) {
                TransferResult.Completed -> Unit
                else -> return@withContext result
            }
        }
        record = checkNotNull(journal.read(transferId)).copy(
            state = TransferState.Completed,
        )
        journal.write(record)
        TransferResult.Completed
    }

    fun chooseConflict(
        transferId: String,
        fileId: String,
        choice: ConflictChoice,
    ) {
        val record = requireNotNull(journal.read(transferId))
        val index = record.files.indexOfFirst { it.fileId == fileId }
        require(index >= 0)
        require(record.files[index].state == FileState.WaitingForConflict)
        journal.write(
            record.withFile(
                index,
                record.files[index].copy(conflictChoice = choice),
            ).copy(state = TransferState.Pending),
        )
    }

    private suspend fun runFile(
        initial: TransferRecord,
        index: Int,
        tree: TransferDestination,
        network: Network?,
        onProgress: (TransferProgress) -> Unit,
    ): TransferResult {
        var record = initial
        val file = record.manifest.files[index]
        var progress = record.files[index]
        val existing = try {
            tree.find(file.destinationPath)
        } catch (_: Exception) {
            return fail(record, index, tree, TransferFailure.DestinationFailure)
        }
        var destinationPath = file.destinationPath
        when (progress.conflictChoice) {
            ConflictChoice.Replace -> if (existing != null) {
                try {
                    tree.delete(existing.documentId)
                } catch (_: Exception) {
                    return fail(record, index, tree, TransferFailure.DestinationFailure)
                }
            }
            ConflictChoice.KeepBoth -> destinationPath = try {
                keepBothPath(tree, file.destinationPath)
            } catch (_: Exception) {
                return fail(record, index, tree, TransferFailure.DestinationFailure)
            }
            ConflictChoice.Skip -> {
                journal.write(
                    record.withFile(
                        index,
                        progress.copy(
                            temporaryDocument = null,
                            bytesWritten = file.size,
                            state = FileState.Skipped,
                        ),
                    ),
                )
                return TransferResult.Completed
            }
            null -> if (existing != null) {
                val matches = try {
                    existing.size == file.size &&
                        digest(tree, existing.documentId) == file.sha256.lowercase()
                } catch (_: Exception) {
                    return fail(record, index, tree, TransferFailure.DestinationFailure)
                }
                if (matches) {
                    journal.write(
                        record.withFile(
                            index,
                            progress.copy(
                                temporaryDocument = null,
                                bytesWritten = file.size,
                                state = FileState.Completed,
                                conflictChoice = ConflictChoice.Skip,
                            ),
                        ),
                    )
                    return TransferResult.Completed
                }
                progress = progress.copy(
                    state = FileState.WaitingForConflict,
                    existingBytes = existing.size,
                )
                record = record.withFile(index, progress).copy(
                    state = TransferState.WaitingForConflict,
                )
                journal.write(record)
                return TransferResult.Conflict(
                    record.id,
                    file.fileId,
                    file.destinationPath,
                    existing.size,
                    file.size,
                )
            }
        }
        return download(
            record,
            index,
            tree,
            destinationPath,
            network,
            onProgress,
        )
    }

    private suspend fun download(
        initial: TransferRecord,
        index: Int,
        tree: TransferDestination,
        destinationPath: String,
        network: Network?,
        onProgress: (TransferProgress) -> Unit,
    ): TransferResult {
        var record = initial
        var progress = record.files[index]
        val file = record.manifest.files[index]
        while (true) {
            var temporary = progress.temporaryDocument?.let { id ->
                runCatching { tree.stat(id) }.getOrNull()
            }
            if (temporary == null || temporary.size != progress.bytesWritten) {
                progress.temporaryDocument?.let {
                    try {
                        tree.delete(it)
                    } catch (_: Exception) {
                        return fail(
                            record,
                            index,
                            tree,
                            TransferFailure.DestinationFailure,
                        )
                    }
                }
                temporary = try {
                    tree.createTemporary(destinationPath, record.id, file.fileId)
                } catch (_: Exception) {
                    return fail(record, index, tree, TransferFailure.DestinationFailure)
                }
                progress = progress.copy(
                    temporaryDocument = temporary.documentId,
                    bytesWritten = 0,
                    state = FileState.Downloading,
                )
                record = record.withFile(index, progress)
                journal.write(record)
            }
            if (temporary.size == file.size) {
                val verified = try {
                    digest(tree, temporary.documentId) == file.sha256.lowercase()
                } catch (_: Exception) {
                    return fail(record, index, tree, TransferFailure.DestinationFailure)
                }
                if (!verified) {
                    return fail(record, index, tree, TransferFailure.DigestMismatch)
                }
                try {
                    tree.finalize(temporary.documentId, destinationPath)
                } catch (_: Exception) {
                    return fail(record, index, tree, TransferFailure.DestinationFailure)
                }
                record = record.withFile(
                    index,
                    progress.copy(
                        temporaryDocument = null,
                        bytesWritten = file.size,
                        state = FileState.Completed,
                    ),
                )
                journal.write(record)
                emitProgress(record, index, file.size, onProgress)
                return TransferResult.Completed
            }
            val offset = progress.bytesWritten
            val response = try {
                callFactory(network).newCall(fileRequest(file, offset)).execute()
            } catch (_: IOException) {
                return retryable(record, index, TransferFailure.HttpFailure)
            }
            response.use { http ->
                if (offset > 0 && http.code == 200) {
                    try {
                        tree.delete(temporary.documentId)
                    } catch (_: Exception) {
                        return fail(
                            record,
                            index,
                            tree,
                            TransferFailure.DestinationFailure,
                        )
                    }
                    progress = progress.copy(
                        temporaryDocument = null,
                        bytesWritten = 0,
                    )
                    record = record.withFile(index, progress)
                    journal.write(record)
                    return@use
                }
                val wantedStatus = if (offset > 0) 206 else 200
                if (http.code != wantedStatus) {
                    return if (http.code == 412 || http.code == 416) {
                        fail(record, index, tree, TransferFailure.SourceChanged)
                    } else {
                        retryable(record, index, TransferFailure.HttpFailure)
                    }
                }
                http.header("ETag")?.let { responseETag ->
                    if (responseETag != file.etag) {
                        return fail(record, index, tree, TransferFailure.SourceChanged)
                    }
                }
                val body = http.body
                    ?: return retryable(record, index, TransferFailure.HttpFailure)
                val hash = MessageDigest.getInstance("SHA-256")
                if (offset > 0) {
                    try {
                        tree.openInput(temporary.documentId).use {
                            hashInto(it, hash)
                        }
                    } catch (_: Exception) {
                        return fail(
                            record,
                            index,
                            tree,
                            TransferFailure.DestinationFailure,
                        )
                    }
                }
                val output = try {
                    tree.openOutput(temporary.documentId, offset)
                } catch (_: DestinationSeekUnsupported) {
                    try {
                        tree.delete(temporary.documentId)
                    } catch (_: Exception) {
                        return fail(
                            record,
                            index,
                            tree,
                            TransferFailure.DestinationFailure,
                        )
                    }
                    progress = progress.copy(
                        temporaryDocument = null,
                        bytesWritten = 0,
                    )
                    record = record.withFile(index, progress)
                    journal.write(record)
                    return@use
                } catch (_: Exception) {
                    return fail(record, index, tree, TransferFailure.DestinationFailure)
                }
                var written = offset
                var checkpointBytes = offset
                var checkpointTime = nowMillis()
                var streamFailure: TransferFailure? = null
                try {
                    output.use { sink ->
                        val source = body.byteStream()
                        try {
                            val buffer = ByteArray(BUFFER_BYTES)
                            while (true) {
                                coroutineContext.ensureActive()
                                val remaining = file.size - written
                                val limit = if (remaining >= buffer.size.toLong()) {
                                    buffer.size
                                } else {
                                    remaining.toInt() + 1
                                }
                                val count = try {
                                    source.read(buffer, 0, limit)
                                } catch (_: IOException) {
                                    streamFailure = TransferFailure.HttpFailure
                                    break
                                }
                                if (count < 0) break
                                if (count.toLong() > remaining) {
                                    streamFailure = TransferFailure.SizeMismatch
                                    break
                                }
                                sink.write(buffer, 0, count)
                                hash.update(buffer, 0, count)
                                written += count
                                val now = nowMillis()
                                if (written - checkpointBytes >= CHECKPOINT_BYTES ||
                                    now - checkpointTime >= CHECKPOINT_MILLIS
                                ) {
                                    progress = progress.copy(
                                        bytesWritten = written,
                                        state = FileState.Downloading,
                                    )
                                    record = record.withFile(index, progress)
                                    journal.write(record)
                                    emitProgress(record, index, written, onProgress)
                                    checkpointBytes = written
                                    checkpointTime = now
                                }
                            }
                        } finally {
                            try {
                                source.close()
                            } catch (_: IOException) {
                                if (streamFailure == null) {
                                    streamFailure = TransferFailure.HttpFailure
                                }
                            }
                        }
                    }
                } catch (cancelled: CancellationException) {
                    journal.write(
                        record.withFile(
                            index,
                            progress.copy(
                                bytesWritten = written,
                                state = FileState.Downloading,
                            ),
                        ),
                    )
                    throw cancelled
                } catch (_: Exception) {
                    return fail(
                        record.withFile(
                            index,
                            progress.copy(bytesWritten = written),
                        ),
                        index,
                        tree,
                        TransferFailure.DestinationFailure,
                    )
                }
                streamFailure?.let { failure ->
                    val checkpoint = record.withFile(
                        index,
                        progress.copy(
                            bytesWritten = written,
                            state = FileState.Downloading,
                        ),
                    )
                    return if (failure == TransferFailure.HttpFailure) {
                        retryable(checkpoint, index, failure)
                    } else {
                        fail(checkpoint, index, tree, failure)
                    }
                }
                progress = progress.copy(bytesWritten = written)
                record = record.withFile(index, progress)
                journal.write(record)
                if (written != file.size) {
                    return fail(record, index, tree, TransferFailure.SizeMismatch)
                }
                if (hash.hex() != file.sha256.lowercase()) {
                    return fail(record, index, tree, TransferFailure.DigestMismatch)
                }
                try {
                    tree.finalize(temporary.documentId, destinationPath)
                } catch (_: Exception) {
                    return fail(record, index, tree, TransferFailure.DestinationFailure)
                }
                record = record.withFile(
                    index,
                    progress.copy(
                        temporaryDocument = null,
                        state = FileState.Completed,
                    ),
                )
                journal.write(record)
                emitProgress(record, index, written, onProgress)
                return TransferResult.Completed
            }
        }
    }

    private fun fail(
        record: TransferRecord,
        index: Int,
        tree: TransferDestination,
        failure: TransferFailure,
    ): TransferResult.Failed {
        val progress = record.files[index]
        var reportedFailure = failure
        progress.temporaryDocument?.let {
            try {
                tree.delete(it)
            } catch (_: Exception) {
                reportedFailure = TransferFailure.DestinationFailure
            }
        }
        journal.write(
            record.withFile(
                index,
                progress.copy(
                    temporaryDocument = null,
                    state = FileState.Failed,
                ),
            ).copy(state = TransferState.Failed),
        )
        return TransferResult.Failed(progress.fileId, reportedFailure)
    }

    private fun retryable(
        record: TransferRecord,
        index: Int,
        failure: TransferFailure,
    ): TransferResult.Failed {
        val progress = record.files[index].copy(state = FileState.Downloading)
        journal.write(record.withFile(index, progress).copy(state = TransferState.Running))
        return TransferResult.Failed(progress.fileId, failure)
    }

    private fun validRecord(record: TransferRecord): Boolean {
        if (record.files.size != record.manifest.files.size ||
            record.manifest.totalBytes < 0
        ) {
            return false
        }
        var total = 0L
        for ((file, progress) in record.manifest.files.zip(record.files)) {
            if (file.fileId.isBlank() ||
                file.fileId != progress.fileId ||
                file.size < 0 ||
                file.etag.isBlank() ||
                !SHA256.matches(file.sha256) ||
                !validDestinationPath(file.destinationPath) ||
                progress.bytesWritten !in 0..file.size ||
                total > Long.MAX_VALUE - file.size
            ) {
                return false
            }
            total += file.size
        }
        return total == record.manifest.totalBytes
    }

    private fun validDestinationPath(path: String): Boolean =
        path.isNotEmpty() &&
            !path.startsWith("/") &&
            !path.contains('\\') &&
            path.none(Char::isISOControl) &&
            path.split('/').none { it.isEmpty() || it == "." || it == ".." }

    private fun fileRequest(file: ManifestFileDto, offset: Long): Request {
        val url = baseUrl.newBuilder()
            .addPathSegments("v1/files")
            .addPathSegment(file.fileId)
            .build()
        return Request.Builder().url(url).apply {
            if (offset > 0) {
                header("Range", "bytes=$offset-")
                header("If-Match", file.etag)
            }
        }.get().build()
    }

    private fun keepBothPath(tree: TransferDestination, original: String): String {
        val slash = original.lastIndexOf('/')
        val prefix = if (slash < 0) "" else original.substring(0, slash + 1)
        val name = original.substring(slash + 1)
        val dot = name.lastIndexOf('.').takeIf { it > 0 } ?: name.length
        val stem = name.substring(0, dot)
        val extension = name.substring(dot)
        for (index in 1..10_000) {
            val candidate = "$prefix$stem (BookBox $index)$extension"
            if (tree.find(candidate) == null) return candidate
        }
        throw IOException("No free Android destination filename")
    }

    private fun digest(tree: TransferDestination, documentId: String): String =
        tree.openInput(documentId).use { source ->
            MessageDigest.getInstance("SHA-256").also {
                hashInto(source, it)
            }.hex()
        }

    private fun hashInto(source: InputStream, hash: MessageDigest) {
        val buffer = ByteArray(BUFFER_BYTES)
        while (true) {
            val count = source.read(buffer)
            if (count < 0) return
            hash.update(buffer, 0, count)
        }
    }

    private fun MessageDigest.hex(): String =
        digest().joinToString("") { "%02x".format(it) }

    private fun emitProgress(
        record: TransferRecord,
        index: Int,
        bytes: Long,
        callback: (TransferProgress) -> Unit,
    ) {
        callback(
            TransferProgress(
                transferId = record.id,
                fileId = record.files[index].fileId,
                bytesWritten = bytes,
                fileBytes = record.manifest.files[index].size,
                completedFiles = record.files.count {
                    it.state == FileState.Completed || it.state == FileState.Skipped
                },
                totalFiles = record.files.size,
            ),
        )
    }

    private companion object {
        const val BUFFER_BYTES = 1024 * 1024
        const val CHECKPOINT_BYTES = 4L * 1024 * 1024
        const val CHECKPOINT_MILLIS = 5_000L
        val SHA256 = Regex("^[0-9a-fA-F]{64}$")
    }
}

private fun TransferRecord.withFile(index: Int, progress: FileProgress): TransferRecord =
    copy(files = files.toMutableList().also { it[index] = progress })

package com.rendref.netstorage.core.transfer

import com.rendref.netstorage.core.model.ManifestDto
import com.rendref.netstorage.core.model.ManifestFileDto
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.security.MessageDigest
import kotlinx.coroutines.test.runTest
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okhttp3.Call
import okhttp3.Headers
import okhttp3.MediaType
import okhttp3.OkHttpClient
import okhttp3.Protocol
import okhttp3.Response
import okhttp3.ResponseBody
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import okio.BufferedSource
import okio.Source
import okio.Timeout
import okio.buffer
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class TransferEngineTest {
    @get:Rule
    val temporaryFolder = TemporaryFolder()

    @Test
    fun `resumes with a byte range and verifies the complete digest before rename`() = runTest {
        val server = MockWebServer()
        val client = server.enableTls()
        server.enqueue(
            MockResponse(
                code = 206,
                body = "efghij",
                headers = Headers.headersOf(
                    "Content-Range",
                    "bytes 4-9/10",
                    "ETag",
                    "\"source-etag\"",
                ),
            ),
        )
        server.start()
        try {
            val destination = FakeTransferDestination()
            destination.putTemporary("partial-1", "abcd".toByteArray())
            val journal = journalWith(
                recordFor(
                    expected = "abcdefghij".toByteArray(),
                    progress = FileProgress(
                        fileId = "signed-file-id",
                        temporaryDocument = "partial-1",
                        bytesWritten = 4,
                        state = FileState.Downloading,
                    ),
                ),
            )
            val engine = engine(server, client, journal, destination)

            val result = engine.run("transfer-1", network = null) {}

            assertEquals(TransferResult.Completed, result)
            assertArrayEquals(
                "abcdefghij".toByteArray(),
                destination.finalBytes("Dune/Dune.m4b"),
            )
            assertNull(destination.temporaryBytes("partial-1"))
            val request = server.takeRequest()
            assertEquals("bytes=4-", request.headers["Range"])
            assertEquals("\"source-etag\"", request.headers["If-Match"])
            val completed = journal.read("transfer-1")!!
            assertEquals(TransferState.Completed, completed.state)
            assertEquals(FileState.Completed, completed.files.single().state)
            assertEquals(10, completed.files.single().bytesWritten)
        } finally {
            server.close()
        }
    }

    @Test
    fun `digest mismatch removes the partial and never creates a final document`() = runTest {
        val server = MockWebServer()
        val client = server.enableTls()
        server.enqueue(MockResponse(body = "0123456789"))
        server.start()
        try {
            val destination = FakeTransferDestination()
            val journal = journalWith(recordFor("abcdefghij".toByteArray()))
            val engine = engine(server, client, journal, destination)

            val result = engine.run("transfer-1", network = null) {}

            assertTrue(result is TransferResult.Failed)
            val failed = result as TransferResult.Failed
            assertEquals(TransferFailure.DigestMismatch, failed.failure)
            assertNull(destination.finalBytes("Dune/Dune.m4b"))
            assertTrue(destination.temporaryIds().isEmpty())
            assertEquals(FileState.Failed, journal.read("transfer-1")!!.files.single().state)
        } finally {
            server.close()
        }
    }

    @Test
    fun `non seekable provider discards only its partial and restarts that file`() = runTest {
        val server = MockWebServer()
        val client = server.enableTls()
        server.enqueue(
            MockResponse(
                code = 206,
                body = "efghij",
                headers = Headers.headersOf("Content-Range", "bytes 4-9/10"),
            ),
        )
        server.enqueue(MockResponse(body = "abcdefghij"))
        server.start()
        try {
            val destination = FakeTransferDestination().apply {
                putTemporary("partial-1", "abcd".toByteArray())
                rejectNextSeek = true
            }
            val journal = journalWith(
                recordFor(
                    expected = "abcdefghij".toByteArray(),
                    progress = FileProgress(
                        fileId = "signed-file-id",
                        temporaryDocument = "partial-1",
                        bytesWritten = 4,
                        state = FileState.Downloading,
                    ),
                ),
            )

            val result = engine(server, client, journal, destination)
                .run("transfer-1", null) {}

            assertEquals(TransferResult.Completed, result)
            assertEquals("bytes=4-", server.takeRequest().headers["Range"])
            assertNull(server.takeRequest().headers["Range"])
            assertTrue("partial-1" in destination.deletedDocumentIds)
            assertArrayEquals(
                "abcdefghij".toByteArray(),
                destination.finalBytes("Dune/Dune.m4b"),
            )
        } finally {
            server.close()
        }
    }

    @Test
    fun `complete verified partial finalizes locally without an invalid range request`() = runTest {
        val server = MockWebServer()
        val client = server.enableTls()
        server.start()
        try {
            val expected = "abcdefghij".toByteArray()
            val destination = FakeTransferDestination().apply {
                putTemporary("partial-complete", expected)
            }
            val journal = journalWith(
                recordFor(
                    expected,
                    FileProgress(
                        fileId = "signed-file-id",
                        temporaryDocument = "partial-complete",
                        bytesWritten = expected.size.toLong(),
                        state = FileState.Downloading,
                    ),
                ),
            )

            val result = engine(server, client, journal, destination)
                .run("transfer-1", null) {}

            assertEquals(TransferResult.Completed, result)
            assertEquals(0, server.requestCount)
            assertArrayEquals(expected, destination.finalBytes("Dune/Dune.m4b"))
        } finally {
            server.close()
        }
    }

    @Test
    fun `ordinary network failure preserves resumable partial and checkpoint`() = runTest {
        val server = MockWebServer()
        server.enableTls()
        server.start()
        try {
            val destination = FakeTransferDestination().apply {
                putTemporary("partial-1", "abcd".toByteArray())
            }
            val journal = journalWith(
                recordFor(
                    "abcdefghij".toByteArray(),
                    FileProgress(
                        fileId = "signed-file-id",
                        temporaryDocument = "partial-1",
                        bytesWritten = 4,
                        state = FileState.Downloading,
                    ),
                ),
            )
            val engine = TransferEngine(
                baseUrl = server.url("/"),
                callFactory = {
                    Call.Factory { throw IOException("offline") }
                },
                journal = journal,
                destination = { destination },
            )

            val result = engine.run("transfer-1", null) {}

            assertEquals(
                TransferResult.Failed("signed-file-id", TransferFailure.HttpFailure),
                result,
            )
            assertArrayEquals("abcd".toByteArray(), destination.temporaryBytes("partial-1"))
            val progress = journal.read("transfer-1")!!.files.single()
            assertEquals("partial-1", progress.temporaryDocument)
            assertEquals(4, progress.bytesWritten)
            assertEquals(FileState.Downloading, progress.state)
        } finally {
            server.close()
        }
    }

    @Test
    fun `mid body network failure checkpoints partial after closing output`() = runTest {
        val server = MockWebServer()
        server.enableTls()
        server.start()
        try {
            val destination = FakeTransferDestination()
            val journal = journalWith(recordFor("abcdefghij".toByteArray()))
            val engine = TransferEngine(
                baseUrl = server.url("/"),
                callFactory = { interruptedBodyClient("abcd".toByteArray()) },
                journal = journal,
                destination = { destination },
            )

            val result = engine.run("transfer-1", null) {}

            assertEquals(
                TransferResult.Failed("signed-file-id", TransferFailure.HttpFailure),
                result,
            )
            val saved = journal.read("transfer-1")!!.files.single()
            assertEquals(FileState.Downloading, saved.state)
            assertEquals(4, saved.bytesWritten)
            assertEquals(0, destination.openOutputCount)
            assertArrayEquals(
                "abcd".toByteArray(),
                destination.temporaryBytes(saved.temporaryDocument!!),
            )
        } finally {
            server.close()
        }
    }

    @Test
    fun `oversized response is bounded before writing beyond expected size`() = runTest {
        val server = MockWebServer()
        val client = server.enableTls()
        server.enqueue(MockResponse(body = "x".repeat(64 * 1024)))
        server.start()
        try {
            val destination = FakeTransferDestination()
            val journal = journalWith(recordFor("abcdefghij".toByteArray()))

            val result = engine(server, client, journal, destination)
                .run("transfer-1", null) {}

            assertEquals(
                TransferFailure.SizeMismatch,
                (result as TransferResult.Failed).failure,
            )
            assertTrue(destination.maxBytesWritten <= 10)
            assertFalse(destination.deletedWhileOutputOpen)
            assertNull(destination.finalBytes("Dune/Dune.m4b"))
        } finally {
            server.close()
        }
    }

    @Test
    fun `engine rejects insecure origin and inconsistent manifest before destination mutation`() =
        runTest {
            val destination = FakeTransferDestination()
            val insecure = MockWebServer()
            insecure.start()
            try {
                org.junit.Assert.assertThrows(IllegalArgumentException::class.java) {
                    TransferEngine(
                        insecure.url("/"),
                        { OkHttpClient() },
                        journalWith(recordFor("abcdefghij".toByteArray())),
                        { destination },
                    )
                }
            } finally {
                insecure.close()
            }

            val secure = MockWebServer()
            val client = secure.enableTls()
            secure.start()
            try {
                val valid = recordFor("abcdefghij".toByteArray())
                val invalid = valid.copy(
                    manifest = valid.manifest.copy(
                        totalBytes = 9,
                        files = valid.manifest.files.map {
                            it.copy(etag = "", destinationPath = "../escape")
                        },
                    ),
                )
                val journal = journalWith(invalid)
                val result = engine(secure, client, journal, destination)
                    .run("transfer-1", null) {}
                assertEquals(
                    TransferResult.Failed(null, TransferFailure.InvalidManifest),
                    result,
                )
                assertTrue(destination.temporaryIds().isEmpty())
                assertEquals(0, secure.requestCount)
            } finally {
                secure.close()
            }
        }

    @Test
    fun `matching existing destination is hashed and completed without a request`() = runTest {
        val server = MockWebServer()
        val client = server.enableTls()
        server.start()
        try {
            val expected = "abcdefghij".toByteArray()
            val destination = FakeTransferDestination().apply {
                putFinal("Dune/Dune.m4b", expected)
            }
            val journal = journalWith(recordFor(expected))

            val result = engine(server, client, journal, destination)
                .run("transfer-1", null) {}

            assertEquals(TransferResult.Completed, result)
            assertEquals(0, server.requestCount)
            assertArrayEquals(expected, destination.finalBytes("Dune/Dune.m4b"))
            assertEquals(
                ConflictChoice.Skip,
                journal.read("transfer-1")!!.files.single().conflictChoice,
            )
        } finally {
            server.close()
        }
    }

    @Test
    fun `replace waits for explicit choice then deletes only the Android destination`() = runTest {
        val server = MockWebServer()
        val client = server.enableTls()
        server.enqueue(MockResponse(body = "abcdefghij"))
        server.start()
        try {
            val destination = FakeTransferDestination().apply {
                putFinal("Dune/Dune.m4b", "XXXXXXXXXX".toByteArray())
            }
            val journal = journalWith(recordFor("abcdefghij".toByteArray()))
            val engine = engine(server, client, journal, destination)

            val conflict = engine.run("transfer-1", null) {}

            assertTrue(conflict is TransferResult.Conflict)
            assertEquals(10L, (conflict as TransferResult.Conflict).existingBytes)
            assertEquals(
                10L,
                journal.read("transfer-1")!!.files.single().existingBytes,
            )
            assertEquals(emptyList<String>(), destination.deletedDocumentIds)
            assertEquals(0, server.requestCount)

            engine.chooseConflict("transfer-1", "signed-file-id", ConflictChoice.Replace)
            val completed = engine.run("transfer-1", null) {}

            assertEquals(TransferResult.Completed, completed)
            assertEquals(1, destination.deletedDocumentIds.size)
            assertArrayEquals(
                "abcdefghij".toByteArray(),
                destination.finalBytes("Dune/Dune.m4b"),
            )
        } finally {
            server.close()
        }
    }

    @Test
    fun `keep both increments the Android filename and skip leaves the original unchanged`() =
        runTest {
            val server = MockWebServer()
            val client = server.enableTls()
            server.enqueue(MockResponse(body = "abcdefghij"))
            server.start()
            try {
                val original = "XXXXXXXXXX".toByteArray()
                val destination = FakeTransferDestination().apply {
                    putFinal("Dune/Dune.m4b", original)
                    putFinal("Dune/Dune (netStorage 1).m4b", original)
                }
                val keepJournal = journalWith(recordFor("abcdefghij".toByteArray()))
                val keepEngine = engine(server, client, keepJournal, destination)
                assertTrue(keepEngine.run("transfer-1", null) {} is TransferResult.Conflict)
                keepEngine.chooseConflict(
                    "transfer-1",
                    "signed-file-id",
                    ConflictChoice.KeepBoth,
                )

                assertEquals(
                    TransferResult.Completed,
                    keepEngine.run("transfer-1", null) {},
                )
                assertArrayEquals(
                    "abcdefghij".toByteArray(),
                    destination.finalBytes("Dune/Dune (netStorage 2).m4b"),
                )
                assertArrayEquals(original, destination.finalBytes("Dune/Dune.m4b"))

                val skipDestination = FakeTransferDestination().apply {
                    putFinal("Dune/Dune.m4b", original)
                }
                val skipJournal = journalWith(recordFor("abcdefghij".toByteArray()))
                val skipEngine = engine(server, client, skipJournal, skipDestination)
                assertTrue(skipEngine.run("transfer-1", null) {} is TransferResult.Conflict)
                skipEngine.chooseConflict(
                    "transfer-1",
                    "signed-file-id",
                    ConflictChoice.Skip,
                )
                assertEquals(
                    TransferResult.Completed,
                    skipEngine.run("transfer-1", null) {},
                )
                assertArrayEquals(original, skipDestination.finalBytes("Dune/Dune.m4b"))
                assertEquals(1, server.requestCount)
            } finally {
                server.close()
            }
        }

    private fun journalWith(record: TransferRecord): TransferJournal =
        TransferJournal(temporaryFolder.newFolder()).also { it.write(record) }

    private fun engine(
        server: MockWebServer,
        client: OkHttpClient,
        journal: TransferJournal,
        destination: FakeTransferDestination,
    ) = TransferEngine(
        baseUrl = server.url("/"),
        callFactory = { client },
        journal = journal,
        destination = { destination },
    )
}

private fun recordFor(
    expected: ByteArray,
    progress: FileProgress = FileProgress(
        fileId = "signed-file-id",
        temporaryDocument = null,
        bytesWritten = 0,
        state = FileState.Pending,
    ),
): TransferRecord = TransferRecord(
    id = "transfer-1",
    manifest = ManifestDto(
        id = "manifest-1",
        files = listOf(
            ManifestFileDto(
                fileId = "signed-file-id",
                sourcePath = "Dune/Dune.m4b",
                destinationPath = "Dune/Dune.m4b",
                size = expected.size.toLong(),
                modifiedAt = "2026-07-26T12:00:00Z",
                etag = "\"source-etag\"",
                sha256 = expected.sha256(),
            ),
        ),
        totalBytes = expected.size.toLong(),
    ),
    files = listOf(progress),
    destinationTree = "content://documents/tree/audiobooks",
    state = TransferState.Pending,
)

private fun ByteArray.sha256(): String =
    MessageDigest.getInstance("SHA-256").digest(this).joinToString("") { "%02x".format(it) }

private class FakeTransferDestination : TransferDestination {
    private data class Stored(
        val id: String,
        var path: String?,
        var bytes: ByteArray,
    )

    private val documents = linkedMapOf<String, Stored>()
    private var nextId = 1
    val deletedDocumentIds = mutableListOf<String>()
    var rejectNextSeek = false
    var maxBytesWritten = 0
    var openOutputCount = 0
    var deletedWhileOutputOpen = false

    override fun find(relativePath: String): DestinationDocument? =
        documents.values.firstOrNull { it.path == relativePath }?.asDocument()

    override fun createTemporary(
        destinationPath: String,
        transferId: String,
        fileId: String,
    ): DestinationDocument {
        val id = "temporary-${nextId++}"
        return Stored(id, null, byteArrayOf()).also {
            documents[id] = it
        }.asDocument()
    }

    override fun stat(documentId: String): DestinationDocument =
        checkNotNull(documents[documentId]).asDocument()

    override fun openInput(documentId: String): InputStream =
        ByteArrayInputStream(checkNotNull(documents[documentId]).bytes)

    override fun openOutput(documentId: String, offset: Long): OutputStream {
        if (offset > 0 && rejectNextSeek) {
            rejectNextSeek = false
            throw DestinationSeekUnsupported()
        }
        val stored = checkNotNull(documents[documentId])
        require(offset in 0..stored.bytes.size.toLong())
        val initial = stored.bytes.copyOf(offset.toInt())
        openOutputCount += 1
        return object : ByteArrayOutputStream() {
            init {
                write(initial)
            }

            override fun close() {
                try {
                    stored.bytes = toByteArray()
                    maxBytesWritten = maxOf(maxBytesWritten, stored.bytes.size)
                    super.close()
                } finally {
                    openOutputCount -= 1
                }
            }
        }
    }

    override fun delete(documentId: String) {
        if (openOutputCount > 0) {
            deletedWhileOutputOpen = true
        }
        checkNotNull(documents.remove(documentId))
        deletedDocumentIds += documentId
    }

    override fun finalize(documentId: String, destinationPath: String): DestinationDocument {
        check(find(destinationPath) == null)
        return checkNotNull(documents[documentId]).also {
            it.path = destinationPath
        }.asDocument()
    }

    fun putTemporary(id: String, bytes: ByteArray) {
        documents[id] = Stored(id, null, bytes)
    }

    fun putFinal(path: String, bytes: ByteArray) {
        val id = "final-${nextId++}"
        documents[id] = Stored(id, path, bytes)
    }

    fun temporaryBytes(id: String): ByteArray? =
        documents[id]?.takeIf { it.path == null }?.bytes

    fun temporaryIds(): Set<String> =
        documents.values.filter { it.path == null }.mapTo(linkedSetOf()) { it.id }

    fun finalBytes(path: String): ByteArray? =
        documents.values.firstOrNull { it.path == path }?.bytes

    private fun Stored.asDocument() = DestinationDocument(id, bytes.size.toLong())
}

private fun interruptedBodyClient(prefix: ByteArray): OkHttpClient =
    OkHttpClient.Builder()
        .addInterceptor { chain ->
            Response.Builder()
                .request(chain.request())
                .protocol(Protocol.HTTP_1_1)
                .code(200)
                .message("OK")
                .body(
                    object : ResponseBody() {
                        override fun contentType(): MediaType? = null

                        override fun contentLength(): Long = 10

                        override fun source(): BufferedSource {
                            var delivered = false
                            return object : Source {
                                override fun read(sink: okio.Buffer, byteCount: Long): Long {
                                    if (!delivered) {
                                        delivered = true
                                        sink.write(prefix)
                                        return prefix.size.toLong()
                                    }
                                    throw IOException("connection lost mid-body")
                                }

                                override fun timeout(): Timeout = Timeout.NONE

                                override fun close() = Unit
                            }.buffer()
                        }
                    },
                )
                .build()
        }
        .build()

private fun MockWebServer.enableTls(): OkHttpClient {
    val certificate = HeldCertificate.Builder()
        .addSubjectAlternativeName("localhost")
        .build()
    val serverCertificates = HandshakeCertificates.Builder()
        .heldCertificate(certificate)
        .build()
    val clientCertificates = HandshakeCertificates.Builder()
        .addTrustedCertificate(certificate.certificate)
        .build()
    useHttps(serverCertificates.sslSocketFactory())
    return OkHttpClient.Builder()
        .sslSocketFactory(
            clientCertificates.sslSocketFactory(),
            clientCertificates.trustManager,
        )
        .build()
}

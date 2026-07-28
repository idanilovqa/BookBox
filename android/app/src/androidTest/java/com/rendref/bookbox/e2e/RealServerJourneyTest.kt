package com.rendref.bookbox.e2e

import android.content.pm.PackageManager
import android.provider.DocumentsContract
import androidx.test.platform.app.InstrumentationRegistry
import com.rendref.bookbox.core.model.ManifestDto
import com.rendref.bookbox.core.network.AuthenticatedCall
import com.rendref.bookbox.core.network.BrowseApi
import com.rendref.bookbox.core.network.CatalogApi
import com.rendref.bookbox.core.network.BookBoxClientFactory
import com.rendref.bookbox.core.network.SessionApi
import com.rendref.bookbox.core.network.TransferManifestApi
import com.rendref.bookbox.core.network.TransferSelection
import com.rendref.bookbox.core.repository.BookQuery
import com.rendref.bookbox.core.storage.RefreshTokenStore
import com.rendref.bookbox.core.storage.SavedConnection
import com.rendref.bookbox.core.storage.ContentResolverDocumentBackend
import com.rendref.bookbox.core.storage.DocumentTree
import com.rendref.bookbox.core.transfer.ConflictChoice
import com.rendref.bookbox.core.transfer.FileProgress
import com.rendref.bookbox.core.transfer.FileState
import com.rendref.bookbox.core.transfer.TransferEngine
import com.rendref.bookbox.core.transfer.TransferFailure
import com.rendref.bookbox.core.transfer.TransferJournal
import com.rendref.bookbox.core.transfer.TransferRecord
import com.rendref.bookbox.core.transfer.TransferResult
import com.rendref.bookbox.core.transfer.TransferState
import java.io.BufferedInputStream
import java.io.BufferedOutputStream
import java.net.Socket
import java.net.URLEncoder
import java.util.UUID
import java.util.concurrent.CopyOnWriteArrayList
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Interceptor
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class RealServerJourneyTest {
    @Test(timeout = 300_000)
    fun completeAuthenticatedReadOnlyJourneyResumesAndKeepsBoth() = runBlocking {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val arguments = InstrumentationRegistry.getArguments()
        val productionPackage = instrumentation.targetContext.packageManager
            .getPackageInfo(
                instrumentation.targetContext.packageName,
                PackageManager.PackageInfoFlags.of(
                    PackageManager.GET_PROVIDERS.toLong(),
                ),
            )
        assertTrue(
            productionPackage.providers.orEmpty().none {
                it.authority == TestDocumentsProvider.AUTHORITY
            },
        )
        val baseUrl = requiredArgument(arguments.getString("e2eBaseUrl")).toHttpUrl()
        val username = requiredArgument(arguments.getString("e2eUsername"))
        val password = requiredArgument(arguments.getString("e2ePassword"))
        val toxiproxyPort = arguments.getString("e2eToxiproxyPort")
            ?.toIntOrNull()
            ?.takeIf { it in 1..65535 }
            ?: 18474

        val approvedConnection = SavedConnection(
            baseUrl = baseUrl.toString(),
            username = username,
        )

        val tokenStore = MemoryRefreshTokenStore()
        val requests = CopyOnWriteArrayList<RecordedRequest>()
        val client = BookBoxClientFactory()
            .create(approvedConnection)
            .newBuilder()
            .addInterceptor(Interceptor { chain ->
                val request = chain.request()
                requests.add(
                    RecordedRequest(
                        method = request.method,
                        path = request.url.encodedPath,
                        range = request.header("Range"),
                    ),
                )
                chain.proceed(request)
            })
            .build()
        val session = SessionApi(client, baseUrl, tokenStore)
        session.login(username, password)
        val authenticated = AuthenticatedCall(client, session)
        val catalog = CatalogApi(authenticated, baseUrl)
        val browse = BrowseApi(authenticated, baseUrl)

        val duneBooks = awaitBooks(catalog, "Dune", minimum = 2)
        assertEquals(2, duneBooks.count { it.title.startsWith("Dune") })
        duneBooks.forEach { book ->
            val rawFiles = browse.bookFiles(book.id)
            assertEquals(book.sourceFolder, rawFiles.sourceFolder)
            assertTrue(rawFiles.files.isNotEmpty())
        }
        val normalBook = awaitBooks(catalog, "Normal Book", minimum = 1)
            .single { it.title == "Normal Book" }
        val duneMessiah = duneBooks.single { it.title == "Dune Messiah" }
        val manifest = TransferManifestApi(authenticated, baseUrl).create(
            listOf(
                TransferSelection.Book(normalBook.id),
                TransferSelection.Book(duneMessiah.id),
            ),
        )

        TestDocumentsControlProvider.reset(instrumentation.context)
        val treeUri = TestDocumentsControlProvider.grantTreeAccess(
            instrumentation.context,
            instrumentation.targetContext.packageName,
        )
        val tree = DocumentTree(
            treeUri = treeUri,
            rootDocumentId = TestDocumentsProvider.ROOT_DOCUMENT_ID,
            backend = ContentResolverDocumentBackend(
                instrumentation.targetContext.contentResolver,
                treeUri,
            ),
        )
        val firstFile = manifest.files.first()
        val conflict = tree.createTemporary(
            firstFile.destinationPath,
            UUID.randomUUID().toString(),
            "conflict-fixture",
        )
        tree.openOutput(conflict.documentId, 0).use {
            it.write("existing Android copy".toByteArray())
        }
        tree.finalize(conflict.documentId, firstFile.destinationPath)

        val transferId = UUID.randomUUID().toString()
        val journal = TransferJournal(instrumentation.targetContext.filesDir)
        journal.write(manifest.pendingRecord(transferId, treeUri.toString()))
        val engine = TransferEngine(
            baseUrl = baseUrl,
            callFactory = { authenticated },
            journal = journal,
            destination = { tree },
        )
        val conflictResult = engine.run(transferId, null) {}
        assertTrue(conflictResult is TransferResult.Conflict)
        conflictResult as TransferResult.Conflict
        assertEquals(firstFile.destinationPath, conflictResult.destinationPath)
        engine.chooseConflict(
            transferId,
            conflictResult.fileId,
            ConflictChoice.KeepBoth,
        )

        assertTrue("Fixture audio is too small for a range-resume test", firstFile.size > 8_192)
        val cutAfterBytes = (firstFile.size / 2 + 4_096)
            .coerceAtMost(firstFile.size - 1)
        addLimitDataToxic(baseUrl.host, toxiproxyPort, cutAfterBytes)
        val interrupted = try {
            engine.run(transferId, null) {}
        } finally {
            removeToxic(baseUrl.host, toxiproxyPort)
        }
        assertEquals(
            TransferResult.Failed(firstFile.fileId, TransferFailure.HttpFailure),
            interrupted,
        )
        val partial = requireNotNull(journal.read(transferId))
            .files
            .first { it.fileId == firstFile.fileId }
            .bytesWritten
        assertTrue("Expected a partial Android copy, got $partial bytes", partial in 1 until firstFile.size)

        assertEquals(TransferResult.Completed, engine.run(transferId, null) {})
        assertEquals(TransferState.Completed, journal.read(transferId)?.state)
        assertTrue(
            requests.any {
                it.method == "GET" &&
                    it.path.startsWith("/v1/files/") &&
                    it.range?.startsWith("bytes=$partial-") == true
            },
        )

        manifest.files.forEachIndexed { index, file ->
            val destinationPath = if (index == 0) {
                keepBothPath(file.destinationPath)
            } else {
                file.destinationPath
            }
            assertEquals(
                file.sha256,
                TestDocumentsControlProvider.sha256(
                    instrumentation.context,
                    destinationPath,
                ),
            )
        }
        val operations = TestDocumentsControlProvider.operations(instrumentation.context)
        assertTrue(operations.any { it.kind == "rename" })
        assertTrue(operations.any { it.kind == "open" })
        assertTrue(operations.none { it.relativePath.contains("H:", ignoreCase = true) })
        assertApprovedServerRequests(requests)
    }

    private suspend fun awaitBooks(
        catalog: CatalogApi,
        query: String,
        minimum: Int,
    ): List<com.rendref.bookbox.core.model.BookCardDto> {
        repeat(120) {
            val books = catalog.books(BookQuery(text = query)).items
            if (books.size >= minimum) return books
            delay(1_000)
        }
        throw AssertionError("Catalog did not contain $minimum results for $query")
    }

    private fun addLimitDataToxic(host: String, port: Int, bytes: Long) {
        val body = """
            {"name":"cut-first-download","type":"limit_data","stream":"downstream","toxicity":1.0,"attributes":{"bytes":$bytes}}
        """.trimIndent()
        assertEquals(
            200,
            toxiproxyRequest(
                host,
                port,
                "POST",
                "/proxies/bookbox/toxics",
                body,
            ),
        )
    }

    private fun removeToxic(host: String, port: Int) {
        val name = URLEncoder.encode("cut-first-download", Charsets.UTF_8.name())
        assertTrue(
            toxiproxyRequest(
                host,
                port,
                "DELETE",
                "/proxies/bookbox/toxics/$name",
                null,
            ) in setOf(204, 404),
        )
    }

    private fun toxiproxyRequest(
        host: String,
        port: Int,
        method: String,
        path: String,
        body: String?,
    ): Int {
        Socket(host, port).use { socket ->
            socket.soTimeout = 10_000
            val bytes = body?.toByteArray(Charsets.UTF_8) ?: byteArrayOf()
            val output = BufferedOutputStream(socket.getOutputStream())
            output.write("$method $path HTTP/1.1\r\n".toByteArray())
            output.write("Host: $host:$port\r\n".toByteArray())
            output.write("Connection: close\r\n".toByteArray())
            output.write("Content-Type: application/json\r\n".toByteArray())
            output.write("Content-Length: ${bytes.size}\r\n\r\n".toByteArray())
            output.write(bytes)
            output.flush()
            val statusLine = BufferedInputStream(socket.getInputStream())
                .bufferedReader(Charsets.US_ASCII)
                .readLine()
                ?: throw AssertionError("Toxiproxy returned no status")
            return statusLine.split(' ').getOrNull(1)?.toIntOrNull()
                ?: throw AssertionError("Invalid Toxiproxy response: $statusLine")
        }
    }

    private fun assertApprovedServerRequests(requests: List<RecordedRequest>) {
        assertTrue(requests.isNotEmpty())
        requests.forEach { request ->
            val approved = when {
                request.method == "POST" && request.path == "/v1/session" -> true
                request.method == "POST" && request.path == "/v1/session/refresh" -> true
                request.method == "GET" && request.path == "/v1/books" -> true
                request.method == "GET" &&
                    Regex("^/v1/books/[0-9a-f-]+/files$").matches(request.path) -> true
                request.method == "POST" &&
                    request.path == "/v1/transfers/manifest" -> true
                request.method == "GET" && request.path.startsWith("/v1/files/") -> true
                else -> false
            }
            assertTrue(
                "Unapproved Android server request: ${request.method} ${request.path}",
                approved,
            )
        }
    }

    private fun ManifestDto.pendingRecord(
        transferId: String,
        destinationTree: String,
    ) = TransferRecord(
        id = transferId,
        manifest = this,
        files = files.map {
            FileProgress(
                fileId = it.fileId,
                temporaryDocument = null,
                bytesWritten = 0,
                state = FileState.Pending,
            )
        },
        destinationTree = destinationTree,
        state = TransferState.Pending,
        displayName = "Normal Book and Dune Messiah",
    )

    private fun keepBothPath(original: String): String {
        val slash = original.lastIndexOf('/')
        val prefix = if (slash < 0) "" else original.substring(0, slash + 1)
        val name = original.substring(slash + 1)
        val dot = name.lastIndexOf('.').takeIf { it > 0 } ?: name.length
        return "$prefix${name.substring(0, dot)} (BookBox 1)${name.substring(dot)}"
    }

    private fun requiredArgument(value: String?): String =
        requireNotNull(value?.takeIf(String::isNotBlank)) {
            "Required E2E instrumentation argument is missing"
        }
}

private data class RecordedRequest(
    val method: String,
    val path: String,
    val range: String?,
)

private class MemoryRefreshTokenStore : RefreshTokenStore {
    private var value: String? = null

    override suspend fun read(): String? = value

    override suspend fun write(token: String) {
        value = token
    }

    override suspend fun clear() {
        value = null
    }
}

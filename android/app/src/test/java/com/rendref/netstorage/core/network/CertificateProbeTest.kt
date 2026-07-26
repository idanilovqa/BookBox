package com.rendref.netstorage.core.network

import java.security.MessageDigest
import java.security.cert.X509Certificate
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okhttp3.Headers.Companion.headersOf
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import com.rendref.netstorage.core.storage.SavedConnection
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class CertificateProbeTest {
    @Test
    fun `inspect captures uppercase fingerprint without credentials`() {
        val certificate = HeldCertificate.Builder()
            .addSubjectAlternativeName("localhost")
            .commonName("netStorage test")
            .build()
        val serverCertificates = HandshakeCertificates.Builder()
            .heldCertificate(certificate)
            .build()
        val server = MockWebServer()
        server.useHttps(serverCertificates.sslSocketFactory())
        server.enqueue(MockResponse(body = "ok"))
        server.start()

        try {
            val observed = CertificateProbe().inspect(server.url("/"))
            val request = server.takeRequest()

            assertEquals(fingerprint(certificate.certificate), observed.fingerprint)
            assertArrayEquals(certificate.certificate.encoded, observed.der)
            assertFalse(request.headers.names().any {
                it.equals("Authorization", ignoreCase = true) ||
                    it.equals("Cookie", ignoreCase = true)
            })
            assertFalse(request.body?.utf8()?.contains("username", ignoreCase = true) == true)
            assertFalse(request.body?.utf8()?.contains("password", ignoreCase = true) == true)
        } finally {
            server.close()
        }
    }

    @Test
    fun `inspect rejects non HTTPS URLs before a request`() {
        assertThrows(IllegalArgumentException::class.java) {
            CertificateProbe().inspect("http://192.168.1.10:8443".toHttpUrl())
        }
    }

    @Test
    fun `inspect rejects credentialed non origin URL before a request`() {
        val server = MockWebServer()
        server.start()
        try {
            val unsafe = server.url("/private")
                .newBuilder()
                .scheme("https")
                .username("owner")
                .password("password-sentinel")
                .query("probe=unsafe")
                .fragment("fragment")
                .build()

            assertThrows(IllegalArgumentException::class.java) {
                CertificateProbe().inspect(unsafe)
            }
            assertEquals(0, server.requestCount)
        } finally {
            server.close()
        }
    }

    @Test
    fun `pinned client accepts exact leaf and same host redirects`() {
        val certificate = HeldCertificate.Builder()
            .addSubjectAlternativeName("localhost")
            .build()
        val serverCertificates = HandshakeCertificates.Builder()
            .heldCertificate(certificate)
            .build()
        val server = MockWebServer()
        server.useHttps(serverCertificates.sslSocketFactory())
        server.enqueue(MockResponse(code = 302, headers = headersOf("Location", "/final")))
        server.enqueue(MockResponse(body = "ready"))
        server.start()

        try {
            val connection = SavedConnection(
                baseUrl = server.url("/").toString(),
                certificateSha256 = fingerprint(certificate.certificate),
                username = "owner",
            )
            val client = PinnedClientFactory().create(connection)
            val response = client.newCall(Request.Builder().url(server.url("/start")).build())
                .execute()

            response.use {
                assertEquals(200, it.code)
                assertEquals("ready", it.body.string())
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun `pinned client rejects redirects to another host`() {
        val certificate = HeldCertificate.Builder()
            .addSubjectAlternativeName("localhost")
            .build()
        val serverCertificates = HandshakeCertificates.Builder()
            .heldCertificate(certificate)
            .build()
        val server = MockWebServer()
        server.useHttps(serverCertificates.sslSocketFactory())
        server.enqueue(
            MockResponse(
                code = 302,
                headers = headersOf("Location", "https://127.0.0.1:65534/elsewhere"),
            ),
        )
        server.start()

        try {
            val connection = SavedConnection(
                baseUrl = server.url("/").toString(),
                certificateSha256 = fingerprint(certificate.certificate),
                username = "owner",
            )
            val client = PinnedClientFactory().create(connection)

            assertThrows(java.io.IOException::class.java) {
                client.newCall(Request.Builder().url(server.url("/start")).build())
                    .execute()
                    .close()
            }
        } finally {
            server.close()
        }
    }

    @Test
    fun `pinned client rejects redirects to another port on the same host`() {
        val certificate = HeldCertificate.Builder()
            .addSubjectAlternativeName("localhost")
            .build()
        val serverCertificates = HandshakeCertificates.Builder()
            .heldCertificate(certificate)
            .build()
        val origin = MockWebServer()
        val otherPort = MockWebServer()
        origin.useHttps(serverCertificates.sslSocketFactory())
        otherPort.useHttps(serverCertificates.sslSocketFactory())
        otherPort.enqueue(MockResponse(body = "must not be reached"))
        otherPort.start()
        origin.enqueue(
            MockResponse(
                code = 302,
                headers = headersOf("Location", otherPort.url("/elsewhere").toString()),
            ),
        )
        origin.start()

        try {
            val connection = SavedConnection(
                baseUrl = origin.url("/").toString(),
                certificateSha256 = fingerprint(certificate.certificate),
                username = "owner",
            )
            val client = PinnedClientFactory().create(connection)

            assertThrows(java.io.IOException::class.java) {
                client.newCall(Request.Builder().url(origin.url("/start")).build())
                    .execute()
                    .close()
            }
            assertEquals(0, otherPort.requestCount)
        } finally {
            origin.close()
            otherPort.close()
        }
    }

    @Test
    fun `pinned client never redirects a credential bearing POST`() {
        val certificate = HeldCertificate.Builder()
            .addSubjectAlternativeName("localhost")
            .build()
        val serverCertificates = HandshakeCertificates.Builder()
            .heldCertificate(certificate)
            .build()
        val server = MockWebServer()
        server.useHttps(serverCertificates.sslSocketFactory())
        server.enqueue(MockResponse(code = 302, headers = headersOf("Location", "/other")))
        server.enqueue(MockResponse(body = "must not be reached"))
        server.start()

        try {
            val connection = SavedConnection(
                baseUrl = server.url("/").toString(),
                certificateSha256 = fingerprint(certificate.certificate),
                username = "owner",
            )
            val client = PinnedClientFactory().create(connection)
            val request = Request.Builder()
                .url(server.url("/v1/session"))
                .post("""{"password":"sentinel"}""".toRequestBody())
                .build()

            assertThrows(java.io.IOException::class.java) {
                client.newCall(request).execute().close()
            }
            assertEquals(1, server.requestCount)
        } finally {
            server.close()
        }
    }

    @Test
    fun `pinned client rejects a different leaf certificate`() {
        val certificate = HeldCertificate.Builder()
            .addSubjectAlternativeName("localhost")
            .build()
        val serverCertificates = HandshakeCertificates.Builder()
            .heldCertificate(certificate)
            .build()
        val server = MockWebServer()
        server.useHttps(serverCertificates.sslSocketFactory())
        server.enqueue(MockResponse(body = "should not be trusted"))
        server.start()

        try {
            val connection = SavedConnection(
                baseUrl = server.url("/").toString(),
                certificateSha256 = "00:".repeat(31) + "00",
                username = "owner",
            )
            val client = PinnedClientFactory().create(connection)

            val failure = assertThrows(java.io.IOException::class.java) {
                client.newCall(Request.Builder().url(server.url("/")).build())
                    .execute()
                    .close()
            }
            assertTrue(
                failure.causesAndSuppressed()
                    .any { it is java.security.cert.CertificateException },
            )
        } finally {
            server.close()
        }
    }

    private fun fingerprint(certificate: X509Certificate): String =
        MessageDigest.getInstance("SHA-256")
            .digest(certificate.encoded)
            .joinToString(":") { byte -> "%02X".format(byte) }

    private fun Throwable.causesAndSuppressed(): Sequence<Throwable> = sequence {
        val remaining = ArrayDeque<Throwable>()
        val seen = java.util.Collections.newSetFromMap(
            java.util.IdentityHashMap<Throwable, Boolean>(),
        )
        remaining.add(this@causesAndSuppressed)
        while (remaining.isNotEmpty()) {
            val next = remaining.removeFirst()
            if (!seen.add(next)) continue
            yield(next)
            next.cause?.let(remaining::add)
            next.suppressed.forEach(remaining::add)
        }
    }
}

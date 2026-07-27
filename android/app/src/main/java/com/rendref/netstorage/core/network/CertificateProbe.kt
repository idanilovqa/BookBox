package com.rendref.netstorage.core.network

import java.security.MessageDigest
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager
import okhttp3.HttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request

data class ObservedCertificate(
    val fingerprint: String,
    val der: ByteArray,
)

fun interface CertificateInspector {
    suspend fun inspect(httpsUrl: HttpUrl): ObservedCertificate
}

class CertificateProbe {
    fun inspect(httpsUrl: HttpUrl): ObservedCertificate {
        require(httpsUrl.isHttps) { "Only HTTPS server addresses are supported" }
        require(
            httpsUrl.encodedUsername.isEmpty() &&
                httpsUrl.encodedPassword.isEmpty() &&
                httpsUrl.query == null &&
                httpsUrl.fragment == null &&
                httpsUrl.encodedPath == "/",
        ) { "Certificate probe requires a clean HTTPS origin" }

        val capture = CapturingTrustManager()
        val context = SSLContext.getInstance("TLS").apply {
            init(null, arrayOf(capture), null)
        }
        val client = OkHttpClient.Builder()
            .sslSocketFactory(context.socketFactory, capture)
            .hostnameVerifier { _, _ -> true }
            .followRedirects(false)
            .followSslRedirects(false)
            .build()
        val request = Request.Builder()
            .url(httpsUrl)
            .get()
            .build()

        client.newCall(request).execute().use {
            // Completing the TLS handshake is sufficient; the response is intentionally ignored.
        }

        val certificate = checkNotNull(capture.leaf) {
            "The server did not present a leaf certificate"
        }
        val der = certificate.encoded
        return ObservedCertificate(
            fingerprint = sha256Fingerprint(der),
            der = der.copyOf(),
        )
    }
}

internal fun sha256Fingerprint(der: ByteArray): String =
    MessageDigest.getInstance("SHA-256")
        .digest(der)
        .joinToString(":") { byte -> "%02X".format(byte) }

private class CapturingTrustManager : X509TrustManager {
    @Volatile
    var leaf: X509Certificate? = null
        private set

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        throw java.security.cert.CertificateException("Client certificates are not accepted")
    }

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val presented = chain?.firstOrNull()
            ?: throw java.security.cert.CertificateException("Server certificate chain is empty")
        leaf = presented
    }

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

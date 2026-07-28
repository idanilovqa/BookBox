package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.storage.SavedConnection
import java.io.IOException
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response

class PinnedClientFactory {
    fun create(connection: SavedConnection): OkHttpClient {
        val baseUrl = connection.baseUrl.toHttpUrl()
        require(baseUrl.isHttps) { "Saved server must use HTTPS" }
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty()) {
            "Server URL must not contain credentials"
        }
        val expectedDigest = decodeFingerprint(connection.certificateSha256)
        val trustManager = ExactLeafTrustManager(expectedDigest)
        val context = SSLContext.getInstance("TLS").apply {
            init(null, arrayOf(trustManager), null)
        }

        return OkHttpClient.Builder()
            .sslSocketFactory(context.socketFactory, trustManager)
            .hostnameVerifier { requestedHost, _ -> requestedHost == baseUrl.host }
            .followRedirects(false)
            .followSslRedirects(false)
            .addInterceptor(SameOriginRedirectInterceptor(baseUrl))
            .build()
    }
}

private class ExactLeafTrustManager(
    private val expectedDigest: ByteArray,
) : X509TrustManager {
    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        throw CertificateException("Client certificates are not accepted")
    }

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull()
            ?: throw CertificateException("Server certificate chain is empty")
        val actualDigest = MessageDigest.getInstance("SHA-256").digest(leaf.encoded)
        if (!MessageDigest.isEqual(expectedDigest, actualDigest)) {
            throw CertificateException("Server certificate does not match the approved fingerprint")
        }
    }

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

private class SameOriginRedirectInterceptor(
    private val savedOrigin: HttpUrl,
) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        var request = checkedRequest(chain.request())
        repeat(MAX_REDIRECTS + 1) { redirectCount ->
            val response = chain.proceed(request)
            if (!response.isRedirect) return response

            val location = response.header("Location")
                ?: return response
            val target = request.url.resolve(location)
                ?: run {
                    response.close()
                    throw IOException("Server returned an invalid redirect")
                }
            if (!target.sameOriginAs(savedOrigin)) {
                response.close()
                throw IOException("Server redirected outside the approved origin")
            }
            if (redirectCount == MAX_REDIRECTS) {
                response.close()
                throw IOException("Too many server redirects")
            }
            if (request.method != "GET" && request.method != "HEAD") {
                response.close()
                throw IOException("Credential-bearing requests are not redirected")
            }

            request = request.newBuilder().url(target).build()
            response.close()
        }
        throw IOException("Too many server redirects")
    }

    private fun checkedRequest(request: Request): Request {
        if (!request.url.sameOriginAs(savedOrigin)) {
            throw IOException("Request is outside the approved HTTPS origin")
        }
        return request
    }

    private companion object {
        const val MAX_REDIRECTS = 10
    }
}

private fun HttpUrl.sameOriginAs(other: HttpUrl): Boolean =
    scheme == other.scheme && host == other.host && port == other.port

private fun decodeFingerprint(value: String): ByteArray {
    require(FINGERPRINT.matches(value)) {
        "Certificate fingerprint must be uppercase colon-separated SHA-256"
    }
    return value.split(':')
        .map { octet -> octet.toInt(16).toByte() }
        .toByteArray()
}

private val FINGERPRINT = Regex("^(?:[0-9A-F]{2}:){31}[0-9A-F]{2}$")

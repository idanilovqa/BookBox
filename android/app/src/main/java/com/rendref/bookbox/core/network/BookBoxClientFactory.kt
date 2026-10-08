package com.rendref.bookbox.core.network

import com.rendref.bookbox.core.storage.SavedConnection
import java.io.IOException
import java.util.concurrent.TimeUnit
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response

class BookBoxClientFactory {
    fun create(connection: SavedConnection): OkHttpClient {
        val baseUrl = connection.baseUrl.toHttpUrl()
        require(baseUrl.scheme == "http") { "Saved BookBox server must use HTTP" }
        require(baseUrl.encodedUsername.isEmpty() && baseUrl.encodedPassword.isEmpty()) {
            "Server URL must not contain credentials"
        }
        return OkHttpClient.Builder()
            .readTimeout(5, TimeUnit.MINUTES)
            .followRedirects(false)
            .followSslRedirects(false)
            .addInterceptor(SameOriginRedirectInterceptor(baseUrl))
            .build()
    }
}

private class SameOriginRedirectInterceptor(
    private val savedOrigin: HttpUrl,
) : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        var request = checkedRequest(chain.request())
        repeat(MAX_REDIRECTS + 1) { redirectCount ->
            val response = chain.proceed(request)
            if (!response.isRedirect) return response
            val location = response.header("Location") ?: return response
            val target = request.url.resolve(location) ?: run {
                response.close()
                throw IOException("Server returned an invalid redirect")
            }
            if (
                target.encodedUsername.isNotEmpty() ||
                target.encodedPassword.isNotEmpty()
            ) {
                response.close()
                throw IOException("Server redirect must not contain credentials")
            }
            if (!target.sameOriginAs(savedOrigin)) {
                response.close()
                throw IOException("Server redirected outside the saved BookBox origin")
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
            throw IOException("Request is outside the saved BookBox origin")
        }
        return request
    }

    private companion object {
        const val MAX_REDIRECTS = 10
    }
}

private fun HttpUrl.sameOriginAs(other: HttpUrl): Boolean =
    scheme == other.scheme && host == other.host && port == other.port

package com.rendref.netstorage.core.network

import java.io.IOException
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import okhttp3.Call
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response

class AuthenticatedCall(
    pinnedClient: OkHttpClient,
    private val session: TokenSession,
) : Call.Factory {
    private val refreshMutex = Mutex()
    private val client = pinnedClient.newBuilder()
        .addInterceptor(AuthenticationInterceptor())
        .build()

    override fun newCall(request: Request): Call = client.newCall(request)

    private inner class AuthenticationInterceptor : Interceptor {
        override fun intercept(chain: Interceptor.Chain): Response {
            val firstToken = session.accessToken ?: refreshIfCurrent(null)
            val firstResponse = chain.proceed(chain.request().withBearer(firstToken))
            if (firstResponse.code != 401 || chain.request().body?.isOneShot() == true) {
                return firstResponse
            }

            firstResponse.close()
            val replacement = refreshIfCurrent(firstToken)
            return chain.proceed(chain.request().withBearer(replacement))
        }
    }

    private fun refreshIfCurrent(failedToken: String?): String = runBlocking {
        refreshMutex.withLock {
            val current = session.accessToken
            if (current != null && current != failedToken) {
                current
            } else {
                try {
                    session.refresh().accessToken
                } catch (failure: CancellationException) {
                    throw failure
                } catch (_: Exception) {
                    throw IOException("Unable to refresh the saved session")
                }
            }
        }
    }
}

private fun Request.withBearer(accessToken: String): Request =
    newBuilder()
        .header("Authorization", "Bearer $accessToken")
        .build()

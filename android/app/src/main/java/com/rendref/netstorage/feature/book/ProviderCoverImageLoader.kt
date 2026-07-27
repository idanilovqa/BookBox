package com.rendref.netstorage.feature.book

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import coil3.ImageLoader
import coil3.network.okhttp.OkHttpNetworkFetcherFactory
import java.io.IOException
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.Response

/**
 * Provider artwork never uses the authenticated, certificate-pinned server client.
 *
 * Candidate cover URLs can point to Open Library or Google Books. This isolated
 * loader has no app credentials and refuses cleartext, URL userinfo, and redirects.
 */
@Composable
internal fun rememberProviderCoverImageLoader(): ImageLoader {
    val context = LocalContext.current
    return remember(context) {
        val publicCalls = OkHttpClient.Builder()
            .followRedirects(false)
            .followSslRedirects(false)
            .addInterceptor(PublicCoverInterceptor)
            .build()
        ImageLoader.Builder(context)
            .components {
                add(OkHttpNetworkFetcherFactory(publicCalls))
            }
            .build()
    }
}

private object PublicCoverInterceptor : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val url = chain.request().url
        if (
            !url.isHttps ||
            url.encodedUsername.isNotEmpty() ||
            url.encodedPassword.isNotEmpty()
        ) {
            throw IOException("Provider cover URL is not trusted")
        }
        return chain.proceed(chain.request())
    }
}

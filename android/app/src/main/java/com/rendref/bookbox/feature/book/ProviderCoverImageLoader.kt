package com.rendref.bookbox.feature.book

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalContext
import coil3.ImageLoader
import coil3.network.okhttp.OkHttpNetworkFetcherFactory
import java.io.IOException
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.HttpUrl
import okhttp3.Response

/**
 * Provider artwork never uses the authenticated BookBox server client.
 *
 * Candidate cover URLs can point to Open Library or Google Books. This isolated
 * loader has no app credentials and refuses cleartext, URL userinfo, and redirects.
 */
@Composable
internal fun rememberProviderCoverImageLoader(): ImageLoader {
    val context = LocalContext.current
    return remember(context) {
        ImageLoader.Builder(context)
            .components {
                add(OkHttpNetworkFetcherFactory(providerCoverClient()))
            }
            .build()
    }
}

internal fun providerCoverClient(): OkHttpClient = OkHttpClient.Builder()
    .addNetworkInterceptor(PublicCoverInterceptor)
    .build()

internal fun trustedProviderCoverUrl(url: HttpUrl): Boolean =
    url.isHttps &&
        url.encodedUsername.isEmpty() &&
        url.encodedPassword.isEmpty()

internal object PublicCoverInterceptor : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val url = chain.request().url
        if (!trustedProviderCoverUrl(url)) {
            throw IOException("Provider cover URL is not trusted")
        }
        return chain.proceed(chain.request())
    }
}

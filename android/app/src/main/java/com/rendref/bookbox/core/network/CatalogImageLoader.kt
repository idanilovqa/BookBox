package com.rendref.bookbox.core.network

import coil3.ImageLoader
import coil3.SingletonImageLoader
import coil3.network.okhttp.OkHttpNetworkFetcherFactory
import okhttp3.Call

/**
 * Installs the app-wide cover loader before the library UI is composed.
 *
 * The supplied factory must be the same [AuthenticatedCall] used by [CatalogApi],
 * so covers retain exact-origin checks and
 * serialized single refresh behavior.
 */
fun installCatalogImageLoader(calls: Call.Factory) {
    SingletonImageLoader.setSafe { context ->
        ImageLoader.Builder(context)
            .components {
                add(OkHttpNetworkFetcherFactory(calls))
            }
            .build()
    }
}

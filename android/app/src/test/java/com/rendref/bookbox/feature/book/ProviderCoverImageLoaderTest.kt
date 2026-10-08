package com.rendref.bookbox.feature.book

import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ProviderCoverImageLoaderTest {
    @Test
    fun `provider client follows redirects and validates every network hop`() {
        val client = providerCoverClient()

        assertTrue(client.followRedirects)
        assertTrue(client.followSslRedirects)
        assertEquals(listOf(PublicCoverInterceptor), client.networkInterceptors)
    }

    @Test
    fun `provider cover trust requires credential free HTTPS`() {
        assertTrue(
            trustedProviderCoverUrl(
                "https://covers.openlibrary.org/b/id/8231856-L.jpg".toHttpUrl(),
            ),
        )
        assertFalse(
            trustedProviderCoverUrl(
                "http://covers.openlibrary.org/b/id/8231856-L.jpg".toHttpUrl(),
            ),
        )
        assertFalse(
            trustedProviderCoverUrl(
                "https://user:secret@covers.openlibrary.org/b/id/8231856-L.jpg".toHttpUrl(),
            ),
        )
    }
}

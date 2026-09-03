package com.rendref.bookbox.feature.library

import org.junit.Assert.assertEquals
import org.junit.Test

class BookCardLabelTest {
    @Test
    fun `corrupted metadata is visible on card badge`() {
        assertEquals("Corrupted metadata", reviewBadgeLabel("needs_review", "corrupted_metadata"))
        assertEquals("Needs review", reviewBadgeLabel("needs_review", null))
    }
}

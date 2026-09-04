package com.rendref.bookbox.feature.library

import org.junit.Assert.assertEquals
import org.junit.Test

class BookCardLabelTest {
    @Test
    fun `corrupted metadata is visible on card badge`() {
        assertEquals("Corrupted metadata", reviewBadgeLabel("needs_review", "corrupted_metadata"))
        assertEquals("Needs review", reviewBadgeLabel("needs_review", null))
    }

    @Test
    fun `Great Courses books are labelled as lectures`() {
        assertEquals("Lecture", cardChipLabel("The Great Courses", "Audiobook"))
        assertEquals("Lecture", cardChipLabel("the great courses", null))
    }

    @Test
    fun `other books keep their genre chip`() {
        assertEquals("Fantasy", cardChipLabel("Discworld", "Fantasy"))
        assertEquals("Audiobook", cardChipLabel(null, "Audiobook"))
        assertEquals(null, cardChipLabel(null, null))
    }
}

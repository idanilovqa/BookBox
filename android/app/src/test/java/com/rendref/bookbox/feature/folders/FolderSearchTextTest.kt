package com.rendref.bookbox.feature.folders

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class FolderSearchTextTest {
    @Test
    fun `searchable text recognizes supplementary Unicode letters`() {
        val deseretLetter = "\uD801\uDC00"

        assertTrue(hasSearchCharacters(deseretLetter))
        assertFalse(hasSearchCharacters(" .-/ "))
    }
}

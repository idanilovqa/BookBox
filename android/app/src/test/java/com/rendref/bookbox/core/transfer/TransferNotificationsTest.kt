package com.rendref.bookbox.core.transfer

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

class TransferNotificationsTest {
    @Test
    fun `notification file label is a safe leaf rather than a path`() {
        assertEquals("Dune.m4b", safeNotificationFileLabel("Dune/Dune.m4b"))
        assertEquals("Selected audiobook", safeNotificationFileLabel("H:\\Audiobooks\\Dune.m4b"))
        assertEquals(
            "Selected audiobook",
            safeNotificationFileLabel("content://documents/tree/audiobooks"),
        )
        assertFalse(safeNotificationFileLabel("/Dune.m4b").contains('/'))
    }

    @Test
    fun `scaled notification progress cannot overflow`() {
        assertEquals(1_000, scaledTransferProgress(Long.MAX_VALUE, Long.MAX_VALUE))
        assertEquals(500, scaledTransferProgress(Long.MAX_VALUE / 2, Long.MAX_VALUE))
        assertEquals(0, scaledTransferProgress(-1, Long.MAX_VALUE))
        assertEquals(0, scaledTransferProgress(10, 0))
    }
}

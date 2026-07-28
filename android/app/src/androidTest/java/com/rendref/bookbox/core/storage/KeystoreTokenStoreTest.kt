package com.rendref.bookbox.core.storage

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.io.File
import java.security.KeyStore
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class KeystoreTokenStoreTest {
    private val context
        get() = InstrumentationRegistry.getInstrumentation().targetContext
    private lateinit var store: KeystoreTokenStore

    @Before
    fun setUp() {
        store = KeystoreTokenStore(context)
        File(context.noBackupFilesDir, "session.token.blocked.tmp").deleteRecursively()
        kotlinx.coroutines.runBlocking { store.clear() }
    }

    @After
    fun tearDown() {
        kotlinx.coroutines.runBlocking { store.clear() }
    }

    @Test
    fun refreshTokenIsEncryptedAndSurvivesANewStoreInstance() =
        kotlinx.coroutines.runBlocking {
            val token = "refresh-token-sentinel"

            store.write(token)

            val tokenFile = File(context.noBackupFilesDir, "session.token")
            assertTrue(tokenFile.isFile)
            assertFalse(tokenFile.readBytes().toString(Charsets.UTF_8).contains(token))
            assertEquals(token, KeystoreTokenStore(context).read())
        }

    @Test
    fun clearDeletesCiphertextAndAndroidKeystoreKey() = kotlinx.coroutines.runBlocking {
        store.write("refresh-token")

        store.clear()

        assertNull(store.read())
        assertFalse(File(context.noBackupFilesDir, "session.token").exists())
        val keyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        assertFalse(keyStore.containsAlias("bookbox_refresh_token_v1"))
    }

    @Test
    fun oversizedRefreshTokenAndCiphertextAreRejected() {
        kotlinx.coroutines.runBlocking {
            assertThrows(IllegalArgumentException::class.java) {
                kotlinx.coroutines.runBlocking { store.write("x".repeat(16 * 1024 + 1)) }
            }

            File(context.noBackupFilesDir, "session.token")
                .writeBytes(ByteArray(64 * 1024 + 1))
            assertThrows(TokenStorageException::class.java) {
                kotlinx.coroutines.runBlocking { store.read() }
            }
        }
    }

    @Test
    fun clearDeletesKeyEvenWhenStaleTemporaryCleanupFails() {
        kotlinx.coroutines.runBlocking {
            store.write("refresh-token")
            val undeletableTemporary = File(
                context.noBackupFilesDir,
                "session.token.blocked.tmp",
            ).apply {
                mkdir()
                File(this, "nested").writeText("keep directory non-empty")
            }

            try {
                assertThrows(TokenStorageException::class.java) {
                    kotlinx.coroutines.runBlocking { store.clear() }
                }

                val keyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
                assertFalse(keyStore.containsAlias("bookbox_refresh_token_v1"))
            } finally {
                File(undeletableTemporary, "nested").delete()
                undeletableTemporary.delete()
            }
        }
    }
}

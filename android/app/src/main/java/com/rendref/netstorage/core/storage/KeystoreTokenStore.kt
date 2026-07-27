package com.rendref.netstorage.core.storage

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.system.Os
import java.io.File
import java.io.FileOutputStream
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

class KeystoreTokenStore(
    context: Context,
) : RefreshTokenStore {
    private val directory = context.noBackupFilesDir
    private val tokenFile = File(directory, TOKEN_FILE)

    override suspend fun read(): String? {
        if (!tokenFile.isFile) return null
        return runCatching {
            require(tokenFile.length() in 1..MAX_CIPHERTEXT_FILE_BYTES) {
                "Encrypted session size is invalid"
            }
            val encoded = tokenFile.readBytes()
            require(encoded.size >= HEADER_SIZE + GCM_TAG_SIZE) {
                "Encrypted session is invalid"
            }
            require(encoded[0] == FORMAT_VERSION) {
                "Encrypted session version is unsupported"
            }
            val nonce = encoded.copyOfRange(1, HEADER_SIZE)
            val ciphertext = encoded.copyOfRange(HEADER_SIZE, encoded.size)
            val cipher = Cipher.getInstance(TRANSFORMATION).apply {
                init(Cipher.DECRYPT_MODE, existingKey(), GCMParameterSpec(GCM_TAG_BITS, nonce))
                updateAAD(byteArrayOf(FORMAT_VERSION))
            }
            val plaintext = cipher.doFinal(ciphertext)
            require(plaintext.size <= MAX_TOKEN_BYTES) { "Encrypted session is too large" }
            plaintext.toString(Charsets.UTF_8)
        }.getOrElse {
            throw TokenStorageException("Unable to read the encrypted session", it)
        }
    }

    override suspend fun write(token: String) {
        require(token.isNotEmpty()) { "Refresh token must not be empty" }
        val plaintext = token.toByteArray(Charsets.UTF_8)
        require(plaintext.size <= MAX_TOKEN_BYTES) { "Refresh token is too large" }
        runCatching {
            directory.mkdirs()
            val cipher = Cipher.getInstance(TRANSFORMATION).apply {
                init(Cipher.ENCRYPT_MODE, getOrCreateKey())
                updateAAD(byteArrayOf(FORMAT_VERSION))
            }
            val nonce = cipher.iv
            check(nonce.size == NONCE_SIZE) { "Unexpected encryption nonce size" }
            val ciphertext = cipher.doFinal(plaintext)
            val temporary = File.createTempFile("$TOKEN_FILE.", ".tmp", directory)
            try {
                FileOutputStream(temporary).use { output ->
                    output.write(byteArrayOf(FORMAT_VERSION))
                    output.write(nonce)
                    output.write(ciphertext)
                    output.flush()
                    output.fd.sync()
                }
                Os.rename(temporary.absolutePath, tokenFile.absolutePath)
            } finally {
                temporary.delete()
            }
        }.getOrElse {
            throw TokenStorageException("Unable to save the encrypted session", it)
        }
    }

    override suspend fun clear() {
        var cleanupFailure: Throwable? = null
        if (tokenFile.exists() && !tokenFile.delete()) {
            cleanupFailure = IllegalStateException("Encrypted session file could not be deleted")
        }
        directory.listFiles { file ->
            file.name.startsWith("$TOKEN_FILE.") && file.name.endsWith(".tmp")
        }?.forEach { temporary ->
            if (!temporary.delete() && cleanupFailure == null) {
                cleanupFailure =
                    IllegalStateException("Temporary session file could not be deleted")
            }
        }
        try {
            keyStore().deleteEntry(KEY_ALIAS)
        } catch (failure: Exception) {
            if (cleanupFailure == null) {
                cleanupFailure = failure
            }
        }
        cleanupFailure?.let {
            throw TokenStorageException("Unable to clear the encrypted session", it)
        }
    }

    private fun getOrCreateKey(): SecretKey {
        val keyStore = keyStore()
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
            .apply {
                init(
                    KeyGenParameterSpec.Builder(
                        KEY_ALIAS,
                        KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
                    )
                        .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                        .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                        .setKeySize(256)
                        .setRandomizedEncryptionRequired(true)
                        .setUserAuthenticationRequired(false)
                        .build(),
                )
            }
            .generateKey()
    }

    private fun existingKey(): SecretKey =
        keyStore().getKey(KEY_ALIAS, null) as? SecretKey
            ?: throw IllegalStateException("Encrypted session key is missing")

    private fun keyStore(): KeyStore =
        KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "netstorage_refresh_token_v1"
        const val TOKEN_FILE = "session.token"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val NONCE_SIZE = 12
        const val GCM_TAG_BITS = 128
        const val GCM_TAG_SIZE = GCM_TAG_BITS / 8
        const val FORMAT_VERSION: Byte = 1
        const val HEADER_SIZE = 1 + NONCE_SIZE
        const val MAX_TOKEN_BYTES = 16 * 1024
        const val MAX_CIPHERTEXT_FILE_BYTES = 64 * 1024L
    }
}

class TokenStorageException(
    message: String,
    cause: Throwable,
) : Exception(message, cause) {
    override fun toString(): String = "${this::class.java.name}: $message"
}

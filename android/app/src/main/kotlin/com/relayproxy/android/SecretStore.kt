package com.relayproxy.android

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import org.json.JSONObject
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Stores the local VPN proxy token separately from ordinary app config.
 *
 * Only AES-GCM ciphertext is persisted in SharedPreferences. The AES key is
 * generated inside Android Keystore and is never exported to the app.
 */
class SecretStore(context: Context) {
    private val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    fun vpnProxyToken(): String = synchronized(vpnTokenLock) {
        val encoded = prefs.getString(KEY_VPN_PROXY_TOKEN, null)?.trim().orEmpty()
        if (encoded.isNotBlank()) {
            val current = runCatching { decrypt(encoded) }.getOrNull()
            if (!current.isNullOrBlank()) return@synchronized current
        }
        val bytes = ByteArray(32).also { SecureRandom().nextBytes(it) }
        val token = Base64.encodeToString(
            bytes,
            Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE,
        )
        prefs.edit().putString(KEY_VPN_PROXY_TOKEN, encrypt(token)).apply()
        token
    }

    // Custom proxy credentials use the same Android Keystore-backed AES-GCM
    // mechanism as the private VPN token. Only non-secret metadata is stored
    // in the ordinary configuration preferences.
    fun customExitPasswords(): Map<String, String> = synchronized(customExitLock) {
        val cipherText = prefs.getString(KEY_CUSTOM_EXIT_PASSWORDS, null)
            ?: return@synchronized emptyMap()
        val json = JSONObject(decrypt(cipherText))
        buildMap {
            for (key in json.keys()) {
                put(key, json.getString(key))
            }
        }
    }

    fun setCustomExitPasswords(passwords: Map<String, String>) = synchronized(customExitLock) {
        val json = JSONObject()
        passwords.filterValues { it.isNotEmpty() }.forEach { (id, value) ->
            json.put(id, value)
        }
        prefs.edit().putString(KEY_CUSTOM_EXIT_PASSWORDS, encrypt(json.toString())).commit().also {
            check(it) { "无法保存加密的自定义出口密码" }
        }
    }

    private fun encrypt(value: String): String {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, secretKey())
        val ciphertext = cipher.doFinal(value.toByteArray(Charsets.UTF_8))
        // Keep the format simple so migration is independent of Java object
        // serialization: [iv length][iv][ciphertext+GCM tag].
        val packed = ByteArray(1 + cipher.iv.size + ciphertext.size)
        packed[0] = cipher.iv.size.toByte()
        System.arraycopy(cipher.iv, 0, packed, 1, cipher.iv.size)
        System.arraycopy(ciphertext, 0, packed, 1 + cipher.iv.size, ciphertext.size)
        return Base64.encodeToString(packed, Base64.NO_WRAP)
    }

    private fun decrypt(encoded: String): String {
        val packed = Base64.decode(encoded, Base64.NO_WRAP)
        require(packed.isNotEmpty()) { "empty encrypted credential" }
        val ivSize = packed[0].toInt() and 0xff
        require(ivSize in 12..32 && packed.size > 1 + ivSize) { "invalid encrypted credential" }
        val iv = packed.copyOfRange(1, 1 + ivSize)
        val ciphertext = packed.copyOfRange(1 + ivSize, packed.size)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, secretKey(), GCMParameterSpec(128, iv))
        return cipher.doFinal(ciphertext).toString(Charsets.UTF_8)
    }

    private fun secretKey(): SecretKey {
        val keyStore = KeyStore.getInstance(KEYSTORE).apply { load(null) }
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }

        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE)
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build()
        )
        return generator.generateKey()
    }

    companion object {
        private val vpnTokenLock = Any()
        private val customExitLock = Any()
        private const val KEY_CUSTOM_EXIT_PASSWORDS = "customExitPasswords"
        private const val PREFS_NAME = "relayproxy_android_secrets"
        private const val KEY_VPN_PROXY_TOKEN = "vpnProxyToken"
        private const val KEYSTORE = "AndroidKeyStore"
        private const val KEY_ALIAS = "relayproxy.identity.access.v1"
        private const val TRANSFORMATION = "AES/GCM/NoPadding"
    }
}

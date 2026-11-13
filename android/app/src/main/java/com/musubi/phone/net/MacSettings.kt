package com.musubi.phone.net

import android.content.Context
import android.os.Build

/** Pairing state in app-private preferences. The token is stored only encrypted (TokenVault). */
class MacSettings(context: Context) {
    private val prefs = context.getSharedPreferences("mac", Context.MODE_PRIVATE)

    val address: MacAddress?
        get() {
            val host = prefs.getString(K_HOST, null) ?: return null
            return MacAddress.parse(host, prefs.getInt(K_PORT, MacAddress.DEFAULT_PORT).toString())
        }

    val pin: String? get() = prefs.getString(K_PIN, null)
    val deviceId: String? get() = prefs.getString(K_DEVICE, null)
    val isPaired: Boolean get() = address != null && pin != null && prefs.contains(K_TOKEN)

    fun token(): String? = prefs.getString(K_TOKEN, null)?.let { TokenVault.decrypt(it) }

    fun savePairing(addr: MacAddress, pin: String, deviceId: String, token: String) {
        prefs.edit()
            .putString(K_HOST, addr.host)
            .putInt(K_PORT, addr.port)
            .putString(K_PIN, pin)
            .putString(K_DEVICE, deviceId)
            .putString(K_TOKEN, TokenVault.encrypt(token))
            .remove(K_LAST_RESULT)
            .apply()
    }

    /** Forgets the Mac; recordings and the agenda stay on the phone. */
    fun unpair() {
        prefs.edit().remove(K_HOST).remove(K_PORT).remove(K_PIN).remove(K_DEVICE).remove(K_TOKEN).remove(K_LAST_RESULT).apply()
        TokenVault.deleteKey()
    }

    var lastSyncAt: Long
        get() = prefs.getLong(K_LAST_SYNC, 0)
        set(v) = prefs.edit().putLong(K_LAST_SYNC, v).apply()

    var lastResult: String?
        get() = prefs.getString(K_LAST_RESULT, null)
        set(v) = prefs.edit().putString(K_LAST_RESULT, v).apply()

    companion object {
        private const val K_HOST = "host"
        private const val K_PORT = "port"
        private const val K_PIN = "pin_sha256"
        private const val K_DEVICE = "device_id"
        private const val K_TOKEN = "token_enc"
        private const val K_LAST_SYNC = "last_sync_at"
        private const val K_LAST_RESULT = "last_result"

        fun deviceName(): String = "${Build.MANUFACTURER} ${Build.MODEL}".trim().take(100)
    }
}

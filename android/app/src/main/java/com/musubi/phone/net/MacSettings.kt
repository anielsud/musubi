package com.musubi.phone.net

import android.content.Context
import android.os.Build

/**
 * Pairing state in app-private preferences. The token is stored only encrypted (TokenVault).
 * The Mac is known by its Bonjour instance name and pinned certificate; its address may change,
 * so [address] is only the last one that worked (or the one typed under Advanced).
 */
class MacSettings(context: Context) {
    private val prefs = context.getSharedPreferences("mac", Context.MODE_PRIVATE)

    /** Last address that worked. */
    var address: MacAddress?
        get() {
            val host = prefs.getString(K_HOST, null) ?: return null
            val port = prefs.getInt(K_PORT, MacAddress.DEFAULT_PORT)
            return MacAddress.parse(host, port.toString()) ?: MacAddress(host, port).takeIf { port in 1..65535 }
        }
        set(v) {
            prefs.edit().apply { if (v == null) remove(K_HOST).remove(K_PORT) else putString(K_HOST, v.host).putInt(K_PORT, v.port) }.apply()
        }

    /** Bonjour instance name ("Musubi on …"); null when paired by a typed address. */
    val instanceName: String? get() = prefs.getString(K_NAME, null)
    val pin: String? get() = prefs.getString(K_PIN, null)
    val deviceId: String? get() = prefs.getString(K_DEVICE, null)
    val isPaired: Boolean get() = (instanceName != null || address != null) && pin != null && prefs.contains(K_TOKEN)

    fun token(): String? = prefs.getString(K_TOKEN, null)?.let { TokenVault.decrypt(it) }

    fun savePairing(name: String?, addr: MacAddress, pin: String, deviceId: String, token: String) {
        prefs.edit()
            .apply { if (name == null) remove(K_NAME) else putString(K_NAME, name) }
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
        prefs.edit().remove(K_NAME).remove(K_HOST).remove(K_PORT).remove(K_PIN).remove(K_DEVICE).remove(K_TOKEN).remove(K_LAST_RESULT).apply()
        TokenVault.deleteKey()
    }

    var lastSyncAt: Long
        get() = prefs.getLong(K_LAST_SYNC, 0)
        set(v) = prefs.edit().putLong(K_LAST_SYNC, v).apply()

    var lastResult: String?
        get() = prefs.getString(K_LAST_RESULT, null)
        set(v) = prefs.edit().putString(K_LAST_RESULT, v).apply()

    companion object {
        private const val K_NAME = "instance_name"
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

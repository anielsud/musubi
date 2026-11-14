package com.musubi.phone.net

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.util.Log

/**
 * Advertises the phone while it listens (docs/phone.md, "Advertising"): `_musubi-phone._tcp`,
 * instance `musubi-<first 8 hex of the certificate SHA-256>` (no device or owner name), TXT
 * `v=2`, `fp=<first 16 hex>` and `pair=1` only while pairing is open. Changing `pair` means
 * registering again. Call from one thread only (PhoneSync's control thread).
 */
class Advertiser(context: Context, private val certSha256Hex: String, private val port: Int) {
    private val nsd = context.applicationContext.getSystemService(NsdManager::class.java)
    private var listener: NsdManager.RegistrationListener? = null
    private var advertisedPair: Boolean? = null

    /** Registers, re-registers or unregisters so the advertisement matches [on] and [pair]. */
    fun update(on: Boolean, pair: Boolean) {
        if (!on) {
            unregister()
            return
        }
        if (advertisedPair == pair && listener != null) return
        unregister()
        val info = NsdServiceInfo().apply {
            serviceName = instanceName(certSha256Hex)
            serviceType = SERVICE_TYPE
            // Qualified: inside apply, a bare `port` is NsdServiceInfo's own (still 0).
            setPort(this@Advertiser.port)
            setAttribute("v", "2")
            setAttribute("fp", certSha256Hex.take(16))
            if (pair) setAttribute("pair", "1")
        }
        val l = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(s: NsdServiceInfo) {
                Log.i(TAG, "advertising")
            }

            override fun onRegistrationFailed(s: NsdServiceInfo, errorCode: Int) {
                Log.w(TAG, "advertising failed: $errorCode")
            }
            override fun onServiceUnregistered(s: NsdServiceInfo) {}
            override fun onUnregistrationFailed(s: NsdServiceInfo, errorCode: Int) {}
        }
        val r = runCatching { nsd.registerService(info, NsdManager.PROTOCOL_DNS_SD, l) }
        if (r.isSuccess) {
            listener = l
            advertisedPair = pair
        } else {
            Log.w(TAG, "advertising refused: ${r.exceptionOrNull()?.javaClass?.simpleName}")
        }
    }

    private fun unregister() {
        listener?.let { runCatching { nsd.unregisterService(it) } }
        listener = null
        advertisedPair = null
    }

    companion object {
        const val SERVICE_TYPE = "_musubi-phone._tcp"
        private const val TAG = "MusubiSync"

        fun instanceName(certSha256Hex: String) = "musubi-" + certSha256Hex.take(8)
    }
}

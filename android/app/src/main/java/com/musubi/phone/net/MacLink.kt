package com.musubi.phone.net

import android.content.Context
import com.musubi.phone.core.ApiResult
import com.musubi.phone.core.DiscoveryParse
import com.musubi.phone.core.PairCode
import com.musubi.phone.core.PairParse
import com.musubi.phone.core.PairReply
import com.musubi.phone.core.PairState

/** Finds the paired Mac's current address at each sync. */
object MacLocator {
    private const val SEARCH_MS = 8_000L

    /**
     * The last address that worked, if it still presents the pinned certificate; otherwise the
     * address Bonjour reports for the paired instance name (whose fp must match the pin).
     */
    fun locate(ctx: Context, settings: MacSettings, pin: String): MacAddress? {
        settings.address?.let { if (Pinning.probe(it.host, it.port, pin)) return it }
        val name = settings.instanceName ?: return null
        for (m in MacDiscovery.find(ctx, name, SEARCH_MS)) {
            if (!DiscoveryParse.matches(pin, m.fp)) continue
            val addr = MacAddress(m.host, m.port)
            if (Pinning.probe(addr.host, addr.port, pin)) {
                settings.address = addr
                return addr
            }
        }
        return null
    }
}

/**
 * One pairing attempt (docs/phone.md, "Pairing, confirmed on both"). Runs off the UI thread;
 * each step returns a message for the owner on failure. Never logs codes, nonces or tokens.
 */
class PairingSession(
    private val name: String?,
    val address: MacAddress,
    /** The advertised 16-hex fp, or null for a typed address. */
    private val advertisedFp: String?,
) {
    var fingerprint: String? = null
        private set
    private var api: HttpMacApi? = null
    private var requestId: String? = null
    @Volatile var cancelled = false

    sealed class Step {
        data class Code(val code: String) : Step()
        data class Failed(val message: String) : Step()
    }

    /** Connects, checks the certificate against the advertisement, sends the request, derives the code. */
    fun request(deviceName: String): Step {
        val fp = runCatching { Pinning.fetchFingerprint(address.host, address.port) }.getOrNull()
            ?: return Step.Failed("Could not reach the Mac")
        if (advertisedFp != null && !DiscoveryParse.matches(fp, advertisedFp)) {
            return Step.Failed("This Mac's certificate does not match what it announced. Not pairing.")
        }
        fingerprint = fp
        val client = HttpMacApi(address, fp, null).also { api = it }
        val nonce = PairCode.newNonce()
        return when (val res = client.pairRequest(deviceName, nonce)) {
            is ApiResult.Ok -> {
                val reply = runCatching { PairParse.requestReply(res.body) }.getOrNull()
                    ?: return Step.Failed("Unexpected reply from the Mac")
                requestId = reply.requestId
                Step.Code(PairCode.derive(PairCode.fingerprintBytes(fp), nonce, reply.macNonce))
            }
            is ApiResult.Http -> Step.Failed(
                when (res.code) {
                    409 -> "The Mac is already pairing another phone; try again shortly"
                    429 -> "Too many pairing attempts; wait a few minutes and try again"
                    403 -> "The Mac refused pairing for now; try again later"
                    else -> "The Mac refused (HTTP ${res.code})"
                },
            )
            is ApiResult.Network -> Step.Failed(if (res.certificateMismatch) "The Mac's certificate changed during pairing" else "Could not reach the Mac")
        }
    }

    /** Sends Confirm, then polls until the Mac allows, denies or the request expires. */
    fun confirmAndWait(): Pair<PairReply?, String> {
        val client = api ?: return null to "Not started"
        val id = requestId ?: return null to "Not started"
        when (val res = client.pairConfirm(id)) {
            is ApiResult.Ok -> Unit
            is ApiResult.Http -> return null to if (res.code == 404 || res.code == 410) "The request expired; try again" else "The Mac refused (HTTP ${res.code})"
            is ApiResult.Network -> return null to "Could not reach the Mac"
        }
        val deadline = System.currentTimeMillis() + WAIT_MS
        while (!cancelled && System.currentTimeMillis() < deadline) {
            when (val res = client.pairState(id)) {
                is ApiResult.Ok -> when (val st = runCatching { PairParse.state(res.body) }.getOrNull()) {
                    PairState.Pending -> Unit
                    PairState.Denied -> return null to "The Mac denied pairing"
                    PairState.Expired -> return null to "The request expired; try again"
                    is PairState.Paired -> return st.reply to "Paired"
                    null -> return null to "Unexpected reply from the Mac"
                }
                is ApiResult.Http -> return null to if (res.code == 404 || res.code == 410) "The request expired; try again" else "The Mac refused (HTTP ${res.code})"
                is ApiResult.Network -> Unit // Wi-Fi blips: keep polling until the deadline.
            }
            Thread.sleep(POLL_MS)
        }
        return null to if (cancelled) "Cancelled" else "The request expired; try again"
    }

    fun save(ctx: Context, reply: PairReply) {
        MacSettings(ctx).savePairing(name, address, fingerprint!!, reply.deviceId, reply.token)
    }

    companion object {
        private const val POLL_MS = 1_000L
        /** A request lives 2 minutes on the Mac; a little slack for clocks and polling. */
        private const val WAIT_MS = 130_000L
    }
}

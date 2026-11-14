package com.musubi.phone.core

import org.json.JSONObject
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64

/**
 * Two-sided pairing (docs/phone.md, "Pairing, confirmed on both"): both sides derive the same
 * 6-digit code from the Mac's certificate and both nonces, and the owner compares them. A machine
 * in the middle shows the phone a different certificate, so the codes differ.
 */
object PairCode {
    const val NONCE_BYTES = 16
    private val LABEL = "musubi-pair-v1".toByteArray(Charsets.US_ASCII)

    fun newNonce(random: SecureRandom = SecureRandom()): ByteArray = ByteArray(NONCE_BYTES).also { random.nextBytes(it) }

    /**
     * First 4 bytes of SHA-256("musubi-pair-v1" ‖ certSha256 ‖ phoneNonce ‖ macNonce) as a
     * big-endian unsigned number, mod 1,000,000, zero-padded to 6 digits.
     */
    fun derive(certSha256: ByteArray, phoneNonce: ByteArray, macNonce: ByteArray): String {
        require(certSha256.size == 32) { "certificate hash must be 32 bytes" }
        require(phoneNonce.size == NONCE_BYTES && macNonce.size == NONCE_BYTES) { "nonces must be 16 bytes" }
        val md = MessageDigest.getInstance("SHA-256")
        md.update(LABEL)
        md.update(certSha256)
        md.update(phoneNonce)
        md.update(macNonce)
        val h = md.digest()
        val n = ((h[0].toLong() and 0xff) shl 24) or ((h[1].toLong() and 0xff) shl 16) or
            ((h[2].toLong() and 0xff) shl 8) or (h[3].toLong() and 0xff)
        return "%06d".format(n % 1_000_000)
    }

    /** "123456" → "123 456", as both screens show it. */
    fun display(code: String): String = code.take(3) + " " + code.drop(3)

    /** Lower-case 64-hex fingerprint → its 32 bytes. */
    fun fingerprintBytes(hex: String): ByteArray {
        require(Hashing.isSha256Hex(hex)) { "not a SHA-256 fingerprint" }
        return ByteArray(32) { i -> hex.substring(i * 2, i * 2 + 2).toInt(16).toByte() }
    }
}

data class PairRequestReply(val requestId: String, val macNonce: ByteArray)

/** State of a pairing request as the Mac reports it. Only [Paired] carries the token. */
sealed class PairState {
    object Pending : PairState()
    object Denied : PairState()
    object Expired : PairState()
    data class Paired(val reply: PairReply) : PairState()
}

/** Parsing of the pairing replies; untrusted input, strictly checked. */
object PairParse {
    private val REQUEST_ID_RE = Regex("^[A-Za-z0-9_-]{1,128}$")

    fun isValidRequestId(id: String) = REQUEST_ID_RE.matches(id)

    fun requestBody(deviceName: String, phoneNonce: ByteArray): ByteArray =
        JSONObject()
            .put("device_name", deviceName.take(100))
            .put("nonce", Base64.getEncoder().encodeToString(phoneNonce))
            .toString().toByteArray(Charsets.UTF_8)

    fun requestReply(body: ByteArray): PairRequestReply {
        val o = JSONObject(String(body, Charsets.UTF_8))
        val id = o.getString("request_id")
        require(isValidRequestId(id)) { "bad request id" }
        val nonceText = o.getString("nonce")
        require(nonceText.length <= 64) { "bad nonce" }
        val nonce = Base64.getDecoder().decode(nonceText)
        require(nonce.size == PairCode.NONCE_BYTES) { "bad nonce" }
        return PairRequestReply(id, nonce)
    }

    fun state(body: ByteArray): PairState {
        val o = JSONObject(String(body, Charsets.UTF_8))
        return when (o.getString("state")) {
            "pending" -> PairState.Pending
            "denied" -> PairState.Denied
            "expired" -> PairState.Expired
            "paired" -> PairState.Paired(ApiParse.pair(body))
            else -> throw IllegalArgumentException("unknown state")
        }
    }
}

/** A Mac found on the local network (Bonjour `_musubi._tcp`). */
data class FoundMac(val name: String, val fp: String, val host: String, val port: Int)

object DiscoveryParse {
    const val SERVICE_TYPE = "_musubi._tcp"
    private val FP_RE = Regex("^[0-9a-f]{16}$")

    /** Checks the TXT record (v=1, fp = 16 hex digits); returns the lower-case fp or null. */
    fun fingerprint(attrs: Map<String, ByteArray?>): String? {
        val v = attrs["v"]?.let { String(it, Charsets.US_ASCII) }
        if (v != "1") return null
        val fp = attrs["fp"]?.let { String(it, Charsets.US_ASCII).trim().lowercase() } ?: return null
        return fp.takeIf { FP_RE.matches(it) }
    }

    /** Instance names are shown and stored; keep them printable and short. */
    fun cleanName(name: String?): String? {
        val n = name?.trim() ?: return null
        if (n.isEmpty() || n.length > 63 || n.any { it.isISOControl() }) return null
        return n
    }

    /** True when [fullFingerprint] (64 hex) begins with the advertised 16-hex [fp]. */
    fun matches(fullFingerprint: String, fp: String): Boolean =
        fullFingerprint.length == 64 && fullFingerprint.take(16).equals(fp, ignoreCase = true)
}

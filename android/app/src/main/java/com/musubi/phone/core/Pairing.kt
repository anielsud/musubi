package com.musubi.phone.core

import java.security.MessageDigest
import java.security.SecureRandom

/**
 * The pairing code (docs/phone.md, "Pairing, confirmed on both"): both sides derive the same six
 * digits from both certificates (as each sees them in the TLS session) and both nonces, and the
 * owner compares them. A machine in the middle changes a certificate, so the codes differ.
 */
object PairCode {
    const val NONCE_BYTES = 16
    private val LABEL = "musubi-pair-v2".toByteArray(Charsets.US_ASCII)

    fun newNonce(random: SecureRandom = SecureRandom()): ByteArray = ByteArray(NONCE_BYTES).also { random.nextBytes(it) }

    /**
     * First 4 bytes of SHA-256("musubi-pair-v2" ‖ Mac cert SHA-256 ‖ phone cert SHA-256 ‖ Mac
     * nonce ‖ phone nonce) as a big-endian unsigned number, mod 1,000,000, zero-padded to 6 digits.
     */
    fun derive(macCertSha256: ByteArray, phoneCertSha256: ByteArray, macNonce: ByteArray, phoneNonce: ByteArray): String {
        require(macCertSha256.size == 32 && phoneCertSha256.size == 32) { "certificate hashes must be 32 bytes" }
        require(macNonce.size == NONCE_BYTES && phoneNonce.size == NONCE_BYTES) { "nonces must be 16 bytes" }
        val md = MessageDigest.getInstance("SHA-256")
        md.update(LABEL)
        md.update(macCertSha256)
        md.update(phoneCertSha256)
        md.update(macNonce)
        md.update(phoneNonce)
        val h = md.digest()
        val n = ((h[0].toLong() and 0xff) shl 24) or ((h[1].toLong() and 0xff) shl 16) or
            ((h[2].toLong() and 0xff) shl 8) or (h[3].toLong() and 0xff)
        return "%06d".format(n % 1_000_000)
    }

    /** "123456" → "123 456", as both screens show it. */
    fun display(code: String): String = code.take(3) + " " + code.drop(3)
}

/**
 * The phone's side of one pairing, as a small state machine shared by the UI (owner) and the
 * connection thread (Mac). The owner opens a 2-minute window; one Mac connection may then ask
 * (`pair_request`); the code is shown and the owner has 2 minutes to Confirm or Cancel. The
 * Mac's `pair_confirm` blocks until the owner has decided. [onPaired] runs (pinning the Mac)
 * before `pair_confirm` is answered with ok, and only when both sides agreed.
 */
class PairingFlow(
    private val now: () -> Long,
    private val onPaired: (macCertSha256: ByteArray, macName: String) -> Unit,
) {
    enum class Failure { CANCELLED, DENIED, EXPIRED, DISCONNECTED }

    sealed class State {
        object Idle : State()
        data class Waiting(val until: Long) : State()
        class Code(
            val macName: String,
            val code: String,
            val macFp: ByteArray,
            val deadline: Long,
            val owner: Any,
            val ownerConfirmed: Boolean,
        ) : State()
        data class Paired(val macName: String) : State()
        data class Failed(val why: Failure) : State()
    }

    sealed class RequestResult {
        data class Ok(val code: String) : RequestResult()
        /** Wire error code: `not_pairing` or `busy`. */
        data class Refused(val error: String) : RequestResult()
    }

    /** Reply to `pair_confirm`: null means ok; otherwise the wire error. */
    enum class Decision(val error: String?) { OK(null), CANCELLED("cancelled"), EXPIRED("expired") }

    private val lock = Object()
    private var state: State = State.Idle

    fun state(): State = synchronized(lock) { expire(); state }

    /** Pairing is open for a new Mac request, or a code is being compared. */
    fun isOpen(): Boolean = synchronized(lock) {
        expire()
        state is State.Waiting || state is State.Code
    }

    fun open() = synchronized(lock) {
        state = State.Waiting(now() + WINDOW_MS)
        lock.notifyAll()
    }

    /** Back to idle (the owner dismissed the result, or unpaired). */
    fun reset() = synchronized(lock) {
        state = State.Idle
        lock.notifyAll()
    }

    fun request(
        connection: Any,
        macName: String,
        macFp: ByteArray,
        phoneFp: ByteArray,
        macNonce: ByteArray,
        phoneNonce: ByteArray,
    ): RequestResult = synchronized(lock) {
        expire()
        when (state) {
            is State.Waiting -> {
                val code = PairCode.derive(macFp, phoneFp, macNonce, phoneNonce)
                state = State.Code(macName, code, macFp.copyOf(), now() + DECIDE_MS, connection, false)
                lock.notifyAll()
                RequestResult.Ok(code)
            }
            is State.Code -> RequestResult.Refused("busy")
            else -> RequestResult.Refused("not_pairing")
        }
    }

    fun ownerConfirm() = synchronized(lock) {
        expire()
        val s = state as? State.Code ?: return
        state = State.Code(s.macName, s.code, s.macFp, s.deadline, s.owner, true)
        lock.notifyAll()
    }

    fun ownerCancel() = synchronized(lock) {
        expire()
        if (state is State.Waiting || state is State.Code) state = State.Failed(Failure.CANCELLED)
        lock.notifyAll()
    }

    /**
     * The Mac allowed: waits (at most until the code's deadline) for the owner. Only the connection
     * that asked may confirm; any other gets `expired`, which is what it would see after a restart.
     */
    fun macConfirm(connection: Any): Decision = synchronized(lock) {
        while (true) {
            expire()
            when (val s = state) {
                is State.Code -> {
                    if (s.owner !== connection) return Decision.EXPIRED
                    if (s.ownerConfirmed) {
                        onPaired(s.macFp, s.macName)
                        state = State.Paired(s.macName)
                        lock.notifyAll()
                        return Decision.OK
                    }
                    val wait = s.deadline - now()
                    if (wait > 0) lock.wait(minOf(wait, 1_000L))
                }
                is State.Failed -> return if (s.why == Failure.CANCELLED) Decision.CANCELLED else Decision.EXPIRED
                else -> return Decision.EXPIRED
            }
        }
        @Suppress("UNREACHABLE_CODE")
        Decision.EXPIRED
    }

    /** The Mac's owner chose Deny. */
    fun macCancel(connection: Any) = synchronized(lock) {
        val s = state
        if (s is State.Code && s.owner === connection) {
            state = State.Failed(Failure.DENIED)
            lock.notifyAll()
        }
    }

    /** The connection that asked has gone before pairing finished. */
    fun connectionClosed(connection: Any) = synchronized(lock) {
        val s = state
        if (s is State.Code && s.owner === connection) {
            state = State.Failed(Failure.DISCONNECTED)
            lock.notifyAll()
        }
    }

    private fun expire() {
        val s = state
        val t = now()
        if ((s is State.Waiting && t >= s.until) || (s is State.Code && t >= s.deadline)) {
            state = State.Failed(Failure.EXPIRED)
            lock.notifyAll()
        }
    }

    companion object {
        /** How long the phone waits for a Mac to ask, after the owner taps Pair. */
        const val WINDOW_MS = 2 * 60_000L
        /** How long the owner has to compare the code once it is shown. */
        const val DECIDE_MS = 2 * 60_000L
    }
}

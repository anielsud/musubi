package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import java.security.MessageDigest
import java.security.SecureRandom
import java.time.Clock
import java.time.OffsetDateTime
import java.util.Base64

/** What the phone says about itself in `hello` and `pair_request`. */
interface PhoneInfo {
    val deviceName: String
    val appVersion: String
    /** SHA-256 of the phone's TLS certificate (32 bytes), as the Mac sees it. */
    val certSha256: ByteArray
    fun addresses(): List<String>
}

/**
 * One connection from a Mac (docs/phone.md, "Protocol"), after the TLS handshake. The Mac sends
 * requests one at a time and the phone answers each. Everything the Mac sends is validated;
 * unknown ops, oversize frames and malformed JSON or fields end the connection without a reply.
 * The client certificate's hash ([macFp]) is checked against the pin on every request: an
 * unpinned Mac (accepted only while pairing is open) may use the pairing ops and `bye` only.
 */
class SyncSession(
    private val store: RecordingStore,
    private val agenda: AgendaStore,
    private val pairing: PairingFlow,
    private val info: PhoneInfo,
    private val macFp: ByteArray,
    private val pin: () -> ByteArray?,
    private val clock: Clock = Clock.systemDefaultZone(),
    private val random: SecureRandom = SecureRandom(),
) {
    enum class End {
        /** `bye` after a full sync (list and agenda, no errors, nothing left out) with nothing pending. */
        SYNCED,
        /** `bye` otherwise. */
        BYE,
        /** The Mac closed the connection without `bye`. */
        CLOSED,
        /** An unpaired Mac asked for more than pairing. */
        NOT_PAIRED,
        /** `hello` with a protocol version other than 2. */
        VERSION,
    }

    /** What this connection did, for the Mac screen. Never holds codes, nonces or transcript text. */
    class Summary {
        var paired = false
        var listed = false
        var listTruncated = false
        var chunksSent = 0
        var statesApplied = 0
        var importedNow = 0
        var agendaUpdated = false
        var errors = 0
        var pairedNow = false
    }

    val summary = Summary()

    private fun isPinned(): Boolean = pin()?.let { MessageDigest.isEqual(it, macFp) } == true

    /** Serves requests until `bye`, end of stream or a violation (thrown as [ProtocolViolation]). */
    fun serve(reader: FrameReader, writer: FrameWriter): End {
        try {
            while (true) {
                val h = reader.readHeader() ?: return End.CLOSED
                val op = h.json["op"] as? String ?: throw ProtocolViolation("no op")
                if (op !in OPS) throw ProtocolViolation("unknown op")
                if (h.bodyLength > 0 && op != "agenda") throw ProtocolViolation("unexpected body")
                val pinned = isPinned()
                if (pinned) summary.paired = true
                when (op) {
                    "bye" -> {
                        reply(writer, ok())
                        return if (pinned && syncComplete() && !pending()) End.SYNCED else End.BYE
                    }
                    "pair_request" -> pairRequest(h.json, writer)
                    "pair_confirm" -> pairConfirm(writer)
                    "pair_cancel" -> {
                        pairing.macCancel(this)
                        reply(writer, ok())
                    }
                    else -> {
                        if (!pinned) {
                            reply(writer, error("not_paired"))
                            return End.NOT_PAIRED
                        }
                        when (op) {
                            "hello" -> if (!hello(h.json, writer)) return End.VERSION
                            "list" -> list(writer)
                            "chunk" -> chunk(h.json, writer)
                            "state" -> state(h.json, writer)
                            "agenda" -> agenda(h, reader, writer)
                        }
                    }
                }
            }
        } finally {
            pairing.connectionClosed(this)
        }
    }

    private fun syncComplete() = summary.listed && summary.agendaUpdated && summary.errors == 0 && !summary.listTruncated

    /** A finished recording the Mac has not yet reported on, or pairing in progress. */
    private fun pending(): Boolean =
        pairing.isOpen() || store.list().any { !it.meta.isRecording && it.sync.state != SyncState.IMPORTED && it.sync.serverState == null }

    private fun hello(j: Map<String, Any?>, w: FrameWriter): Boolean {
        val v = j["v"] as? Long ?: throw ProtocolViolation("bad v")
        name(j, "mac_name")
        if (v != PROTOCOL_VERSION) {
            reply(w, error("unsupported_version"))
            return false
        }
        reply(
            w,
            ok().put("v", PROTOCOL_VERSION)
                .put("device_name", info.deviceName)
                .put("app_version", info.appVersion)
                .put("addresses", JSONArray(info.addresses())),
        )
        return true
    }

    /**
     * Finished recordings not yet imported, oldest first, as many as fit one frame; the rest
     * follow on a later sync. Imported ones are left out: their audio is gone and the Mac has them.
     */
    private fun list(w: FrameWriter) {
        val items = ArrayList<String>()
        var size = LIST_OVERHEAD
        var truncated = false
        for (r in store.list().sortedBy { it.meta.start }) {
            if (r.meta.isRecording || r.sync.state == SyncState.IMPORTED) continue
            val item = JSONObject()
                .put("meta", JSONObject(r.meta.toJson()))
                .put("state", r.sync.serverState ?: JSONObject.NULL)
                .toString()
            val bytes = item.toByteArray(Charsets.UTF_8).size + 1
            if (size + bytes > Frame.MAX_JSON) {
                truncated = true
                break
            }
            size += bytes
            items += item
        }
        summary.listed = true
        summary.listTruncated = truncated
        val json = """{"ok":true,"recordings":[${items.joinToString(",")}]}"""
        w.write(json.toByteArray(Charsets.UTF_8))
    }

    private fun chunk(j: Map<String, Any?>, w: FrameWriter) {
        val rid = rid(j)
        val n = j["n"] as? Long ?: throw ProtocolViolation("bad n")
        if (n !in 0 until RecordingMeta.MAX_CHUNKS) throw ProtocolViolation("bad n")
        val r = store.read(rid)
        if (r == null || r.meta.isRecording) return fail(w, "unknown_recording")
        if (r.sync.state == SyncState.IMPORTED) return fail(w, "gone")
        val c = r.meta.chunks.firstOrNull { it.n.toLong() == n } ?: return fail(w, "unknown_chunk")
        val f = r.chunkFile(c.n)
        if (!f.isFile) return fail(w, "gone")
        // Re-hash so the Mac never receives bytes that differ from meta.json.
        if (f.length() != c.size || c.size > Frame.MAX_BODY || Hashing.sha256File(f) != c.sha256) return fail(w, "chunk_changed")
        val head = ok().put("sha256", c.sha256).put("body", c.size)
        w.write(head.toString().toByteArray(Charsets.UTF_8), c.size) { out ->
            f.inputStream().use { input ->
                val buf = ByteArray(64 * 1024)
                var left = c.size
                while (left > 0) {
                    val k = input.read(buf, 0, minOf(buf.size.toLong(), left).toInt())
                    if (k < 0) break
                    out.write(buf, 0, k)
                    left -= k
                }
            }
        }
        summary.chunksSent++
        store.updateSync(rid) {
            it.copy(state = if (it.state == SyncState.ON_PHONE) SyncState.UPLOADING else it.state, uploaded = it.uploaded + c.n)
        }
    }

    private fun state(j: Map<String, Any?>, w: FrameWriter) {
        val rid = rid(j)
        val st = j["state"] as? String ?: throw ProtocolViolation("bad state")
        if (st !in MAC_STATES) throw ProtocolViolation("bad state")
        val sessionId = optString(j, "session_id", MAX_ID)?.also { s -> if (s.any { it.isISOControl() }) throw ProtocolViolation("bad session_id") }
        // Shown to the owner: control characters become spaces rather than ending the sync.
        val err = optString(j, "error", MAX_ERROR)?.map { if (it.isISOControl()) ' ' else it }?.joinToString("")
        val r = store.read(rid)
        if (r == null || r.meta.isRecording) return fail(w, "unknown_recording")
        when (st) {
            "imported" -> {
                if (r.sync.state != SyncState.IMPORTED) summary.importedNow++
                store.updateSync(rid) {
                    it.copy(state = SyncState.IMPORTED, serverState = st, sessionId = sessionId, error = null,
                        importedAt = it.importedAt ?: OffsetDateTime.now(clock))
                }
                store.deleteAudio(rid)
            }
            "failed" -> store.updateSync(rid) {
                if (it.state == SyncState.IMPORTED) it
                else it.copy(state = SyncState.FAILED, serverState = st, sessionId = sessionId, error = err ?: "The Mac could not import it")
            }
            else -> store.updateSync(rid) {
                if (it.state == SyncState.IMPORTED) it
                else it.copy(state = SyncState.ON_MAC, serverState = st, sessionId = sessionId, error = null)
            }
        }
        summary.statesApplied++
        reply(w, ok())
    }

    private fun agenda(h: FrameHeader, reader: FrameReader, w: FrameWriter) {
        if (h.bodyLength <= 0) throw ProtocolViolation("agenda without body")
        val body = reader.readBody(h.bodyLength, AgendaStore.MAX_BYTES.toLong())
        val now = OffsetDateTime.now(clock)
        val a = runCatching { AgendaParse.agenda(body, now) }.getOrNull() ?: return fail(w, "bad_agenda")
        agenda.replace(a, now)
        summary.agendaUpdated = true
        reply(w, ok())
    }

    private fun pairRequest(j: Map<String, Any?>, w: FrameWriter) {
        val macName = name(j, "mac_name")
        val macNonce = decodeNonce(j["nonce"] as? String ?: throw ProtocolViolation("bad nonce"))
            ?: throw ProtocolViolation("bad nonce")
        val phoneNonce = PairCode.newNonce(random)
        when (val r = pairing.request(this, macName, macFp, info.certSha256, macNonce, phoneNonce)) {
            is PairingFlow.RequestResult.Ok -> reply(
                w,
                ok().put("device_name", info.deviceName).put("nonce", Base64.getEncoder().encodeToString(phoneNonce)),
            )
            is PairingFlow.RequestResult.Refused -> reply(w, error(r.error))
        }
    }

    private fun pairConfirm(w: FrameWriter) {
        val d = pairing.macConfirm(this)
        if (d == PairingFlow.Decision.OK) {
            summary.pairedNow = true
            reply(w, ok())
        } else {
            reply(w, error(d.error!!))
        }
    }

    private fun ok() = JSONObject().put("ok", true)
    private fun error(code: String) = JSONObject().put("ok", false).put("error", code)

    private fun reply(w: FrameWriter, o: JSONObject) = w.write(o.toString().toByteArray(Charsets.UTF_8))

    private fun fail(w: FrameWriter, code: String) {
        summary.errors++
        reply(w, error(code))
    }

    private fun rid(j: Map<String, Any?>): String {
        val rid = j["rid"] as? String ?: throw ProtocolViolation("bad rid")
        if (!RecordingMeta.isValidId(rid)) throw ProtocolViolation("bad rid")
        return rid
    }

    private fun optString(j: Map<String, Any?>, key: String, max: Int): String? {
        if (!j.containsKey(key) || j[key] == null) return null
        val s = j[key] as? String ?: throw ProtocolViolation("bad $key")
        if (s.length > max) throw ProtocolViolation("$key too long")
        return s.ifEmpty { null }
    }

    /** A display name: 1..100 characters, no control characters. */
    private fun name(j: Map<String, Any?>, key: String): String {
        val s = (j[key] as? String)?.trim() ?: throw ProtocolViolation("bad $key")
        if (s.isEmpty() || s.length > MAX_NAME || s.any { it.isISOControl() }) throw ProtocolViolation("bad $key")
        return s
    }

    companion object {
        const val PROTOCOL_VERSION = 2L
        const val MAX_NAME = 100
        const val MAX_ID = 200
        const val MAX_ERROR = 500
        private const val LIST_OVERHEAD = 64
        val OPS = setOf("hello", "list", "chunk", "state", "agenda", "bye", "pair_request", "pair_confirm", "pair_cancel")
        val MAC_STATES = setOf("received", "waiting", "transcribing", "imported", "failed")

        /** A 16-byte nonce in base64 (standard or URL alphabet, padded or not). */
        fun decodeNonce(s: String): ByteArray? {
            if (s.length > 32) return null
            for (d in listOf(Base64.getDecoder(), Base64.getUrlDecoder())) {
                val b = runCatching { d.decode(s) }.getOrNull() ?: continue
                if (b.size == PairCode.NONCE_BYTES) return b
            }
            return null
        }
    }
}

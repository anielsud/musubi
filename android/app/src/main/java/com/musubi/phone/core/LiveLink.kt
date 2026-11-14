package com.musubi.phone.core

import org.json.JSONObject
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong

/** Writes values as [StrictJson] reads them (maps, lists, strings, longs, doubles, booleans, null). */
object JsonOut {
    fun write(v: Any?): String = StringBuilder().also { write(it, v) }.toString()

    private fun write(sb: StringBuilder, v: Any?) {
        when (v) {
            null -> sb.append("null")
            is String -> sb.append(JSONObject.quote(v))
            is Boolean, is Long, is Int -> sb.append(v.toString())
            is Double -> {
                if (v.isNaN() || v.isInfinite()) throw IllegalArgumentException("not a JSON number")
                sb.append(v.toString())
            }
            is Map<*, *> -> {
                sb.append('{')
                var first = true
                for ((k, x) in v) {
                    if (!first) sb.append(',')
                    first = false
                    sb.append(JSONObject.quote(k as String)).append(':')
                    write(sb, x)
                }
                sb.append('}')
            }
            is List<*> -> {
                sb.append('[')
                v.forEachIndexed { i, x ->
                    if (i > 0) sb.append(',')
                    write(sb, x)
                }
                sb.append(']')
            }
            else -> throw IllegalArgumentException("not a JSON value")
        }
    }
}

/**
 * Explore's live connection (docs/phone.md, part 3), after `live_start`: the Mac opened it, and from
 * then on both sides send requests. The phone asks `rpc` (an app method, answered with its result)
 * and `asset` (a file of the Mac's built frontend); the Mac sends `event`s and `ping`s. Every frame
 * has an id (the sender's counter; a reply repeats it). Anything else from the Mac, or a reply to
 * nothing asked, is a [ProtocolViolation] and ends the connection.
 *
 * [run] reads on the connection's thread; [rpc], [asset] and [end] may be called from any thread.
 */
class LiveLink(
    private val reader: FrameReader,
    private val writer: FrameWriter,
    private val onEvent: (name: String, dataJson: String) -> Unit,
    private val nowMs: () -> Long = { System.nanoTime() / 1_000_000 },
) {
    /** An answer: [resultJson] for an rpc, [type] and [body] for an asset; [error] when not ok. */
    class Reply(
        val ok: Boolean,
        val resultJson: String? = null,
        val error: String? = null,
        val message: String? = null,
        val type: String? = null,
        val body: ByteArray? = null,
    )

    private enum class Kind { RPC, ASSET, END }

    private class Pending(val kind: Kind, val deadline: Long, val done: (Reply) -> Unit)

    private val ids = AtomicLong(0)
    private val pending = ConcurrentHashMap<Long, Pending>()
    private val writeLock = Any()

    @Volatile var isOpen = true
        private set

    /** Reads the Mac's frames until the connection ends; then every open request fails "closed". */
    fun run() {
        try {
            while (true) {
                val h = reader.readHeader() ?: break
                handle(h)
            }
        } finally {
            close()
        }
    }

    fun close() {
        isOpen = false
        for (id in pending.keys.toList()) pending.remove(id)?.done?.invoke(Reply(false, error = "closed"))
    }

    private fun handle(h: FrameHeader) {
        val j = h.json
        val id = j["id"] as? Long ?: throw ProtocolViolation("live frame without an id")
        if (j.containsKey("ok")) {
            val ok = j["ok"] as? Boolean ?: throw ProtocolViolation("bad ok")
            val p = pending.remove(id) ?: throw ProtocolViolation("reply to nothing asked")
            if (h.bodyLength > 0 && (!ok || p.kind == Kind.END)) throw ProtocolViolation("unexpected body")
            val body = if (h.bodyLength > 0) reader.readBody(h.bodyLength, MAX_VALUE) else null
            p.done(if (ok) answer(p.kind, j, body) else failure(j))
            return
        }
        when (j["op"]) {
            "event" -> {
                val name = j["name"] as? String
                if (name == null || !EVENT_NAME.matches(name)) throw ProtocolViolation("bad event name")
                val data = value(j, "data", h)
                onEvent(name, data)
            }
            "ping" -> {
                if (h.bodyLength > 0) throw ProtocolViolation("ping with a body")
                write("""{"id":$id,"ok":true}""")
            }
            else -> throw ProtocolViolation("unknown live op")
        }
    }

    /** A JSON value that is either [key] in the frame or, when too large for it, the frame's body. */
    private fun value(j: Map<String, Any?>, key: String, h: FrameHeader): String {
        if (h.bodyLength > 0) {
            if (j.containsKey(key)) throw ProtocolViolation("$key both inline and as body")
            val body = reader.readBody(h.bodyLength, MAX_VALUE)
            try {
                StrictJson.parse(body)
            } catch (e: StrictJson.Malformed) {
                throw ProtocolViolation("$key body isn't JSON")
            }
            return String(body, Charsets.UTF_8)
        }
        return JsonOut.write(j[key])
    }

    private fun answer(kind: Kind, j: Map<String, Any?>, body: ByteArray?): Reply = when (kind) {
        Kind.RPC -> {
            val json = if (body != null) {
                try {
                    StrictJson.parse(body)
                } catch (e: StrictJson.Malformed) {
                    throw ProtocolViolation("result body isn't JSON")
                }
                String(body, Charsets.UTF_8)
            } else {
                if (!j.containsKey("result")) "null" else JsonOut.write(j["result"])
            }
            Reply(true, resultJson = json)
        }
        Kind.ASSET -> {
            val type = j["type"] as? String
            if (type == null || !CONTENT_TYPE.matches(type)) throw ProtocolViolation("bad content type")
            Reply(true, type = type, body = body ?: ByteArray(0))
        }
        Kind.END -> Reply(true)
    }

    private fun failure(j: Map<String, Any?>): Reply {
        val code = (j["error"] as? String)?.takeIf { ERROR_CODE.matches(it) } ?: "unknown"
        val msg = (j["message"] as? String)?.take(MAX_MESSAGE)?.map { if (it.isISOControl()) ' ' else it }?.joinToString("")
        return Reply(false, error = code, message = msg)
    }

    /**
     * Calls the Mac's app method [method] with [argsJson] (a JSON array); [done] gets the answer,
     * on the reading thread or, for a refusal here, at once.
     */
    fun rpc(method: String, argsJson: String, done: (Reply) -> Unit) {
        if (!METHOD.matches(method) || argsJson.length > MAX_ARGS_CHARS) return done(Reply(false, error = "bad_call"))
        val args = try {
            StrictJson.parse(argsJson) as? List<*>
        } catch (e: StrictJson.Malformed) {
            null
        }
        if (args == null || args.size > MAX_ARGS) return done(Reply(false, error = "bad_call"))
        sendFrame(Kind.RPC, RPC_TIMEOUT_MS, done) { id ->
            """{"op":"rpc","id":$id,"method":${JSONObject.quote(method)},"args":${JsonOut.write(args)}}"""
        }
    }

    /** Fetches [path] (with its query) from the Mac's frontend; blocks up to [timeoutMs]. */
    fun asset(path: String, timeoutMs: Long = ASSET_TIMEOUT_MS): Reply {
        if (!path.startsWith("/") || path.length > MAX_PATH || path.any { it.isISOControl() || it == '\\' }) {
            return Reply(false, error = "bad_path")
        }
        val latch = CountDownLatch(1)
        var got: Reply? = null
        sendFrame(Kind.ASSET, timeoutMs, { r -> got = r; latch.countDown() }) { id ->
            """{"op":"asset","id":$id,"path":${JSONObject.quote(path)}}"""
        }
        return if (latch.await(timeoutMs, TimeUnit.MILLISECONDS)) got!! else Reply(false, error = "timeout")
    }

    /** The app left the foreground: tell the Mac, which answers and closes. */
    fun end() {
        sendFrame(Kind.END, 5_000L, {}) { id -> """{"op":"live_end","id":$id}""" }
    }

    /** Fails requests the Mac has not answered in time; called about once a second. */
    fun expire() {
        val now = nowMs()
        for ((id, p) in pending) {
            if (now >= p.deadline) pending.remove(id)?.done?.invoke(Reply(false, error = "timeout"))
        }
    }

    private fun sendFrame(kind: Kind, timeoutMs: Long, done: (Reply) -> Unit, frame: (Long) -> String) {
        if (!isOpen) return done(Reply(false, error = "closed"))
        if (pending.size >= MAX_PENDING) return done(Reply(false, error = "busy"))
        val id = ids.incrementAndGet()
        pending[id] = Pending(kind, nowMs() + timeoutMs, done)
        try {
            write(frame(id))
        } catch (e: Exception) {
            pending.remove(id)?.done?.invoke(Reply(false, error = "closed"))
        }
    }

    private fun write(json: String) {
        synchronized(writeLock) { writer.write(json.toByteArray(Charsets.UTF_8)) }
    }

    companion object {
        const val MAX_VALUE = 16L * 1024 * 1024
        const val MAX_PENDING = 32
        const val MAX_ARGS = 16
        const val MAX_ARGS_CHARS = 60 * 1024
        const val MAX_PATH = 216
        const val MAX_MESSAGE = 300
        const val RPC_TIMEOUT_MS = 90_000L
        const val ASSET_TIMEOUT_MS = 30_000L
        val METHOD = Regex("^[A-Z][A-Za-z0-9]{0,63}$")
        val EVENT_NAME = Regex("^[a-z][a-z0-9:_.-]{0,63}$")
        val ERROR_CODE = Regex("^[a-z0-9_]{1,40}$")
        val CONTENT_TYPE = Regex("^[a-z]+/[a-z0-9.+-]+(; charset=[a-z0-9-]+)?$")
    }
}

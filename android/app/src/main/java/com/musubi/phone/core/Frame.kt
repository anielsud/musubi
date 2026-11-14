package com.musubi.phone.core

import java.io.EOFException
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream

/**
 * Framing of the phone protocol (docs/phone.md, "Protocol"): a 4-byte big-endian length, that
 * many bytes of UTF-8 JSON (at most 64 KB), then, if the JSON has `"body": n`, n raw bytes (at
 * most 64 MB). Anything outside these bounds is a [ProtocolViolation]: the connection closes.
 */
object Frame {
    const val MAX_JSON = 64 * 1024
    const val MAX_BODY = 64L * 1024 * 1024
}

/** The peer broke the protocol (oversize, malformed, unknown op): close without replying. */
class ProtocolViolation(msg: String) : IOException(msg)

/** A request as read: its JSON object, and the length of the body that follows it (0 if none). */
class FrameHeader(val json: Map<String, Any?>, val bodyLength: Long)

/**
 * Reads frames. [timeouts] lets the socket layer apply an idle timeout while waiting for a
 * request to start and a per-frame deadline once it has.
 */
class FrameReader(private val input: InputStream, private val timeouts: Timeouts = Timeouts.NONE) {
    interface Timeouts {
        /** Waiting for the first byte of the next request. */
        fun idle()
        /** Reading the rest of a frame of [bytes] bytes; may throw when the frame takes too long. */
        fun inFrame(bytes: Long)
        fun check()

        companion object {
            val NONE = object : Timeouts {
                override fun idle() {}
                override fun inFrame(bytes: Long) {}
                override fun check() {}
            }
        }
    }

    /** Returns null on a clean end of stream before a new frame starts. */
    fun readHeader(): FrameHeader? {
        timeouts.idle()
        val first = input.read()
        if (first < 0) return null
        timeouts.inFrame(4)
        val rest = readExact(3)
        val len = (first.toLong() shl 24) or ((rest[0].toLong() and 0xff) shl 16) or
            ((rest[1].toLong() and 0xff) shl 8) or (rest[2].toLong() and 0xff)
        if (len <= 0 || len > Frame.MAX_JSON) throw ProtocolViolation("frame length out of bounds")
        timeouts.inFrame(len)
        val bytes = readExact(len.toInt())
        val json = try {
            StrictJson.parseObject(bytes)
        } catch (e: StrictJson.Malformed) {
            throw ProtocolViolation("malformed JSON")
        }
        val body = when (val b = json["body"]) {
            null -> if (json.containsKey("body")) throw ProtocolViolation("bad body") else 0L
            is Long -> b
            else -> throw ProtocolViolation("bad body")
        }
        if (body < 0 || body > Frame.MAX_BODY) throw ProtocolViolation("body out of bounds")
        return FrameHeader(json, body)
    }

    /** Reads a body into memory; [max] is the op's own cap (at most [Frame.MAX_BODY]). */
    fun readBody(length: Long, max: Long): ByteArray {
        if (length > max) throw ProtocolViolation("body too large for this op")
        timeouts.inFrame(length)
        return readExact(length.toInt())
    }

    private fun readExact(n: Int): ByteArray {
        val buf = ByteArray(n)
        var off = 0
        while (off < n) {
            timeouts.check()
            val r = input.read(buf, off, minOf(n - off, 64 * 1024))
            if (r < 0) throw EOFException("connection closed mid-frame")
            off += r
        }
        return buf
    }
}

class FrameWriter(private val output: OutputStream) {
    /** Writes one frame; [body] (exactly [bodyLength] bytes) follows when [bodyLength] > 0. */
    fun write(json: ByteArray, bodyLength: Long = 0, body: ((OutputStream) -> Unit)? = null) {
        if (json.isEmpty() || json.size > Frame.MAX_JSON) throw IOException("reply frame out of bounds")
        val n = json.size
        val header = byteArrayOf((n ushr 24).toByte(), (n ushr 16).toByte(), (n ushr 8).toByte(), n.toByte())
        output.write(header)
        output.write(json)
        if (bodyLength > 0) {
            val counting = CountingStream(output)
            body?.invoke(counting) ?: throw IOException("missing body")
            if (counting.count != bodyLength) throw IOException("body length mismatch")
        }
        output.flush()
    }

    private class CountingStream(private val out: OutputStream) : OutputStream() {
        var count = 0L
        override fun write(b: Int) { out.write(b); count++ }
        override fun write(b: ByteArray, off: Int, len: Int) { out.write(b, off, len); count += len }
    }
}

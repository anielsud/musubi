package com.musubi.phone.core

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.EOFException

class FrameTest {
    private fun reader(bytes: ByteArray) = FrameReader(ByteArrayInputStream(bytes))

    private fun lengthOnly(n: Long) = byteArrayOf((n ushr 24).toByte(), (n ushr 16).toByte(), (n ushr 8).toByte(), n.toByte())

    private fun violates(bytes: ByteArray) {
        val r = runCatching { reader(bytes).readHeader() }
        assertTrue("expected a violation, got $r", r.exceptionOrNull() is ProtocolViolation)
    }

    @Test
    fun readsAFrameAndItsBody() {
        val body = ByteArray(1000) { it.toByte() }
        val r = reader(Frames.frame("""{"op":"agenda","body":1000}""", body))
        val h = r.readHeader()!!
        assertEquals("agenda", h.json["op"])
        assertEquals(1000L, h.bodyLength)
        assertArrayEquals(body, r.readBody(h.bodyLength, Frame.MAX_BODY))
        assertNull(r.readHeader())
    }

    @Test
    fun lengthBounds() {
        violates(lengthOnly(0))
        violates(lengthOnly(Frame.MAX_JSON + 1L))
        violates(lengthOnly(0xffffffffL))
        // Exactly 64 KB is allowed.
        val pad = " ".repeat(Frame.MAX_JSON - """{"op":"list"}""".length)
        assertEquals("list", reader(Frames.frame("""{"op":"list"}$pad""")).readHeader()!!.json["op"])
    }

    @Test
    fun bodyBounds() {
        violates(Frames.frame("""{"op":"agenda","body":-1}"""))
        violates(Frames.frame("""{"op":"agenda","body":${Frame.MAX_BODY + 1}}"""))
        violates(Frames.frame("""{"op":"agenda","body":1.5}"""))
        violates(Frames.frame("""{"op":"agenda","body":"10"}"""))
        violates(Frames.frame("""{"op":"agenda","body":null}"""))
        val big = reader(Frames.frame("""{"op":"agenda","body":100}""", ByteArray(100)))
        val h = big.readHeader()!!
        assertTrue(runCatching { big.readBody(h.bodyLength, 99) }.exceptionOrNull() is ProtocolViolation)
    }

    @Test
    fun truncatedFramesFail() {
        val full = Frames.frame("""{"op":"list"}""")
        assertTrue(runCatching { reader(full.copyOf(2)).readHeader() }.exceptionOrNull() is EOFException)
        assertTrue(runCatching { reader(full.copyOf(full.size - 1)).readHeader() }.exceptionOrNull() is EOFException)
        val withBody = reader(Frames.frame("""{"op":"agenda","body":10}""", ByteArray(5)))
        val h = withBody.readHeader()!!
        assertTrue(runCatching { withBody.readBody(h.bodyLength, 100) }.exceptionOrNull() is EOFException)
    }

    @Test
    fun malformedJsonIsAViolation() {
        for (bad in listOf(
            "{'op':'list'}", // single quotes
            "{op:\"list\"}", // unquoted key
            """{"op":"list",}""", // trailing comma
            """{"op":"list"} x""", // trailing data
            """{"op":"list" /* c */}""",
            """{"op":"list","op":"bye"}""", // duplicate key
            """["op","list"]""", // not an object
            """{"n":01}""", // leading zero
            """{"s":"a${'\u0001'}b"}""", // raw control character
            """{"s":"\x41"}""",
            "[".repeat(40) + "]".repeat(40),
        )) violates(Frames.frame(bad))
        // Invalid UTF-8.
        val bytes = byteArrayOf('{'.code.toByte(), '"'.code.toByte(), 0xC3.toByte(), 0x28, '"'.code.toByte(), ':'.code.toByte(), '1'.code.toByte(), '}'.code.toByte())
        violates(lengthOnly(bytes.size.toLong()) + bytes)
    }

    @Test
    fun strictJsonAcceptsWellFormedInput() {
        val v = StrictJson.parse("""{"a":[1,-2.5e3,true,false,null],"b":"é\n\"","c":{}}""") as Map<*, *>
        assertEquals(listOf(1L, -2500.0, true, false, null), v["a"])
        assertEquals("é\n\"", v["b"])
        assertEquals(emptyMap<String, Any?>(), v["c"])
    }

    @Test
    fun writerFramesAndChecksBodyLength() {
        val out = ByteArrayOutputStream()
        FrameWriter(out).write("""{"ok":true,"body":3}""".toByteArray(), 3) { it.write(byteArrayOf(1, 2, 3)) }
        val r = Frames.replies(out.toByteArray()).single()
        assertArrayEquals(byteArrayOf(1, 2, 3), r.body)
        val short = runCatching { FrameWriter(ByteArrayOutputStream()).write("{}".toByteArray(), 3) { it.write(1) } }
        assertTrue(short.isFailure)
        assertTrue(runCatching { FrameWriter(ByteArrayOutputStream()).write(ByteArray(Frame.MAX_JSON + 1)) }.isFailure)
    }
}

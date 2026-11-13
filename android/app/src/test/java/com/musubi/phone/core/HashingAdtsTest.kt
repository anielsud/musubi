package com.musubi.phone.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class HashingAdtsTest {
    @Test
    fun sha256KnownVectors() {
        assertEquals("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", Hashing.sha256Hex(ByteArray(0)))
        assertEquals("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Hashing.sha256Hex("abc".toByteArray()))
    }

    @Test
    fun fileHashMatchesInMemoryHashAcrossBufferBoundaries() {
        val bytes = ByteArray(200_000) { (it * 31).toByte() }
        val f = File(TestUtil.tempDir(), "x.bin").apply { writeBytes(bytes) }
        assertEquals(Hashing.sha256Hex(bytes), Hashing.sha256File(f))
    }

    @Test
    fun hexValidation() {
        assertTrue(Hashing.isSha256Hex("0".repeat(64)))
        assertFalse(Hashing.isSha256Hex("0".repeat(63)))
        assertFalse(Hashing.isSha256Hex("A".repeat(64)))
    }

    @Test
    fun adtsHeaderEncodesLengthAndFormat() {
        val h = Adts.header(100, 48000, 1)
        assertEquals(7, h.size)
        assertEquals(0xFF, h[0].toInt() and 0xFF)
        assertEquals(0xF1, h[1].toInt() and 0xFF)
        val len = ((h[3].toInt() and 0x03) shl 11) or ((h[4].toInt() and 0xFF) shl 3) or ((h[5].toInt() and 0xE0) ushr 5)
        assertEquals(107, len)
        assertEquals(3, (h[2].toInt() ushr 2) and 0x0F) // 48 kHz
        assertEquals(1, (h[2].toInt() ushr 6) and 0x03) // AAC LC
    }

    @Test
    fun completeLengthStopsBeforePartialFrame() {
        val whole = TestUtil.adtsFrames(5)
        val f = File(TestUtil.tempDir(), "c.aac")
        f.writeBytes(whole + TestUtil.adtsFrames(1).copyOf(50))
        assertEquals(whole.size.toLong(), Adts.completeLength(f))
        assertEquals(whole.size.toLong(), Adts.truncateToCompleteFrames(f))
        assertEquals(whole.size.toLong(), f.length())
    }

    @Test
    fun completeFileIsUntouched() {
        val whole = TestUtil.adtsFrames(3)
        val f = File(TestUtil.tempDir(), "c.aac").apply { writeBytes(whole) }
        assertEquals(whole.size.toLong(), Adts.truncateToCompleteFrames(f))
    }
}

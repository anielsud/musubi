package com.musubi.phone.core

import java.io.ByteArrayOutputStream
import java.io.File

/**
 * Removes where a photo was taken before it can leave the phone: every GPS tag in the JPEG's EXIF
 * (the GPS IFD's entries and the values they point to are zeroed and the IFD left empty, so no
 * other offset moves and the orientation and the rest of EXIF stay) and any XMP segment (which
 * can repeat the location). The image data is not touched. Plain JVM, unit-tested.
 */
object ExifLocation {
    private const val GPS_IFD_TAG = 0x8825
    private val XMP = "http://ns.adobe.com/xap/1.0/\u0000".toByteArray(Charsets.ISO_8859_1)
    private val XMP_EXT = "http://ns.adobe.com/xmp/extension/\u0000".toByteArray(Charsets.ISO_8859_1)
    private val EXIF = byteArrayOf(0x45, 0x78, 0x69, 0x66, 0, 0) // "Exif\0\0"

    /** Rewrites [f] without location if it had any; returns whether it changed. */
    fun strip(f: File): Boolean {
        val out = strip(f.readBytes()) ?: return false
        Files.writeAtomic(f, out)
        return true
    }

    /** The JPEG without location, or null when there was none (or it isn't a JPEG). */
    fun strip(jpeg: ByteArray): ByteArray? {
        if (jpeg.size < 4 || u8(jpeg, 0) != 0xFF || u8(jpeg, 1) != 0xD8) return null
        val b = jpeg.copyOf()
        val out = ByteArrayOutputStream(b.size)
        out.write(b, 0, 2)
        var pos = 2
        var changed = false
        while (pos + 4 <= b.size && u8(b, pos) == 0xFF) {
            val marker = u8(b, pos + 1)
            if (marker == 0xDA || marker == 0xD9) break // image data from here: copied as it is
            if (marker == 0x01 || marker in 0xD0..0xD7 || marker == 0xFF) {
                out.write(b, pos, if (marker == 0xFF) 1 else 2)
                pos += if (marker == 0xFF) 1 else 2
                continue
            }
            val len = u16(b, pos + 2, false)
            val end = pos + 2 + len
            if (len < 2 || end > b.size) break
            val body = pos + 4
            if (marker == 0xE1 && startsWith(b, body, end, XMP) || marker == 0xE1 && startsWith(b, body, end, XMP_EXT)) {
                changed = true // dropped: segments are independent, so nothing else moves
            } else {
                if (marker == 0xE1 && startsWith(b, body, end, EXIF) && zeroGps(b, body + EXIF.size, end)) changed = true
                out.write(b, pos, end - pos)
            }
            pos = end
        }
        if (!changed) return null
        out.write(b, pos, b.size - pos)
        return out.toByteArray()
    }

    /** Empties the GPS IFD of the TIFF structure in b[tiff, end); true if it had one with entries. */
    private fun zeroGps(b: ByteArray, tiff: Int, end: Int): Boolean {
        if (tiff + 8 > end) return false
        val le = when {
            u8(b, tiff) == 0x49 && u8(b, tiff + 1) == 0x49 -> true
            u8(b, tiff) == 0x4D && u8(b, tiff + 1) == 0x4D -> false
            else -> return false
        }
        val ifd0L = tiff + u32(b, tiff + 4, le)
        if (ifd0L < tiff || ifd0L + 2 > end) return false
        val ifd0 = ifd0L.toInt()
        val n0 = u16(b, ifd0, le)
        var gps = -1L
        for (i in 0 until n0) {
            val e = ifd0 + 2 + 12 * i
            if (e + 12 > end) return false
            if (u16(b, e, le) == GPS_IFD_TAG) gps = u32(b, e + 8, le)
        }
        if (gps < 0) return false
        val gL = tiff + gps
        if (gL < tiff || gL + 2 > end) return false
        val g = gL.toInt()
        val n = u16(b, g, le)
        if (n == 0) return false
        for (i in 0 until n) {
            val e = g + 2 + 12 * i
            if (e + 12 > end) break
            val bytes = typeSize(u16(b, e + 2, le)) * u32(b, e + 4, le)
            if (bytes > 4) {
                val at = tiff + u32(b, e + 8, le)
                if (at >= tiff && at + bytes <= end) b.fill(0, at.toInt(), (at + bytes).toInt())
            }
        }
        b.fill(0, g, minOf(end, g + 2 + 12 * n + 4))
        return true
    }

    private fun typeSize(t: Int): Long = when (t) {
        1, 2, 6, 7 -> 1
        3, 8 -> 2
        4, 9, 11 -> 4
        5, 10, 12 -> 8
        else -> 0
    }

    private fun startsWith(b: ByteArray, at: Int, end: Int, p: ByteArray): Boolean =
        at + p.size <= end && (p.indices).all { b[at + it] == p[it] }

    private fun u8(b: ByteArray, i: Int) = b[i].toInt() and 0xFF
    private fun u16(b: ByteArray, i: Int, le: Boolean) =
        if (le) u8(b, i) or (u8(b, i + 1) shl 8) else (u8(b, i) shl 8) or u8(b, i + 1)
    private fun u32(b: ByteArray, i: Int, le: Boolean): Long =
        if (le) (u16(b, i, true).toLong() or (u16(b, i + 2, true).toLong() shl 16))
        else ((u16(b, i, false).toLong() shl 16) or u16(b, i + 2, false).toLong())
}

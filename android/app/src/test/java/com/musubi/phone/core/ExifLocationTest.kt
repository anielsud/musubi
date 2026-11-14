package com.musubi.phone.core

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.time.Clock

class ExifLocationTest {
    /** A JPEG with EXIF (orientation 6, a GPS IFD with latitude), an XMP segment repeating it, and image data. */
    private fun jpeg(le: Boolean, gps: Boolean = true): ByteArray {
        val t = ByteArrayOutputStream()
        fun u16(v: Int) = if (le) t.write(byteArrayOf(v.toByte(), (v shr 8).toByte())) else t.write(byteArrayOf((v shr 8).toByte(), v.toByte()))
        fun u32(v: Int) = if (le) { u16(v and 0xFFFF); u16(v ushr 16) } else { u16(v ushr 16); u16(v and 0xFFFF) }
        t.write(if (le) "II".toByteArray() else "MM".toByteArray()); u16(42); u32(8)
        // IFD0 at 8: orientation, then the GPS pointer.
        u16(if (gps) 2 else 1)
        u16(0x0112); u16(3); u32(1); u16(6); u16(0)
        if (gps) { u16(0x8825); u16(4); u32(1); u32(8 + 2 + 24 + 4) }
        u32(0)
        if (gps) {
            // GPS IFD at 38: LatitudeRef "N", Latitude 3 rationals out of line at 38 + 2 + 24 + 4 = 68.
            u16(2)
            u16(1); u16(2); u32(2); t.write(byteArrayOf('N'.code.toByte(), 0, 0, 0))
            u16(2); u16(5); u32(3); u32(68)
            u32(0)
            for (v in listOf(52, 1, 13, 1, 2245, 100)) u32(v)
        }
        val tiff = t.toByteArray()
        val out = ByteArrayOutputStream()
        fun seg(marker: Int, body: ByteArray) {
            out.write(0xFF); out.write(marker); out.write((body.size + 2) shr 8); out.write((body.size + 2) and 0xFF); out.write(body)
        }
        out.write(0xFF); out.write(0xD8)
        seg(0xE1, "Exif\u0000\u0000".toByteArray(Charsets.ISO_8859_1) + tiff)
        seg(0xE1, "http://ns.adobe.com/xap/1.0/\u0000<x:xmpmeta exif:GPSLatitude=\"52,13.37N\"/>".toByteArray(Charsets.ISO_8859_1))
        seg(0xDB, ByteArray(65) { it.toByte() })
        out.write(0xFF); out.write(0xDA); out.write(0); out.write(8); out.write(ByteArray(6))
        out.write(ByteArray(300) { (it * 7).toByte() })
        out.write(0xFF); out.write(0xD9)
        return out.toByteArray()
    }

    private fun indexOf(b: ByteArray, p: ByteArray): Int = (0..b.size - p.size).firstOrNull { i -> p.indices.all { b[i + it] == p[it] } } ?: -1

    @Test
    fun gpsAndXmpGoOrientationAndImageStay() {
        for (le in listOf(true, false)) {
            val src = jpeg(le)
            val out = ExifLocation.strip(src)!!
            // XMP dropped: the location text is nowhere.
            assertEquals(-1, indexOf(out, "GPSLatitude".toByteArray()))
            // The EXIF segment keeps its size and the orientation; the GPS IFD is empty and its values zeroed.
            val tiff = 4 + 6 + 2 // SOI, APP1 marker+length, "Exif\0\0"
            assertEquals(src.copyOfRange(0, tiff + 38).toList(), out.copyOfRange(0, tiff + 38).toList())
            assertTrue(out.copyOfRange(tiff + 38, tiff + 68 + 24).all { it == 0.toByte() })
            // Image data and the tables after it unchanged.
            val sos = indexOf(src, byteArrayOf(0xFF.toByte(), 0xDA.toByte()))
            val sos2 = indexOf(out, byteArrayOf(0xFF.toByte(), 0xDA.toByte()))
            assertArrayEquals(src.copyOfRange(sos, src.size), out.copyOfRange(sos2, out.size))
            // Nothing left to strip.
            assertNull(ExifLocation.strip(out))
        }
    }

    @Test
    fun noLocationNoChange() {
        val noGps = jpeg(true, gps = false)
        val withoutXmp = ExifLocation.strip(noGps)!! // only the XMP goes
        assertNull(ExifLocation.strip(withoutXmp))
        assertNull(ExifLocation.strip(byteArrayOf(1, 2, 3, 4)))
        // Damaged offsets never throw.
        val bad = jpeg(false).also { it[4 + 6 + 2 + 7] = 0x7F }
        ExifLocation.strip(bad)
    }

    @Test
    fun addedPhotosLoseTheirLocation() {
        val phone = FakePhone()
        val editor = DetailsEditor(phone.store, Clock.systemUTC())
        val r = TestUtil.finishedRecording(phone.store, 1)
        val f = editor.incoming(r.id).also { it.writeBytes(jpeg(true)) }
        val d = editor.addPhoto(r.id, f)
        val kept = r.photoFile("photo-0001.jpg").readBytes()
        assertEquals(-1, indexOf(kept, "GPSLatitude".toByteArray()))
        assertFalse(kept.contentEquals(jpeg(true)))
        assertEquals(Hashing.sha256Hex(kept), d.photos.single().sha256)
        assertEquals(kept.size.toLong(), d.photos.single().size)
    }
}

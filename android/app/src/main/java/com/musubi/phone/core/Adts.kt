package com.musubi.phone.core

import java.io.File
import java.io.RandomAccessFile

/**
 * ADTS framing for AAC-LC. Each encoded frame is written with a 7-byte header, so a file is
 * readable up to its last complete frame even if the app dies mid-write.
 */
object Adts {
    const val HEADER_SIZE = 7
    const val SAMPLES_PER_FRAME = 1024
    private const val MAX_FRAME = 0x1FFF // 13-bit length field

    private val SAMPLE_RATES = intArrayOf(96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350)

    /** Header for one AAC-LC frame whose raw payload is [payloadLen] bytes. */
    fun header(payloadLen: Int, sampleRate: Int, channels: Int): ByteArray {
        val freqIdx = SAMPLE_RATES.indexOf(sampleRate)
        require(freqIdx >= 0) { "unsupported sample rate" }
        require(channels in 1..7) { "unsupported channel count" }
        val frameLen = payloadLen + HEADER_SIZE
        require(frameLen <= MAX_FRAME) { "frame too large" }
        val profile = 1 // AAC LC (object type 2) minus 1
        return byteArrayOf(
            0xFF.toByte(),
            0xF1.toByte(), // MPEG-4, layer 0, no CRC
            ((profile shl 6) or (freqIdx shl 2) or (channels shr 2)).toByte(),
            (((channels and 3) shl 6) or (frameLen shr 11)).toByte(),
            ((frameLen shr 3) and 0xFF).toByte(),
            (((frameLen and 7) shl 5) or 0x1F).toByte(),
            0xFC.toByte(),
        )
    }

    /**
     * Length of the prefix of [file] made of complete ADTS frames. Stops at the first byte
     * that is not a frame start or at a frame that runs past the end of the file.
     */
    fun completeLength(file: File): Long {
        RandomAccessFile(file, "r").use { raf ->
            val total = raf.length()
            var pos = 0L
            val h = ByteArray(HEADER_SIZE)
            while (pos + HEADER_SIZE <= total) {
                raf.seek(pos)
                raf.readFully(h)
                if ((h[0].toInt() and 0xFF) != 0xFF || (h[1].toInt() and 0xF0) != 0xF0) break
                val len = ((h[3].toInt() and 0x03) shl 11) or ((h[4].toInt() and 0xFF) shl 3) or ((h[5].toInt() and 0xE0) ushr 5)
                if (len < HEADER_SIZE) break
                if (pos + len > total) break
                pos += len
            }
            return pos
        }
    }

    /** Cuts a trailing partial frame (from a crash mid-write). Returns the new length. */
    fun truncateToCompleteFrames(file: File): Long {
        val good = completeLength(file)
        if (good != file.length()) {
            RandomAccessFile(file, "rw").use { it.setLength(good) }
        }
        return good
    }
}

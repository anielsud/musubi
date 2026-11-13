package com.musubi.phone.core

import java.io.File
import java.io.FileOutputStream
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.sin

/**
 * 48 kHz → 16 kHz for the live-text recognizer: a windowed-sinc low-pass (cut-off 7.2 kHz)
 * evaluated at every third input sample. Keeps its history between calls, so blocks of any
 * length join without clicks. Not thread-safe: one feeder thread owns it. Plain JVM, unit-tested.
 */
class Resampler48kTo16k(taps: Int = 63) {
    private val h: FloatArray
    private val histLen = taps - 1
    private var hist = FloatArray(histLen)
    /** How many input samples to skip before the next output (0..2), carried across calls. */
    private var skip = 0

    init {
        require(taps % 2 == 1) { "odd tap count" }
        val fc = 7_200.0 / 48_000.0
        val mid = (taps - 1) / 2
        val raw = DoubleArray(taps) { i ->
            val m = i - mid
            val sinc = if (m == 0) 2 * fc else sin(2 * PI * fc * m) / (PI * m)
            val hamming = 0.54 - 0.46 * cos(2 * PI * i / (taps - 1))
            sinc * hamming
        }
        val sum = raw.sum()
        h = FloatArray(taps) { (raw[it] / sum).toFloat() }
    }

    /** [len] bytes of 16-bit little-endian mono PCM in; float samples in -1..1 out. */
    fun process(pcm: ByteArray, len: Int): FloatArray {
        val n = len / 2
        val buf = FloatArray(histLen + n)
        System.arraycopy(hist, 0, buf, 0, histLen)
        for (i in 0 until n) {
            val s = (pcm[2 * i].toInt() and 0xFF) or (pcm[2 * i + 1].toInt() shl 8)
            buf[histLen + i] = s.toShort() / 32768f
        }
        val out = FloatArray((n - skip + 2).coerceAtLeast(0) / 3)
        var t = histLen + skip
        var k = 0
        while (t < buf.size) {
            var acc = 0f
            for (j in h.indices) acc += h[j] * buf[t - j]
            out[k++] = acc
            t += 3
        }
        skip = t - buf.size
        System.arraycopy(buf, buf.size - histLen, hist, 0, histLen)
        return if (k == out.size) out else out.copyOf(k)
    }

    fun reset() {
        hist = FloatArray(histLen)
        skip = 0
    }
}

/** One read of the recorder's PCM: a copy, and where it starts in the recording (48 kHz samples). */
class PcmBlock(val bytes: ByteArray, val len: Int, val startSample: Long)

/**
 * A bounded hand-off from the recorder thread: [offer] never blocks, and when the queue is full
 * the new item is dropped and counted. This is what keeps the recording independent of live text.
 */
class DropQueue<T>(capacity: Int) {
    private val q = ArrayBlockingQueue<T>(capacity)
    private val droppedCount = AtomicLong()

    val dropped: Long get() = droppedCount.get()
    val size: Int get() = q.size

    fun offer(item: T): Boolean {
        if (q.offer(item)) return true
        droppedCount.incrementAndGet()
        return false
    }

    fun poll(timeoutMs: Long): T? = q.poll(timeoutMs, TimeUnit.MILLISECONDS)

    fun clear() = q.clear()
}

/**
 * Appends finalized live-text lines to `live.txt` in a recording's folder, each with its offset
 * from the start of the recording: `[0:01:23] text`. Stays on the phone: sync uploads only the
 * chunks listed in meta.json, and RecordingStore.deleteAudio removes this file with the audio.
 */
class LiveTextFile(val file: File, private val maxBytes: Long = MAX_BYTES) {
    /** Returns false (and writes nothing) once the file has reached its size cap. */
    fun append(offsetMs: Long, text: String): Boolean {
        val clean = clean(text)
        if (clean.isEmpty()) return true
        val line = "[${formatOffset(offsetMs)}] $clean\n".toByteArray(Charsets.UTF_8)
        if (file.length() + line.size > maxBytes) return false
        FileOutputStream(file, true).use { it.write(line) }
        return true
    }

    companion object {
        const val NAME = "live.txt"
        const val MAX_BYTES = 4L * 1024 * 1024
        private const val MAX_LINE_CHARS = 2_000

        fun formatOffset(ms: Long): String {
            val s = (ms.coerceAtLeast(0)) / 1000
            return "%d:%02d:%02d".format(s / 3600, (s / 60) % 60, s % 60)
        }

        /** One line of plain text: control characters become spaces, length is capped. */
        fun clean(text: String): String =
            text.map { if (it.isISOControl()) ' ' else it }.joinToString("").trim().replace(Regex(" {2,}"), " ").take(MAX_LINE_CHARS)

        fun read(file: File): String = String(Files.readBounded(file, MAX_BYTES.toInt()), Charsets.UTF_8)
    }
}

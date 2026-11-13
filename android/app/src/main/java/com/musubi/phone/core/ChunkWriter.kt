package com.musubi.phone.core

import java.io.File
import java.io.FileOutputStream
import java.io.IOException

/**
 * Writes encoded AAC frames as ADTS into chunk-NNNN.aac files of one recording, starting a new
 * file every [samplesPerChunk] samples (at a frame boundary, so nothing is lost between files).
 * Each finished chunk is hashed and added to meta.json right away. Plain JVM, unit-tested.
 */
class ChunkWriter(
    private val store: RecordingStore,
    private val recordingId: String,
    private val sampleRate: Int,
    private val channels: Int,
    private val samplesPerChunk: Long,
    /** fsync at most this often, bounding what a power loss can take. */
    private val syncEveryMillis: Long = 5_000,
    private val nowMillis: () -> Long = System::currentTimeMillis,
) {
    private val dir = store.dirFor(recordingId)
    private var n = -1
    private var out: FileOutputStream? = null
    private var samplesInChunk = 0L
    private var bytesInChunk = 0L
    private var lastSync = 0L

    var totalSamples = 0L
        private set

    val chunkIndex: Int get() = n

    /** Appends one raw AAC frame (no header). */
    fun writeFrame(payload: ByteArray, offset: Int = 0, length: Int = payload.size) {
        if (out == null || samplesInChunk >= samplesPerChunk) rotate()
        val o = out ?: throw IOException("no chunk open")
        val header = Adts.header(length, sampleRate, channels)
        // One write per frame: a crash leaves at most one partial frame, cut on recovery.
        val frame = ByteArray(header.size + length)
        System.arraycopy(header, 0, frame, 0, header.size)
        System.arraycopy(payload, offset, frame, header.size, length)
        o.write(frame)
        bytesInChunk += frame.size
        samplesInChunk += Adts.SAMPLES_PER_FRAME
        totalSamples += Adts.SAMPLES_PER_FRAME
        val now = nowMillis()
        if (now - lastSync >= syncEveryMillis) {
            o.fd.sync()
            lastSync = now
        }
    }

    /** Finishes the open chunk and records it; an empty last chunk is removed. */
    fun close() {
        finishCurrent()
    }

    private fun rotate() {
        finishCurrent()
        val next = n + 1
        if (next >= RecordingMeta.MAX_CHUNKS) throw IOException("chunk limit reached")
        n = next
        out = FileOutputStream(File(dir, chunkName(n)))
        samplesInChunk = 0
        bytesInChunk = 0
        lastSync = nowMillis()
    }

    private fun finishCurrent() {
        val o = out ?: return
        o.flush()
        o.fd.sync()
        o.close()
        out = null
        val f = File(dir, chunkName(n))
        if (bytesInChunk == 0L) {
            f.delete()
            return
        }
        val info = ChunkInfo(n, f.length(), Hashing.sha256File(f))
        store.updateMeta(recordingId) { m -> m.copy(chunks = m.chunks.filter { it.n != info.n } + info) }
    }
}

package com.musubi.phone.core

import java.io.File
import java.nio.file.Files as NioFiles
import java.time.Clock
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneOffset

object TestUtil {
    val T0: OffsetDateTime = OffsetDateTime.of(2026, 11, 17, 10, 0, 0, 0, ZoneOffset.ofHours(1))

    fun tempDir(): File = NioFiles.createTempDirectory("musubi-test").toFile().also { it.deleteOnExit() }

    fun clockAt(t: OffsetDateTime): Clock = Clock.fixed(t.toInstant(), t.offset)

    /** Bytes of [count] ADTS frames with [payload]-byte bodies. */
    fun adtsFrames(count: Int, payload: Int = 100, fill: Byte = 0x11): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        repeat(count) {
            out.write(Adts.header(payload, 48000, 1))
            out.write(ByteArray(payload) { fill })
        }
        return out.toByteArray()
    }

    fun newMeta(id: String = RecordingMeta.newId(), end: OffsetDateTime? = null, event: EventRef? = null) =
        RecordingMeta(id, T0, end, "Test Phone", event, emptyList())

    /** Creates a finished recording with [chunks] chunk files of real ADTS frames. */
    fun finishedRecording(store: RecordingStore, chunks: Int, start: OffsetDateTime = T0): Recording {
        val id = RecordingMeta.newId()
        store.create(RecordingMeta(id, start, null, "Test Phone", null, emptyList()))
        val infos = (0 until chunks).map { n ->
            val f = File(store.dirFor(id), chunkName(n))
            f.writeBytes(adtsFrames(10, fill = n.toByte()))
            ChunkInfo(n, f.length(), Hashing.sha256File(f))
        }
        store.updateMeta(id) { it.copy(end = start.plusMinutes(10), chunks = infos) }
        return store.read(id)!!
    }

    fun setMtime(f: File, t: Instant) {
        f.setLastModified(t.toEpochMilli())
    }
}

package com.musubi.phone.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

class RecoveryAndChunksTest {
    @Test
    fun recoveryClosesInterruptedRecordingAndKeepsChunks() {
        val store = RecordingStore(TestUtil.tempDir())
        val m = TestUtil.newMeta()
        val dir = store.create(m)
        val c0 = File(dir, chunkName(0)).apply { writeBytes(TestUtil.adtsFrames(20)) }
        // Chunk 0 was finished and recorded in meta before the crash; chunk 1 was being written.
        store.updateMeta(m.id) { it.copy(chunks = listOf(ChunkInfo(0, c0.length(), Hashing.sha256File(c0)))) }
        val full = TestUtil.adtsFrames(8)
        val c1 = File(dir, chunkName(1)).apply { writeBytes(full + TestUtil.adtsFrames(1).copyOf(30)) }
        val lastWrite = TestUtil.T0.plusMinutes(7).toInstant()
        TestUtil.setMtime(c0, TestUtil.T0.plusMinutes(5).toInstant())
        TestUtil.setMtime(c1, lastWrite)

        assertEquals(listOf(m.id), store.recoverInterrupted())

        val r = store.read(m.id)!!
        assertFalse(r.meta.isRecording)
        assertEquals(lastWrite.toEpochMilli() / 1000, r.meta.end!!.toInstant().toEpochMilli() / 1000)
        assertEquals(TestUtil.T0.offset, r.meta.end!!.offset)
        assertEquals(listOf(0, 1), r.meta.chunks.map { it.n })
        assertEquals(full.size.toLong(), r.meta.chunks[1].size) // partial frame cut
        assertEquals(Hashing.sha256Hex(full), r.meta.chunks[1].sha256)
        assertEquals(SyncState.ON_PHONE, r.sync.state)
    }

    @Test
    fun recoveryDropsEmptyChunkAndHandlesNoAudio() {
        val store = RecordingStore(TestUtil.tempDir())
        val m = TestUtil.newMeta()
        val dir = store.create(m)
        File(dir, chunkName(0)).createNewFile()
        store.recoverInterrupted()
        val r = store.read(m.id)!!
        assertEquals(TestUtil.T0, r.meta.end)
        assertTrue(r.meta.chunks.isEmpty())
        assertFalse(File(dir, chunkName(0)).exists())
    }

    @Test
    fun recoveryLeavesFinishedRecordingsAlone() {
        val store = RecordingStore(TestUtil.tempDir())
        val r = TestUtil.finishedRecording(store, 2)
        val before = File(r.dir, "meta.json").readText()
        assertTrue(store.recoverInterrupted().isEmpty())
        assertEquals(before, File(r.dir, "meta.json").readText())
    }

    @Test
    fun chunkWriterRotatesAtFrameBoundariesAndRecordsHashes() {
        val store = RecordingStore(TestUtil.tempDir())
        val m = TestUtil.newMeta()
        store.create(m)
        // 3 frames per chunk (3 * 1024 samples).
        val w = ChunkWriter(store, m.id, 48000, 1, samplesPerChunk = 3L * 1024, nowMillis = { 0L })
        val payload = ByteArray(50) { 7 }
        repeat(7) { w.writeFrame(payload) }
        // Two chunks are finished and already in meta.json while chunk 2 is still open.
        assertEquals(listOf(0, 1), store.read(m.id)!!.meta.chunks.map { it.n })
        w.close()
        val meta = store.read(m.id)!!.meta
        assertEquals(listOf(0, 1, 2), meta.chunks.map { it.n })
        for (c in meta.chunks) {
            val f = File(store.dirFor(m.id), c.name)
            assertEquals(f.length(), c.size)
            assertEquals(Hashing.sha256File(f), c.sha256)
            assertEquals(f.length(), Adts.completeLength(f))
        }
        assertEquals(3L * 57, meta.chunks[0].size)
        assertEquals(1L * 57, meta.chunks[2].size)
        assertEquals(7L * 1024, w.totalSamples)
    }

    @Test
    fun chunkWriterWithNoFramesLeavesNoChunk() {
        val store = RecordingStore(TestUtil.tempDir())
        val m = TestUtil.newMeta()
        store.create(m)
        ChunkWriter(store, m.id, 48000, 1, 1024).close()
        assertTrue(store.read(m.id)!!.meta.chunks.isEmpty())
        assertTrue(store.deleteIfEmpty(m.id))
        assertNull(store.read(m.id))
    }
}

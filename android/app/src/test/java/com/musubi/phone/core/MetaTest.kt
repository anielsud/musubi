package com.musubi.phone.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.json.JSONObject
import java.io.File

class MetaTest {
    @Test
    fun roundTripKeepsEverything() {
        val ev = EventRef("evt-1", "Quarterly review with Example Corp", TestUtil.T0.minusMinutes(5))
        val m = RecordingMeta(
            RecordingMeta.newId(), TestUtil.T0, TestUtil.T0.plusMinutes(42), "Test Phone", ev,
            listOf(ChunkInfo(0, 1234, "a".repeat(64)), ChunkInfo(1, 99, "b".repeat(64))),
        )
        assertEquals(m, RecordingMeta.fromJson(m.toJson()))
    }

    @Test
    fun openRecordingHasNullEndAndEvent() {
        val m = TestUtil.newMeta()
        val o = JSONObject(m.toJson())
        assertTrue(o.isNull("end"))
        assertTrue(o.isNull("event"))
        val back = RecordingMeta.fromJson(m.toJson())
        assertTrue(back.isRecording)
        assertNull(back.event)
    }

    @Test
    fun timesCarryZoneOffset() {
        val o = JSONObject(TestUtil.newMeta().toJson())
        assertEquals("2026-11-17T10:00:00+01:00", o.getString("start"))
    }

    @Test
    fun chunkNamesAreFourDigits() {
        val o = JSONObject(TestUtil.newMeta().copy(chunks = listOf(ChunkInfo(7, 1, "c".repeat(64)))).toJson())
        assertEquals("chunk-0007.aac", o.getJSONArray("chunks").getJSONObject(0).getString("name"))
        assertEquals(7, chunkNumber("chunk-0007.aac"))
        assertNull(chunkNumber("chunk-7.aac"))
        assertNull(chunkNumber("../chunk-0001.aac"))
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsBadId() {
        RecordingMeta.fromJson(TestUtil.newMeta().toJson().replace(Regex("\"id\": \"[^\"]+\""), "\"id\": \"../../etc\""))
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsBadHash() {
        RecordingMeta.fromJson(TestUtil.newMeta().copy(chunks = listOf(ChunkInfo(0, 1, "a".repeat(64)))).toJson().replace("a".repeat(64), "XYZ"))
    }

    @Test
    fun atomicWriteReplacesAndLeavesNoTemp() {
        val dir = TestUtil.tempDir()
        val f = File(dir, "meta.json")
        Files.writeAtomic(f, "one".toByteArray())
        Files.writeAtomic(f, "two".toByteArray())
        assertEquals("two", f.readText())
        assertFalse(File(dir, "meta.json.tmp").exists())
    }

    @Test
    fun storeUpdateRewritesMeta() {
        val store = RecordingStore(TestUtil.tempDir())
        val m = TestUtil.newMeta()
        store.create(m)
        val ev = EventRef("evt-2", "Hallway chat", TestUtil.T0)
        store.updateMeta(m.id) { it.copy(event = ev) }
        assertEquals(ev, store.read(m.id)!!.meta.event)
        assertEquals(SyncState.ON_PHONE, store.read(m.id)!!.sync.state)
    }

    @Test
    fun storeSkipsUnreadableFolders() {
        val root = TestUtil.tempDir()
        val store = RecordingStore(root)
        store.create(TestUtil.newMeta())
        val broken = File(root, RecordingMeta.newId()).apply { mkdirs() }
        File(broken, "meta.json").writeText("{not json")
        assertEquals(1, store.list().size)
        assertTrue(broken.exists()) // never deleted
    }
}

package com.musubi.phone.core

import org.json.JSONObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.File

class DetailsTest {
    private val phone = FakePhone()
    private val editor = DetailsEditor(phone.store, TestUtil.clockAt(TestUtil.T0.plusHours(1)))
    private fun err(r: Frames.Reply) = r.json.optString("error")

    private fun jpeg(seed: Int, n: Int = 400): ByteArray =
        byteArrayOf(0xFF.toByte(), 0xD8.toByte(), 0xFF.toByte(), 0xE0.toByte()) + ByteArray(n) { (it + seed).toByte() }

    private fun addPhoto(id: String, bytes: ByteArray): Details {
        val f = editor.incoming(id)
        f.writeBytes(bytes)
        return editor.addPhoto(id, f)
    }

    private fun refused(fn: () -> Unit) {
        try {
            fn()
            fail("not refused")
        } catch (_: DetailsEditor.Refused) {
        }
    }

    @Test
    fun editsRaiseRevAndKeepTheAudio() {
        val r = TestUtil.finishedRecording(phone.store, 2)
        val audio = r.chunkFile(0).readBytes()
        assertEquals(0, r.meta.details.rev)
        assertEquals(1, editor.setNotes(r.id, "Invented note\nline two").rev)
        assertEquals(1, editor.setNotes(r.id, "Invented note\nline two").rev) // unchanged: no new rev
        assertEquals(2, editor.addPerson(r.id, "  Ana   Example ").rev)
        assertEquals(2, editor.addPerson(r.id, "ana example").rev) // already there
        assertEquals(3, editor.addPerson(r.id, "Bo Sample", "bo@example.test").rev)
        assertEquals(4, editor.removePerson(r.id, "Ana Example").rev)
        val d = addPhoto(r.id, jpeg(1))
        assertEquals(5, d.rev)
        assertEquals("photo-0001.jpg", d.photos.single().name)
        val after = phone.store.read(r.id)!!
        assertEquals("Invented note\nline two", after.meta.details.notes)
        assertEquals(TestUtil.T0.plusHours(1).toInstant(), after.meta.details.notesUpdated!!.toInstant())
        assertEquals(listOf(Attendee("Bo Sample", "bo@example.test")), after.meta.details.attendees)
        assertEquals(r.meta.chunks, after.meta.chunks)
        assertArrayEquals(audio, r.chunkFile(0).readBytes())
        assertArrayEquals(jpeg(1), after.photoFile("photo-0001.jpg").readBytes())
        assertEquals(Hashing.sha256Hex(jpeg(1)), after.meta.details.photos.single().sha256)
        // No stray incoming files.
        assertTrue(r.dir.listFiles()!!.none { it.name.startsWith(DetailsEditor.INCOMING_PREFIX) })
    }

    @Test
    fun editingWhileRecordingLeavesTheRecorderAlone() {
        val m = TestUtil.newMeta().also { phone.store.create(it) }
        editor.setNotes(m.id, "during")
        // The recorder adds a chunk and stops as it does: its rewrites keep the details.
        phone.store.updateMeta(m.id) { it.copy(chunks = it.chunks + ChunkInfo(0, 10, "a".repeat(64))) }
        phone.store.updateMeta(m.id) { it.copy(end = TestUtil.T0.plusMinutes(5)) }
        val after = phone.store.read(m.id)!!.meta
        assertEquals("during", after.details.notes)
        assertEquals(1, after.details.rev)
        assertEquals(1, after.chunks.size)
    }

    @Test
    fun bounds() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        editor.setNotes(r.id, "x".repeat(Details.MAX_NOTES))
        refused { editor.setNotes(r.id, "x".repeat(Details.MAX_NOTES + 1)) }
        refused { editor.addPerson(r.id, "   ") }
        refused { editor.addPerson(r.id, "n".repeat(Details.MAX_NAME + 1)) }
        repeat(Details.MAX_PEOPLE) { editor.addPerson(r.id, "Person $it") }
        refused { editor.addPerson(r.id, "One too many") }
        // Not a JPEG, or too big: refused, and the incoming file is gone.
        val notJpeg = editor.incoming(r.id).also { it.writeBytes(byteArrayOf(1, 2, 3, 4)) }
        refused { editor.addPhoto(r.id, notJpeg) }
        assertFalse(notJpeg.exists())
        val big = editor.incoming(r.id).also { f ->
            f.writeBytes(jpeg(1))
            java.io.RandomAccessFile(f, "rw").use { it.setLength(Details.MAX_PHOTO_BYTES + 1) }
        }
        refused { editor.addPhoto(r.id, big) }
        repeat(Details.MAX_PHOTOS) { addPhoto(r.id, jpeg(it, 10)) }
        refused { addPhoto(r.id, jpeg(99, 10)) }
        // Only incoming files in the recording's own folder are taken.
        val elsewhere = File(TestUtil.tempDir(), DetailsEditor.INCOMING_PREFIX + "x.tmp").also { it.writeBytes(jpeg(1)) }
        try {
            editor.addPhoto(r.id, elsewhere)
            fail("taken from elsewhere")
        } catch (_: IllegalArgumentException) {
        }
    }

    @Test
    fun deletingAPhotoRemovesItsFileAndEntry() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        addPhoto(r.id, jpeg(1))
        addPhoto(r.id, jpeg(2))
        val d = editor.deletePhoto(r.id, "photo-0001.jpg")
        assertEquals(listOf("photo-0002.jpg"), d.photos.map { it.name })
        assertFalse(File(r.dir, "photo-0001.jpg").exists())
        // Numbers aren't reused, so the Mac never mistakes a new photo for an old one.
        assertEquals("photo-0003.jpg", addPhoto(r.id, jpeg(3)).photos.last().name)
        try {
            editor.deletePhoto(r.id, "../meta.json")
            fail("a path deleted")
        } catch (_: IllegalArgumentException) {
        }
    }

    @Test
    fun metaV2RoundTripsAndV1Reads() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        editor.setNotes(r.id, "n")
        editor.addPerson(r.id, "Ana Example")
        addPhoto(r.id, jpeg(1))
        val text = File(r.dir, RecordingStore.META).readText()
        val o = JSONObject(text)
        assertEquals(2, o.getInt("version"))
        val d = o.getJSONObject("details")
        assertEquals(3, d.getLong("rev"))
        assertEquals("Ana Example", d.getJSONArray("attendees").getJSONObject(0).getString("name"))
        assertFalse(d.getJSONArray("attendees").getJSONObject(0).has("email"))
        assertEquals("n", d.getJSONObject("notes").getString("text"))
        assertTrue(d.getJSONObject("notes").has("updated"))
        val p = d.getJSONArray("photos").getJSONObject(0)
        assertEquals(setOf("name", "size", "sha256", "taken"), p.keySet())
        assertEquals(RecordingMeta.fromJson(text), phone.store.read(r.id)!!.meta)
        // A version 1 meta has no details.
        o.remove("details")
        o.put("version", 1)
        assertEquals(Details(), RecordingMeta.fromJson(o.toString()).details)
        // A bad photo name in meta.json makes it unreadable rather than a path.
        p.put("name", "../x.jpg")
        o.put("details", d)
        try {
            RecordingMeta.fromJson(o.toString())
            fail("bad name read")
        } catch (_: IllegalArgumentException) {
        }
    }

    @Test
    fun fileSendsAListedPhoto() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        addPhoto(r.id, jpeg(1))
        val reply = phone.run(Frames.op("file", "rid" to r.id, "name" to "photo-0001.jpg")).replies.single()
        assertTrue(reply.json.getBoolean("ok"))
        assertEquals(Hashing.sha256Hex(jpeg(1)), reply.json.getString("sha256"))
        assertArrayEquals(jpeg(1), reply.body)

        val open = TestUtil.newMeta().also { phone.store.create(it) }
        File(r.dir, "photo-0009.jpg").writeBytes(jpeg(9)) // on disk, not listed
        val run = phone.run(
            Frames.op("file", "rid" to RecordingMeta.newId(), "name" to "photo-0001.jpg"),
            Frames.op("file", "rid" to open.id, "name" to "photo-0001.jpg"),
            Frames.op("file", "rid" to r.id, "name" to "photo-0009.jpg"),
        )
        assertEquals(listOf("unknown_recording", "unknown_recording", "unknown_file"), run.replies.map { err(it) })
        File(r.dir, "photo-0001.jpg").appendBytes(byteArrayOf(1))
        assertEquals("file_changed", err(phone.run(Frames.op("file", "rid" to r.id, "name" to "photo-0001.jpg")).replies.single()))
        File(r.dir, "photo-0001.jpg").delete()
        assertEquals("gone", err(phone.run(Frames.op("file", "rid" to r.id, "name" to "photo-0001.jpg")).replies.single()))
        for (bad in listOf("../meta.json", "meta.json", "photo-1.jpg", "photo-0001.jpg/..", "chunk-0000.aac")) {
            assertTrue(bad, phone.run(Frames.op("file", "rid" to r.id, "name" to bad)).violation)
        }
        assertTrue(phone.run(Frames.op("file", "rid" to r.id)).violation)
    }

    @Test
    fun listIncludesImportedWithNewerDetails() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        editor.setNotes(r.id, "before")
        phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "session_id" to "s-1", "details_rev" to 1))
        assertEquals(1, phone.store.read(r.id)!!.sync.detailsRev)
        fun listed() = phone.run(Frames.op("list")).replies.single().json.getJSONArray("recordings")
        assertEquals(0, listed().length())
        editor.setNotes(r.id, "after")
        val recs = listed()
        assertEquals(1, recs.length())
        assertEquals("imported", recs.getJSONObject(0).getString("state"))
        assertEquals(2, recs.getJSONObject(0).getJSONObject("meta").getJSONObject("details").getLong("rev"))
        phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "session_id" to "s-1", "details_rev" to 2))
        assertEquals(0, listed().length())
    }

    @Test
    fun photosDeletedOnceImportedWithTheCurrentRev() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        addPhoto(r.id, jpeg(1))
        addPhoto(r.id, jpeg(2)) // rev 2
        // Imported, but the Mac applied rev 1 only: the audio goes, the photos stay.
        phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "session_id" to "s-1", "details_rev" to 1))
        assertFalse(r.chunkFile(0).exists())
        assertTrue(File(r.dir, "photo-0001.jpg").exists())
        // Imported without details_rev (an older Mac): they stay too.
        phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "session_id" to "s-1"))
        assertTrue(File(r.dir, "photo-0002.jpg").exists())
        // Rev 2 applied: they go; meta.json keeps listing them.
        phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "session_id" to "s-1", "details_rev" to 2))
        assertFalse(File(r.dir, "photo-0001.jpg").exists())
        assertFalse(File(r.dir, "photo-0002.jpg").exists())
        assertEquals(2, phone.store.read(r.id)!!.meta.details.photos.size)
        // A rev the phone never had is a violation, as is a bad one.
        assertTrue(phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "details_rev" to 3)).violation)
        assertTrue(phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "details_rev" to -1)).violation)
        assertTrue(phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "details_rev" to "2")).violation)
    }

    @Test
    fun aRecordingWithLongNotesStillFitsTheList() {
        val r = TestUtil.finishedRecording(phone.store, 30)
        editor.setNotes(r.id, "word ".repeat(Details.MAX_NOTES / 5))
        repeat(Details.MAX_PEOPLE) { editor.addPerson(r.id, "Invented Person Number $it") }
        repeat(Details.MAX_PHOTOS) { addPhoto(r.id, jpeg(it, 10)) }
        val recs = phone.run(Frames.op("list")).replies.single().json.getJSONArray("recordings")
        assertEquals(1, recs.length())
    }

    @Test
    fun syncStatusKeepsDetailsRev() {
        val s = SyncStatus(detailsRev = 7)
        assertEquals(7, SyncStatus.fromJson(s.toJson()).detailsRev)
        assertEquals(0, SyncStatus.fromJson("{}").detailsRev)
        assertNull(SyncStatus.fromJson("{}").sessionId)
    }
}

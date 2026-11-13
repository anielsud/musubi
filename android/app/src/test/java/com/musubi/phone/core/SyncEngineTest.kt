package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.time.OffsetDateTime

/** A Mac that behaves like the API table in docs/phone.md, with knobs for failures. */
class FakeMac : MacApi {
    val chunks = mutableMapOf<Pair<String, Int>, ByteArray>()
    val completed = mutableMapOf<String, String>()
    var state = "received"
    var stateError: String? = null
    var down = false
    var wrongCert = false
    var unauthorized = false
    var putCode: Int? = null
    var statusCode: Int? = null
    var stateBody: String? = null
    var agendaBody = JSONObject().put("events", JSONArray()).put("drafting", 0).toString()
    val calls = mutableListOf<String>()

    private fun gate(): ApiResult? = when {
        down -> ApiResult.Network("ConnectException")
        wrongCert -> ApiResult.Network("SSLHandshakeException", certificateMismatch = true)
        unauthorized -> ApiResult.Http(401, ByteArray(0))
        else -> null
    }

    override fun putChunk(rid: String, n: Int, file: File, sha256: String): ApiResult {
        calls += "put $n"
        gate()?.let { return it }
        putCode?.let { return ApiResult.Http(it, ByteArray(0)) }
        val bytes = file.readBytes()
        if (Hashing.sha256Hex(bytes) != sha256) return ApiResult.Http(400, ByteArray(0))
        val prev = chunks[rid to n]
        if (prev != null && !prev.contentEquals(bytes)) return ApiResult.Http(409, ByteArray(0))
        chunks[rid to n] = bytes
        return ApiResult.Ok(204, ByteArray(0))
    }

    override fun complete(rid: String, metaJson: ByteArray): ApiResult {
        calls += "complete"
        gate()?.let { return it }
        completed[rid] = String(metaJson)
        return ApiResult.Ok(202, JSONObject().put("state", "received").toString().toByteArray())
    }

    override fun recordingState(rid: String): ApiResult {
        calls += "state"
        gate()?.let { return it }
        statusCode?.let { return ApiResult.Http(it, ByteArray(0)) }
        val body = stateBody ?: JSONObject().put("state", state).put("session_id", "s-1").put("error", stateError ?: JSONObject.NULL).toString()
        return ApiResult.Ok(200, body.toByteArray())
    }

    override fun agenda(days: Int): ApiResult {
        calls += "agenda $days"
        gate()?.let { return it }
        return ApiResult.Ok(200, agendaBody.toByteArray())
    }
}

class SyncEngineTest {
    private val now = TestUtil.T0.plusHours(2)
    private val store = RecordingStore(TestUtil.tempDir(), TestUtil.clockAt(now))
    private val agenda = AgendaStore(File(TestUtil.tempDir(), "agenda.json"))
    private val mac = FakeMac()
    private fun engine(at: OffsetDateTime = now) = SyncEngine(store, agenda, mac, TestUtil.clockAt(at))
    private fun audioFiles(r: Recording) = r.dir.listFiles()!!.filter { chunkNumber(it.name) != null }

    @Test
    fun uploadsCompletesAndKeepsAudioUntilImported() {
        val r = TestUtil.finishedRecording(store, 3)
        mac.state = "transcribing"
        val rep = engine().run()
        assertEquals(SyncOutcome.DONE, rep.outcome)
        assertEquals(3, mac.chunks.size)
        assertEquals(String(store.metaBytes(r.id)), mac.completed[r.id])
        assertEquals("agenda 10", mac.calls.last())
        val after = store.read(r.id)!!
        assertEquals(SyncState.ON_MAC, after.sync.state)
        assertEquals("transcribing", after.sync.serverState)
        assertEquals(3, audioFiles(after).size)
    }

    @Test
    fun deletesAudioOnlyAfterImportedAndKeepsMeta() {
        val r = TestUtil.finishedRecording(store, 2)
        mac.state = "waiting"
        engine().run()
        assertEquals(2, audioFiles(store.read(r.id)!!).size)
        mac.state = "imported"
        engine().run()
        val after = store.read(r.id)!!
        assertEquals(SyncState.IMPORTED, after.sync.state)
        assertEquals("s-1", after.sync.sessionId)
        assertTrue(audioFiles(after).isEmpty())
        assertTrue(File(after.dir, "meta.json").isFile)
        assertEquals(2, after.meta.chunks.size)
    }

    @Test
    fun metaPrunedThirtyDaysAfterImport() {
        val r = TestUtil.finishedRecording(store, 1)
        mac.state = "imported"
        engine().run()
        RecordingStore(store.root, TestUtil.clockAt(now.plusDays(29))).pruneImported()
        assertNotNull(store.read(r.id))
        RecordingStore(store.root, TestUtil.clockAt(now.plusDays(31))).pruneImported()
        assertNull(store.read(r.id))
    }

    @Test
    fun resumesWithoutResendingAcknowledgedChunks() {
        val r = TestUtil.finishedRecording(store, 3)
        store.updateSync(r.id) { it.copy(state = SyncState.UPLOADING, uploaded = setOf(0, 1)) }
        engine().run()
        assertEquals(listOf("put 2"), mac.calls.filter { it.startsWith("put") })
    }

    @Test
    fun macUnreachableKeepsStateForRetry() {
        val r = TestUtil.finishedRecording(store, 2)
        mac.down = true
        assertEquals(SyncOutcome.RETRY, engine().run().outcome)
        val after = store.read(r.id)!!
        assertEquals(SyncState.UPLOADING, after.sync.state)
        assertEquals(2, audioFiles(after).size)
        mac.down = false
        engine().run()
        assertEquals(SyncState.ON_MAC, store.read(r.id)!!.sync.state)
    }

    @Test
    fun certificateMismatchIsReportedAndNothingChanges() {
        val r = TestUtil.finishedRecording(store, 1)
        mac.wrongCert = true
        val rep = engine().run()
        assertEquals(SyncOutcome.RETRY, rep.outcome)
        assertTrue(rep.message.contains("certificate"))
        assertEquals(1, audioFiles(store.read(r.id)!!).size)
    }

    @Test
    fun conflictFailsRecordingButKeepsAudioAndContinuesWithOthers() {
        val bad = TestUtil.finishedRecording(store, 1, TestUtil.T0)
        val good = TestUtil.finishedRecording(store, 1, TestUtil.T0.plusMinutes(30))
        mac.chunks[bad.id to 0] = byteArrayOf(1, 2, 3)
        engine().run()
        val b = store.read(bad.id)!!
        assertEquals(SyncState.FAILED, b.sync.state)
        assertTrue(b.sync.error!!.contains("different audio"))
        assertEquals(1, audioFiles(b).size)
        assertEquals(SyncState.ON_MAC, store.read(good.id)!!.sync.state)
    }

    @Test
    fun unauthorizedStopsWithoutTouchingRecordings() {
        val r = TestUtil.finishedRecording(store, 1)
        mac.unauthorized = true
        assertEquals(SyncOutcome.UNAUTHORIZED, engine().run().outcome)
        assertEquals(1, audioFiles(store.read(r.id)!!).size)
    }

    @Test
    fun serverFailureKeepsAudio() {
        val r = TestUtil.finishedRecording(store, 1)
        mac.state = "failed"
        mac.stateError = "transcription failed"
        engine().run()
        val after = store.read(r.id)!!
        assertEquals(SyncState.FAILED, after.sync.state)
        assertEquals("transcription failed", after.sync.error)
        assertEquals(1, audioFiles(after).size)
    }

    @Test
    fun macThatLostTheRecordingGetsItAgain() {
        val r = TestUtil.finishedRecording(store, 2)
        engine().run()
        mac.chunks.clear()
        mac.statusCode = 404
        engine().run()
        assertEquals(SyncState.UPLOADING, store.read(r.id)!!.sync.state)
        mac.statusCode = null
        engine().run()
        assertEquals(2, mac.chunks.size)
        assertEquals(SyncState.ON_MAC, store.read(r.id)!!.sync.state)
    }

    @Test
    fun unknownServerStateNeverDeletes() {
        val r = TestUtil.finishedRecording(store, 1)
        mac.stateBody = """{"state":"imported-ish"}"""
        engine().run()
        // complete() succeeded (state received), then the odd poll reply is ignored.
        val after = store.read(r.id)!!
        assertEquals(SyncState.ON_MAC, after.sync.state)
        assertEquals(1, audioFiles(after).size)
    }

    @Test
    fun chunkChangedOnPhoneIsNotUploaded() {
        val r = TestUtil.finishedRecording(store, 1)
        r.chunkFile(0).appendBytes(byteArrayOf(9))
        engine().run()
        assertEquals(SyncState.FAILED, store.read(r.id)!!.sync.state)
        assertTrue(mac.chunks.isEmpty())
    }

    @Test
    fun activeRecordingIsSkipped() {
        val m = TestUtil.newMeta()
        store.create(m)
        File(store.dirFor(m.id), chunkName(0)).writeBytes(TestUtil.adtsFrames(3))
        engine().run()
        assertTrue(mac.calls.none { it.startsWith("put") || it == "complete" })
        assertEquals(SyncState.ON_PHONE, store.read(m.id)!!.sync.state)
    }

    @Test
    fun agendaReplacedAndOldEventsDropped() {
        fun ev(id: String, start: OffsetDateTime, hours: Long) = JSONObject()
            .put("id", id).put("start", RecordingMeta.formatTime(start)).put("end", RecordingMeta.formatTime(start.plusHours(hours)))
            .put("subject", "Meeting $id").put("location", "Hall B").put("organizer", "Avery Example")
            .put("attendees", JSONArray().put(JSONObject().put("name", "Robin Sample")))
            .put("brief", JSONObject.NULL)
        mac.agendaBody = JSONObject().put("drafting", 2).put("events", JSONArray()
            .put(ev("old", now.minusDays(3), 1))
            .put(ev("yesterday", now.minusHours(20), 1))
            .put(ev("next", now.plusDays(1), 1))).toString()
        engine().run()
        val a = agenda.load()!!
        assertEquals(listOf("yesterday", "next"), a.events.map { it.id })
        assertEquals(2, a.drafting)
        assertEquals(listOf("Robin Sample"), a.events[1].attendees)
    }

    @Test
    fun malformedAgendaKeepsOldCopy() {
        agenda.replace(Agenda(emptyList(), 5, now), now)
        mac.agendaBody = "[]"
        assertEquals(SyncOutcome.RETRY, engine().run().outcome)
        assertEquals(5, agenda.load()!!.drafting)
    }
}

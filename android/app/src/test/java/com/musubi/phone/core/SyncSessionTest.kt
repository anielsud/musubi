package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.util.Base64

class SyncSessionTest {
    private val phone = FakePhone()
    private fun err(r: Frames.Reply) = r.json.optString("error")

    @Test
    fun helloReportsThePhone() {
        val run = phone.run(Frames.op("hello", "v" to 2, "mac_name" to "Test Mac"))
        val r = run.replies.single().json
        assertTrue(r.getBoolean("ok"))
        assertEquals(2, r.getInt("v"))
        assertEquals("Test Phone", r.getString("device_name"))
        assertEquals("0.3.0", r.getString("app_version"))
        assertEquals(listOf("192.168.1.20", "100.101.102.103"), (0 until r.getJSONArray("addresses").length()).map { r.getJSONArray("addresses").getString(it) })
        assertEquals(SyncSession.End.CLOSED, run.end)
    }

    @Test
    fun helloWithAnotherVersionEnds() {
        val run = phone.run(Frames.op("hello", "v" to 3, "mac_name" to "Test Mac"), Frames.op("list"))
        assertEquals("unsupported_version", err(run.replies.single()))
        assertEquals(SyncSession.End.VERSION, run.end)
        assertTrue(phone.run(Frames.op("hello", "v" to "2", "mac_name" to "Test Mac")).violation)
        assertTrue(phone.run(Frames.op("hello", "v" to 2, "mac_name" to "")).violation)
        assertTrue(phone.run(Frames.op("hello", "v" to 2, "mac_name" to "a\nb")).violation)
    }

    @Test
    fun listShowsFinishedRecordingsNotYetImported() {
        val a = TestUtil.finishedRecording(phone.store, 2, TestUtil.T0)
        val b = TestUtil.finishedRecording(phone.store, 1, TestUtil.T0.plusHours(1))
        val imported = TestUtil.finishedRecording(phone.store, 1, TestUtil.T0.minusHours(1))
        phone.store.updateSync(imported.id) { it.copy(state = SyncState.IMPORTED, serverState = "imported") }
        phone.store.create(TestUtil.newMeta()) // still recording
        phone.store.updateSync(b.id) { it.copy(state = SyncState.ON_MAC, serverState = "waiting") }

        val recs = phone.run(Frames.op("list")).replies.single().json.getJSONArray("recordings")
        assertEquals(2, recs.length())
        assertEquals(a.id, recs.getJSONObject(0).getJSONObject("meta").getString("id"))
        assertTrue(recs.getJSONObject(0).isNull("state"))
        assertEquals(2, recs.getJSONObject(0).getJSONObject("meta").getJSONArray("chunks").length())
        assertEquals(b.id, recs.getJSONObject(1).getJSONObject("meta").getString("id"))
        assertEquals("waiting", recs.getJSONObject(1).getString("state"))
        // The meta sent is meta.json itself.
        assertEquals(RecordingMeta.fromJson(recs.getJSONObject(0).getJSONObject("meta").toString()), a.meta)
    }

    @Test
    fun listFitsOneFrameAndTheRestWaits() {
        repeat(40) { i -> TestUtil.finishedRecording(phone.store, 30, TestUtil.T0.plusHours(i.toLong())) }
        val run = phone.run(Frames.op("list"), Frames.op("agenda", "body" to 2).let { it + "{}".toByteArray() }, Frames.op("bye"))
        val n = run.replies[0].json.getJSONArray("recordings").length()
        assertTrue("$n", n in 1 until 40)
        assertTrue(run.session.summary.listTruncated)
        assertEquals(SyncSession.End.BYE, run.end)
    }

    @Test
    fun chunkSendsTheBytesItsMetaDescribes() {
        val r = TestUtil.finishedRecording(phone.store, 2)
        val run = phone.run(Frames.op("chunk", "rid" to r.id, "n" to 1))
        val reply = run.replies.single()
        assertTrue(reply.json.getBoolean("ok"))
        assertEquals(r.meta.chunks[1].sha256, reply.json.getString("sha256"))
        assertArrayEquals(r.chunkFile(1).readBytes(), reply.body)
        val s = phone.store.read(r.id)!!.sync
        assertEquals(SyncState.UPLOADING, s.state)
        assertEquals(setOf(1), s.uploaded)
    }

    @Test
    fun chunkRefusals() {
        val r = TestUtil.finishedRecording(phone.store, 2)
        val unknown = RecordingMeta.newId()
        val open = TestUtil.newMeta().also { phone.store.create(it) }
        val run = phone.run(
            Frames.op("chunk", "rid" to unknown, "n" to 0),
            Frames.op("chunk", "rid" to open.id, "n" to 0),
            Frames.op("chunk", "rid" to r.id, "n" to 5),
        )
        assertEquals(listOf("unknown_recording", "unknown_recording", "unknown_chunk"), run.replies.map { err(it) })

        r.chunkFile(0).appendBytes(byteArrayOf(1))
        assertEquals("chunk_changed", err(phone.run(Frames.op("chunk", "rid" to r.id, "n" to 0)).replies.single()))

        for (bad in listOf(
            Frames.op("chunk", "rid" to r.id, "n" to 10_000),
            Frames.op("chunk", "rid" to r.id, "n" to -1),
            Frames.op("chunk", "rid" to r.id, "n" to "0"),
            Frames.op("chunk", "rid" to r.id, "n" to 0.5),
            Frames.op("chunk", "rid" to "../x", "n" to 0),
            Frames.op("chunk", "rid" to r.id.uppercase(), "n" to 0),
            Frames.op("chunk", "n" to 0),
        )) assertTrue(phone.run(bad).violation)
    }

    @Test
    fun importedDeletesTheAudioAndKeepsMeta() {
        val r = TestUtil.finishedRecording(phone.store, 2)
        LiveTextFile(File(r.dir, LiveTextFile.NAME)).append(0, "invented sentence")
        val run = phone.run(Frames.op("state", "rid" to r.id, "state" to "imported", "session_id" to "s-1"))
        assertTrue(run.replies.single().json.getBoolean("ok"))
        val after = phone.store.read(r.id)!!
        assertEquals(SyncState.IMPORTED, after.sync.state)
        assertEquals("s-1", after.sync.sessionId)
        assertTrue(after.sync.importedAt != null)
        assertFalse(r.chunkFile(0).exists())
        assertNull(phone.store.liveTextFile(r.id))
        assertTrue(File(r.dir, RecordingStore.META).isFile)
        // A later state never brings it back, and its chunks are gone.
        phone.run(Frames.op("state", "rid" to r.id, "state" to "transcribing"))
        assertEquals(SyncState.IMPORTED, phone.store.read(r.id)!!.sync.state)
        assertEquals("gone", err(phone.run(Frames.op("chunk", "rid" to r.id, "n" to 0)).replies.single()))
        // Kept 30 days after import, then pruned.
        assertEquals(0, phone.store.pruneImported())
    }

    @Test
    fun otherStatesAreShown() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        phone.run(Frames.op("state", "rid" to r.id, "state" to "transcribing"))
        assertEquals(SyncState.ON_MAC, phone.store.read(r.id)!!.sync.state)
        assertEquals("transcribing", phone.store.read(r.id)!!.sync.serverState)
        phone.run(Frames.op("state", "rid" to r.id, "state" to "failed", "error" to "no\nspeech"))
        val s = phone.store.read(r.id)!!.sync
        assertEquals(SyncState.FAILED, s.state)
        assertEquals("no speech", s.error)
        assertTrue(r.chunkFile(0).exists())

        assertEquals("unknown_recording", err(phone.run(Frames.op("state", "rid" to RecordingMeta.newId(), "state" to "received")).replies.single()))
        assertTrue(phone.run(Frames.op("state", "rid" to r.id, "state" to "deleted")).violation)
        assertTrue(phone.run(Frames.op("state", "rid" to r.id, "state" to "received", "session_id" to "x".repeat(201))).violation)
        assertTrue(phone.run(Frames.op("state", "rid" to r.id, "state" to "received", "error" to 5)).violation)
    }

    private fun agendaFrame(body: ByteArray) = Frames.frame(JSONObject().put("op", "agenda").put("body", body.size).toString(), body)

    @Test
    fun agendaReplacesTheStoredOne() {
        val body = JSONObject().put("drafting", 2).put("made_at", "2026-11-17T08:00:00+01:00").put("events", JSONArray()
            .put(JSONObject().put("id", "e1").put("start", "2026-11-17T12:00:00+01:00").put("end", "2026-11-17T13:00:00+01:00")
                .put("subject", "Invented lunch").put("location", "").put("organizer", "").put("attendees", JSONArray())
                .put("brief", JSONObject.NULL))
            .put(JSONObject().put("id", "old").put("start", "2026-11-10T12:00:00+01:00").put("end", "2026-11-10T13:00:00+01:00")))
            .toString().toByteArray()
        val run = phone.run(agendaFrame(body))
        assertTrue(run.replies.single().json.getBoolean("ok"))
        val a = phone.agenda.load()!!
        assertEquals(listOf("e1"), a.events.map { it.id })
        assertEquals(2, a.drafting)

        assertEquals("bad_agenda", err(phone.run(agendaFrame("[]".toByteArray())).replies.single()))
        assertEquals("bad_agenda", err(phone.run(agendaFrame("{\"events\":1}".toByteArray())).replies.single()))
        assertEquals(listOf("e1"), phone.agenda.load()!!.events.map { it.id })
        assertTrue(phone.run(Frames.op("agenda")).violation)
        assertTrue(phone.run(Frames.frame("""{"op":"list","body":2}""", "{}".toByteArray())).violation)
    }

    @Test
    fun unknownOrMalformedRequestsCloseWithoutReply() {
        for (bad in listOf(Frames.op("delete"), Frames.frame("""{"v":2}"""), Frames.frame("""{"op":7}"""), Frames.frame("{'op':'list'}"))) {
            val run = phone.run(bad, Frames.op("list"))
            assertTrue(run.violation)
            assertTrue(run.replies.isEmpty())
        }
    }

    private fun emptyAgenda() = agendaFrame("""{"events":[],"drafting":0}""".toByteArray())

    @Test
    fun byeAfterAFullSyncWithNothingPendingEndsTheWindow() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        val run = phone.run(Frames.op("hello", "v" to 2, "mac_name" to "Test Mac"), Frames.op("list"),
            Frames.op("chunk", "rid" to r.id, "n" to 0), Frames.op("state", "rid" to r.id, "state" to "received"),
            emptyAgenda(), Frames.op("bye"))
        assertEquals(SyncSession.End.SYNCED, run.end)
        assertTrue(run.replies.last().json.getBoolean("ok"))
    }

    @Test
    fun byeWithSomethingPendingKeepsTheWindow() {
        val r = TestUtil.finishedRecording(phone.store, 1)
        // The Mac never reported on the recording.
        assertEquals(SyncSession.End.BYE, phone.run(Frames.op("list"), emptyAgenda(), Frames.op("bye")).end)
        // No agenda: not a full sync.
        phone.store.updateSync(r.id) { it.copy(serverState = "received", state = SyncState.ON_MAC) }
        assertEquals(SyncSession.End.BYE, phone.run(Frames.op("list"), Frames.op("bye")).end)
        // An error during the sync.
        assertEquals(SyncSession.End.BYE, phone.run(Frames.op("list"), Frames.op("chunk", "rid" to RecordingMeta.newId(), "n" to 0), emptyAgenda(), Frames.op("bye")).end)
        // Pairing open.
        phone.pairing.open()
        assertEquals(SyncSession.End.BYE, phone.run(Frames.op("list"), emptyAgenda(), Frames.op("bye")).end)
    }

    @Test
    fun anUnpairedMacMayOnlyPair() {
        val stranger = ByteArray(32) { 7 }
        val run = phone.run(Frames.op("list"), Frames.op("list"), fp = stranger)
        assertEquals("not_paired", err(run.replies.single()))
        assertEquals(SyncSession.End.NOT_PAIRED, run.end)
        assertEquals("not_paired", err(phone.run(Frames.op("hello", "v" to 2, "mac_name" to "X"), fp = stranger).replies.single()))
        assertEquals(SyncSession.End.BYE, phone.run(Frames.op("bye"), fp = stranger).end)
    }

    private val macNonce = ByteArray(16) { (0x40 + it).toByte() }

    @Test
    fun pairingOverTheConnection() {
        val stranger = ByteArray(32) { 9 }
        phone.pin = null
        assertEquals("not_pairing", err(phone.run(Frames.op("pair_request", "mac_name" to "Test Mac", "nonce" to Base64.getEncoder().encodeToString(macNonce)), fp = stranger).replies.single()))

        phone.pairing.open()
        // The owner confirms as soon as the code shows; the Mac's pair_confirm then succeeds.
        val t = Thread {
            while (phone.pairing.state() !is PairingFlow.State.Code) Thread.sleep(5)
            phone.pairing.ownerConfirm()
        }.apply { start() }
        val run = phone.run(
            Frames.op("pair_request", "mac_name" to "Test Mac", "nonce" to Base64.getEncoder().withoutPadding().encodeToString(macNonce)),
            Frames.op("pair_confirm"),
            Frames.op("hello", "v" to 2, "mac_name" to "Test Mac"),
            fp = stranger,
        )
        t.join()
        val req = run.replies[0].json
        assertEquals("Test Phone", req.getString("device_name"))
        val phoneNonce = Base64.getDecoder().decode(req.getString("nonce"))
        assertEquals(16, phoneNonce.size)
        val code = (phone.pairing.state() as PairingFlow.State.Paired).let { PairCode.derive(stranger, phone.info.certSha256, macNonce, phoneNonce) }
        assertEquals(6, code.length)
        assertTrue(run.replies[1].json.getBoolean("ok"))
        assertArrayEquals(stranger, phone.pin)
        assertEquals("Test Mac", phone.pairedWith.single().second)
        // Pinned now: the same connection may sync.
        assertTrue(run.replies[2].json.getBoolean("ok"))
        assertTrue(run.session.summary.pairedNow)
    }

    @Test
    fun pairingShowsTheCodeBothSidesCompute() {
        val stranger = ByteArray(32) { 9 }
        phone.pairing.open()
        var shown: String? = null
        val t = Thread {
            while (phone.pairing.state() !is PairingFlow.State.Code) Thread.sleep(5)
            shown = (phone.pairing.state() as PairingFlow.State.Code).code
            phone.pairing.ownerCancel()
        }.apply { start() }
        val run = phone.run(
            Frames.op("pair_request", "mac_name" to "Test Mac", "nonce" to Base64.getEncoder().encodeToString(macNonce)),
            Frames.op("pair_confirm"),
            fp = stranger,
        )
        t.join()
        val phoneNonce = Base64.getDecoder().decode(run.replies[0].json.getString("nonce"))
        assertEquals(PairCode.derive(stranger, phone.info.certSha256, macNonce, phoneNonce), shown)
        assertEquals("cancelled", err(run.replies[1]))
        assertTrue(phone.pairedWith.isEmpty())
    }

    @Test
    fun macDenyAndBadPairRequests() {
        val stranger = ByteArray(32) { 9 }
        phone.pairing.open()
        val run = phone.run(
            Frames.op("pair_request", "mac_name" to "Test Mac", "nonce" to Base64.getEncoder().encodeToString(macNonce)),
            Frames.op("pair_cancel"),
            fp = stranger,
        )
        assertTrue(run.replies[1].json.getBoolean("ok"))
        assertEquals(PairingFlow.State.Failed(PairingFlow.Failure.DENIED), phone.pairing.state())

        phone.pairing.open()
        for (nonce in listOf("short", Base64.getEncoder().encodeToString(ByteArray(15)), Base64.getEncoder().encodeToString(ByteArray(17)), "!!!!")) {
            assertTrue(nonce, phone.run(Frames.op("pair_request", "mac_name" to "Test Mac", "nonce" to nonce), fp = stranger).violation)
        }
        assertTrue(phone.run(Frames.op("pair_request", "mac_name" to "x".repeat(101), "nonce" to Base64.getEncoder().encodeToString(macNonce)), fp = stranger).violation)
    }

    @Test
    fun liveTextIsNeverListedOrServed() {
        val r = TestUtil.finishedRecording(phone.store, 2)
        LiveTextFile(File(r.dir, LiveTextFile.NAME)).append(0, "invented sentence")
        val list = phone.run(Frames.op("list")).replies.single().json.toString()
        assertFalse(list.contains("live"))
        assertFalse(list.contains("invented"))
    }
}

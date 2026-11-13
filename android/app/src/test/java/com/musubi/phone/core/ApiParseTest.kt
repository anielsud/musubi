package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ApiParseTest {
    @Test
    fun badEventsAreSkippedAndBriefsKeepTheirLabel() {
        val body = JSONObject().put("drafting", 1).put("events", JSONArray()
            .put(JSONObject().put("id", "e1").put("start", "2026-11-18T09:00:00+01:00").put("end", "2026-11-18T09:30:00+01:00")
                .put("subject", "Breakfast with Example Corp")
                .put("brief", JSONObject().put("text", "Talk about the pilot.").put("made_at", "2026-11-16T20:00:00+01:00")
                    .put("sensitivity", JSONObject().put("level", "internal").put("share", "internal").put("why", JSONObject.NULL))))
            .put(JSONObject().put("id", "e2").put("start", "not a time").put("end", "2026-11-18T09:30:00+01:00"))
            .put(JSONObject().put("id", "e3").put("start", "2026-11-18T10:00:00+01:00").put("end", "2026-11-18T09:00:00+01:00"))
            .put(JSONObject().put("id", "e4").put("start", "2026-11-18T11:00:00+01:00").put("end", "2026-11-18T12:00:00+01:00")
                .put("brief", JSONObject().put("text", "Unlabelled"))))
        val a = ApiParse.agenda(body.toString().toByteArray(), null)
        assertEquals(listOf("e1", "e4"), a.events.map { it.id })
        assertEquals("internal", a.events[0].brief!!.sensitivity.share)
        assertEquals("unlabelled", a.events[1].brief!!.sensitivity.level)
        assertNull(a.events[1].brief!!.madeAt)
    }

    @Test
    fun storedAgendaRoundTrips() {
        val body = JSONObject().put("drafting", 0).put("events", JSONArray()
            .put(JSONObject().put("id", "e1").put("start", "2026-11-18T09:00:00+01:00").put("end", "2026-11-18T09:30:00+01:00")
                .put("attendees", JSONArray().put(JSONObject().put("name", "Jamie Placeholder")))
                .put("brief", JSONObject().put("text", "x").put("sensitivity", JSONObject().put("level", "l").put("share", "party-only").put("why", "pay")))))
        val a = ApiParse.agenda(body.toString().toByteArray(), TestUtil.T0)
        assertEquals(a, ApiParse.agendaFromStored(ApiParse.agendaToJson(a)))
    }

    @Test
    fun recordingStateMustBeKnown() {
        assertEquals("waiting", ApiParse.recordingState("""{"state":"waiting"}""".toByteArray()).state)
        val r = runCatching { ApiParse.recordingState("""{"state":"deleted"}""".toByteArray()) }
        assert(r.isFailure)
    }

    @Test
    fun pairReplyIsValidated() {
        val ok = ApiParse.pair("""{"device_id":"dev-1","token":"AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_"}""".toByteArray())
        assertEquals("dev-1", ok.deviceId)
        assert(runCatching { ApiParse.pair("""{"device_id":"dev-1","token":"short"}""".toByteArray()) }.isFailure)
        assert(runCatching { ApiParse.pair("""{"device_id":"dev 1\n","token":"AbCdEfGhIjKlMnOpQrSt"}""".toByteArray()) }.isFailure)
    }

    @Test
    fun mergePrefersSyncedEvents() {
        fun ev(id: String, subject: String, min: Long, phone: Boolean) = AgendaEvent(id, TestUtil.T0.plusMinutes(min), TestUtil.T0.plusMinutes(min + 30),
            subject, "", "", emptyList(), null, phone)
        val merged = AgendaStore.merge(
            listOf(ev("m1", "Booth demo", 60, false)),
            listOf(ev("p1", "booth demo", 60, true), ev("p2", "Dinner", 300, true)),
            TestUtil.T0,
        )
        assertEquals(listOf("m1", "p2"), merged.map { it.id })
    }
}

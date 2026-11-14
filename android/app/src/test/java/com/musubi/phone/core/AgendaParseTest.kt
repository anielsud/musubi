package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class AgendaParseTest {
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
        val a = AgendaParse.agenda(body.toString().toByteArray(), null)
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
        val a = AgendaParse.agenda(body.toString().toByteArray(), TestUtil.T0)
        assertEquals(a, AgendaParse.agendaFromStored(AgendaParse.agendaToJson(a).toByteArray()))
    }

    @Test
    fun mergePrefersSyncedEvents() {
        fun ev(id: String, subject: String, min: Long, phone: Boolean) = AgendaEvent(id, TestUtil.T0.plusMinutes(min), TestUtil.T0.plusMinutes(min + 30),
            subject, "", "", emptyList(), null, fromPhone = phone)
        val merged = AgendaStore.merge(
            listOf(ev("m1", "Booth demo", 60, false)),
            listOf(ev("p1", "booth demo", 60, true), ev("p2", "Dinner", 300, true)),
            TestUtil.T0,
        )
        assertEquals(listOf("m1", "p2"), merged.map { it.id })
    }

    @Test
    fun sourcesAndOpenItemsAreParsedCappedAndRoundTrip() {
        fun src(n: Int) = JSONObject().put("n", n).put("title", "Planning call").put("kind", "meeting").put("occurred_at", "2026-11-10T15:00:00Z")
            .put("speakers", "Robin Vale").put("text", "x".repeat(5000))
            .put("sensitivity", JSONObject().put("level", "internal").put("share", "internal").put("why", ""))
        val srcs = JSONArray()
        for (n in listOf(1, 1, 0, 2000) + (2..40)) srcs.put(src(n)) // a repeat, out-of-range ns, then 39 more
        srcs.put(JSONObject().put("n", "3"))
        val open = JSONArray()
        open.put(JSONObject().put("text", "Send the plan").put("who", "Robin Vale").put("due", "2026-11-20").put("thread", "Rollout")
            .put("raised", "2026-11-12T09:00:00Z").put("overdue", true).put("quiet", false).put("private", true)
            .put("sources", JSONArray().put(src(1)).put(src(2)).put(src(3)).put(src(4))))
        open.put(JSONObject().put("text", "")) // no text: skipped
        for (i in 0 until 20) open.put(JSONObject().put("text", "More $i").put("raised", JSONObject.NULL))
        val body = JSONObject().put("drafting", 0).put("events", JSONArray()
            .put(JSONObject().put("id", "e1").put("start", "2026-11-18T09:00:00+01:00").put("end", "2026-11-18T09:30:00+01:00")
                .put("brief", JSONObject().put("text", "Ask [1]").put("sensitivity", JSONObject().put("level", "internal").put("share", "internal"))
                    .put("sources", srcs))
                .put("open", open)))
        val a = AgendaParse.agenda(body.toString().toByteArray(), TestUtil.T0)
        val e = a.events.single()
        val ns = e.brief!!.sources.map { it.n }
        assertEquals(AgendaParse.MAX_BRIEF_SOURCES, ns.size)
        assertEquals(listOf(1) + (2..30), ns)
        assertEquals(AgendaParse.MAX_PASSAGE, e.brief!!.sources[0].text.length)
        assertEquals(AgendaParse.MAX_OPEN, e.open.size)
        val o = e.open[0]
        assertEquals(listOf("Send the plan", "Robin Vale", "2026-11-20", "Rollout"), listOf(o.text, o.who, o.due, o.thread))
        assertEquals(true, o.overdue && o.private && !o.quiet)
        assertEquals(AgendaParse.MAX_OPEN_SOURCES, o.sources.size)
        assertNull(e.open[1].raised)
        assertEquals(a, AgendaParse.agendaFromStored(AgendaParse.agendaToJson(a).toByteArray()))
    }

    @Test
    fun olderAgendasWithoutSourcesOrOpenStillParse() {
        val body = JSONObject().put("drafting", 0).put("events", JSONArray()
            .put(JSONObject().put("id", "e1").put("start", "2026-11-18T09:00:00+01:00").put("end", "2026-11-18T09:30:00+01:00")
                .put("brief", JSONObject().put("text", "Ask [1]").put("sensitivity", JSONObject().put("level", "internal").put("share", "internal")))))
        val e = AgendaParse.agenda(body.toString().toByteArray(), null).events.single()
        assertEquals(emptyList<Source>(), e.brief!!.sources)
        assertEquals(emptyList<OpenItem>(), e.open)
    }
}

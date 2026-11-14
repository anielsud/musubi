package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import java.time.OffsetDateTime

data class Sensitivity(val level: String, val share: String, val why: String)
data class Brief(val text: String, val madeAt: OffsetDateTime?, val sensitivity: Sensitivity)

data class AgendaEvent(
    val id: String,
    val start: OffsetDateTime,
    val end: OffsetDateTime,
    val subject: String,
    val location: String,
    val organizer: String,
    val attendees: List<String>,
    val brief: Brief?,
    /** True for events read from the phone's own calendar (never synced from the Mac). */
    val fromPhone: Boolean = false,
)

data class Agenda(val events: List<AgendaEvent>, val drafting: Int, val fetchedAt: OffsetDateTime?)

/**
 * Parsing of the agenda the Mac pushes (docs/phone.md, "Agenda JSON"). Untrusted input: strict
 * JSON, types checked, strings and lists capped, malformed events skipped.
 */
object AgendaParse {
    const val MAX_EVENTS = 1000
    const val MAX_ATTENDEES = 200
    const val MAX_SHORT = 500
    const val MAX_BRIEF = 200_000

    /** Throws when the body is not a JSON object with an `events` array. */
    fun agenda(body: ByteArray, fetchedAt: OffsetDateTime?): Agenda {
        val o = StrictJson.parseObject(body)
        val arr = o["events"] as? List<*> ?: throw IllegalArgumentException("no events")
        val events = ArrayList<AgendaEvent>()
        for (e in arr.take(MAX_EVENTS)) {
            val m = e.asObject() ?: continue
            runCatching { events += event(m) }
        }
        val drafting = ((o["drafting"] as? Long) ?: 0L).coerceIn(0, 10_000).toInt()
        return Agenda(events, drafting, fetchedAt)
    }

    private fun event(e: Map<String, Any?>): AgendaEvent {
        val start = RecordingMeta.parseTime(e["start"] as String)
        val end = RecordingMeta.parseTime(e["end"] as String)
        require(!end.isBefore(start)) { "end before start" }
        val att = e["attendees"] as? List<*> ?: emptyList<Any?>()
        val names = att.take(MAX_ATTENDEES).mapNotNull { a -> (a.asObject()?.get("name") as? String)?.takeIf { it.isNotEmpty() }?.take(MAX_SHORT) }
        val brief = e["brief"].asObject()?.let { b ->
            val s = b["sensitivity"].asObject()
            Brief(
                text = (b["text"] as String).take(MAX_BRIEF),
                madeAt = (b["made_at"] as? String)?.let { runCatching { RecordingMeta.parseTime(it) }.getOrNull() },
                // A brief with no label is shown as unlabelled, never as public.
                sensitivity = Sensitivity(
                    level = s.str("level")?.take(64) ?: "unlabelled",
                    share = s.str("share")?.take(64) ?: "unknown",
                    why = s.str("why")?.take(MAX_SHORT) ?: "",
                ),
            )
        }
        return AgendaEvent(
            id = (e["id"] as String).take(MAX_SHORT).also { require(it.isNotEmpty()) },
            start = start,
            end = end,
            subject = (e["subject"] as? String ?: "").take(MAX_SHORT),
            location = (e["location"] as? String ?: "").take(MAX_SHORT),
            organizer = (e["organizer"] as? String ?: "").take(MAX_SHORT),
            attendees = names,
            brief = brief,
        )
    }

    @Suppress("UNCHECKED_CAST")
    private fun Any?.asObject(): Map<String, Any?>? = this as? Map<String, Any?>

    private fun Map<String, Any?>?.str(k: String): String? = (this?.get(k) as? String)?.takeIf { it.isNotEmpty() }

    /** The phone's own copy of the agenda (agenda.json); same shape as the Mac's, plus fetched_at. */
    fun agendaToJson(a: Agenda): String {
        val arr = JSONArray()
        for (e in a.events) {
            val o = JSONObject()
                .put("id", e.id)
                .put("start", RecordingMeta.formatTime(e.start))
                .put("end", RecordingMeta.formatTime(e.end))
                .put("subject", e.subject)
                .put("location", e.location)
                .put("organizer", e.organizer)
                .put("attendees", JSONArray(e.attendees.map { JSONObject().put("name", it) }))
            o.put("brief", e.brief?.let { b ->
                JSONObject()
                    .put("text", b.text)
                    .put("made_at", b.madeAt?.let { RecordingMeta.formatTime(it) } ?: JSONObject.NULL)
                    .put("sensitivity", JSONObject().put("level", b.sensitivity.level).put("share", b.sensitivity.share).put("why", b.sensitivity.why))
            } ?: JSONObject.NULL)
            arr.put(o)
        }
        return JSONObject()
            .put("events", arr)
            .put("drafting", a.drafting)
            .put("fetched_at", a.fetchedAt?.let { RecordingMeta.formatTime(it) } ?: JSONObject.NULL)
            .toString()
    }

    fun agendaFromStored(bytes: ByteArray): Agenda {
        val o = StrictJson.parseObject(bytes)
        val fetched = (o["fetched_at"] as? String)?.let { runCatching { RecordingMeta.parseTime(it) }.getOrNull() }
        return agenda(bytes, fetched)
    }
}

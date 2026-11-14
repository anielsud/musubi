package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import java.time.OffsetDateTime

data class Sensitivity(val level: String, val share: String, val why: String)
data class Brief(
    val text: String,
    val madeAt: OffsetDateTime?,
    val sensitivity: Sensitivity,
    /** What the text cites as [n], by n; empty in agendas from before sources were sent. */
    val sources: List<Source> = emptyList(),
)

/** A passage behind a brief or an open item, with its own label. */
data class Source(
    val n: Int,
    val title: String,
    val kind: String,
    val occurredAt: OffsetDateTime?,
    val speakers: String,
    val text: String,
    val sensitivity: Sensitivity,
)

/** An item open between the owner and the event's people, as the desktop's prep shows it. */
data class OpenItem(
    val text: String,
    val who: String,
    val due: String,
    val thread: String,
    val raised: OffsetDateTime?,
    val quiet: Boolean,
    val overdue: Boolean,
    val private: Boolean,
    val sources: List<Source>,
)

data class AgendaEvent(
    val id: String,
    val start: OffsetDateTime,
    val end: OffsetDateTime,
    val subject: String,
    val location: String,
    val organizer: String,
    val attendees: List<String>,
    val brief: Brief?,
    /** Open between us; empty in older agendas. */
    val open: List<OpenItem> = emptyList(),
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
    const val MAX_BRIEF_SOURCES = 30
    const val MAX_OPEN = 15
    const val MAX_OPEN_SOURCES = 3
    const val MAX_PASSAGE = 1200
    const val MAX_OPEN_TEXT = 2000
    const val MAX_SOURCE_N = 999

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
            Brief(
                text = (b["text"] as String).take(MAX_BRIEF),
                madeAt = time(b["made_at"]),
                sensitivity = sensitivity(b["sensitivity"].asObject()),
                sources = sources(b["sources"], MAX_BRIEF_SOURCES),
            )
        }
        val open = (e["open"] as? List<*> ?: emptyList<Any?>()).take(MAX_OPEN * 4).mapNotNull { x ->
            val o = x.asObject() ?: return@mapNotNull null
            val text = (o["text"] as? String)?.takeIf { it.isNotBlank() }?.take(MAX_OPEN_TEXT) ?: return@mapNotNull null
            OpenItem(
                text = text,
                who = o.str("who")?.take(MAX_SHORT) ?: "",
                due = o.str("due")?.take(64) ?: "",
                thread = o.str("thread")?.take(MAX_SHORT) ?: "",
                raised = time(o["raised"]),
                quiet = o["quiet"] == true,
                overdue = o["overdue"] == true,
                private = o["private"] == true,
                sources = sources(o["sources"], MAX_OPEN_SOURCES),
            )
        }.take(MAX_OPEN)
        return AgendaEvent(
            id = (e["id"] as String).take(MAX_SHORT).also { require(it.isNotEmpty()) },
            start = start,
            end = end,
            subject = (e["subject"] as? String ?: "").take(MAX_SHORT),
            location = (e["location"] as? String ?: "").take(MAX_SHORT),
            organizer = (e["organizer"] as? String ?: "").take(MAX_SHORT),
            attendees = names,
            brief = brief,
            open = open,
        )
    }

    private fun time(v: Any?): OffsetDateTime? = (v as? String)?.let { runCatching { RecordingMeta.parseTime(it) }.getOrNull() }

    // A label that is missing is shown as unlabelled, never as public.
    private fun sensitivity(s: Map<String, Any?>?) = Sensitivity(
        level = s.str("level")?.take(64) ?: "unlabelled",
        share = s.str("share")?.take(64) ?: "unknown",
        why = s.str("why")?.take(MAX_SHORT) ?: "",
    )

    /** At most [max] sources with a distinct n in 1..MAX_SOURCE_N; malformed ones are skipped. */
    private fun sources(v: Any?, max: Int): List<Source> {
        val out = ArrayList<Source>()
        val seen = HashSet<Int>()
        for (x in (v as? List<*> ?: return emptyList()).take(max * 4)) {
            if (out.size == max) break
            val o = x.asObject() ?: continue
            val n = (o["n"] as? Long)?.takeIf { it in 1..MAX_SOURCE_N.toLong() }?.toInt() ?: continue
            if (!seen.add(n)) continue
            out += Source(
                n = n,
                title = o.str("title")?.take(MAX_SHORT) ?: "",
                kind = o.str("kind")?.take(64) ?: "",
                occurredAt = time(o["occurred_at"]),
                speakers = o.str("speakers")?.take(MAX_SHORT) ?: "",
                text = (o["text"] as? String ?: "").take(MAX_PASSAGE),
                sensitivity = sensitivity(o["sensitivity"].asObject()),
            )
        }
        return out
    }

    private fun sensJson(s: Sensitivity) = JSONObject().put("level", s.level).put("share", s.share).put("why", s.why)

    private fun sourcesJson(l: List<Source>) = JSONArray(l.map { s ->
        JSONObject().put("n", s.n).put("title", s.title).put("kind", s.kind)
            .put("occurred_at", s.occurredAt?.let { RecordingMeta.formatTime(it) } ?: JSONObject.NULL)
            .put("speakers", s.speakers).put("text", s.text).put("sensitivity", sensJson(s.sensitivity))
    })

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
                    .put("sensitivity", sensJson(b.sensitivity))
                    .put("sources", sourcesJson(b.sources))
            } ?: JSONObject.NULL)
            o.put("open", JSONArray(e.open.map { x ->
                JSONObject().put("text", x.text).put("who", x.who).put("due", x.due).put("thread", x.thread)
                    .put("raised", x.raised?.let { RecordingMeta.formatTime(it) } ?: JSONObject.NULL)
                    .put("quiet", x.quiet).put("overdue", x.overdue).put("private", x.private)
                    .put("sources", sourcesJson(x.sources))
            }))
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

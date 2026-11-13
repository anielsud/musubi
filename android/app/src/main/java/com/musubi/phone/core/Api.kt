package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.time.OffsetDateTime

/** Outcome of one HTTP call to the Mac. Bodies are already size-bounded by the client. */
sealed class ApiResult {
    data class Ok(val code: Int, val body: ByteArray) : ApiResult()
    data class Http(val code: Int, val body: ByteArray) : ApiResult()
    /** Mac not reachable, TLS pin mismatch, timeout. Retried on the next run. */
    data class Network(val message: String, val certificateMismatch: Boolean = false) : ApiResult()
}

/** The Mac's API (docs/phone.md, part 2). Implemented over pinned TLS by HttpMacApi. */
interface MacApi {
    fun putChunk(rid: String, n: Int, file: File, sha256: String): ApiResult
    fun complete(rid: String, metaJson: ByteArray): ApiResult
    fun recordingState(rid: String): ApiResult
    fun agenda(days: Int): ApiResult
}

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

data class ServerRecordingState(val state: String, val sessionId: String?, val error: String?)

data class PairReply(val deviceId: String, val token: String)

/**
 * Parsing of everything the Mac sends. Responses are treated as untrusted: types are checked,
 * strings and lists are capped, unknown states are rejected, malformed events are skipped.
 */
object ApiParse {
    val SERVER_STATES = setOf("received", "waiting", "transcribing", "imported", "failed")
    const val MAX_EVENTS = 1000
    const val MAX_ATTENDEES = 200
    const val MAX_SHORT = 500
    const val MAX_BRIEF = 200_000
    private val TOKEN_RE = Regex("^[A-Za-z0-9._~+/=-]{16,512}$")
    private val DEVICE_ID_RE = Regex("^[A-Za-z0-9._:-]{1,128}$")

    fun pair(body: ByteArray): PairReply {
        val o = JSONObject(String(body, Charsets.UTF_8))
        val id = o.getString("device_id")
        val token = o.getString("token")
        require(DEVICE_ID_RE.matches(id)) { "bad device id" }
        require(TOKEN_RE.matches(token)) { "bad token" }
        return PairReply(id, token)
    }

    fun recordingState(body: ByteArray): ServerRecordingState {
        val o = JSONObject(String(body, Charsets.UTF_8))
        val state = o.getString("state")
        require(state in SERVER_STATES) { "unknown state" }
        return ServerRecordingState(
            state,
            o.optStringOrNull("session_id")?.take(MAX_SHORT),
            o.optStringOrNull("error")?.take(MAX_SHORT),
        )
    }

    fun agenda(body: ByteArray, fetchedAt: OffsetDateTime?): Agenda {
        val o = JSONObject(String(body, Charsets.UTF_8))
        val arr = o.getJSONArray("events")
        val events = ArrayList<AgendaEvent>()
        for (i in 0 until minOf(arr.length(), MAX_EVENTS)) {
            val e = arr.optJSONObject(i) ?: continue
            runCatching { events += event(e) }
        }
        val drafting = o.optInt("drafting", 0).coerceIn(0, 10_000)
        return Agenda(events, drafting, fetchedAt)
    }

    private fun event(e: JSONObject): AgendaEvent {
        val start = RecordingMeta.parseTime(e.getString("start"))
        val end = RecordingMeta.parseTime(e.getString("end"))
        require(!end.isBefore(start)) { "end before start" }
        val att = e.optJSONArray("attendees") ?: JSONArray()
        val names = (0 until minOf(att.length(), MAX_ATTENDEES)).mapNotNull { i ->
            att.optJSONObject(i)?.optStringOrNull("name")?.take(MAX_SHORT)
        }
        val brief = if (!e.has("brief") || e.isNull("brief")) null else {
            val b = e.getJSONObject("brief")
            val s = b.optJSONObject("sensitivity")
            Brief(
                text = b.getString("text").take(MAX_BRIEF),
                madeAt = b.optStringOrNull("made_at")?.let { runCatching { RecordingMeta.parseTime(it) }.getOrNull() },
                // A brief with no label is shown as unlabelled, never as public.
                sensitivity = Sensitivity(
                    level = s?.optStringOrNull("level")?.take(64) ?: "unlabelled",
                    share = s?.optStringOrNull("share")?.take(64) ?: "unknown",
                    why = s?.optStringOrNull("why")?.take(MAX_SHORT) ?: "",
                ),
            )
        }
        return AgendaEvent(
            id = e.getString("id").take(MAX_SHORT).also { require(it.isNotEmpty()) },
            start = start,
            end = end,
            subject = e.optString("subject", "").take(MAX_SHORT),
            location = e.optString("location", "").take(MAX_SHORT),
            organizer = e.optString("organizer", "").take(MAX_SHORT),
            attendees = names,
            brief = brief,
        )
    }

    /** The phone's own copy of the agenda (agenda.json); same shape as the Mac's reply. */
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

    fun agendaFromStored(text: String): Agenda {
        val o = JSONObject(text)
        val fetched = o.optStringOrNull("fetched_at")?.let { runCatching { RecordingMeta.parseTime(it) }.getOrNull() }
        return agenda(text.toByteArray(), fetched)
    }
}

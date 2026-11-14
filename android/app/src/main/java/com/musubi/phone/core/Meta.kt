package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import java.time.OffsetDateTime
import java.time.format.DateTimeFormatter
import java.time.temporal.ChronoUnit
import java.util.UUID

/** The event a recording belongs to, as the phone knew it. */
data class EventRef(val id: String, val subject: String, val start: OffsetDateTime)

data class ChunkInfo(val n: Int, val size: Long, val sha256: String) {
    val name: String get() = chunkName(n)
}

/**
 * Contents of a recording's meta.json. This file is sent as-is to the Mac on `complete`,
 * so it holds only what the Mac needs; phone-side sync progress lives in sync.json.
 */
data class RecordingMeta(
    val id: String,
    val start: OffsetDateTime,
    val end: OffsetDateTime?,
    val deviceName: String,
    val event: EventRef?,
    val chunks: List<ChunkInfo>,
) {
    val isRecording: Boolean get() = end == null

    fun toJson(): String {
        val o = JSONObject()
        o.put("version", VERSION)
        o.put("id", id)
        o.put("start", formatTime(start))
        o.put("end", end?.let { formatTime(it) } ?: JSONObject.NULL)
        o.put("device_name", deviceName)
        o.put("event", event?.let {
            JSONObject().put("id", it.id).put("subject", it.subject).put("start", formatTime(it.start))
        } ?: JSONObject.NULL)
        val arr = JSONArray()
        for (c in chunks) {
            arr.put(JSONObject().put("n", c.n).put("name", c.name).put("size", c.size).put("sha256", c.sha256))
        }
        o.put("chunks", arr)
        return o.toString(2)
    }

    companion object {
        const val VERSION = 1
        const val MAX_CHUNKS = 10_000
        private const val MAX_TEXT = 1000

        fun fromJson(text: String): RecordingMeta {
            val o = JSONObject(text)
            val id = o.getString("id")
            require(isValidId(id)) { "bad recording id" }
            val start = parseTime(o.getString("start"))
            val end = if (o.isNull("end")) null else parseTime(o.getString("end"))
            val deviceName = o.optString("device_name", "").take(MAX_TEXT)
            val event = if (!o.has("event") || o.isNull("event")) null else {
                val e = o.getJSONObject("event")
                EventRef(e.getString("id").take(MAX_TEXT), e.optString("subject", "").take(MAX_TEXT), parseTime(e.getString("start")))
            }
            val arr = o.optJSONArray("chunks") ?: JSONArray()
            require(arr.length() <= MAX_CHUNKS) { "too many chunks" }
            val chunks = (0 until arr.length()).map { i ->
                val c = arr.getJSONObject(i)
                val n = c.getInt("n")
                require(n in 0 until MAX_CHUNKS) { "bad chunk number" }
                val size = c.getLong("size")
                require(size >= 0) { "bad chunk size" }
                val sha = c.getString("sha256")
                require(Hashing.isSha256Hex(sha)) { "bad chunk hash" }
                ChunkInfo(n, size, sha)
            }
            return RecordingMeta(id, start, end, deviceName, event, chunks)
        }

        fun newId(): String = UUID.randomUUID().toString()

        fun isValidId(id: String): Boolean =
            id.length == 36 && runCatching { UUID.fromString(id).toString() == id }.getOrDefault(false)

        /** ISO-8601 with zone offset, millisecond precision (e.g. 2026-11-17T10:02:03.120+01:00). */
        fun formatTime(t: OffsetDateTime): String =
            t.truncatedTo(ChronoUnit.MILLIS).format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)

        fun parseTime(s: String): OffsetDateTime = OffsetDateTime.parse(s, DateTimeFormatter.ISO_OFFSET_DATE_TIME)
    }
}

fun chunkName(n: Int): String = "chunk-%04d.aac".format(n)

private val CHUNK_RE = Regex("^chunk-(\\d{4})\\.aac$")

fun chunkNumber(fileName: String): Int? = CHUNK_RE.matchEntire(fileName)?.groupValues?.get(1)?.toInt()

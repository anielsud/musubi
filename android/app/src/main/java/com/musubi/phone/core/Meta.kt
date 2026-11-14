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

/** A person the owner listed as present (picked from the event or typed). */
data class Attendee(val name: String, val email: String? = null)

/**
 * The notes as meta.json lists them: their text is notes.txt in the recording's folder (UTF-8),
 * fetched with `file` like a photo, so a long note never makes meta.json (and `list`) large.
 */
data class NotesInfo(val size: Long = 0, val sha256: String = EMPTY_SHA256, val updated: OffsetDateTime? = null) {
    val isEmpty: Boolean get() = size == 0L

    companion object {
        /** SHA-256 of no bytes: the empty notes'. */
        const val EMPTY_SHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    }
}

/** A photo in the recording's folder, as listed in meta.json. */
data class PhotoInfo(val name: String, val size: Long, val sha256: String, val taken: OffsetDateTime)

/**
 * What the owner adds to a recording (docs/phone.md, "Recording details"): notes, people, photos.
 * [rev] goes up with every edit; the Mac reports the rev it applied (`details_rev`).
 */
data class Details(
    val rev: Long = 0,
    val attendees: List<Attendee> = emptyList(),
    val notes: NotesInfo = NotesInfo(),
    val photos: List<PhotoInfo> = emptyList(),
) {
    fun toJson(): JSONObject {
        val att = JSONArray()
        for (a in attendees) att.put(JSONObject().put("name", a.name).also { o -> a.email?.let { o.put("email", it) } })
        val ph = JSONArray()
        for (p in photos) ph.put(JSONObject().put("name", p.name).put("size", p.size).put("sha256", p.sha256).put("taken", RecordingMeta.formatTime(p.taken)))
        return JSONObject()
            .put("rev", rev)
            .put("attendees", att)
            .put("notes", JSONObject().put("size", notes.size).put("sha256", notes.sha256)
                .put("updated", notes.updated?.let { RecordingMeta.formatTime(it) } ?: JSONObject.NULL))
            .put("photos", ph)
    }

    companion object {
        const val MAX_NOTES = 20_000
        /** notes.txt: at most [MAX_NOTES] characters, at most 4 bytes each in UTF-8. */
        const val NOTES_FILE = "notes.txt"
        const val MAX_NOTES_BYTES = MAX_NOTES * 4L
        const val MAX_PEOPLE = 50
        const val MAX_NAME = 100
        const val MAX_EMAIL = 320
        const val MAX_PHOTOS = 30
        const val MAX_PHOTO_BYTES = 15L * 1024 * 1024
        private val PHOTO_RE = Regex("^photo-[0-9]{4}\\.jpg$")

        /** The only shape a photo's file name has: it can never name a path outside the recording's folder. */
        fun isPhotoName(name: String): Boolean = PHOTO_RE.matches(name)

        /** The names `file` serves: the notes and photos. */
        fun isFileName(name: String): Boolean = name == NOTES_FILE || isPhotoName(name)

        fun photoName(n: Int): String = "photo-%04d.jpg".format(n)

        /** A person's name as kept: trimmed, 1..[MAX_NAME] characters, no control characters; null otherwise. */
        fun cleanName(s: String): String? {
            val t = s.trim().replace(Regex("\\s+"), " ")
            return t.takeIf { it.isNotEmpty() && it.length <= MAX_NAME && it.none { c -> c.isISOControl() } }
        }

        fun fromJson(o: JSONObject): Details {
            val rev = o.optLong("rev", 0)
            require(rev >= 0) { "bad rev" }
            val att = o.optJSONArray("attendees") ?: JSONArray()
            require(att.length() <= MAX_PEOPLE) { "too many people" }
            val people = (0 until att.length()).map { i ->
                val a = att.getJSONObject(i)
                val name = cleanName(a.getString("name")) ?: throw IllegalArgumentException("bad name")
                Attendee(name, a.optStringOrNull("email")?.take(MAX_EMAIL))
            }
            val n = o.optJSONObject("notes")
            val notes = if (n == null) NotesInfo() else {
                val size = n.optLong("size", 0)
                require(size in 0..MAX_NOTES_BYTES) { "bad notes size" }
                val sha = n.optString("sha256", NotesInfo.EMPTY_SHA256)
                require(Hashing.isSha256Hex(sha)) { "bad notes hash" }
                NotesInfo(size, sha, n.optStringOrNull("updated")?.let { RecordingMeta.parseTime(it) })
            }
            val ph = o.optJSONArray("photos") ?: JSONArray()
            require(ph.length() <= MAX_PHOTOS) { "too many photos" }
            val photos = (0 until ph.length()).map { i ->
                val p = ph.getJSONObject(i)
                val name = p.getString("name")
                require(isPhotoName(name)) { "bad photo name" }
                val size = p.getLong("size")
                require(size in 1..MAX_PHOTO_BYTES) { "bad photo size" }
                val sha = p.getString("sha256")
                require(Hashing.isSha256Hex(sha)) { "bad photo hash" }
                PhotoInfo(name, size, sha, RecordingMeta.parseTime(p.getString("taken")))
            }
            require(photos.map { it.name }.toSet().size == photos.size) { "photo listed twice" }
            return Details(rev, people, notes, photos)
        }
    }
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
    val details: Details = Details(),
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
        o.put("details", details.toJson())
        return o.toString(2)
    }

    companion object {
        /** 2 adds `details` (a version 1 meta reads as having none). */
        const val VERSION = 2
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
            val details = o.optJSONObject("details")?.let { Details.fromJson(it) } ?: Details()
            return RecordingMeta(id, start, end, deviceName, event, chunks, details)
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

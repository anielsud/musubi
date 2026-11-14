package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import java.time.OffsetDateTime

/**
 * Phone-side progress of one recording, kept in sync.json beside meta.json.
 * States match what the Recordings screen shows.
 */
enum class SyncState(val wire: String, val label: String) {
    ON_PHONE("on_phone", "On phone"),
    UPLOADING("uploading", "Uploading"),
    ON_MAC("on_mac", "On Mac"),
    IMPORTED("imported", "Transcribed"),
    FAILED("failed", "Failed");

    companion object {
        fun fromWire(s: String): SyncState = entries.firstOrNull { it.wire == s } ?: ON_PHONE
    }
}

data class SyncStatus(
    val state: SyncState = SyncState.ON_PHONE,
    /** Chunk numbers the Mac has acknowledged with 204. */
    val uploaded: Set<Int> = emptySet(),
    /** Last state the Mac reported: received, waiting, transcribing, imported, failed. */
    val serverState: String? = null,
    val sessionId: String? = null,
    val error: String? = null,
    /** When the Mac reported `imported`; meta.json is kept for 30 days after this. */
    val importedAt: OffsetDateTime? = null,
    /** The details rev the Mac reported it applied (`details_rev`). */
    val detailsRev: Long = 0,
) {
    fun toJson(): String {
        val o = JSONObject()
        o.put("state", state.wire)
        o.put("uploaded", JSONArray(uploaded.sorted()))
        o.put("server_state", serverState ?: JSONObject.NULL)
        o.put("session_id", sessionId ?: JSONObject.NULL)
        o.put("error", error ?: JSONObject.NULL)
        o.put("imported_at", importedAt?.let { RecordingMeta.formatTime(it) } ?: JSONObject.NULL)
        o.put("details_rev", detailsRev)
        return o.toString(2)
    }

    companion object {
        fun fromJson(text: String): SyncStatus {
            val o = JSONObject(text)
            val arr = o.optJSONArray("uploaded") ?: JSONArray()
            val up = (0 until arr.length()).map { arr.getInt(it) }.filter { it in 0 until RecordingMeta.MAX_CHUNKS }.toSet()
            return SyncStatus(
                state = SyncState.fromWire(o.optString("state", "on_phone")),
                uploaded = up,
                serverState = o.optStringOrNull("server_state"),
                sessionId = o.optStringOrNull("session_id"),
                error = o.optStringOrNull("error"),
                importedAt = o.optStringOrNull("imported_at")?.let { runCatching { RecordingMeta.parseTime(it) }.getOrNull() },
                detailsRev = o.optLong("details_rev", 0).coerceAtLeast(0),
            )
        }
    }
}

internal fun JSONObject.optStringOrNull(key: String): String? =
    if (!has(key) || isNull(key)) null else optString(key, "").takeIf { it.isNotEmpty() }

package com.musubi.phone.core

/** One of the phone's calendars, as the picker shows it. */
data class DeviceCal(val id: Long, val name: String, val account: String)

/**
 * Which of the phone's calendars Today merges in: none unless the owner switched it on (a calendar
 * permission alone never does) and chose them. Only the choice is stored; pure, for JVM tests.
 */
object CalendarChoice {
    const val MAX_CHOSEN = 200

    /** The calendars to read: chosen ones that still exist, only when shown and allowed. */
    fun toRead(show: Boolean, granted: Boolean, chosen: Set<Long>, available: List<DeviceCal>): List<Long> =
        if (!show || !granted) emptyList() else available.map { it.id }.filter { it in chosen }.distinct()

    /** A content-provider selection on [column] for [ids]; null when there is nothing to read. */
    fun selection(column: String, ids: List<Long>): Pair<String, Array<String>>? {
        if (ids.isEmpty()) return null
        return "$column IN (${ids.joinToString(",") { "?" }})" to ids.map { it.toString() }.toTypedArray()
    }

    /** Stored ids back to numbers; anything else is ignored. */
    fun parse(stored: Set<String>?): Set<Long> =
        (stored ?: emptySet()).asSequence().mapNotNull { it.toLongOrNull() }.filter { it >= 0 }.take(MAX_CHOSEN).toSet()

    fun label(c: DeviceCal): String = if (c.account.isBlank() || c.account == c.name) c.name.ifBlank { "(unnamed)" } else "${c.name.ifBlank { "(unnamed)" }} · ${c.account}"
}

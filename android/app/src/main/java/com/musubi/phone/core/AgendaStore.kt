package com.musubi.phone.core

import java.io.File
import java.time.Duration
import java.time.OffsetDateTime

/** The synced agenda (agenda.json in app-private storage), replaced each time the Mac pushes one. */
class AgendaStore(private val file: File) {
    @Synchronized
    fun load(): Agenda? {
        if (!file.isFile) return null
        return runCatching { AgendaParse.agendaFromStored(Files.readBounded(file, MAX_BYTES)) }.getOrNull()
    }

    /** Replaces the stored agenda, dropping events that ended more than a day before [now]. */
    @Synchronized
    fun replace(a: Agenda, now: OffsetDateTime) {
        val kept = a.copy(events = dropEnded(a.events, now))
        file.parentFile?.mkdirs()
        Files.writeAtomic(file, AgendaParse.agendaToJson(kept).toByteArray())
    }

    companion object {
        const val MAX_BYTES = 16 * 1024 * 1024

        fun dropEnded(events: List<AgendaEvent>, now: OffsetDateTime): List<AgendaEvent> {
            val cutoff = now.minus(Duration.ofDays(1))
            return events.filter { it.end.isAfter(cutoff) }
        }

        /**
         * Synced events first; a phone-calendar event is added unless a synced one has the same
         * subject (ignoring case) and starts within a minute of it.
         */
        fun merge(synced: List<AgendaEvent>, phone: List<AgendaEvent>, now: OffsetDateTime): List<AgendaEvent> {
            val extra = phone.filter { p ->
                synced.none { s ->
                    s.subject.trim().equals(p.subject.trim(), ignoreCase = true) &&
                        Duration.between(s.start, p.start).abs() <= Duration.ofMinutes(1)
                }
            }
            return dropEnded(synced + extra, now).sortedBy { it.start }
        }
    }
}

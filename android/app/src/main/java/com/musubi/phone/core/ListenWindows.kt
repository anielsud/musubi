package com.musubi.phone.core

/**
 * When the phone listens for its Mac (docs/phone.md, "Listening"): while the app is in the
 * foreground; up to 8 minutes after a recording stops; and up to 3 minutes in the periodic job.
 * Each kind is a window with an end time. They matter only once a Mac is paired; pairing has its
 * own window ([PairingFlow]).
 */
class ListenWindows(private val now: () -> Long) {
    enum class Kind { FOREGROUND, AFTER_RECORDING, PERIODIC }

    private val until = HashMap<Kind, Long>()

    @Synchronized
    fun open(kind: Kind, durationMs: Long = Long.MAX_VALUE) {
        val end = if (durationMs == Long.MAX_VALUE) Long.MAX_VALUE else now() + durationMs
        // Reopening never shortens a window that is already open for longer.
        until[kind] = maxOf(until[kind] ?: 0L, end)
    }

    @Synchronized
    fun close(kind: Kind) {
        until.remove(kind)
    }

    /** The paired Mac said `bye` after a successful sync with nothing pending: every window ends. */
    @Synchronized
    fun endEarly() {
        until.clear()
    }

    @Synchronized
    fun isOpen(kind: Kind): Boolean {
        val t = until[kind] ?: return false
        if (now() >= t) {
            until.remove(kind)
            return false
        }
        return true
    }

    /** True when the server should be listening for the paired Mac. */
    @Synchronized
    fun anyOpen(): Boolean = Kind.entries.any { isOpen(it) }

    companion object {
        const val AFTER_RECORDING_MS = 8 * 60_000L
        const val PERIODIC_MS = 3 * 60_000L
    }
}

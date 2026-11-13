package com.musubi.phone.rec

import com.musubi.phone.BuildConfig

object RecorderConfig {
    const val SAMPLE_RATE = 48_000
    const val CHANNELS = 1
    const val BIT_RATE = 96_000

    /** A new chunk file every 5 minutes. Debug builds may shorten it (-PchunkSeconds=N) for smoke tests. */
    val CHUNK_SECONDS: Long = if (BuildConfig.DEBUG && BuildConfig.CHUNK_SECONDS_OVERRIDE > 0) BuildConfig.CHUNK_SECONDS_OVERRIDE else 300

    /** Refuse to start below this much free space; stop cleanly below the second. */
    const val MIN_FREE_TO_START = 500L * 1024 * 1024
    const val MIN_FREE_TO_CONTINUE = 100L * 1024 * 1024
}

/** What the UI shows while recording. Written by the recorder thread, read by the UI. */
object RecorderStatus {
    @Volatile var recordingId: String? = null
    @Volatile var startedAtMillis: Long = 0
    @Volatile var eventSubject: String? = null
    /** Peak level of the last read, 0..1. */
    @Volatile var level: Float = 0f
    @Volatile var source: String = ""
    /** Last error that ended or prevented a recording, shown once. */
    @Volatile var lastError: String? = null

    val isRecording: Boolean get() = recordingId != null
}

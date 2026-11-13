package com.musubi.phone.core

import java.io.File
import java.io.IOException
import java.time.Clock
import java.time.Duration
import java.time.Instant
import java.time.OffsetDateTime

/** A recording folder as listed by the store. */
data class Recording(val dir: File, val meta: RecordingMeta, val sync: SyncStatus) {
    val id: String get() = meta.id
    fun chunkFile(n: Int): File = File(dir, chunkName(n))
}

/**
 * All recordings live under [root] (app-private storage): one folder per recording, named by
 * its UUID, holding meta.json, sync.json and chunk-NNNN.aac. Every meta/sync write goes through
 * [lock] so the recorder, the sync worker and the UI never interleave a read-modify-write.
 */
class RecordingStore(val root: File, private val clock: Clock = Clock.systemDefaultZone()) {
    private val lock = Any()

    init {
        root.mkdirs()
    }

    fun dirFor(id: String): File {
        require(RecordingMeta.isValidId(id)) { "bad recording id" }
        return File(root, id)
    }

    fun create(meta: RecordingMeta): File = synchronized(lock) {
        val dir = dirFor(meta.id)
        if (!dir.mkdirs() && !dir.isDirectory) throw IOException("cannot create recording folder")
        writeMeta(dir, meta)
        writeSync(dir, SyncStatus())
        dir
    }

    fun read(id: String): Recording? = synchronized(lock) { readDir(dirFor(id)) }

    /** All readable recordings, newest first. Unreadable folders are skipped, never deleted. */
    fun list(): List<Recording> = synchronized(lock) {
        (root.listFiles() ?: emptyArray())
            .filter { it.isDirectory && RecordingMeta.isValidId(it.name) }
            .mapNotNull { readDir(it) }
            .sortedByDescending { it.meta.start }
    }

    fun updateMeta(id: String, fn: (RecordingMeta) -> RecordingMeta): RecordingMeta = synchronized(lock) {
        val dir = dirFor(id)
        val cur = RecordingMeta.fromJson(File(dir, META).readText())
        val next = fn(cur)
        require(next.id == cur.id) { "id is fixed" }
        writeMeta(dir, next)
        next
    }

    fun updateSync(id: String, fn: (SyncStatus) -> SyncStatus): SyncStatus = synchronized(lock) {
        val dir = dirFor(id)
        val next = fn(readSync(dir))
        writeSync(dir, next)
        next
    }

    /** Removes the audio of a recording the Mac has imported; meta.json and sync.json stay. */
    fun deleteAudio(id: String) = synchronized(lock) {
        val dir = dirFor(id)
        (dir.listFiles() ?: emptyArray()).filter { chunkNumber(it.name) != null }.forEach { it.delete() }
    }

    /** Drops folders of recordings imported more than [keep] ago. Nothing else is ever pruned. */
    fun pruneImported(keep: Duration = Duration.ofDays(30)): Int = synchronized(lock) {
        val cutoff = OffsetDateTime.now(clock).minus(keep)
        var n = 0
        for (r in list()) {
            val at = r.sync.importedAt ?: continue
            if (r.sync.state == SyncState.IMPORTED && at.isBefore(cutoff)) {
                r.dir.deleteRecursively()
                n++
            }
        }
        n
    }

    /**
     * Closes recordings left open by a killed process. Must run only when no recorder is active
     * in this process (the app calls it at process start, before the service can run).
     * Chunks are kept: a partial trailing ADTS frame is cut, every chunk is hashed, and the end
     * time is the last chunk's modified time. A recording with no audio at all is removed.
     */
    fun recoverInterrupted(): List<String> = synchronized(lock) {
        val recovered = mutableListOf<String>()
        for (r in list()) {
            if (!r.meta.isRecording) continue
            val files = (r.dir.listFiles() ?: emptyArray())
                .mapNotNull { f -> chunkNumber(f.name)?.let { it to f } }
                .sortedBy { it.first }
            val chunks = mutableListOf<ChunkInfo>()
            var lastModified: Long? = null
            for ((n, f) in files) {
                // Read the time first: cutting a partial frame would change it.
                val modified = f.lastModified()
                val size = Adts.truncateToCompleteFrames(f)
                if (size == 0L) {
                    f.delete()
                    continue
                }
                chunks += ChunkInfo(n, size, Hashing.sha256File(f))
                lastModified = maxOf(lastModified ?: 0L, modified)
            }
            if (chunks.isEmpty()) {
                // Killed before any audio reached disk: nothing to keep or upload.
                r.dir.deleteRecursively()
                continue
            }
            val end = lastModified?.let { OffsetDateTime.ofInstant(Instant.ofEpochMilli(it), r.meta.start.offset) }
                ?.takeIf { !it.isBefore(r.meta.start) } ?: r.meta.start
            writeMeta(r.dir, r.meta.copy(end = end, chunks = chunks))
            recovered += r.id
        }
        recovered
    }

    fun metaBytes(id: String): ByteArray = synchronized(lock) {
        Files.readBounded(File(dirFor(id), META), MAX_META_BYTES)
    }

    /** Deletes a recording that never captured audio (e.g. the mic could not be opened). */
    fun deleteIfEmpty(id: String): Boolean = synchronized(lock) {
        val dir = dirFor(id)
        val hasAudio = (dir.listFiles() ?: emptyArray()).any { chunkNumber(it.name) != null && it.length() > 0 }
        if (!hasAudio) dir.deleteRecursively()
        !hasAudio
    }

    private fun readDir(dir: File): Recording? {
        val metaFile = File(dir, META)
        if (!metaFile.isFile) return null
        val meta = runCatching { RecordingMeta.fromJson(String(Files.readBounded(metaFile, MAX_META_BYTES))) }.getOrNull()
            ?: return null
        if (meta.id != dir.name) return null
        return Recording(dir, meta, readSync(dir))
    }

    private fun readSync(dir: File): SyncStatus {
        val f = File(dir, SYNC)
        if (!f.isFile) return SyncStatus()
        return runCatching { SyncStatus.fromJson(String(Files.readBounded(f, MAX_META_BYTES))) }.getOrElse { SyncStatus() }
    }

    private fun writeMeta(dir: File, meta: RecordingMeta) = Files.writeAtomic(File(dir, META), meta.toJson().toByteArray())
    private fun writeSync(dir: File, s: SyncStatus) = Files.writeAtomic(File(dir, SYNC), s.toJson().toByteArray())

    companion object {
        const val META = "meta.json"
        const val SYNC = "sync.json"
        const val MAX_META_BYTES = 4 * 1024 * 1024
    }
}

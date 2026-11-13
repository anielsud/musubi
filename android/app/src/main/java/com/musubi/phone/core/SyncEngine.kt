package com.musubi.phone.core

import java.time.Clock
import java.time.OffsetDateTime

/** How a sync run ended. */
enum class SyncOutcome {
    /** Everything that could be done was done. */
    DONE,
    /** The Mac was not reachable or had a temporary error; try again later. */
    RETRY,
    /** The Mac no longer accepts this phone's token (revoked or re-paired elsewhere). */
    UNAUTHORIZED,
}

data class SyncReport(val outcome: SyncOutcome, val message: String)

/**
 * Moves recordings through on_phone → uploading → on_mac → imported, and refreshes the agenda.
 * Audio is deleted only after the Mac reports `imported`; nothing is deleted on any error.
 * Pure logic over [RecordingStore], [AgendaStore] and a [MacApi]; unit-tested on the JVM.
 */
class SyncEngine(
    private val store: RecordingStore,
    private val agendaStore: AgendaStore,
    private val api: MacApi,
    private val clock: Clock = Clock.systemDefaultZone(),
) {
    private class Stop(val outcome: SyncOutcome, val msg: String) : Exception()

    fun run(): SyncReport {
        return try {
            // Oldest first, so the backlog drains in the order meetings happened.
            for (r in store.list().sortedBy { it.meta.start }) {
                if (r.meta.isRecording) continue
                syncRecording(r)
            }
            store.pruneImported()
            fetchAgenda()
            SyncReport(SyncOutcome.DONE, "Synced")
        } catch (s: Stop) {
            SyncReport(s.outcome, s.msg)
        }
    }

    fun syncRecording(r: Recording) {
        var sync = r.sync
        if (sync.state == SyncState.ON_PHONE || sync.state == SyncState.UPLOADING) {
            sync = upload(r, sync) ?: return
        }
        if (sync.state == SyncState.ON_MAC) poll(r)
    }

    /** Uploads missing chunks then completes. Returns the new status, or null if it ended in FAILED. */
    private fun upload(r: Recording, start: SyncStatus): SyncStatus? {
        var sync = store.updateSync(r.id) { it.copy(state = SyncState.UPLOADING, error = null) }
        for (c in r.meta.chunks.sortedBy { it.n }) {
            if (c.n in sync.uploaded) continue
            val f = r.chunkFile(c.n)
            if (!f.isFile) return fail(r, "Chunk ${c.n} is missing on the phone")
            // Re-hash so the Mac never receives bytes that differ from meta.json.
            if (f.length() != c.size || Hashing.sha256File(f) != c.sha256) return fail(r, "Chunk ${c.n} changed on the phone")
            when (val res = api.putChunk(r.id, c.n, f, c.sha256)) {
                is ApiResult.Ok -> sync = store.updateSync(r.id) { it.copy(uploaded = it.uploaded + c.n) }
                is ApiResult.Http -> when (res.code) {
                    409 -> return fail(r, "The Mac holds different audio for chunk ${c.n}")
                    else -> { handleHttp(r, res.code, "uploading chunk ${c.n}"); return null }
                }
                is ApiResult.Network -> throw unreachable(res)
            }
        }
        when (val res = api.complete(r.id, store.metaBytes(r.id))) {
            is ApiResult.Ok -> {
                val st = runCatching { ApiParse.recordingState(res.body) }.getOrNull()
                    ?: throw Stop(SyncOutcome.RETRY, "Unexpected reply from the Mac")
                return applyServerState(r, st)
            }
            is ApiResult.Http -> {
                if (res.code == 409) return fail(r, "The Mac rejected the recording (chunks did not match)")
                handleHttp(r, res.code, "completing")
                return null
            }
            is ApiResult.Network -> throw unreachable(res)
        }
    }

    private fun poll(r: Recording) {
        when (val res = api.recordingState(r.id)) {
            is ApiResult.Ok -> {
                val st = runCatching { ApiParse.recordingState(res.body) }.getOrNull()
                    ?: throw Stop(SyncOutcome.RETRY, "Unexpected reply from the Mac")
                applyServerState(r, st)
            }
            is ApiResult.Http -> {
                if (res.code == 404) {
                    // The Mac lost it: upload again (chunk PUTs are idempotent).
                    store.updateSync(r.id) { SyncStatus(state = SyncState.UPLOADING) }
                    return
                }
                handleHttp(r, res.code, "checking")
            }
            is ApiResult.Network -> throw unreachable(res)
        }
    }

    private fun applyServerState(r: Recording, st: ServerRecordingState): SyncStatus? {
        return when (st.state) {
            "imported" -> {
                val s = store.updateSync(r.id) {
                    it.copy(state = SyncState.IMPORTED, serverState = st.state, sessionId = st.sessionId, error = null, importedAt = OffsetDateTime.now(clock))
                }
                store.deleteAudio(r.id)
                s
            }
            "failed" -> {
                store.updateSync(r.id) { it.copy(state = SyncState.FAILED, serverState = st.state, error = st.error ?: "The Mac could not import it") }
                null
            }
            else -> store.updateSync(r.id) { it.copy(state = SyncState.ON_MAC, serverState = st.state, error = null) }
        }
    }

    /** Throws for errors that stop the whole run; otherwise marks this recording failed (audio kept). */
    private fun handleHttp(r: Recording, code: Int, what: String) {
        when {
            code == 401 || code == 403 -> throw Stop(SyncOutcome.UNAUTHORIZED, "The Mac no longer accepts this phone; pair again")
            code >= 500 || code == 408 || code == 429 -> throw Stop(SyncOutcome.RETRY, "Mac busy (HTTP $code)")
            else -> fail(r, "The Mac refused it while $what (HTTP $code)")
        }
    }

    private fun unreachable(res: ApiResult.Network) = Stop(
        SyncOutcome.RETRY,
        if (res.certificateMismatch) "The Mac's certificate is not the paired one; nothing was sent" else "Mac not reachable",
    )

    private fun fail(r: Recording, reason: String): SyncStatus? {
        store.updateSync(r.id) { it.copy(state = SyncState.FAILED, error = reason) }
        return null
    }

    private fun fetchAgenda() {
        when (val res = api.agenda(AGENDA_DAYS)) {
            is ApiResult.Ok -> {
                val now = OffsetDateTime.now(clock)
                val a = runCatching { ApiParse.agenda(res.body, now) }.getOrNull()
                    ?: throw Stop(SyncOutcome.RETRY, "Unexpected agenda from the Mac")
                agendaStore.replace(a, now)
            }
            is ApiResult.Http -> {
                if (res.code == 401 || res.code == 403) throw Stop(SyncOutcome.UNAUTHORIZED, "The Mac no longer accepts this phone; pair again")
                throw Stop(SyncOutcome.RETRY, "Agenda not available (HTTP ${res.code})")
            }
            is ApiResult.Network -> throw unreachable(res)
        }
    }

    companion object {
        const val AGENDA_DAYS = 10
    }
}

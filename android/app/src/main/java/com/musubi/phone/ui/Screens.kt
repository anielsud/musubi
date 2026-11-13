package com.musubi.phone.ui

import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.ScrollView
import android.widget.TextView
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.progressindicator.LinearProgressIndicator
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.musubi.phone.App
import com.musubi.phone.DeviceCalendar
import com.musubi.phone.MainActivity
import com.musubi.phone.core.AgendaEvent
import com.musubi.phone.core.AgendaStore
import com.musubi.phone.core.ApiResult
import com.musubi.phone.core.ApiParse
import com.musubi.phone.core.EventRef
import com.musubi.phone.core.Recording
import com.musubi.phone.core.SyncState
import com.musubi.phone.net.HttpMacApi
import com.musubi.phone.net.MacAddress
import com.musubi.phone.net.MacSettings
import com.musubi.phone.net.Pinning
import com.musubi.phone.rec.RecorderService
import com.musubi.phone.rec.RecorderStatus
import com.musubi.phone.sync.SyncScheduler
import java.time.Duration
import java.time.OffsetDateTime
import java.util.concurrent.Executors

/** A screen is a view plus refresh hooks; MainActivity swaps them in and out. */
abstract class Screen(protected val a: MainActivity) {
    abstract val title: String
    val view: View by lazy { build() }
    protected abstract fun build(): View
    open fun refresh() {}
    open fun tick() {}
}

/** Events shown on the phone: synced agenda merged with the phone's calendar. */
fun mergedEvents(a: MainActivity): List<AgendaEvent> {
    val synced = App.agenda(a).load()?.events ?: emptyList()
    return AgendaStore.merge(synced, DeviceCalendar.events(a, 10), OffsetDateTime.now())
}

fun AgendaEvent.ref() = EventRef(id, subject, start)

/** Lets the owner pick the event a recording belongs to, or none. */
fun pickEvent(a: MainActivity, around: OffsetDateTime, onPick: (EventRef?) -> Unit) {
    val events = mergedEvents(a).filter { Duration.between(it.start, around).abs() <= Duration.ofHours(12) }
    val labels = listOf("No event (ad hoc)") + events.map { "${Fmt.day(it.start)} ${Fmt.time(it.start)}  ${it.subject}" }
    MaterialAlertDialogBuilder(a)
        .setTitle("Which meeting is this?")
        .setItems(labels.toTypedArray()) { _, i -> onPick(if (i == 0) null else events[i - 1].ref()) }
        .setNegativeButton("Cancel", null)
        .show()
}

private fun scrolling(a: MainActivity, inner: View): ScrollView = ScrollView(a).apply {
    addView(inner, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
}

class TodayScreen(a: MainActivity) : Screen(a) {
    override val title = "Today"
    private lateinit var record: com.google.android.material.button.MaterialButton
    private lateinit var list: LinearLayout
    private lateinit var status: TextView

    override fun build(): View {
        val root = a.vbox(16)
        record = a.button("Record (ad hoc)") { a.startRecording(null) }.apply {
            minHeight = a.dp(72)
            textSize = 20f
        }
        root.addView(record, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        status = a.label("", 13f, secondary = true)
        root.addView(status.marginTop(a, 8))
        list = a.vbox()
        root.addView(scrolling(a, list).marginTop(a, 8), LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f).also { it.topMargin = a.dp(8) })
        return root
    }

    override fun tick() {
        record.text = if (RecorderStatus.isRecording) "Recording… (open)" else "Record (ad hoc)"
    }

    override fun refresh() {
        tick()
        val agenda = App.agenda(a).load()
        val parts = mutableListOf<String>()
        parts += agenda?.fetchedAt?.let { "Agenda from the Mac: ${Fmt.day(it)} ${Fmt.time(it)}" } ?: "No agenda synced yet"
        if ((agenda?.drafting ?: 0) > 0) parts += "${agenda!!.drafting} briefs still being drafted on the Mac"
        status.text = parts.joinToString(" · ")
        list.removeAllViews()
        if (!DeviceCalendar.granted(a)) {
            list.addView(a.button("Also show the phone's calendar", outlined = true) { a.requestCalendar() })
        }
        val events = mergedEvents(a)
        if (events.isEmpty()) list.addView(a.label("No upcoming events.", secondary = true).marginTop(a, 16))
        var lastDay = ""
        for (e in events) {
            val d = Fmt.day(e.start)
            if (d != lastDay) {
                list.addView(a.label(d, 17f, bold = true).marginTop(a, 16))
                lastDay = d
            }
            list.addView(eventCard(e))
        }
    }

    private fun eventCard(e: AgendaEvent): View {
        val box = a.vbox(12)
        box.addView(a.label("${Fmt.time(e.start)}–${Fmt.time(e.end)}" + if (e.fromPhone) "  (phone calendar)" else "", 13f, secondary = true))
        box.addView(a.label(e.subject.ifBlank { "(no subject)" }, 16f, bold = true))
        if (e.location.isNotBlank()) box.addView(a.label(e.location, 13f, secondary = true))
        if (e.attendees.isNotEmpty()) {
            val shown = e.attendees.take(6).joinToString(", ") + if (e.attendees.size > 6) " +${e.attendees.size - 6}" else ""
            box.addView(a.label(shown, 13f, secondary = true))
        }
        val row = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.END }
        if (e.brief != null) row.addView(a.button("Brief", outlined = true) { a.openBrief(e) })
        row.addView(a.button("Record this meeting") { a.startRecording(e.ref()) }.also {
            it.layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = a.dp(8) }
        })
        box.addView(row.marginTop(a, 4))
        return a.card(box)
    }
}

class RecordingScreen(a: MainActivity) : Screen(a) {
    override val title = "Recording"
    private lateinit var elapsed: TextView
    private lateinit var meter: LinearProgressIndicator
    private lateinit var event: TextView
    private lateinit var source: TextView
    private val live = LiveTextPanel(a)
    private val openedAt = System.currentTimeMillis()

    override fun build(): View {
        val root = a.vbox(24)
        elapsed = a.label("00:00", 56f, bold = true).apply { gravity = Gravity.CENTER }
        root.addView(elapsed, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        root.addView(a.label("Level", 13f, secondary = true).marginTop(a, 24))
        meter = LinearProgressIndicator(a).apply { max = 100; trackThickness = a.dp(16) }
        root.addView(meter, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        source = a.label("", 12f, secondary = true)
        root.addView(source)
        event = a.label("", 16f)
        root.addView(event.marginTop(a, 24))
        root.addView(a.button("Change event", outlined = true) {
            pickEvent(a, OffsetDateTime.now()) { RecorderService.setEvent(a, it); RecorderStatus.eventSubject = it?.subject }
        })
        root.addView(a.button("Stop") { RecorderService.stop(a) }.apply { minHeight = a.dp(72); textSize = 20f }.marginTop(a, 32),
            LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT).also { it.topMargin = a.dp(32) })
        root.addView(live.view.marginTop(a, 24))
        return scrolling(a, root)
    }

    override fun tick() {
        if (!RecorderStatus.isRecording) {
            // Give the service a moment to start; then this screen has nothing to show.
            if (System.currentTimeMillis() - openedAt > 3_000) a.pop()
            elapsed.text = "Starting…"
            return
        }
        elapsed.text = Fmt.elapsed(System.currentTimeMillis() - RecorderStatus.startedAtMillis)
        meter.setProgressCompat((RecorderStatus.level * 100).toInt().coerceIn(0, 100), false)
        source.text = "Microphone: ${RecorderStatus.source}"
        event.text = "Event: " + (RecorderStatus.eventSubject ?: "none (ad hoc)")
        live.tick()
    }
}

class RecordingsScreen(a: MainActivity) : Screen(a) {
    override val title = "Recordings"
    private lateinit var list: LinearLayout
    private var lastSig = ""
    private var ticks = 0

    override fun build(): View {
        list = a.vbox(16)
        return scrolling(a, list)
    }

    override fun tick() {
        // Every 2 s; refresh() only rebuilds when something changed, so the list follows sync progress.
        if (++ticks % 8 == 0) refresh()
    }

    override fun refresh() {
        val recs = App.store(a).list()
        val sig = recs.joinToString { it.id + it.sync.state + it.sync.uploaded.size + it.meta.chunks.size + it.meta.event?.id + it.sync.serverState }
        if (sig == lastSig && list.childCount > 0) return
        lastSig = sig
        list.removeAllViews()
        if (recs.isEmpty()) list.addView(a.label("No recordings on this phone.", secondary = true))
        for (r in recs) list.addView(item(r))
    }

    private fun stateText(r: Recording): String {
        if (r.meta.isRecording) return if (r.id == RecorderStatus.recordingId) "Recording" else "Recording (interrupted)"
        val s = r.sync
        return when (s.state) {
            SyncState.ON_PHONE -> "On phone"
            SyncState.UPLOADING -> "Uploading (${s.uploaded.size}/${r.meta.chunks.size} parts)"
            SyncState.ON_MAC -> "On Mac" + (s.serverState?.let { " ($it)" } ?: "")
            SyncState.IMPORTED -> "Transcribed"
            SyncState.FAILED -> "Failed: " + (s.error ?: "unknown reason")
        }
    }

    private fun item(r: Recording): View {
        val box = a.vbox(12)
        val m = r.meta
        val dur = m.end?.let { Fmt.elapsed(Duration.between(m.start, it).toMillis()) } ?: "…"
        box.addView(a.label("${Fmt.day(m.start)} ${Fmt.time(m.start)} · $dur", 13f, secondary = true))
        box.addView(a.label(m.event?.subject?.ifBlank { "(no subject)" } ?: "Ad hoc", 16f, bold = true))
        val mb = m.chunks.sumOf { it.size } / (1024.0 * 1024.0)
        box.addView(a.label(stateText(r) + if (r.sync.state != SyncState.IMPORTED) " · %.1f MB".format(mb) else "", 14f))
        if (r.id != RecorderStatus.recordingId && App.store(a).liveTextFile(r.id) != null) {
            box.addView(a.button("Live text", outlined = true) { a.push(LiveTextScreen(a, r.id)) })
        }
        val c = a.card(box)
        val editable = !m.isRecording && (r.sync.state == SyncState.ON_PHONE || r.sync.state == SyncState.UPLOADING)
        c.setOnClickListener {
            when {
                r.id == RecorderStatus.recordingId -> a.push(RecordingScreen(a))
                editable -> pickEvent(a, m.start) { ev ->
                    runCatching { App.store(a).updateMeta(r.id) { it.copy(event = ev) } }
                    lastSig = ""
                    refresh()
                }
            }
        }
        return c
    }
}

class BriefScreen(a: MainActivity, private val e: AgendaEvent) : Screen(a) {
    override val title = "Brief"

    override fun build(): View {
        val box = a.vbox(16)
        box.addView(a.label(e.subject, 18f, bold = true))
        box.addView(a.label("${Fmt.day(e.start)} ${Fmt.time(e.start)}–${Fmt.time(e.end)}", 13f, secondary = true))
        val b = e.brief
        if (b == null) {
            box.addView(a.label("No brief for this event.").marginTop(a, 16))
        } else {
            val sens = a.vbox(12)
            sens.addView(a.label("Sensitivity: ${b.sensitivity.level} · share: ${b.sensitivity.share}", 14f, bold = true))
            shareHelp(b.sensitivity.share)?.let { sens.addView(a.label(it, 13f, secondary = true)) }
            if (b.sensitivity.why.isNotBlank()) sens.addView(a.label("Why: ${b.sensitivity.why}", 13f))
            box.addView(a.card(sens).marginTop(a, 12))
            b.madeAt?.let { box.addView(a.label("Drafted ${Fmt.day(it)} ${Fmt.time(it)}", 12f, secondary = true).marginTop(a, 8)) }
            box.addView(a.label(b.text, 15f).apply { setTextIsSelectable(true) }.marginTop(a, 12))
        }
        return scrolling(a, box)
    }

    private fun shareHelp(share: String) = when (share) {
        "public" -> "Anyone may hear this."
        "internal" -> "Colleagues only, not people outside the company."
        "party-only" -> "Only people who were party to the source."
        "owner-only" -> "Personal details: for you only."
        else -> null
    }
}

class MacScreen(a: MainActivity) : Screen(a) {
    override val title = "Mac"
    private lateinit var box: LinearLayout
    private var checkedFingerprint: String? = null
    private var checkedAddress: MacAddress? = null
    private val io = Executors.newSingleThreadExecutor()
    private var syncing = false

    override fun build(): View {
        box = a.vbox(16)
        return scrolling(a, box)
    }

    override fun tick() {
        if (syncing != SyncScheduler.running) {
            syncing = SyncScheduler.running
            refresh()
        }
    }

    override fun refresh() {
        box.removeAllViews()
        val s = MacSettings(a)
        if (s.isPaired) paired(s) else unpaired()
    }

    private fun paired(s: MacSettings) {
        val addr = s.address!!
        box.addView(a.label("Paired with ${addr.host}:${addr.port}", 16f, bold = true))
        box.addView(a.label("Certificate: ${Pinning.shortForm(s.pin ?: "")}", 13f, secondary = true))
        val last = if (s.lastSyncAt > 0) {
            val t = OffsetDateTime.ofInstant(java.time.Instant.ofEpochMilli(s.lastSyncAt), java.time.ZoneId.systemDefault())
            "${Fmt.day(t)} ${Fmt.time(t)}"
        } else "never"
        box.addView(a.label("Last sync: $last", 14f).marginTop(a, 16))
        s.lastResult?.let { box.addView(a.label("Last attempt: $it", 13f, secondary = true)) }
        if (SyncScheduler.running) box.addView(a.label("Syncing…", 14f).marginTop(a, 8))
        box.addView(a.button("Sync now") { SyncScheduler.syncSoon(a); a.toast("Sync started") }.marginTop(a, 16))
        box.addView(a.button("Unpair", outlined = true) {
            MaterialAlertDialogBuilder(a)
                .setMessage("Forget this Mac? Recordings stay on the phone and upload after you pair again.")
                .setPositiveButton("Unpair") { _, _ -> s.unpair(); refresh() }
                .setNegativeButton("Cancel", null)
                .show()
        }.marginTop(a, 8))
    }

    private fun field(hint: String, value: String, type: Int): Pair<TextInputLayout, EditText> {
        val layout = TextInputLayout(a)
        layout.hint = hint
        val edit = TextInputEditText(layout.context)
        edit.inputType = type
        edit.setText(value)
        layout.addView(edit)
        return layout to edit
    }

    private fun unpaired() {
        box.addView(a.label("Pair with the Mac", 16f, bold = true))
        box.addView(a.label("On the Mac: Settings → Phone → Pair a phone. Enter the address it shows.", 13f, secondary = true))
        val (hostL, host) = field("Mac address (name or IP)", checkedAddress?.host ?: "", InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI)
        val (portL, port) = field("Port", (checkedAddress?.port ?: MacAddress.DEFAULT_PORT).toString(), InputType.TYPE_CLASS_NUMBER)
        box.addView(hostL.marginTop(a, 12))
        box.addView(portL.marginTop(a, 8))
        val progress = ProgressBar(a).apply { visibility = View.GONE }
        box.addView(a.button("Check Mac") {
            val addr = MacAddress.parse(host.text.toString(), port.text.toString())
            if (addr == null) { hostL.error = "Enter a host name or IP address only"; return@button }
            hostL.error = null
            progress.visibility = View.VISIBLE
            io.execute {
                val fp = runCatching { Pinning.fetchFingerprint(addr.host, addr.port) }.getOrNull()
                a.runOnUiThread {
                    progress.visibility = View.GONE
                    if (fp == null) a.toast("Could not reach the Mac at that address")
                    else { checkedAddress = addr; checkedFingerprint = fp; refresh() }
                }
            }
        }.marginTop(a, 8))
        box.addView(progress)

        val fp = checkedFingerprint
        val addr = checkedAddress
        if (fp != null && addr != null) {
            box.addView(a.label("The Mac at ${addr.host} shows this certificate:", 14f).marginTop(a, 24))
            box.addView(a.label(Pinning.shortForm(fp), 26f, bold = true).apply { typeface = android.graphics.Typeface.MONOSPACE })
            box.addView(a.label("Pair only if it matches the digits on the Mac.", 13f, secondary = true))
            val (codeL, code) = field("8-digit code from the Mac", "", InputType.TYPE_CLASS_NUMBER)
            box.addView(codeL.marginTop(a, 12))
            box.addView(a.button("It matches — pair") {
                val c = code.text.toString().trim()
                if (!Regex("^\\d{8}$").matches(c)) { codeL.error = "8 digits"; return@button }
                codeL.error = null
                io.execute {
                    val res = HttpMacApi(addr, fp, null).pair(c, MacSettings.deviceName())
                    val msg: String? = when (res) {
                        is ApiResult.Ok -> runCatching { ApiParse.pair(res.body) }.fold(
                            onSuccess = { MacSettings(a).savePairing(addr, fp, it.deviceId, it.token); null },
                            onFailure = { "Unexpected reply from the Mac" },
                        )
                        is ApiResult.Http -> if (res.code == 401 || res.code == 403) "Wrong or expired code" else "The Mac refused (HTTP ${res.code})"
                        is ApiResult.Network -> if (res.certificateMismatch) "The Mac's certificate changed since you checked it" else "Could not reach the Mac"
                    }
                    a.runOnUiThread {
                        if (msg != null) a.toast(msg) else {
                            checkedFingerprint = null
                            a.toast("Paired")
                            SyncScheduler.syncSoon(a)
                            refresh()
                        }
                    }
                }
            }.marginTop(a, 8))
        }
    }
}

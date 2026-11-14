package com.musubi.phone.ui

import android.graphics.Typeface
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import com.google.android.material.button.MaterialButton
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.materialswitch.MaterialSwitch
import com.musubi.phone.App
import com.musubi.phone.MainActivity
import com.musubi.phone.core.LiveModel
import com.musubi.phone.core.LiveTextFile
import com.musubi.phone.rec.LiveModelDownload
import com.musubi.phone.rec.LivePrefs
import com.musubi.phone.rec.LiveStatus
import com.musubi.phone.rec.RecorderService

private fun mb(bytes: Long) = "%.0f MB".format(bytes / 1_000_000.0)

private fun modelState(a: MainActivity): String = when {
    LiveModelDownload.running -> "Downloading… ${LiveModelDownload.done * 100 / LiveModel.totalBytes}%"
    LiveModel.isPresent(LiveModel.dir(a.filesDir)) -> "Downloaded (${mb(LiveModel.totalBytes)})"
    else -> "Not downloaded (${mb(LiveModel.totalBytes)})"
}

/** The Live text part of the Recording screen: the switch, its state, and the latest lines. */
class LiveTextPanel(private val a: MainActivity) {
    private lateinit var switch: MaterialSwitch
    private lateinit var state: TextView
    private lateinit var download: MaterialButton
    private lateinit var text: TextView
    private var settingSwitch = false

    val view: View by lazy { build() }

    private fun build(): View {
        val box = a.vbox()
        switch = MaterialSwitch(a).apply {
            this.text = "Live text"
            textSize = 16f
            setOnCheckedChangeListener { _, on -> if (!settingSwitch) toggled(on) }
        }
        box.addView(switch, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        state = a.label("", 12f, secondary = true)
        box.addView(state)
        download = a.button("Download the model", outlined = true) { LiveModelDownload.start(a) }
        box.addView(download)
        text = a.label("", 15f).apply { setTextIsSelectable(true); gravity = Gravity.BOTTOM }
        box.addView(text.marginTop(a, 8))
        return box
    }

    private fun toggled(on: Boolean) {
        if (on && !LiveModel.isPresent(LiveModel.dir(a.filesDir))) {
            setSwitch(false)
            MaterialAlertDialogBuilder(a)
                .setMessage("Live text needs a speech model (${mb(LiveModel.totalBytes)}), downloaded once and kept on this phone. Download it now?")
                .setPositiveButton("Download") { _, _ -> LiveModelDownload.start(a) }
                .setNegativeButton("Cancel", null)
                .show()
            return
        }
        LivePrefs(a).on = on
        if (on) LiveStatus.state = LiveStatus.State.STARTING
        RecorderService.setLive(a, on)
    }

    private fun setSwitch(on: Boolean) {
        if (switch.isChecked == on) return
        settingSwitch = true
        switch.isChecked = on
        settingSwitch = false
    }

    fun tick() {
        LiveStatus.message?.let {
            LiveStatus.message = null
            a.toast(it)
        }
        val present = LiveModel.isPresent(LiveModel.dir(a.filesDir))
        val s = LiveStatus.state
        setSwitch(s != LiveStatus.State.OFF)
        state.text = when {
            !present -> modelState(a) + " · Live text needs the model" + (LiveModelDownload.error?.let { " · $it" } ?: "")
            s == LiveStatus.State.STARTING -> "Starting…"
            s == LiveStatus.State.ON -> "On this phone only; the Mac transcribes the audio itself"
            else -> "Off"
        }
        download.visibility = if (!present && !LiveModelDownload.running) View.VISIBLE else View.GONE
        if (s == LiveStatus.State.OFF && LiveStatus.lines().isEmpty()) {
            text.text = ""
            return
        }
        val shown = LiveStatus.lines().takeLast(SHOWN_LINES).joinToString("\n")
        val partial = LiveStatus.partial
        text.text = if (partial.isEmpty()) shown else (if (shown.isEmpty()) "" else "$shown\n") + "… $partial"
    }

    companion object {
        private const val SHOWN_LINES = 6
    }
}

/** Settings: the live text model (more settings may join later). */
class SettingsScreen(a: MainActivity) : Screen(a) {
    override val title = "Settings"
    private lateinit var state: TextView
    private lateinit var download: MaterialButton
    private lateinit var cancel: MaterialButton
    private lateinit var delete: MaterialButton

    override fun build(): View {
        val box = a.vbox(16)
        box.addView(a.label("Live text model", 16f, bold = true))
        box.addView(a.label(
            "Shows what is said while recording, on this phone only. English, streaming zipformer (20M parameters). " +
                "Downloaded once from Hugging Face and checked against a fixed checksum; nothing is sent anywhere.",
            13f, secondary = true,
        ))
        state = a.label("", 14f)
        box.addView(state.marginTop(a, 8))
        download = a.button("Download") { LiveModelDownload.start(a); refresh() }
        cancel = a.button("Cancel download", outlined = true) { LiveModelDownload.cancel() }
        delete = a.button("Delete", outlined = true) {
            if (LiveStatus.state != LiveStatus.State.OFF) {
                a.toast("Turn live text off first")
                return@button
            }
            MaterialAlertDialogBuilder(a)
                .setMessage("Delete the live text model? Live text stays off until you download it again.")
                .setPositiveButton("Delete") { _, _ -> LiveModel.delete(LiveModel.dir(a.filesDir)); refresh() }
                .setNegativeButton("Cancel", null)
                .show()
        }
        box.addView(download.marginTop(a, 8))
        box.addView(cancel)
        box.addView(delete)
        return box
    }

    override fun tick() = refresh()

    override fun refresh() {
        val present = LiveModel.isPresent(LiveModel.dir(a.filesDir))
        val running = LiveModelDownload.running
        state.text = modelState(a) + (if (!running && !present) LiveModelDownload.error?.let { "\n$it" } ?: "" else "")
        download.visibility = if (!present && !running) View.VISIBLE else View.GONE
        cancel.visibility = if (running) View.VISIBLE else View.GONE
        delete.visibility = if (!running && (present || LiveModel.dir(a.filesDir).exists())) View.VISIBLE else View.GONE
    }
}

/** A finished recording's live text, read-only. */
class LiveTextScreen(a: MainActivity, private val recordingId: String) : Screen(a) {
    override val title = "Live text"

    override fun build(): View {
        val box = a.vbox(16)
        box.addView(a.label("A preview from a small model on the phone. The Mac's transcript is the record.", 13f, secondary = true))
        val f = App.store(a).liveTextFile(recordingId)
        val body = f?.let { runCatching { LiveTextFile.read(it) }.getOrNull() }
        box.addView(a.label(body?.ifBlank { "(no text)" } ?: "No live text for this recording.", 15f).apply {
            setTextIsSelectable(true)
            typeface = Typeface.DEFAULT
        }.marginTop(a, 12))
        return android.widget.ScrollView(a).apply {
            addView(box, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        }
    }
}

package com.musubi.phone.ui

import android.graphics.Typeface
import android.os.Handler
import android.os.Looper
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import androidx.appcompat.app.AlertDialog
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.google.android.material.textfield.TextInputEditText
import com.google.android.material.textfield.TextInputLayout
import com.musubi.phone.MainActivity
import com.musubi.phone.core.FoundMac
import com.musubi.phone.core.PairCode
import com.musubi.phone.net.MacAddress
import com.musubi.phone.net.MacDiscovery
import com.musubi.phone.net.MacSettings
import com.musubi.phone.net.PairingSession
import com.musubi.phone.net.Pinning
import com.musubi.phone.sync.SyncScheduler
import com.musubi.phone.update.Updater
import java.time.OffsetDateTime
import java.util.concurrent.Executors

/** Mac tab: find and pair a Mac, sync, unpair; and app updates. */
class MacScreen(a: MainActivity) : Screen(a) {
    override val title = "Mac"
    private lateinit var macBox: LinearLayout
    private lateinit var foundBox: LinearLayout
    private lateinit var updateBox: LinearLayout
    private val io = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private val found = LinkedHashMap<String, FoundMac>()
    private var discovery: MacDiscovery? = null
    private var searching = false
    private var syncing = false
    private var shownUpdateStatus: String? = null
    private var checking = false
    private var showAdvanced = false

    override fun build(): View {
        val root = a.vbox(16)
        macBox = a.vbox()
        updateBox = a.vbox()
        root.addView(macBox)
        root.addView(updateBox.marginTop(a, 32))
        return ScrollView(a).apply {
            addView(root, ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        }
    }

    override fun tick() {
        if (syncing != SyncScheduler.running) {
            syncing = SyncScheduler.running
            refresh()
        }
        if (shownUpdateStatus != Updater.status(a)) renderUpdates()
    }

    override fun refresh() {
        macBox.removeAllViews()
        val s = MacSettings(a)
        if (s.isPaired) {
            stopSearch()
            paired(s)
        } else {
            unpaired()
        }
        renderUpdates()
    }

    private fun paired(s: MacSettings) {
        val addr = s.address
        box(a.label("Paired with " + (s.instanceName ?: addr?.let { "${it.host}:${it.port}" } ?: "a Mac"), 16f, bold = true))
        box(a.label("Certificate: ${Pinning.shortForm(s.pin ?: "")}", 13f, secondary = true))
        addr?.let { box(a.label("Last address: ${it.host}:${it.port}", 13f, secondary = true)) }
        val last = if (s.lastSyncAt > 0) {
            val t = OffsetDateTime.ofInstant(java.time.Instant.ofEpochMilli(s.lastSyncAt), java.time.ZoneId.systemDefault())
            "${Fmt.day(t)} ${Fmt.time(t)}"
        } else "never"
        box(a.label("Last sync: $last", 14f).marginTop(a, 16))
        s.lastResult?.let { box(a.label("Last attempt: $it", 13f, secondary = true)) }
        if (SyncScheduler.running) box(a.label("Syncing…", 14f).marginTop(a, 8))
        box(a.button("Sync now") { SyncScheduler.syncSoon(a); a.toast("Sync started") }.marginTop(a, 16))
        box(a.button("Unpair", outlined = true) {
            MaterialAlertDialogBuilder(a)
                .setMessage("Forget this Mac? Recordings stay on the phone and upload after you pair again.")
                .setPositiveButton("Unpair") { _, _ -> s.unpair(); refresh() }
                .setNegativeButton("Cancel", null)
                .show()
        }.marginTop(a, 8))
    }

    private fun box(v: View) = macBox.addView(v)

    private fun unpaired() {
        box(a.label("Pair with a Mac", 16f, bold = true))
        box(a.label("Macs on this network with Musubi's phone access on (Mac: Settings → Phone).", 13f, secondary = true))
        foundBox = a.vbox()
        box(foundBox.marginTop(a, 8))
        renderFound()
        if (!searching && found.isEmpty()) startSearch()

        box(a.button(if (showAdvanced) "Hide advanced" else "Advanced", outlined = true) {
            showAdvanced = !showAdvanced
            refresh()
        }.marginTop(a, 24))
        if (showAdvanced) advanced()
    }

    private fun renderFound() {
        if (!::foundBox.isInitialized) return
        foundBox.removeAllViews()
        for (m in found.values) {
            val card = a.vbox(12)
            card.addView(a.label(m.name, 16f, bold = true))
            card.addView(a.label("Certificate starts ${Pinning.shortForm(m.fp)}", 13f, secondary = true))
            foundBox.addView(a.card(card).apply { setOnClickListener { pair(m.name, MacAddress(m.host, m.port), m.fp) } })
        }
        when {
            searching -> foundBox.addView(a.label(if (found.isEmpty()) "Searching…" else "Still searching…", 13f, secondary = true))
            found.isEmpty() -> foundBox.addView(a.label("No Mac found. Check that the phone is on the same Wi-Fi and phone access is on.", 13f, secondary = true))
        }
        if (!searching) foundBox.addView(a.button("Search again", outlined = true) { found.clear(); startSearch() }.marginTop(a, 8))
    }

    private fun startSearch() {
        stopSearch()
        searching = true
        discovery = MacDiscovery(a) { m -> main.post { if (searching) { found[m.name + "|" + m.fp] = m; renderFound() } } }.also { it.start() }
        renderFound()
        main.postDelayed(stopSearchRunnable, SEARCH_MS)
    }

    private val stopSearchRunnable = Runnable {
        stopSearch()
        renderFound()
    }

    private fun stopSearch() {
        main.removeCallbacks(stopSearchRunnable)
        discovery?.stop()
        discovery = null
        searching = false
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

    /** A typed address, for when Bonjour cannot reach (Tailscale, some networks). */
    private fun advanced() {
        box(a.label("Connect by address (when the Mac is not found, e.g. over Tailscale). Compare the code on both screens.", 13f, secondary = true).marginTop(a, 8))
        val (hostL, host) = field("Mac address (name or IP)", "", InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI)
        val (portL, port) = field("Port", MacAddress.DEFAULT_PORT.toString(), InputType.TYPE_CLASS_NUMBER)
        box(hostL.marginTop(a, 8))
        box(portL.marginTop(a, 8))
        box(a.button("Connect") {
            val addr = MacAddress.parse(host.text.toString(), port.text.toString())
            if (addr == null) { hostL.error = "Enter a host name or IP address only"; return@button }
            hostL.error = null
            pair(null, addr, null)
        }.marginTop(a, 8))
    }

    /** Pairing: connect and request, show the code with Confirm/Cancel, then wait for the Mac. */
    private fun pair(name: String?, addr: MacAddress, fp: String?) {
        stopSearch()
        renderFound()
        val session = PairingSession(name, addr, fp)
        val connecting = MaterialAlertDialogBuilder(a)
            .setMessage("Connecting to ${name ?: addr.host}…")
            .setCancelable(false)
            .setNegativeButton("Cancel") { _, _ -> session.cancelled = true }
            .show()
        io.execute {
            val step = session.request(MacSettings.deviceName())
            a.runOnUiThread {
                connecting.dismiss()
                if (session.cancelled) return@runOnUiThread
                when (step) {
                    is PairingSession.Step.Failed -> message(step.message)
                    is PairingSession.Step.Code -> showCode(session, name ?: addr.host, step.code)
                }
            }
        }
    }

    private fun showCode(session: PairingSession, macName: String, code: String) {
        val v = a.vbox(24)
        v.addView(a.label("The Mac shows a code. Confirm only if it is the same:", 15f))
        v.addView(a.label(PairCode.display(code), 40f, bold = true).apply {
            typeface = Typeface.MONOSPACE
            gravity = Gravity.CENTER
        }.marginTop(a, 16))
        v.addView(a.label("Certificate ${Pinning.shortForm(session.fingerprint ?: "")}", 12f, secondary = true).marginTop(a, 16))
        MaterialAlertDialogBuilder(a)
            .setTitle("Pair with $macName?")
            .setView(v)
            .setCancelable(false)
            .setPositiveButton("Confirm") { _, _ -> confirm(session) }
            .setNegativeButton("Cancel") { _, _ -> session.cancelled = true }
            .show()
    }

    private fun confirm(session: PairingSession) {
        val waiting: AlertDialog = MaterialAlertDialogBuilder(a)
            .setMessage("Waiting for the Mac. Choose Allow there.")
            .setCancelable(false)
            .setNegativeButton("Cancel") { _, _ -> session.cancelled = true }
            .show()
        io.execute {
            val (reply, msg) = session.confirmAndWait()
            if (reply != null && !session.cancelled) session.save(a, reply)
            a.runOnUiThread {
                waiting.dismiss()
                if (reply != null && !session.cancelled) {
                    a.toast("Paired")
                    SyncScheduler.syncSoon(a)
                    refresh()
                } else if (!session.cancelled) {
                    message(msg)
                }
            }
        }
    }

    private fun message(text: String) {
        MaterialAlertDialogBuilder(a).setMessage(text).setPositiveButton("OK", null).show()
    }

    private fun renderUpdates() {
        updateBox.removeAllViews()
        val status = Updater.status(a)
        shownUpdateStatus = status
        updateBox.addView(a.label("App updates", 16f, bold = true))
        updateBox.addView(a.label("Installed: v${Updater.installedVersion()}", 13f, secondary = true))
        if (checking) updateBox.addView(a.label("Checking…", 14f).marginTop(a, 8))
        else status?.let { updateBox.addView(a.label(it, 14f).marginTop(a, 8)) }
        updateBox.addView(a.button("Check for updates", outlined = true) {
            if (checking) return@button
            checking = true
            renderUpdates()
            Updater.checkNow(a) {
                checking = false
                renderUpdates()
            }
        }.marginTop(a, 8))
        if (Updater.hasPending(a) && Updater.needsInstallPermission(a)) {
            updateBox.addView(a.label("To install updates, allow Musubi to install apps.", 13f, secondary = true).marginTop(a, 8))
            updateBox.addView(a.button("Allow installing updates", outlined = true) { Updater.openInstallSetting(a) })
        }
    }

    companion object {
        private const val SEARCH_MS = 15_000L
    }
}

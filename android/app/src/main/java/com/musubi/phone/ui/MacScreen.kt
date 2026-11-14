package com.musubi.phone.ui

import android.graphics.Typeface
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import com.google.android.material.dialog.MaterialAlertDialogBuilder
import com.musubi.phone.MainActivity
import com.musubi.phone.core.PairCode
import com.musubi.phone.core.PairingFlow
import com.musubi.phone.sync.PhoneSync
import com.musubi.phone.update.Updater
import java.time.Instant
import java.time.OffsetDateTime
import java.time.ZoneId

/** Mac tab: the paired Mac and its last sync, pairing, unpairing; and app updates. */
class MacScreen(a: MainActivity) : Screen(a) {
    override val title = "Mac"
    private lateinit var macBox: LinearLayout
    private lateinit var updateBox: LinearLayout
    private var shownUpdateStatus: String? = null
    private var checking = false
    private var shownSig = ""
    private var countdown: TextView? = null

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

    private fun signature(): String {
        val p = PhoneSync.pairing.state()
        val detail = when (p) {
            is PairingFlow.State.Waiting -> p.until.toString()
            is PairingFlow.State.Code -> p.code + p.ownerConfirmed
            else -> p.toString()
        }
        return listOf(p.javaClass.simpleName, detail, PhoneSync.isPaired, PhoneSync.macName, PhoneSync.lastSyncAt,
            PhoneSync.lastResult, PhoneSync.problem, PhoneSync.listening).joinToString("|")
    }

    override fun tick() {
        if (signature() != shownSig) renderMac()
        // Only the countdown text changes each second, not the whole box.
        (PhoneSync.pairing.state() as? PairingFlow.State.Waiting)?.let { p ->
            val t = waitingText(p)
            countdown?.let { if (it.text.toString() != t) it.text = t }
        }
        if (shownUpdateStatus != Updater.status(a)) renderUpdates()
    }

    override fun refresh() {
        renderMac()
        renderUpdates()
    }

    private fun box(v: View) = macBox.addView(v)

    private fun renderMac() {
        shownSig = signature()
        macBox.removeAllViews()
        if (PhoneSync.isPaired) {
            box(a.label("Paired with " + (PhoneSync.macName ?: "your Mac"), 16f, bold = true))
            val last = PhoneSync.lastSyncAt.takeIf { it > 0 }?.let {
                val t = OffsetDateTime.ofInstant(Instant.ofEpochMilli(it), ZoneId.systemDefault())
                "${Fmt.day(t)} ${Fmt.time(t)}"
            } ?: "never"
            box(a.label("Last sync: $last", 14f).marginTop(a, 12))
            PhoneSync.lastResult?.let { box(a.label(it, 13f, secondary = true)) }
            if (PhoneSync.listening) box(a.label("Your Mac can sync now.", 13f, secondary = true).marginTop(a, 8))
        } else {
            box(a.label("No Mac paired", 16f, bold = true))
            box(a.label("On the Mac, turn on Musubi's phone sync (Settings → Phone), then tap Pair with your Mac here.", 13f, secondary = true))
        }
        PhoneSync.problem?.let { box(a.label(it, 13f).marginTop(a, 8)) }
        pairingPart()
        if (PhoneSync.isPaired) {
            box(a.button("Unpair", outlined = true) {
                MaterialAlertDialogBuilder(a)
                    .setMessage("Forget this Mac? Recordings stay on the phone and sync after you pair again.")
                    .setPositiveButton("Unpair") { _, _ -> PhoneSync.unpair(); renderMac() }
                    .setNegativeButton("Cancel", null)
                    .show()
            }.marginTop(a, 8))
        }
    }

    private fun pairingPart() {
        when (val p = PhoneSync.pairing.state()) {
            is PairingFlow.State.Waiting -> {
                box(a.label("Waiting for your Mac…", 16f, bold = true).marginTop(a, 24))
                box(a.label(waitingText(p), 13f, secondary = true).also { countdown = it })
                box(a.button("Cancel", outlined = true) { PhoneSync.cancelPairing() }.marginTop(a, 8))
            }
            is PairingFlow.State.Code -> {
                box(a.label("Pair with ${p.macName}?", 16f, bold = true).marginTop(a, 24))
                if (p.ownerConfirmed) {
                    box(a.label("Confirmed. Waiting for your Mac: choose Allow there.", 14f).marginTop(a, 8))
                } else {
                    box(a.label("Your Mac shows a code. Confirm only if it is the same:", 14f).marginTop(a, 8))
                }
                box(a.label(PairCode.display(p.code), 40f, bold = true).apply {
                    typeface = Typeface.MONOSPACE
                    gravity = Gravity.CENTER
                }.marginTop(a, 16))
                val row = LinearLayout(a).apply { orientation = LinearLayout.HORIZONTAL; gravity = Gravity.END }
                row.addView(a.button("Cancel", outlined = true) { PhoneSync.cancelPairing() })
                if (!p.ownerConfirmed) {
                    row.addView(a.button("Confirm") { PhoneSync.confirmPairing(); renderMac() }.also {
                        it.layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT).apply { marginStart = a.dp(8) }
                    })
                }
                box(row.marginTop(a, 16))
            }
            is PairingFlow.State.Paired -> {
                box(a.label("Paired with ${p.macName}.", 14f).marginTop(a, 24))
                pairButton()
            }
            is PairingFlow.State.Failed -> {
                box(a.label(failure(p.why), 14f).marginTop(a, 24))
                pairButton()
            }
            PairingFlow.State.Idle -> pairButton()
        }
    }

    private fun waitingText(p: PairingFlow.State.Waiting): String {
        val left = ((p.until - PhoneSync.now()) / 1000).coerceAtLeast(0)
        return "Musubi on the Mac shows a code when it finds this phone. %d:%02d left.".format(left / 60, left % 60)
    }

    private fun pairButton() {
        box(a.button("Pair with your Mac") { PhoneSync.startPairing(); renderMac() }.marginTop(a, 16))
    }

    private fun failure(why: PairingFlow.Failure) = when (why) {
        PairingFlow.Failure.CANCELLED -> "Pairing cancelled."
        PairingFlow.Failure.DENIED -> "Your Mac denied pairing."
        PairingFlow.Failure.EXPIRED -> "Pairing timed out. Check that phone sync is on on the Mac and both are on the same network, then try again."
        PairingFlow.Failure.DISCONNECTED -> "The connection to your Mac was lost. Try again."
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
}

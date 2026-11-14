package com.musubi.phone.sync

import android.content.Context
import android.os.Build
import android.os.SystemClock
import com.musubi.phone.App
import com.musubi.phone.BuildConfig
import com.musubi.phone.core.AddressFilter
import com.musubi.phone.core.FrameWriter
import com.musubi.phone.core.Hashing
import com.musubi.phone.core.ListenWindows
import com.musubi.phone.core.LiveLink
import com.musubi.phone.core.PairingFlow
import com.musubi.phone.core.PhoneInfo
import com.musubi.phone.core.ProtocolViolation
import com.musubi.phone.core.SyncSession
import com.musubi.phone.net.Advertiser
import com.musubi.phone.net.ClientTrust
import com.musubi.phone.net.PhoneIdentity
import com.musubi.phone.net.TlsServer
import java.net.NetworkInterface
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** The paired Mac, in app-private preferences: its certificate hash (not a secret) and name. */
class PairedMac(context: Context) {
    private val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    val pinHex: String? get() = prefs.getString(K_PIN, null)?.takeIf { Hashing.isSha256Hex(it) }
    val pin: ByteArray? get() = pinHex?.let { h -> ByteArray(32) { i -> h.substring(i * 2, i * 2 + 2).toInt(16).toByte() } }
    val name: String? get() = prefs.getString(K_NAME, null)
    val isPaired: Boolean get() = pinHex != null

    fun save(pin: ByteArray, name: String) {
        prefs.edit().putString(K_PIN, Hashing.hex(pin)).putString(K_NAME, name)
            .remove(K_LAST_SYNC).remove(K_LAST_RESULT).commit()
    }

    fun unpair() {
        prefs.edit().remove(K_PIN).remove(K_NAME).remove(K_LAST_SYNC).remove(K_LAST_RESULT).commit()
    }

    var lastSyncAt: Long
        get() = prefs.getLong(K_LAST_SYNC, 0)
        set(v) = prefs.edit().putLong(K_LAST_SYNC, v).apply()

    var lastResult: String?
        get() = prefs.getString(K_LAST_RESULT, null)
        set(v) = prefs.edit().putString(K_LAST_RESULT, v).apply()

    companion object {
        private const val PREFS = "paired-mac"
        private const val K_PIN = "pin_sha256"
        private const val K_NAME = "name"
        private const val K_LAST_SYNC = "last_sync_at"
        private const val K_LAST_RESULT = "last_result"
    }
}

/**
 * The phone side of sync (docs/phone.md, "Sync: the Mac dials the phone"). Opens and closes the
 * listening windows, runs the TLS server and the advertisement while one is open (or pairing),
 * and records what each connection did. A control thread re-evaluates every second.
 */
object PhoneSync {
    private lateinit var app: Context
    private lateinit var paired: PairedMac
    /** Monotonic: a wall-clock change (time zone, manual set, network time) must not stretch a window. */
    fun now(): Long = SystemClock.elapsedRealtime()
    private val windows = ListenWindows { now() }
    lateinit var pairing: PairingFlow
        private set
    private val ctl = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "musubi-sync-control").apply { isDaemon = true } }
    private var identity: PhoneIdentity? = null
    private var server: TlsServer? = null
    private var advertiser: Advertiser? = null
    @Volatile private var ticking = false
    @Volatile private var lastError: String? = null
    private val quiet = Object()

    /** Explore's connection while the Mac holds one (docs/phone.md, part 3); null otherwise. */
    @Volatile var live: LiveLink? = null
        private set
    /** Where the Mac's events go: the Explore screen's bridge, while it is open. */
    @Volatile var liveEvents: ((name: String, dataJson: String) -> Unit)? = null

    fun init(context: Context) {
        app = context.applicationContext
        paired = PairedMac(app)
        pairing = PairingFlow({ now() }) { fp, name -> paired.save(fp, name) }
        Migration.run(app)
    }

    val isPaired: Boolean get() = paired.isPaired
    val macName: String? get() = paired.name
    val lastSyncAt: Long get() = paired.lastSyncAt
    val lastResult: String? get() = paired.lastResult
    /** Set when the Keystore or the server could not start; shown on the Mac screen. */
    val problem: String? get() = lastError
    val listening: Boolean get() = server?.isRunning == true

    fun deviceName(): String = "${Build.MANUFACTURER} ${Build.MODEL}".trim().take(100)

    fun foreground(on: Boolean) {
        if (on) windows.open(ListenWindows.Kind.FOREGROUND) else windows.close(ListenWindows.Kind.FOREGROUND)
        // Explore lasts only while the app is in front.
        if (!on) live?.end()
        kick()
    }

    /** Explore may start: a Mac is paired and the app is in the foreground. */
    private fun liveAllowed(): Boolean = paired.isPaired && windows.isOpen(ListenWindows.Kind.FOREGROUND)

    /** Opens a window of [kind] for [ms] when a Mac is paired. Returns false when there is none. */
    fun openWindow(kind: ListenWindows.Kind, ms: Long): Boolean {
        if (!paired.isPaired) return false
        windows.open(kind, ms)
        kick()
        return true
    }

    fun closeWindow(kind: ListenWindows.Kind) {
        windows.close(kind)
        kick()
    }

    /** Blocks until the window of [kind] has ended and no connection is in progress, at most [maxMs]. */
    fun awaitWindowEnd(kind: ListenWindows.Kind, maxMs: Long) {
        val end = now() + maxMs
        synchronized(quiet) {
            while (now() < end && (windows.isOpen(kind) || server?.busy == true)) {
                quiet.wait(1_000)
            }
        }
    }

    fun startPairing() {
        pairing.open()
        kick()
    }

    fun confirmPairing() = pairing.ownerConfirm()

    fun cancelPairing() {
        pairing.ownerCancel()
        kick()
    }

    fun dismissPairing() = pairing.reset()

    fun unpair() {
        paired.unpair()
        windows.endEarly()
        kick()
    }

    private fun kick() {
        ctl.execute { evaluate() }
    }

    /** Control thread only. */
    private fun evaluate() {
        val want = (paired.isPaired && windows.anyOpen()) || pairing.isOpen()
        try {
            if (want) {
                val id = identity ?: PhoneIdentity.get().also { identity = it }
                val s = server ?: TlsServer(id, ClientTrust({ paired.pin }, { pairing.isOpen() }, ::refused), ::serve, ::idleMs).also { server = it }
                if (!s.isRunning) s.start()
                val adv = advertiser ?: Advertiser(app, id.sha256Hex, TlsServer.PORT).also { advertiser = it }
                adv.update(on = true, pair = pairing.isOpen(), live = liveAllowed())
                lastError = null
            } else {
                advertiser?.update(on = false, pair = false)
                server?.stop()
            }
        } catch (e: Exception) {
            // Exception class only: messages from the Keystore or sockets are not for the screen.
            lastError = "Could not listen for the Mac (${e.javaClass.simpleName})"
            advertiser?.update(on = false, pair = false)
            server?.stop()
        }
        live?.expire()
        synchronized(quiet) { quiet.notifyAll() }
        if (want && !ticking) {
            ticking = true
            ctl.schedule({ ticking = false; evaluate() }, 1, TimeUnit.SECONDS)
        }
    }

    /** While a code is shown, the Mac waits for its owner (up to 2 minutes) before pair_confirm. */
    private fun idleMs(): Int = when {
        pairing.state() is PairingFlow.State.Code -> (PairingFlow.DECIDE_MS + 30_000L).toInt()
        // The Mac pings every 30 seconds while Explore is open.
        live != null -> LIVE_READ_MS
        else -> TlsServer.IDLE_MS
    }

    private const val LIVE_READ_MS = 95_000

    private fun refused() {
        paired.lastResult = "Refused a Mac that is not the paired one"
    }

    private val info = object : PhoneInfo {
        override val deviceName: String get() = deviceName()
        override val appVersion: String get() = BuildConfig.VERSION_NAME
        override val certSha256: ByteArray get() = identity!!.sha256
        override fun addresses(): List<String> = runCatching {
            val all = NetworkInterface.getNetworkInterfaces()?.toList().orEmpty()
                .filter { it.isUp && !it.isLoopback }
                .flatMap { it.inetAddresses.toList() }
            AddressFilter.reportable(all)
        }.getOrDefault(emptyList())
    }

    /** Server thread: one connection. */
    private fun serve(socket: javax.net.ssl.SSLSocket, macFp: ByteArray, reader: com.musubi.phone.core.FrameReader) {
        val session = SyncSession(App.store(app), App.agenda(app), pairing, info, macFp, { paired.pin }, liveAllowed = ::liveAllowed)
        val writer = FrameWriter(socket.outputStream.buffered())
        val end = try {
            session.serve(reader, writer)
        } catch (e: ProtocolViolation) {
            null
        } catch (e: java.io.IOException) {
            SyncSession.End.CLOSED
        }
        record(session.summary, if (end == SyncSession.End.LIVE) SyncSession.End.BYE else end)
        if (end == SyncSession.End.SYNCED) {
            // The app in front keeps listening: the Mac comes back for Explore.
            val front = windows.isOpen(ListenWindows.Kind.FOREGROUND)
            windows.endEarly()
            if (front) windows.open(ListenWindows.Kind.FOREGROUND)
        }
        runCatching { App.store(app).pruneImported() }
        kick()
        if (end == SyncSession.End.LIVE) serveLive(reader, writer)
    }

    /** Server thread: Explore's connection, until either side ends it. */
    private fun serveLive(reader: com.musubi.phone.core.FrameReader, writer: FrameWriter) {
        val link = LiveLink(reader, writer, { name, data -> liveEvents?.invoke(name, data) })
        live = link
        kick()
        try {
            link.run()
        } catch (e: java.io.IOException) {
            // Closed by the Mac, a timeout or a protocol violation: Explore closes.
        } finally {
            live = null
            kick()
        }
    }

    private fun record(s: SyncSession.Summary, end: SyncSession.End?) {
        // A Mac may go on to sync on the connection it paired on; then the sync is the news.
        if (s.pairedNow && !s.listed && !s.agendaUpdated && s.chunksSent == 0) {
            paired.lastResult = "Paired"
            return
        }
        if (!s.paired) {
            if (end == SyncSession.End.NOT_PAIRED) paired.lastResult = "Refused a Mac that is not the paired one"
            return
        }
        val did = buildList {
            if (s.chunksSent > 0) add("${s.chunksSent} part${if (s.chunksSent == 1) "" else "s"} sent")
            if (s.importedNow > 0) add("${s.importedNow} transcribed")
            if (s.agendaUpdated) add("agenda updated")
        }.joinToString(", ")
        val text = when (end) {
            SyncSession.End.SYNCED, SyncSession.End.BYE ->
                if (s.errors > 0) "Synced with errors" + (if (did.isNotEmpty()) ": $did" else "")
                else "Synced" + (if (did.isNotEmpty()) ": $did" else "")
            SyncSession.End.VERSION -> "The Mac uses a different protocol version; update Musubi"
            null -> "The Mac sent an invalid request; connection closed"
            else -> "Interrupted" + (if (did.isNotEmpty()) " after $did" else "")
        }
        if (end == SyncSession.End.SYNCED || end == SyncSession.End.BYE) paired.lastSyncAt = System.currentTimeMillis()
        paired.lastResult = text
    }
}

/** One-time cleanup of the client-side sync of 0.2.x: its token, key, settings and jobs. */
private object Migration {
    fun run(ctx: Context) {
        val flags = ctx.getSharedPreferences("migrations", Context.MODE_PRIVATE)
        if (flags.getBoolean("v3-sync", false)) return
        // The old pairing held a bearer token and pointed at the Mac as a server; it cannot carry over.
        ctx.getSharedPreferences("mac", Context.MODE_PRIVATE).edit().clear().commit()
        runCatching {
            java.security.KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.deleteEntry("musubi-pairing-token")
        }
        runCatching {
            val wm = androidx.work.WorkManager.getInstance(ctx)
            wm.cancelUniqueWork("sync-periodic")
            wm.cancelUniqueWork("sync-now")
        }
        flags.edit().putBoolean("v3-sync", true).apply()
    }
}

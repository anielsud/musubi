package com.musubi.phone.rec

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import com.musubi.phone.App
import com.musubi.phone.MainActivity
import com.musubi.phone.R
import com.musubi.phone.core.ChunkWriter
import com.musubi.phone.core.EventRef
import com.musubi.phone.core.LiveModel
import com.musubi.phone.core.RecordingMeta
import com.musubi.phone.core.ListenWindows
import com.musubi.phone.sync.PhoneSync
import java.time.OffsetDateTime

/**
 * Foreground service (type microphone) that owns one recording at a time: ongoing notification
 * with Stop, a partial wake lock, and the [AudioEngine]. Not sticky: if the process is killed,
 * the next launch closes the recording (RecordingStore.recoverInterrupted) and keeps its audio.
 *
 * When a recording stops and a Mac is paired, the service stays in the foreground for the
 * 8-minute listening window, its type switched from microphone to dataSync, so the Mac can
 * collect the recording. It never starts a foreground service from the background: the service
 * is already in the foreground when it changes type.
 */
class RecorderService : Service() {
    private var engine: AudioEngine? = null
    private var live: LiveTextController? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private var syncWakeLock: PowerManager.WakeLock? = null
    private var holdingForSync = false
    private val main = Handler(Looper.getMainLooper())

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> start(intent)
            ACTION_STOP -> stop()
            ACTION_SET_EVENT -> setEvent(intent)
            ACTION_LIVE -> when {
                engine == null -> stopSelf()
                intent.getBooleanExtra(EXTRA_LIVE_ON, false) -> startLive()
                else -> stopLive(flush = true)
            }
            else -> if (engine == null) stopSelf()
        }
        return START_NOT_STICKY
    }

    private fun start(intent: Intent) {
        if (engine != null) return
        // startForeground first: Android requires it promptly after startForegroundService.
        val startedAt = System.currentTimeMillis()
        ServiceCompat.startForeground(
            this, NOTIFICATION_ID, notification(startedAt, intent.getStringExtra(EXTRA_EVENT_SUBJECT)),
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE else 0,
        )
        val store = App.store(this)
        if (store.root.usableSpace < RecorderConfig.MIN_FREE_TO_START) {
            RecorderStatus.lastError = "Not enough free space to record (needs 500 MB)"
            finish()
            return
        }
        val event = eventFrom(intent)
        val meta = RecordingMeta(
            id = RecordingMeta.newId(),
            start = OffsetDateTime.now(),
            end = null,
            deviceName = PhoneSync.deviceName(),
            event = event,
            chunks = emptyList(),
        )
        try {
            store.create(meta)
        } catch (e: Exception) {
            RecorderStatus.lastError = "Could not create the recording folder"
            finish()
            return
        }
        wakeLock = (getSystemService(Context.POWER_SERVICE) as PowerManager)
            .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "musubi:recording")
            .also { it.setReferenceCounted(false); it.acquire(WAKE_LOCK_MAX_MS) }

        val writer = ChunkWriter(store, meta.id, RecorderConfig.SAMPLE_RATE, RecorderConfig.CHANNELS, RecorderConfig.CHUNK_SECONDS * RecorderConfig.SAMPLE_RATE)
        RecorderStatus.lastError = null
        RecorderStatus.recordingId = meta.id
        RecorderStatus.startedAtMillis = startedAt
        RecorderStatus.eventSubject = event?.subject
        engine = AudioEngine(writer, store.root) { err -> main.post { onEngineFinished(meta.id, err) } }.also { it.start() }
        LiveStatus.clear()
        if (LivePrefs(this).on) startLive()
    }

    /** Live text on for the current recording, if the model is there. */
    private fun startLive() {
        val id = RecorderStatus.recordingId ?: return
        val e = engine ?: return
        if (live != null) return
        if (!LiveModel.isPresent(LiveModel.dir(filesDir))) {
            LiveStatus.state = LiveStatus.State.OFF
            LiveStatus.message = "Live text needs its model: download it in Settings"
            return
        }
        val c = LiveTextController(this, App.store(this).dirFor(id)) { stopped ->
            main.post {
                if (live === stopped) {
                    live = null
                    engine?.live = null
                }
            }
        }
        live = c
        e.live = c
        c.start()
    }

    private fun stopLive(flush: Boolean) {
        val c = live ?: return
        live = null
        engine?.live = null
        c.stop(flush)
    }

    private fun stop() {
        val e = engine
        if (e == null) {
            finish()
            return
        }
        e.requestStop()
    }

    private fun setEvent(intent: Intent) {
        val id = RecorderStatus.recordingId ?: return
        val event = eventFrom(intent)
        runCatching { App.store(this).updateMeta(id) { it.copy(event = event) } }
        RecorderStatus.eventSubject = event?.subject
        getSystemService(NotificationManager::class.java)
            .notify(NOTIFICATION_ID, notification(RecorderStatus.startedAtMillis, event?.subject))
    }

    private fun onEngineFinished(id: String, error: String?) {
        stopLive(flush = true)
        closeRecording(id)
        RecorderStatus.lastError = error
        engine = null
        finish(holdForSync = true)
    }

    /** Sets the end time once; a recording that captured nothing is removed. Idempotent. */
    private fun closeRecording(id: String) {
        val store = App.store(this)
        runCatching {
            if (store.read(id) == null) return
            if (!store.deleteIfEmpty(id)) {
                store.updateMeta(id) { if (it.end == null) it.copy(end = OffsetDateTime.now()) else it }
            }
        }
    }

    private fun finish(holdForSync: Boolean = false) {
        RecorderStatus.recordingId = null
        RecorderStatus.eventSubject = null
        wakeLock?.let { if (it.isHeld) it.release() }
        wakeLock = null
        if (holdForSync && PhoneSync.openWindow(ListenWindows.Kind.AFTER_RECORDING, ListenWindows.AFTER_RECORDING_MS) && holdForSync()) return
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    /**
     * Stays in the foreground (type dataSync) while the after-recording window is open. Returns
     * false when Android refuses the type change; the window then stays open only as long as the
     * process happens to live, and the periodic job is the fallback.
     */
    private fun holdForSync(): Boolean {
        try {
            ServiceCompat.startForeground(
                this, NOTIFICATION_ID, syncNotification(),
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC else 0,
            )
        } catch (e: Exception) {
            return false
        }
        holdingForSync = true
        syncWakeLock = (getSystemService(Context.POWER_SERVICE) as PowerManager)
            .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "musubi:sync")
            .also { it.setReferenceCounted(false); it.acquire(ListenWindows.AFTER_RECORDING_MS + 2 * 60_000L) }
        Thread({
            PhoneSync.awaitWindowEnd(ListenWindows.Kind.AFTER_RECORDING, ListenWindows.AFTER_RECORDING_MS + 60_000L)
            main.post { endSyncHold() }
        }, "musubi-sync-hold").apply { isDaemon = true }.start()
        return true
    }

    private fun endSyncHold() {
        if (!holdingForSync) return
        holdingForSync = false
        syncWakeLock?.let { if (it.isHeld) it.release() }
        syncWakeLock = null
        // A new recording started meanwhile: it owns the foreground now.
        if (engine != null) return
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    /** Android 15 caps dataSync time per day; when it ends the hold, the window ends with it. */
    override fun onTimeout(startId: Int, fgsType: Int) {
        if (fgsType and ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC != 0 && holdingForSync) {
            PhoneSync.closeWindow(ListenWindows.Kind.AFTER_RECORDING)
            endSyncHold()
        }
    }

    override fun onDestroy() {
        // If the system tears the service down, stop the engine so the last chunk is closed.
        stopLive(flush = false)
        engine?.let {
            it.requestStop()
            it.join(3_000)
            RecorderStatus.recordingId?.let { id -> closeRecording(id) }
            RecorderStatus.recordingId = null
        }
        wakeLock?.let { if (it.isHeld) it.release() }
        syncWakeLock?.let { if (it.isHeld) it.release() }
        super.onDestroy()
    }

    private fun notification(startedAt: Long, subject: String?): Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, RecorderService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_mic)
            .setContentTitle(getString(R.string.recording_now))
            .setContentText(subject ?: getString(R.string.ad_hoc))
            .setWhen(startedAt)
            .setUsesChronometer(true)
            .setShowWhen(true)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setContentIntent(open)
            .addAction(R.drawable.ic_stop, getString(R.string.stop), stop)
            .build()
    }

    private fun syncNotification(): Notification {
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_laptop)
            .setContentTitle(getString(R.string.ready_for_mac))
            .setContentText(getString(R.string.ready_for_mac_text))
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setContentIntent(open)
            .build()
    }

    companion object {
        const val ACTION_START = "com.musubi.phone.START"
        const val ACTION_STOP = "com.musubi.phone.STOP"
        const val ACTION_SET_EVENT = "com.musubi.phone.SET_EVENT"
        const val ACTION_LIVE = "com.musubi.phone.LIVE"
        private const val EXTRA_LIVE_ON = "on"
        private const val EXTRA_EVENT_ID = "event_id"
        private const val EXTRA_EVENT_SUBJECT = "event_subject"
        private const val EXTRA_EVENT_START = "event_start"
        const val CHANNEL_ID = "recording"
        private const val NOTIFICATION_ID = 1
        private const val WAKE_LOCK_MAX_MS = 12L * 60 * 60 * 1000

        fun createChannel(ctx: Context) {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                val ch = NotificationChannel(CHANNEL_ID, ctx.getString(R.string.channel_recording), NotificationManager.IMPORTANCE_LOW)
                ctx.getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
            }
        }

        private fun putEvent(i: Intent, e: EventRef?): Intent {
            if (e != null) {
                i.putExtra(EXTRA_EVENT_ID, e.id).putExtra(EXTRA_EVENT_SUBJECT, e.subject)
                    .putExtra(EXTRA_EVENT_START, RecordingMeta.formatTime(e.start))
            }
            return i
        }

        private fun eventFrom(i: Intent): EventRef? {
            val id = i.getStringExtra(EXTRA_EVENT_ID) ?: return null
            val start = i.getStringExtra(EXTRA_EVENT_START)?.let { runCatching { RecordingMeta.parseTime(it) }.getOrNull() } ?: return null
            return EventRef(id, i.getStringExtra(EXTRA_EVENT_SUBJECT) ?: "", start)
        }

        fun start(ctx: Context, event: EventRef?) {
            ContextCompat.startForegroundService(ctx, putEvent(Intent(ctx, RecorderService::class.java).setAction(ACTION_START), event))
        }

        fun stop(ctx: Context) {
            ctx.startService(Intent(ctx, RecorderService::class.java).setAction(ACTION_STOP))
        }

        fun setLive(ctx: Context, on: Boolean) {
            ctx.startService(Intent(ctx, RecorderService::class.java).setAction(ACTION_LIVE).putExtra(EXTRA_LIVE_ON, on))
        }

        fun setEvent(ctx: Context, event: EventRef?) {
            ctx.startService(putEvent(Intent(ctx, RecorderService::class.java).setAction(ACTION_SET_EVENT), event))
        }
    }
}

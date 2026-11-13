package com.musubi.phone.rec

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.Message
import android.os.Messenger
import android.os.SystemClock
import android.util.Log
import com.musubi.phone.core.DropQueue
import com.musubi.phone.core.LiveModel
import com.musubi.phone.core.LiveTextFile
import com.musubi.phone.core.PcmBlock
import com.musubi.phone.core.Resampler48kTo16k
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger

/** What the Recording screen shows about live text. Written by the controller, read by the UI. */
object LiveStatus {
    enum class State { OFF, STARTING, ON }

    @Volatile var state = State.OFF
    @Volatile var partial = ""
    /** Shown once by the UI (why live text turned off), then cleared. */
    @Volatile var message: String? = null
    private val lines = ArrayDeque<String>()

    fun lines(): List<String> = synchronized(lines) { lines.toList() }

    fun addLine(line: String) = synchronized(lines) {
        lines.addLast(line)
        while (lines.size > MAX_LINES) lines.removeFirst()
    }

    fun clear() {
        synchronized(lines) { lines.clear() }
        partial = ""
    }

    private const val MAX_LINES = 50
}

/** The owner's last choice for the Live text switch. */
class LivePrefs(ctx: Context) {
    private val p = ctx.getSharedPreferences("live_text", Context.MODE_PRIVATE)
    var on: Boolean
        get() = p.getBoolean("on", false)
        set(v) = p.edit().putBoolean("on", v).apply()
}

/**
 * Live text for one recording. The recorder thread calls [offer] with each PCM read; that only
 * copies the bytes into a bounded queue and never blocks (a full queue drops the block, for live
 * text only). A feeder thread resamples to 16 kHz and sends the audio to [LiveTextService] in its
 * own process, keeping at most [WINDOW] blocks unanswered. Any failure — the service dying, an
 * error from it, no answer — turns live text off with a message; the recording is not touched.
 */
class LiveTextController(
    private val ctx: Context,
    recordingDir: File,
    private val onStopped: (LiveTextController) -> Unit,
) {
    private val queue = DropQueue<PcmBlock>(QUEUE_BLOCKS)
    private val file = LiveTextFile(File(recordingDir, LiveTextFile.NAME))
    private val active = AtomicBoolean(true)
    private val inFlight = AtomicInteger(0)
    private val finished = CountDownLatch(1)
    private val results = HandlerThread("live-results").also { it.start() }
    private val incoming = Messenger(Handler(results.looper) { onResult(it); true })
    @Volatile private var remote: Messenger? = null
    @Volatile private var ready = false
    @Volatile private var lastAckAt = 0L
    @Volatile private var fileFull = false
    @Volatile private var flushOnStop = false
    private var bound = false
    private var feeder: Thread? = null

    // Speed, for the log line at the end (never any text).
    @Volatile private var decodeNs = 0L
    @Volatile private var audioMs = 0L

    /** Recognizer time per second of audio sent (real-time factor), and blocks dropped so far. */
    val realTimeFactor: Double get() = if (audioMs > 0) decodeNs / 1e6 / audioMs else 0.0
    val droppedBlocks: Long get() = queue.dropped

    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName?, binder: IBinder) {
            runCatching { binder.linkToDeath({ fail("Live text stopped: its engine quit") }, 0) }
            val m = Messenger(binder)
            remote = m
            val start = Message.obtain(null, LiveProtocol.START)
            start.replyTo = incoming
            start.data = Bundle().apply { putString(LiveProtocol.KEY_DIR, LiveModel.dir(ctx.filesDir).path) }
            if (runCatching { m.send(start) }.isFailure) fail("Live text could not start")
        }

        override fun onServiceDisconnected(name: ComponentName?) = fail("Live text stopped: its engine quit")
    }

    fun start() {
        current = this
        LiveStatus.partial = ""
        LiveStatus.state = LiveStatus.State.STARTING
        val ok = runCatching {
            ctx.bindService(Intent(ctx, LiveTextService::class.java), connection, Context.BIND_AUTO_CREATE)
        }.getOrDefault(false)
        bound = ok
        if (!ok) {
            fail("Live text could not start")
            return
        }
        feeder = Thread({ feedLoop() }, "live-feeder").also {
            it.priority = Thread.MIN_PRIORITY
            it.start()
        }
    }

    /** Recorder thread. Never blocks, never throws. */
    fun offer(pcm: ByteArray, len: Int, startSample: Long) {
        if (!active.get() || !ready) return
        try {
            queue.offer(PcmBlock(pcm.copyOf(len), len, startSample))
        } catch (_: Throwable) {
            // Out of memory for a 4 KB copy: skip this block; the recording goes on.
        }
    }

    /** Turned off by the owner, or the recording stopped ([flush]: let the model finish its last line). */
    fun stop(flush: Boolean) {
        flushOnStop = flush
        if (!active.compareAndSet(true, false)) return
        Thread({ shutdown(flush, null) }, "live-stop").start()
    }

    private fun fail(message: String) {
        if (!active.compareAndSet(true, false)) return
        Thread({ shutdown(false, message) }, "live-stop").start()
    }

    private fun shutdown(flush: Boolean, error: String?) {
        feeder?.let { if (it !== Thread.currentThread()) it.join(2_000) }
        val r = remote
        if (flush && error == null && r != null && ready) {
            val m = Message.obtain(null, LiveProtocol.FINISH)
            if (runCatching { r.send(m) }.isSuccess) finished.await(3, TimeUnit.SECONDS)
        }
        if (bound) runCatching { ctx.unbindService(connection) }
        bound = false
        remote = null
        ready = false
        queue.clear()
        results.quitSafely()
        // A quick off-and-on starts a new controller before this one is done: leave its state alone.
        if (current === this) {
            current = null
            LiveStatus.partial = ""
            LiveStatus.state = LiveStatus.State.OFF
        }
        if (error != null) LiveStatus.message = error
        val rtf = realTimeFactor
        Log.i(TAG, "live text off: audio %.1f s, decode %.1f s, rtf %.3f, dropped %d blocks, error %s"
            .format(audioMs / 1000.0, decodeNs / 1e9, rtf, queue.dropped, error != null))
        onStopped(this)
    }

    private fun feedLoop() {
        val resampler = Resampler48kTo16k()
        var pending = FloatArray(0)
        var pendingStartMs = -1L
        try {
            while (active.get()) {
                if (!ready) {
                    Thread.sleep(20)
                    continue
                }
                if (inFlight.get() >= WINDOW) {
                    // The recognizer is behind: the queue fills and the recorder's blocks are dropped.
                    if (SystemClock.elapsedRealtime() - lastAckAt > STALL_MS) {
                        fail("Live text stopped: the recognizer stopped answering")
                        return
                    }
                    Thread.sleep(10)
                    continue
                }
                val block = queue.poll(100) ?: continue
                val samples = resampler.process(block.bytes, block.len)
                if (pendingStartMs < 0) pendingStartMs = block.startSample * 1000 / RecorderConfig.SAMPLE_RATE
                pending = if (pending.isEmpty()) samples else pending + samples
                if (pending.size < SEND_SAMPLES) continue
                val r = remote ?: continue
                send(r, pending, pendingStartMs)
                pending = FloatArray(0)
                pendingStartMs = -1
            }
            // Stopped with flush: the last fraction of a second still goes to the recognizer.
            val r = remote
            if (flushOnStop && ready && r != null) {
                while (true) {
                    val block = queue.poll(0) ?: break
                    if (pendingStartMs < 0) pendingStartMs = block.startSample * 1000 / RecorderConfig.SAMPLE_RATE
                    pending += resampler.process(block.bytes, block.len)
                }
                if (pending.isNotEmpty()) send(r, pending, pendingStartMs)
            }
        } catch (_: InterruptedException) {
        } catch (t: Throwable) {
            fail("Live text stopped: ${t.javaClass.simpleName}")
        }
    }

    private fun send(r: Messenger, pcm: FloatArray, startMs: Long) {
        val m = Message.obtain(null, LiveProtocol.AUDIO)
        m.data = Bundle().apply {
            putFloatArray(LiveProtocol.KEY_PCM, pcm)
            putLong(LiveProtocol.KEY_MS, startMs)
        }
        if (inFlight.getAndIncrement() == 0) lastAckAt = SystemClock.elapsedRealtime()
        audioMs += pcm.size * 1000L / LiveProtocol.SAMPLE_RATE
        r.send(m)
    }

    private fun onResult(msg: Message) {
        if (!active.get() && msg.what != LiveProtocol.DONE && msg.what != LiveProtocol.FINAL) return
        val d = msg.data
        when (msg.what) {
            LiveProtocol.READY -> {
                lastAckAt = SystemClock.elapsedRealtime()
                ready = true
                LiveStatus.state = LiveStatus.State.ON
                Log.i(TAG, "live text ready, model checked and loaded in ${d.getLong(LiveProtocol.KEY_LOAD_MS)} ms")
            }
            LiveProtocol.PARTIAL -> if (active.get()) LiveStatus.partial = display(d.getString(LiveProtocol.KEY_TEXT) ?: "")
            LiveProtocol.FINAL -> {
                val text = display(d.getString(LiveProtocol.KEY_TEXT) ?: "")
                val ms = d.getLong(LiveProtocol.KEY_MS)
                if (current === this) LiveStatus.partial = ""
                LiveStatus.addLine("[${LiveTextFile.formatOffset(ms)}] $text")
                if (!fileFull) {
                    val ok = runCatching { file.append(ms, text) }.getOrElse {
                        fail("Live text stopped: could not save live.txt")
                        return
                    }
                    if (!ok) {
                        fileFull = true
                        LiveStatus.message = "live.txt is full; new lines are shown but not saved"
                    }
                }
            }
            LiveProtocol.ACK -> {
                inFlight.decrementAndGet()
                lastAckAt = SystemClock.elapsedRealtime()
                decodeNs += d.getLong(LiveProtocol.KEY_DECODE_NS)
            }
            LiveProtocol.DONE -> finished.countDown()
            LiveProtocol.ERROR -> {
                if (d.getInt(LiveProtocol.KEY_CODE) == LiveProtocol.ERR_CORRUPT) {
                    runCatching { LiveModel.delete(LiveModel.dir(ctx.filesDir)) }
                }
                fail(d.getString(LiveProtocol.KEY_MESSAGE) ?: "Live text stopped")
            }
        }
    }

    companion object {
        private const val TAG = "MusubiLive"
        @Volatile private var current: LiveTextController? = null
        /** ~2 s of recorder reads (4 KB each at 48 kHz). */
        const val QUEUE_BLOCKS = 48
        /** Unanswered blocks allowed in the live process (each ~200 ms of audio). */
        const val WINDOW = 4
        private const val SEND_SAMPLES = LiveProtocol.SAMPLE_RATE / 5
        private const val STALL_MS = 15_000L

        /** The model writes upper case only; show it in sentence case. */
        fun display(text: String): String {
            val t = LiveTextFile.clean(text).lowercase()
            return t.replace(Regex("\\bi\\b"), "I").replaceFirstChar { it.uppercaseChar() }
        }
    }
}

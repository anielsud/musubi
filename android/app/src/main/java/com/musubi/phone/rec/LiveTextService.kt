package com.musubi.phone.rec

import android.app.Service
import android.content.Intent
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.os.IBinder
import android.os.Message
import android.os.Messenger
import android.os.Process
import android.os.SystemClock
import com.k2fsa.sherpa.onnx.EndpointConfig
import com.k2fsa.sherpa.onnx.FeatureConfig
import com.k2fsa.sherpa.onnx.OnlineModelConfig
import com.k2fsa.sherpa.onnx.OnlineRecognizer
import com.k2fsa.sherpa.onnx.OnlineRecognizerConfig
import com.k2fsa.sherpa.onnx.OnlineStream
import com.k2fsa.sherpa.onnx.OnlineTransducerModelConfig
import com.musubi.phone.core.LiveModel
import java.io.File

/** Messages between the recorder process and the live-text process. */
object LiveProtocol {
    // recorder → live
    const val START = 1
    const val AUDIO = 2
    const val FINISH = 3

    // live → recorder
    const val READY = 10
    const val PARTIAL = 11
    const val FINAL = 12
    const val ACK = 13
    const val DONE = 14
    const val ERROR = 15

    const val KEY_DIR = "dir"
    const val KEY_PCM = "pcm"
    const val KEY_MS = "ms"
    const val KEY_TEXT = "text"
    const val KEY_DECODE_NS = "decode_ns"
    const val KEY_LOAD_MS = "load_ms"
    const val KEY_MESSAGE = "message"
    const val KEY_CODE = "code"

    const val ERR_FAILED = 0
    const val ERR_CORRUPT = 1

    const val SAMPLE_RATE = 16_000
}

/**
 * Runs the speech recognizer in its own process (android:process=":live"), so that a crash in
 * native code can end live text but never the recording: the recorder process only sees the
 * binding die and turns live text off. Bound by [LiveTextController]; released when unbound.
 * Transcript text is passed back to the recorder process only; it is never logged.
 */
class LiveTextService : Service() {
    private lateinit var thread: HandlerThread
    private lateinit var handler: Handler
    private var client: Messenger? = null
    private var recognizer: OnlineRecognizer? = null
    private var stream: OnlineStream? = null
    private var lastPartial = ""
    private var segmentStartMs = -1L
    private var lastBlockMs = 0L

    override fun onCreate() {
        super.onCreate()
        // Background priority: the recognizer must never compete with the recorder for CPU.
        thread = HandlerThread("live-recognizer", Process.THREAD_PRIORITY_BACKGROUND).also { it.start() }
        handler = Handler(thread.looper) { msg ->
            try {
                handle(msg)
            } catch (t: Throwable) {
                release()
                error(LiveProtocol.ERR_FAILED, "Live text stopped: the recognizer failed")
            }
            true
        }
    }

    override fun onBind(intent: Intent?): IBinder = Messenger(handler).binder

    override fun onUnbind(intent: Intent?): Boolean {
        handler.post { release(); client = null }
        return false
    }

    override fun onDestroy() {
        handler.post { release() }
        thread.quitSafely()
        super.onDestroy()
    }

    private fun handle(msg: Message) {
        when (msg.what) {
            LiveProtocol.START -> start(msg)
            LiveProtocol.AUDIO -> audio(msg.data)
            LiveProtocol.FINISH -> finish()
        }
    }

    private fun start(msg: Message) {
        client = msg.replyTo
        release()
        val dir = File(msg.data.getString(LiveProtocol.KEY_DIR) ?: "")
        // Only the app's own model folder is ever loaded.
        val expected = LiveModel.dir(filesDir).canonicalFile
        if (dir.canonicalFile != expected) {
            error(LiveProtocol.ERR_FAILED, "Live text model folder is not valid")
            return
        }
        val t0 = SystemClock.elapsedRealtime()
        if (LiveModel.firstCorrupt(dir) != null) {
            error(LiveProtocol.ERR_CORRUPT, "The live text model is damaged. Download it again in Settings.")
            return
        }
        val config = OnlineRecognizerConfig(
            featConfig = FeatureConfig(sampleRate = LiveProtocol.SAMPLE_RATE, featureDim = 80),
            modelConfig = OnlineModelConfig(
                transducer = OnlineTransducerModelConfig(
                    encoder = File(dir, LiveModel.ENCODER.name).path,
                    decoder = File(dir, LiveModel.DECODER.name).path,
                    joiner = File(dir, LiveModel.JOINER.name).path,
                ),
                tokens = File(dir, LiveModel.TOKENS.name).path,
                numThreads = 1,
                modelType = "zipformer",
            ),
            endpointConfig = EndpointConfig(),
            enableEndpoint = true,
            decodingMethod = "greedy_search",
        )
        val r = OnlineRecognizer(null, config)
        recognizer = r
        val s = r.createStream()
        stream = s
        // A fresh stream misses the first ~2 s of speech (measured 2026-11-13 with this model,
        // on the Mac too); a second of silence first, then a reset, avoids that.
        s.acceptWaveform(FloatArray(LiveProtocol.SAMPLE_RATE), LiveProtocol.SAMPLE_RATE)
        while (r.isReady(s)) r.decode(s)
        r.reset(s)
        lastPartial = ""
        segmentStartMs = -1
        send(LiveProtocol.READY, Bundle().apply { putLong(LiveProtocol.KEY_LOAD_MS, SystemClock.elapsedRealtime() - t0) })
    }

    private fun audio(data: Bundle) {
        val r = recognizer
        val s = stream
        val pcm = data.getFloatArray(LiveProtocol.KEY_PCM)
        val ms = data.getLong(LiveProtocol.KEY_MS)
        var decodeNs = 0L
        if (r != null && s != null && pcm != null && pcm.isNotEmpty()) {
            val t0 = System.nanoTime()
            s.acceptWaveform(pcm, LiveProtocol.SAMPLE_RATE)
            while (r.isReady(s)) r.decode(s)
            val text = r.getResult(s).text.trim()
            decodeNs = System.nanoTime() - t0
            lastBlockMs = ms
            // A line starts where its first words appeared, less the model's look-ahead.
            if (text.isNotEmpty() && segmentStartMs < 0) segmentStartMs = (ms - LOOKAHEAD_MS).coerceAtLeast(0)
            if (r.isEndpoint(s)) {
                if (text.isNotEmpty()) emitFinal(text)
                r.reset(s)
                if (lastPartial.isNotEmpty()) sendText(LiveProtocol.PARTIAL, "")
                lastPartial = ""
                segmentStartMs = -1
            } else if (text != lastPartial) {
                lastPartial = text
                sendText(LiveProtocol.PARTIAL, text)
            }
        }
        send(LiveProtocol.ACK, Bundle().apply { putLong(LiveProtocol.KEY_DECODE_NS, decodeNs) })
    }

    /** End of recording: flush what the model still holds, report it, then release. */
    private fun finish() {
        val r = recognizer
        val s = stream
        if (r != null && s != null) {
            s.acceptWaveform(FloatArray(LiveProtocol.SAMPLE_RATE / 2), LiveProtocol.SAMPLE_RATE)
            s.inputFinished()
            while (r.isReady(s)) r.decode(s)
            val text = r.getResult(s).text.trim()
            if (text.isNotEmpty()) emitFinal(text)
        }
        release()
        send(LiveProtocol.DONE, Bundle())
    }

    private fun emitFinal(text: String) {
        val b = Bundle()
        b.putString(LiveProtocol.KEY_TEXT, text)
        b.putLong(LiveProtocol.KEY_MS, if (segmentStartMs >= 0) segmentStartMs else lastBlockMs)
        send(LiveProtocol.FINAL, b)
    }

    private fun sendText(what: Int, text: String) = send(what, Bundle().apply { putString(LiveProtocol.KEY_TEXT, text) })

    private fun error(code: Int, message: String) {
        send(LiveProtocol.ERROR, Bundle().apply {
            putInt(LiveProtocol.KEY_CODE, code)
            putString(LiveProtocol.KEY_MESSAGE, message)
        })
    }

    private fun send(what: Int, data: Bundle) {
        val c = client ?: return
        val m = Message.obtain(null, what)
        m.data = data
        runCatching { c.send(m) } // the recorder process went away: nothing to tell
    }

    private fun release() {
        runCatching { stream?.release() }
        runCatching { recognizer?.release() }
        stream = null
        recognizer = null
    }

    companion object {
        private const val LOOKAHEAD_MS = 600L
    }
}

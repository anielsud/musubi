package com.musubi.phone

import android.Manifest
import android.app.ActivityManager
import android.content.Context
import android.content.Intent
import android.os.ParcelFileDescriptor
import android.os.Process
import android.util.Log
import androidx.test.platform.app.InstrumentationRegistry
import com.musubi.phone.core.Adts
import com.musubi.phone.core.LiveModel
import com.musubi.phone.core.LiveTextFile
import com.musubi.phone.core.ModelDownloader
import com.musubi.phone.rec.LivePrefs
import com.musubi.phone.rec.LiveStatus
import com.musubi.phone.rec.LiveTextController
import com.musubi.phone.rec.RecorderService
import com.musubi.phone.rec.RecorderStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Test
import org.junit.runners.MethodSorters
import java.io.File
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.time.Duration
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Live text on a device or emulator. Needs a synthetic 48 kHz mono 16-bit WAV pushed to
 * /data/local/tmp/live-test.wav (invented sentences only) and network access for the model.
 * Prints the recognized text and real-time factor to logcat (tag LiveTextDeviceTest): test
 * output, not app logging.
 */
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class LiveTextDeviceTest {
    private val inst = InstrumentationRegistry.getInstrumentation()
    private val ctx: Context = inst.targetContext
    private val modelDir get() = LiveModel.dir(ctx.filesDir)

    @Before
    fun model() {
        if (LiveModel.firstCorrupt(modelDir) == null) return
        ModelDownloader(modelDir, LiveModel.FILES, LiveModel::url).run { _, _ -> }
        assertEquals(null, LiveModel.firstCorrupt(modelDir))
    }

    private fun wavPcm(): ByteArray {
        val pfd = inst.uiAutomation.executeShellCommand("cat /data/local/tmp/live-test.wav")
        val bytes = ParcelFileDescriptor.AutoCloseInputStream(pfd).use { it.readBytes() }
        val bb = ByteBuffer.wrap(bytes).order(ByteOrder.LITTLE_ENDIAN)
        // Walk the RIFF chunks to "fmt " and "data".
        var pos = 12
        var data: ByteArray? = null
        while (pos + 8 <= bytes.size) {
            val id = String(bytes, pos, 4)
            val len = bb.getInt(pos + 4)
            if (id == "fmt ") {
                assertEquals(1, bb.getShort(pos + 10).toInt()) // mono
                assertEquals(48_000, bb.getInt(pos + 12))
                assertEquals(16, bb.getShort(pos + 22).toInt())
            }
            if (id == "data") data = bytes.copyOfRange(pos + 8, minOf(bytes.size, pos + 8 + len))
            pos += 8 + len + (len and 1)
        }
        return data ?: error("no data chunk")
    }

    private class Run(val c: LiveTextController, val stopped: CountDownLatch)

    private fun controller(dir: File): Run {
        val latch = CountDownLatch(1)
        val c = LiveTextController(ctx, dir) { latch.countDown() }
        c.start()
        return Run(c, latch)
    }

    private fun waitFor(timeoutMs: Long, cond: () -> Boolean): Boolean {
        val end = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < end) {
            if (cond()) return true
            Thread.sleep(20)
        }
        return cond()
    }

    @Test
    fun a_recognizesSyntheticSpeechAtRecorderPace() {
        val pcm = wavPcm()
        val dir = File(ctx.cacheDir, "live-test-a").apply { deleteRecursively(); mkdirs() }
        val run = controller(dir)
        assertTrue("live text did not start", waitFor(20_000) { LiveStatus.state == LiveStatus.State.ON })
        // Same block size and pace as the recorder: 4096 bytes every ~42.7 ms.
        val block = 4096
        val t0 = System.nanoTime()
        var off = 0
        var sample = 0L
        val partials = mutableListOf<String>()
        while (off < pcm.size) {
            val len = minOf(block, pcm.size - off)
            run.c.offer(pcm.copyOfRange(off, off + len), len, sample)
            sample += len / 2
            off += len
            val due = t0 + sample * 1_000_000_000L / 48_000
            val p = LiveStatus.partial
            if (p != partials.lastOrNull()) partials += p
            val wait = due - System.nanoTime()
            if (wait > 0) Thread.sleep(wait / 1_000_000, (wait % 1_000_000).toInt())
        }
        Thread.sleep(500)
        val rtf = run.c.realTimeFactor
        val dropped = run.c.droppedBlocks
        run.c.stop(flush = true)
        assertTrue(run.stopped.await(10, TimeUnit.SECONDS))
        val text = LiveTextFile.read(File(dir, LiveTextFile.NAME))
        Log.i(TAG, "audio %.1f s, rtf %.3f, dropped %d blocks".format(sample / 48_000.0, rtf, dropped))
        partials.forEachIndexed { i, p -> Log.i(TAG, "partial $i: $p") }
        LiveStatus.lines().forEach { Log.i(TAG, "screen: $it") }
        text.lines().forEach { Log.i(TAG, "live.txt: $it") }
        assertTrue(text.lines().count { it.startsWith("[0:00:") } >= 2)
        assertTrue(text.startsWith("[0:00:00] The weather"))
        assertTrue(text.contains("draft budget"))
        assertTrue(text.trimEnd().endsWith("take forward"))
        assertEquals(0L, dropped)
    }

    @Test
    fun b_feedingFasterThanRealTimeDropsInsteadOfBlocking() {
        val pcm = wavPcm()
        val dir = File(ctx.cacheDir, "live-test-b").apply { deleteRecursively(); mkdirs() }
        val run = controller(dir)
        assertTrue(waitFor(20_000) { LiveStatus.state == LiveStatus.State.ON })
        val t0 = System.nanoTime()
        var off = 0
        var sample = 0L
        repeat(3) {
            off = 0
            while (off < pcm.size) {
                val len = minOf(4096, pcm.size - off)
                run.c.offer(pcm.copyOfRange(off, off + len), len, sample)
                sample += len / 2
                off += len
            }
        }
        val ms = (System.nanoTime() - t0) / 1_000_000
        Log.i(TAG, "offered %.1f s of audio in %d ms, dropped %d blocks".format(sample / 48_000.0, ms, run.c.droppedBlocks))
        assertTrue("offer blocked: $ms ms", ms < 2_000)
        assertTrue(run.c.droppedBlocks > 0)
        run.c.stop(flush = false)
        assertTrue(run.stopped.await(10, TimeUnit.SECONDS))
        assertEquals(LiveStatus.State.OFF, LiveStatus.state)
    }

    @Test
    fun c_engineCrashTurnsLiveTextOffAndLeavesThisProcessAlone() {
        val dir = File(ctx.cacheDir, "live-test-c").apply { deleteRecursively(); mkdirs() }
        val run = controller(dir)
        assertTrue(waitFor(20_000) { LiveStatus.state == LiveStatus.State.ON })
        val am = ctx.getSystemService(ActivityManager::class.java)
        val live = am.runningAppProcesses.first { it.processName.endsWith(":live") }
        assertTrue(live.pid != Process.myPid())
        Process.killProcess(live.pid)
        assertTrue(run.stopped.await(10, TimeUnit.SECONDS))
        assertEquals(LiveStatus.State.OFF, LiveStatus.state)
        assertTrue(LiveStatus.message!!.contains("quit"))
        LiveStatus.message = null
    }

    @Test
    fun d_corruptModelIsRefusedAndRemoved() {
        val tokens = File(modelDir, LiveModel.TOKENS.name)
        tokens.writeBytes(ByteArray(LiveModel.TOKENS.size.toInt()) { 'x'.code.toByte() })
        val dir = File(ctx.cacheDir, "live-test-d").apply { deleteRecursively(); mkdirs() }
        val run = controller(dir)
        assertTrue(run.stopped.await(30, TimeUnit.SECONDS))
        assertTrue(LiveStatus.message!!.contains("damaged"))
        LiveStatus.message = null
        assertFalse(modelDir.exists())
    }

    /** The real recorder with live text switched on and off mid-recording, then with the model gone. */
    @Test
    fun e_recordingIsUnaffectedByLiveText() {
        inst.uiAutomation.grantRuntimePermission(ctx.packageName, Manifest.permission.RECORD_AUDIO)
        LivePrefs(ctx).on = false
        val activity = inst.startActivitySync(Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        val store = App.store(ctx)

        val wall0 = System.currentTimeMillis()
        inst.runOnMainSync { RecorderService.start(activity, null) }
        assertTrue(waitFor(5_000) { RecorderStatus.isRecording })
        val id = RecorderStatus.recordingId!!
        Thread.sleep(3_000)
        RecorderService.setLive(activity, true)
        assertTrue(waitFor(20_000) { LiveStatus.state == LiveStatus.State.ON })
        Thread.sleep(4_000)
        RecorderService.setLive(activity, false)
        assertTrue(waitFor(10_000) { LiveStatus.state == LiveStatus.State.OFF })
        Thread.sleep(2_000)
        RecorderService.setLive(activity, true)
        assertTrue(waitFor(20_000) { LiveStatus.state == LiveStatus.State.ON })
        // Kill the recognizer's process mid-recording.
        val am = ctx.getSystemService(ActivityManager::class.java)
        Process.killProcess(am.runningAppProcesses.first { it.processName.endsWith(":live") }.pid)
        assertTrue(waitFor(10_000) { LiveStatus.state == LiveStatus.State.OFF })
        LiveStatus.message = null
        // Model missing: the switch is refused with a message, the recording carries on.
        LiveModel.delete(modelDir)
        RecorderService.setLive(activity, true)
        assertTrue(waitFor(5_000) { LiveStatus.message != null })
        assertTrue(LiveStatus.message!!.contains("model"))
        LiveStatus.message = null
        assertEquals(LiveStatus.State.OFF, LiveStatus.state)
        Thread.sleep(2_000)
        assertTrue(RecorderStatus.isRecording)
        RecorderService.stop(activity)
        assertTrue(waitFor(10_000) { !RecorderStatus.isRecording })
        val wallMs = System.currentTimeMillis() - wall0

        val r = store.read(id)!!
        assertNotNull(r.meta.end)
        val audioMs = r.meta.chunks.sumOf { c -> countFrames(File(r.dir, c.name)) } * Adts.SAMPLES_PER_FRAME * 1000L / 48_000
        Log.i(TAG, "recording: wall %d ms, audio %d ms, chunks %d".format(wallMs, audioMs, r.meta.chunks.size))
        // Continuous audio for the whole recording: no gap where live text was switched or crashed.
        assertTrue("audio $audioMs ms vs wall $wallMs ms", audioMs > wallMs - 2_500 && audioMs <= wallMs)
        assertTrue(Duration.between(r.meta.start, r.meta.end).toMillis() > audioMs - 2_000)
        inst.runOnMainSync { activity.finish() }
    }

    private fun countFrames(f: File): Long {
        val b = f.readBytes()
        var pos = 0
        var n = 0L
        while (pos + 7 <= b.size) {
            val len = ((b[pos + 3].toInt() and 0x03) shl 11) or ((b[pos + 4].toInt() and 0xFF) shl 3) or ((b[pos + 5].toInt() and 0xE0) ushr 5)
            if (len < 7) break
            pos += len
            n++
        }
        return n
    }

    companion object {
        private const val TAG = "LiveTextDeviceTest"
    }
}

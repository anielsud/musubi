package com.musubi.phone.core

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.File
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.sin
import kotlin.math.sqrt

class ResamplerTest {
    private fun sinePcm(freq: Double, seconds: Double, amp: Double = 0.5): ByteArray {
        val n = (48_000 * seconds).toInt()
        val out = ByteArray(n * 2)
        for (i in 0 until n) {
            val s = (amp * 32767 * sin(2 * PI * freq * i / 48_000)).toInt()
            out[2 * i] = (s and 0xFF).toByte()
            out[2 * i + 1] = (s shr 8).toByte()
        }
        return out
    }

    private fun rms(x: FloatArray, from: Int) = sqrt(x.drop(from).sumOf { it.toDouble() * it } / (x.size - from))

    @Test
    fun outputsOneSampleForEveryThree() {
        val out = Resampler48kTo16k().process(sinePcm(440.0, 1.0), 96_000)
        assertEquals(16_000, out.size)
    }

    @Test
    fun keepsSpeechBandAndRemovesWhatWouldAlias() {
        val speech = Resampler48kTo16k().process(sinePcm(1_000.0, 0.5), 48_000)
        // A 0.5-amplitude sine has RMS 0.354; the pass band keeps it (within 2%).
        assertEquals(0.354, rms(speech, 100), 0.007)
        val high = Resampler48kTo16k().process(sinePcm(12_000.0, 0.5), 48_000)
        // 12 kHz would fold to 4 kHz at 16 kHz: it must be at least 40 dB down.
        assertTrue(rms(high, 100) < 0.354 / 100)
    }

    @Test
    fun blockSizesDoNotChangeTheResult() {
        val pcm = sinePcm(700.0, 0.3)
        val whole = Resampler48kTo16k().process(pcm, pcm.size)
        val r = Resampler48kTo16k()
        val parts = mutableListOf<Float>()
        var off = 0
        val sizes = intArrayOf(4096, 2, 6, 4094, 1000, 3070, 8192)
        var i = 0
        while (off < pcm.size) {
            val len = minOf(sizes[i++ % sizes.size], pcm.size - off)
            parts += r.process(pcm.copyOfRange(off, off + len), len).toList()
            off += len
        }
        assertEquals(whole.size, parts.size)
        for (k in whole.indices) assertEquals(whole[k], parts[k], 1e-6f)
    }

    @Test
    fun silenceStaysSilent() {
        val out = Resampler48kTo16k().process(ByteArray(9_600), 9_600)
        assertTrue(out.all { it == 0f })
    }
}

class DropQueueTest {
    @Test
    fun fullQueueDropsNewItemsWithoutBlocking() {
        val q = DropQueue<Int>(3)
        val t0 = System.nanoTime()
        val accepted = (1..10).map { q.offer(it) }
        val ms = (System.nanoTime() - t0) / 1_000_000
        assertEquals(listOf(true, true, true) + List(7) { false }, accepted)
        assertEquals(7L, q.dropped)
        assertTrue("offer must not wait", ms < 100)
        // The oldest audio is kept; drops are the newest.
        assertEquals(1, q.poll(0))
        assertEquals(2, q.poll(0))
        assertTrue(q.offer(11))
        assertEquals(3, q.poll(0))
        assertEquals(11, q.poll(0))
        assertNull(q.poll(0))
    }

    @Test
    fun slowConsumerNeverStallsTheProducer() {
        val q = DropQueue<ByteArray>(48)
        val consumer = Thread {
            while (true) {
                q.poll(50) ?: continue
                try { Thread.sleep(20) } catch (_: InterruptedException) { return@Thread }
            }
        }
        consumer.start()
        val t0 = System.nanoTime()
        repeat(5_000) { q.offer(ByteArray(4096)) }
        val ms = (System.nanoTime() - t0) / 1_000_000
        consumer.interrupt()
        assertTrue("producer took $ms ms", ms < 2_000)
        assertTrue(q.dropped > 4_000)
    }
}

class LiveTextFileTest {
    @Test
    fun appendsLinesWithOffsets() {
        val dir = TestUtil.tempDir()
        val f = LiveTextFile(File(dir, LiveTextFile.NAME))
        assertTrue(f.append(1_500, "Hello there"))
        assertTrue(f.append(3_723_000, "second\nline\twith  controls"))
        assertTrue(f.append(5_000, "   "))
        assertEquals("[0:00:01] Hello there\n[1:02:03] second line with controls\n", LiveTextFile.read(f.file))
    }

    @Test
    fun stopsAtTheSizeCap() {
        val f = LiveTextFile(File(TestUtil.tempDir(), LiveTextFile.NAME), maxBytes = 40)
        assertTrue(f.append(0, "first line of text"))
        assertFalse(f.append(1_000, "this one would pass the cap"))
        assertEquals("[0:00:00] first line of text\n", LiveTextFile.read(f.file))
    }

    @Test
    fun deletedWithTheAudioAndNeverUploaded() {
        val store = RecordingStore(TestUtil.tempDir(), TestUtil.clockAt(TestUtil.T0.plusHours(2)))
        val r = TestUtil.finishedRecording(store, 2)
        LiveTextFile(File(r.dir, LiveTextFile.NAME)).append(0, "invented sentence")
        assertTrue(store.liveTextFile(r.id) != null)

        // The Mac sees meta.json and chunks only (SyncSessionTest.liveTextIsNeverListedOrServed).
        assertFalse(store.metaBytes(r.id).toString(Charsets.UTF_8).contains("live"))

        store.deleteAudio(r.id)
        assertNull(store.liveTextFile(r.id))
        assertTrue(File(r.dir, RecordingStore.META).isFile)
    }
}

class ModelDownloaderTest {
    private val content = "invented model bytes".toByteArray()
    private val good = ModelFile("m.onnx", content.size.toLong(), Hashing.sha256Hex(content))

    private class FakeConn(url: URL, private val code: Int, private val body: ByteArray, private val location: String? = null) :
        HttpURLConnection(url) {
        override fun connect() {}
        override fun disconnect() {}
        override fun usingProxy() = false
        override fun getResponseCode() = code
        override fun getHeaderField(name: String?) = if (name == "Location") location else null
        override fun getContentLengthLong() = body.size.toLong()
        override fun getInputStream(): InputStream = ByteArrayInputStream(body)
    }

    private fun downloader(dir: File, file: ModelFile, serve: (URL) -> HttpURLConnection) =
        ModelDownloader(dir, listOf(file), { "https://example.invalid/${it.name}" }, serve)

    @Test
    fun followsHttpsRedirectsAndKeepsAVerifiedFile() {
        val dir = TestUtil.tempDir()
        downloader(dir, good) { u ->
            if (u.host == "example.invalid") FakeConn(u, 302, ByteArray(0), "https://cdn.example.invalid/x") else FakeConn(u, 200, content)
        }.run { _, _ -> }
        assertArrayEquals(content, File(dir, "m.onnx").readBytes())
        assertTrue(dir.listFiles()!!.none { it.name.endsWith(ModelDownloader.PART) })
    }

    @Test
    fun refusesAPlainHttpRedirect() {
        val dir = TestUtil.tempDir()
        val e = runCatching {
            downloader(dir, good) { u -> FakeConn(u, 302, ByteArray(0), "http://cdn.example.invalid/x") }.run { _, _ -> }
        }.exceptionOrNull()
        assertTrue(e is ModelDownloader.DownloadError)
        assertFalse(File(dir, "m.onnx").exists())
    }

    @Test
    fun wrongHashOrSizeLeavesNothingBehind() {
        val dir = TestUtil.tempDir()
        val tampered = content.copyOf().also { it[0] = 'X'.code.toByte() }
        assertTrue(runCatching { downloader(dir, good) { u -> FakeConn(u, 200, tampered) }.run { _, _ -> } }.exceptionOrNull() is ModelDownloader.DownloadError)
        assertTrue(runCatching { downloader(dir, good) { u -> FakeConn(u, 200, content + content) }.run { _, _ -> } }.exceptionOrNull() is ModelDownloader.DownloadError)
        assertTrue(dir.listFiles()!!.isEmpty())
    }

    @Test
    fun liveModelPinsEveryFile() {
        assertEquals(4, LiveModel.FILES.size)
        for (f in LiveModel.FILES) {
            assertTrue(Hashing.isSha256Hex(f.sha256))
            assertTrue(LiveModel.url(f).startsWith("https://huggingface.co/"))
            assertTrue(abs(f.size) > 0)
        }
    }
}

package com.musubi.phone.rec

import android.annotation.SuppressLint
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
import android.media.MediaRecorder
import com.musubi.phone.core.ChunkWriter
import java.io.File
import kotlin.math.abs

/** An error whose message is fit to show the owner. */
class EngineError(message: String) : Exception(message)

/**
 * Captures the microphone with AudioRecord (VOICE_RECOGNITION, falling back to MIC), encodes
 * AAC-LC 48 kHz mono 96 kbps with MediaCodec and hands each frame to [ChunkWriter], which writes
 * ADTS chunk files. Runs on its own thread until [requestStop] or a fatal error.
 *
 * (MediaRecorder cannot do this: its setNextOutputFile only supports MPEG-4 output, and
 * stopping/restarting it every 5 minutes would drop audio at each boundary.)
 */
class AudioEngine(
    private val writer: ChunkWriter,
    private val storageDir: File,
    private val onFinished: (error: String?) -> Unit,
) {
    @Volatile private var stopRequested = false
    /** Live text, when on: gets a copy of each read without ever blocking this thread. */
    @Volatile var live: LiveTextController? = null
    private var thread: Thread? = null

    fun start() {
        thread = Thread({ runLoop() }, "musubi-recorder").also {
            it.priority = Thread.MAX_PRIORITY
            it.start()
        }
    }

    fun requestStop() {
        stopRequested = true
    }

    fun join(timeoutMs: Long) {
        thread?.join(timeoutMs)
    }

    @SuppressLint("MissingPermission") // RECORD_AUDIO is checked before the service starts.
    private fun openRecord(): AudioRecord? {
        val minBuf = AudioRecord.getMinBufferSize(RecorderConfig.SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        if (minBuf <= 0) return null
        val size = maxOf(minBuf * 4, RecorderConfig.SAMPLE_RATE * 2) // at least 1 s of audio buffered
        for ((src, name) in listOf(MediaRecorder.AudioSource.VOICE_RECOGNITION to "voice_recognition", MediaRecorder.AudioSource.MIC to "mic")) {
            val r = runCatching {
                AudioRecord(src, RecorderConfig.SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, size)
            }.getOrNull() ?: continue
            if (r.state == AudioRecord.STATE_INITIALIZED) {
                RecorderStatus.source = name
                return r
            }
            r.release()
        }
        return null
    }

    private fun openEncoder(): MediaCodec {
        val fmt = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_AAC, RecorderConfig.SAMPLE_RATE, RecorderConfig.CHANNELS)
        fmt.setInteger(MediaFormat.KEY_AAC_PROFILE, MediaCodecInfo.CodecProfileLevel.AACObjectLC)
        fmt.setInteger(MediaFormat.KEY_BIT_RATE, RecorderConfig.BIT_RATE)
        fmt.setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, 16 * 1024)
        val codec = MediaCodec.createEncoderByType(MediaFormat.MIMETYPE_AUDIO_AAC)
        codec.configure(fmt, null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
        codec.start()
        return codec
    }

    private fun runLoop() {
        var error: String? = null
        var record: AudioRecord? = null
        var codec: MediaCodec? = null
        try {
            var rec = openRecord() ?: throw EngineError("The microphone could not be opened")
            record = rec
            codec = openEncoder()
            rec.startRecording()
            val pcm = ByteArray(4096) // ~43 ms of 16-bit mono
            val info = MediaCodec.BufferInfo()
            var samplesIn = 0L
            var lastSpaceCheck = 0L
            var deadReads = 0
            while (!stopRequested) {
                val n = rec.read(pcm, 0, pcm.size)
                if (n < 0) {
                    // The audio server restarted or the mic was taken away: reopen and keep going.
                    deadReads++
                    if (deadReads > 20) throw EngineError("The microphone stopped working")
                    runCatching { rec.release() }
                    Thread.sleep(250)
                    val again = openRecord() ?: continue // released record reads negative: retried
                    rec = again
                    record = rec
                    rec.startRecording()
                    continue
                }
                deadReads = 0
                if (n == 0) continue
                RecorderStatus.level = peak(pcm, n)
                live?.offer(pcm, n, samplesIn)
                samplesIn = feed(codec, pcm, n, samplesIn)
                drain(codec, info, false)
                val now = System.currentTimeMillis()
                if (now - lastSpaceCheck > 10_000) {
                    lastSpaceCheck = now
                    if (storageDir.usableSpace < RecorderConfig.MIN_FREE_TO_CONTINUE) {
                        error = "Stopped: phone storage almost full"
                        break
                    }
                }
            }
            // Flush the encoder so the last frames reach the file.
            val idx = codec.dequeueInputBuffer(100_000)
            if (idx >= 0) codec.queueInputBuffer(idx, 0, 0, ptsUs(samplesIn), MediaCodec.BUFFER_FLAG_END_OF_STREAM)
            drain(codec, info, true)
        } catch (t: Throwable) {
            error = (t as? EngineError)?.message ?: "Recording stopped unexpectedly"
        } finally {
            runCatching { record?.stop() }
            runCatching { record?.release() }
            runCatching { codec?.stop() }
            runCatching { codec?.release() }
            val closeErr = runCatching { writer.close() }.exceptionOrNull()
            if (closeErr != null && error == null) error = "Could not finish the last audio file"
            RecorderStatus.level = 0f
            onFinished(error)
        }
    }

    private fun feed(codec: MediaCodec, pcm: ByteArray, len: Int, samplesBefore: Long): Long {
        var off = 0
        var samples = samplesBefore
        while (off < len) {
            val idx = codec.dequeueInputBuffer(10_000)
            if (idx < 0) {
                drain(codec, MediaCodec.BufferInfo(), false)
                continue
            }
            val buf = codec.getInputBuffer(idx) ?: continue
            buf.clear()
            val take = minOf(buf.remaining(), len - off)
            buf.put(pcm, off, take)
            codec.queueInputBuffer(idx, 0, take, ptsUs(samples), 0)
            samples += take / 2
            off += take
        }
        return samples
    }

    private var frameBuf = ByteArray(4096)

    private fun drain(codec: MediaCodec, info: MediaCodec.BufferInfo, untilEos: Boolean) {
        val deadline = System.currentTimeMillis() + 2_000
        while (true) {
            val idx = codec.dequeueOutputBuffer(info, if (untilEos) 10_000 else 0)
            if (idx == MediaCodec.INFO_TRY_AGAIN_LATER) {
                if (!untilEos || System.currentTimeMillis() > deadline) return
                continue
            }
            if (idx < 0) continue // format / buffers changed
            val buf = codec.getOutputBuffer(idx)
            val isConfig = info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG != 0
            if (buf != null && info.size > 0 && !isConfig) {
                if (frameBuf.size < info.size) frameBuf = ByteArray(info.size)
                buf.position(info.offset)
                buf.get(frameBuf, 0, info.size)
                writer.writeFrame(frameBuf, 0, info.size)
            }
            codec.releaseOutputBuffer(idx, false)
            if (info.flags and MediaCodec.BUFFER_FLAG_END_OF_STREAM != 0) return
        }
    }

    private fun ptsUs(samples: Long) = samples * 1_000_000L / RecorderConfig.SAMPLE_RATE

    private fun peak(pcm: ByteArray, len: Int): Float {
        var max = 0
        var i = 0
        while (i + 1 < len) {
            val s = (pcm[i].toInt() and 0xFF) or (pcm[i + 1].toInt() shl 8)
            val a = abs(s.toShort().toInt())
            if (a > max) max = a
            i += 2
        }
        return max / 32768f
    }
}

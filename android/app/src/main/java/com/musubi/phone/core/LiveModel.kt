package com.musubi.phone.core

import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest

/** One file of the live-text model, pinned by size and SHA-256. */
data class ModelFile(val name: String, val size: Long, val sha256: String)

/**
 * The live-text model: sherpa-onnx's streaming zipformer transducer for English, 20M parameters
 * (Apache-2.0; trained with icefall on LibriSpeech), int8 encoder and joiner, fp32 decoder.
 * Fetched from one pinned Hugging Face commit; every file is checked against its pinned size and
 * SHA-256 before it is kept. Nothing else is downloaded, and nothing is sent.
 */
object LiveModel {
    const val ID = "zipformer-en-20M-2023-02-17"
    private const val BASE =
        "https://huggingface.co/csukuangfj/sherpa-onnx-streaming-zipformer-en-20M-2023-02-17/resolve/d42f2d9f7ca24806fb667456a18a9f1b60f70d16/"

    val ENCODER = ModelFile("encoder-epoch-99-avg-1.int8.onnx", 42_845_182, "3810755ce7c3ab26b42a8bcf39d191308fa27fb0f53358823ba46141d03b7eb3")
    val DECODER = ModelFile("decoder-epoch-99-avg-1.onnx", 2_092_272, "45a7f940ecfb53d89fa270ad11b88b961e53a317203eb24b1c8e95ed208b0f30")
    val JOINER = ModelFile("joiner-epoch-99-avg-1.int8.onnx", 259_572, "e085d73b593cf9b0707f370dbd656d58327d3fe36d80d849202ef81df02cb01e")
    val TOKENS = ModelFile("tokens.txt", 5_048, "49e3c2646595fd907228b3c6787069658f67b17377c60aeb8619c4551b2316fb")
    val FILES = listOf(ENCODER, DECODER, JOINER, TOKENS)

    val totalBytes: Long = FILES.sumOf { it.size }

    fun url(f: ModelFile): String = BASE + f.name

    /** Where the model lives: app-private storage, one folder per model id. */
    fun dir(filesDir: File): File = File(File(filesDir, "live-model"), ID)

    /** Cheap check (names and sizes); the recognizer checks hashes before loading. */
    fun isPresent(dir: File): Boolean = FILES.all { File(dir, it.name).let { f -> f.isFile && f.length() == it.size } }

    /** Full check: every file's SHA-256. Returns the first bad file's name, or null. */
    fun firstCorrupt(dir: File): String? = FILES.firstOrNull { f ->
        val file = File(dir, f.name)
        !file.isFile || file.length() != f.size || Hashing.sha256File(file) != f.sha256
    }?.name

    fun delete(dir: File) {
        dir.deleteRecursively()
    }
}

/**
 * Downloads [files] into [dir]: HTTPS only (every redirect too), at most the pinned size per file,
 * hashed while streaming, written to a temp file and renamed into place only when size and
 * SHA-256 match. Files already present and correct are kept. Plain JVM, unit-tested.
 */
class ModelDownloader(
    private val dir: File,
    private val files: List<ModelFile>,
    private val urlFor: (ModelFile) -> String,
    private val open: (URL) -> HttpURLConnection = { it.openConnection() as HttpURLConnection },
) {
    class DownloadError(message: String) : IOException(message)

    @Volatile var cancelled = false

    /** [progress] gets (bytes done, bytes total). Throws [DownloadError] with a message fit to show. */
    fun run(progress: (Long, Long) -> Unit) {
        if (!dir.isDirectory && !dir.mkdirs()) throw DownloadError("Could not create the model folder")
        dir.listFiles()?.filter { it.name.endsWith(PART) }?.forEach { it.delete() }
        val total = files.sumOf { it.size }
        var done = 0L
        for (f in files) {
            val target = File(dir, f.name)
            if (target.isFile && target.length() == f.size && Hashing.sha256File(target) == f.sha256) {
                done += f.size
                progress(done, total)
                continue
            }
            target.delete()
            val before = done
            fetch(f, target) { n -> progress(before + n, total) }
            done += f.size
        }
    }

    private fun fetch(f: ModelFile, target: File, progress: (Long) -> Unit) {
        val part = File(dir, f.name + PART)
        try {
            val conn = connect(urlFor(f))
            try {
                val declared = conn.contentLengthLong
                if (declared > f.size) throw DownloadError("Unexpected file size from the server")
                conn.inputStream.use { input -> copyChecked(input, part, f, progress) }
            } finally {
                conn.disconnect()
            }
            if (!part.renameTo(target)) throw DownloadError("Could not save the model")
        } finally {
            part.delete()
        }
    }

    private fun copyChecked(input: InputStream, part: File, f: ModelFile, progress: (Long) -> Unit) {
        val md = MessageDigest.getInstance("SHA-256")
        var n = 0L
        FileOutputStream(part).use { out ->
            val buf = ByteArray(64 * 1024)
            while (true) {
                if (cancelled) throw DownloadError("Download cancelled")
                val r = input.read(buf)
                if (r < 0) break
                n += r
                if (n > f.size) throw DownloadError("Unexpected file size from the server")
                md.update(buf, 0, r)
                out.write(buf, 0, r)
                progress(n)
            }
            out.flush()
            out.fd.sync()
        }
        if (n != f.size) throw DownloadError("Download incomplete")
        if (Hashing.hex(md.digest()) != f.sha256) throw DownloadError("Downloaded file failed its checksum")
    }

    /** Follows redirects by hand so that every hop must be HTTPS. */
    private fun connect(start: String): HttpURLConnection {
        var url = URL(start)
        repeat(MAX_REDIRECTS + 1) {
            if (url.protocol != "https") throw DownloadError("Refusing a non-HTTPS address")
            val c = open(url)
            c.instanceFollowRedirects = false
            c.connectTimeout = 15_000
            c.readTimeout = 30_000
            c.setRequestProperty("Accept-Encoding", "identity")
            val code = c.responseCode
            when (code) {
                200 -> return c
                301, 302, 303, 307, 308 -> {
                    val loc = c.getHeaderField("Location")
                    c.disconnect()
                    if (loc.isNullOrBlank()) throw DownloadError("Bad redirect from the server")
                    url = URL(url, loc)
                }
                else -> {
                    c.disconnect()
                    throw DownloadError("The server answered HTTP $code")
                }
            }
        }
        throw DownloadError("Too many redirects")
    }

    companion object {
        const val PART = ".part"
        const val MAX_REDIRECTS = 8
    }
}

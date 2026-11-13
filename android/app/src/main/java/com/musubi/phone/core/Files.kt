package com.musubi.phone.core

import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.InputStream
import java.security.MessageDigest

/** Small file helpers shared by the store, the recorder and sync. Plain JVM, unit-tested. */
object Files {
    /**
     * Writes [bytes] to [target] so that a reader sees either the old file or the new one,
     * never a partial file: write a temp file in the same folder, fsync it, rename over.
     */
    fun writeAtomic(target: File, bytes: ByteArray) {
        val dir = target.parentFile ?: throw IOException("no parent folder")
        val tmp = File(dir, target.name + ".tmp")
        FileOutputStream(tmp).use { out ->
            out.write(bytes)
            out.flush()
            out.fd.sync()
        }
        if (!tmp.renameTo(target)) {
            tmp.delete()
            throw IOException("rename failed for ${target.name}")
        }
    }

    /** Reads at most [maxBytes]; a larger file is an error rather than a memory spike. */
    fun readBounded(file: File, maxBytes: Int): ByteArray {
        val len = file.length()
        if (len > maxBytes) throw IOException("${file.name} too large")
        return file.inputStream().use { readBounded(it, maxBytes) }
    }

    fun readBounded(input: InputStream, maxBytes: Int): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        val buf = ByteArray(16 * 1024)
        var total = 0
        while (true) {
            val n = input.read(buf)
            if (n < 0) break
            total += n
            if (total > maxBytes) throw IOException("response too large")
            out.write(buf, 0, n)
        }
        return out.toByteArray()
    }
}

object Hashing {
    fun sha256Hex(bytes: ByteArray): String = hex(MessageDigest.getInstance("SHA-256").digest(bytes))

    /** Streams the file through SHA-256 so large chunks never sit in memory. */
    fun sha256File(file: File): String {
        val md = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buf = ByteArray(64 * 1024)
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                md.update(buf, 0, n)
            }
        }
        return hex(md.digest())
    }

    fun hex(bytes: ByteArray): String {
        val sb = StringBuilder(bytes.size * 2)
        for (b in bytes) {
            val v = b.toInt() and 0xff
            sb.append(HEX[v ushr 4]).append(HEX[v and 0x0f])
        }
        return sb.toString()
    }

    fun isSha256Hex(s: String): Boolean = s.length == 64 && s.all { it in '0'..'9' || it in 'a'..'f' }

    private val HEX = "0123456789abcdef".toCharArray()
}

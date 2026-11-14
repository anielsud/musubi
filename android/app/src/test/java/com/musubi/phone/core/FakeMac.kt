package com.musubi.phone.core

import org.json.JSONObject
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.File

/** Builds request frames the way the Mac sends them. */
object Frames {
    fun frame(json: String, body: ByteArray? = null): ByteArray {
        val j = json.toByteArray(Charsets.UTF_8)
        val out = ByteArrayOutputStream()
        out.write(byteArrayOf((j.size ushr 24).toByte(), (j.size ushr 16).toByte(), (j.size ushr 8).toByte(), j.size.toByte()))
        out.write(j)
        body?.let { out.write(it) }
        return out.toByteArray()
    }

    fun op(op: String, vararg kv: Pair<String, Any?>): ByteArray {
        val o = JSONObject().put("op", op)
        for ((k, v) in kv) o.put(k, v ?: JSONObject.NULL)
        return frame(o.toString())
    }

    /** A reply: its JSON and, if it announced one, its body. */
    class Reply(val json: JSONObject, val body: ByteArray?)

    fun replies(bytes: ByteArray): List<Reply> {
        val input = DataInputStream(ByteArrayInputStream(bytes))
        val out = ArrayList<Reply>()
        while (input.available() > 0) {
            val n = input.readInt()
            val j = ByteArray(n).also { input.readFully(it) }
            val o = JSONObject(String(j, Charsets.UTF_8))
            val body = if (o.has("body")) ByteArray(o.getLong("body").toInt()).also { input.readFully(it) } else null
            out += Reply(o, body)
        }
        return out
    }
}

/** A phone store, agenda and pairing as the session sees them, with a Mac certificate to present. */
class FakePhone(
    val macFp: ByteArray = ByteArray(32) { it.toByte() },
    var pin: ByteArray? = macFp,
) {
    val store = RecordingStore(TestUtil.tempDir(), TestUtil.clockAt(TestUtil.T0.plusHours(2)))
    val agenda = AgendaStore(File(TestUtil.tempDir(), "agenda.json"))
    var now = 1_000_000L
    val pairedWith = mutableListOf<Pair<ByteArray, String>>()
    val pairing = PairingFlow({ now }) { fp, name -> pairedWith += fp to name; pin = fp }
    val info = object : PhoneInfo {
        override val deviceName = "Test Phone"
        override val appVersion = "0.3.0"
        override val certSha256 = ByteArray(32) { (it + 32).toByte() }
        override fun addresses() = listOf("192.168.1.20", "100.101.102.103")
    }

    fun session(fp: ByteArray = macFp) =
        SyncSession(store, agenda, pairing, info, fp, { pin }, TestUtil.clockAt(TestUtil.T0.plusHours(2)))

    class Run(val end: SyncSession.End?, val replies: List<Frames.Reply>, val session: SyncSession, val violation: Boolean)

    /** Feeds [requests] to a new session and collects what it answered. */
    fun run(vararg requests: ByteArray, fp: ByteArray = macFp): Run {
        val input = ByteArrayInputStream(requests.fold(ByteArray(0)) { a, b -> a + b })
        val output = ByteArrayOutputStream()
        val s = session(fp)
        var violation = false
        val end = try {
            s.serve(FrameReader(input), FrameWriter(output))
        } catch (e: ProtocolViolation) {
            violation = true
            null
        }
        return Run(end, Frames.replies(output.toByteArray()), s, violation)
    }
}

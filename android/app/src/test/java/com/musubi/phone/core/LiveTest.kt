package com.musubi.phone.core

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.PipedInputStream
import java.io.PipedOutputStream
import java.util.Collections
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

class LiveLinkTest {
    private val events = Collections.synchronizedList(ArrayList<Pair<String, String>>())
    private val out = ByteArrayOutputStream()
    private var now = 0L

    private fun link(macFrames: ByteArray) =
        LiveLink(FrameReader(ByteArrayInputStream(macFrames)), FrameWriter(out), { n, d -> events += n to d }, { now })

    private fun sent(): List<Frames.Reply> = synchronized(out) { Frames.replies(out.toByteArray()) }

    private fun rpcInto(l: LiveLink, method: String, args: String, into: MutableList<LiveLink.Reply>) = l.rpc(method, args) { into += it }

    @Test
    fun callsAreFramedWithIdsAndAnswersMatched() {
        val big = "\"" + "x".repeat(100_000) + "\""
        val l = link(
            Frames.frame("""{"id":2,"ok":true,"body":${big.length}}""", big.toByteArray()) +
                Frames.frame("""{"id":1,"ok":true,"result":[{"title":"Invented hit","n":3,"score":0.5,"seen":true,"x":null}]}""") +
                Frames.frame("""{"id":3,"ok":false,"error":"not_allowed"}""") +
                Frames.frame("""{"id":4,"ok":false,"error":"failed","message":"not ready:\nno dataset"}"""),
        )
        val got = ArrayList<LiveLink.Reply>()
        rpcInto(l, "Search", """["roadmap","owner",15]""", got)
        rpcInto(l, "Big", "[]", got)
        rpcInto(l, "SetPreference", """["a","b",true]""", got)
        rpcInto(l, "Topics", """["owner"]""", got)
        l.run()
        val reqs = sent()
        assertEquals(listOf(1L, 2L, 3L, 4L), reqs.map { it.json.getLong("id") })
        assertEquals("rpc", reqs[0].json.getString("op"))
        assertEquals("Search", reqs[0].json.getString("method"))
        assertEquals(JSONArray("""["roadmap","owner",15]""").toString(), reqs[0].json.getJSONArray("args").toString())
        // Answers in the Mac's order, each to its own call.
        assertEquals(big, got[0].resultJson)
        assertEquals(JSONArray("""[{"title":"Invented hit","n":3,"score":0.5,"seen":true,"x":null}]""").toString(), JSONArray(got[1].resultJson).toString())
        assertEquals("not_allowed", got[2].error)
        assertFalse(got[2].ok)
        assertEquals("not ready: no dataset", got[3].message)
        assertFalse(l.isOpen)
    }

    @Test
    fun eventsInlineAndAsBodyAndPingsAnswered() {
        val data = """{"id":"1","text":"${"y".repeat(70_000)}"}"""
        val l = link(
            Frames.frame("""{"op":"event","id":1,"name":"ask:token","data":{"id":"1","text":"Invented"}}""") +
                Frames.frame("""{"op":"ping","id":2}""") +
                Frames.frame("""{"op":"event","id":3,"name":"ask:done","body":${data.length}}""", data.toByteArray()),
        )
        l.run()
        assertEquals(listOf("ask:token", "ask:done"), events.map { it.first })
        assertEquals("Invented", JSONObject(events[0].second).getString("text"))
        assertEquals(data, events[1].second)
        val pong = sent().single().json
        assertEquals(2L, pong.getLong("id"))
        assertTrue(pong.getBoolean("ok"))
    }

    @Test
    fun anAssetBlocksUntilItsBodyArrives() {
        val mac = PipedOutputStream()
        val input = PipedInputStream(mac, 1 shl 20)
        val l = LiveLink(FrameReader(input), FrameWriter(out), { _, _ -> }, { now })
        val reader = Thread { runCatching { l.run() } }.apply { start() }
        var got: LiveLink.Reply? = null
        val done = CountDownLatch(1)
        Thread { got = l.asset("/assets/index-1.js"); done.countDown() }.start()
        // Wait for the request, then answer it.
        val deadline = System.currentTimeMillis() + 5_000
        while (sent().isEmpty() && System.currentTimeMillis() < deadline) Thread.sleep(5)
        val req = sent().single().json
        assertEquals("asset", req.getString("op"))
        assertEquals("/assets/index-1.js", req.getString("path"))
        mac.write(Frames.frame("""{"id":${req.getLong("id")},"ok":true,"type":"text/javascript; charset=utf-8","body":3}""", "abc".toByteArray()))
        mac.flush()
        assertTrue(done.await(5, TimeUnit.SECONDS))
        assertTrue(got!!.ok)
        assertEquals("text/javascript; charset=utf-8", got!!.type)
        assertArrayEquals("abc".toByteArray(), got!!.body)
        mac.close()
        reader.join(5_000)
        assertFalse(l.isOpen)
    }

    @Test
    fun violationsEndTheConnection() {
        for (bad in listOf(
            """{"op":"event","name":"ask:token","data":1}""", // no id
            """{"id":9,"ok":true}""", // a reply to nothing asked
            """{"op":"rpc","id":1,"method":"Search","args":[]}""", // the Mac doesn't call the phone
            """{"op":"event","id":1,"name":"Bad Name!","data":1}""",
            """{"op":"list","id":1}""",
        )) {
            val l = link(Frames.frame(bad))
            assertTrue(bad, runCatching { l.run() }.exceptionOrNull() is ProtocolViolation)
            assertFalse(l.isOpen)
        }
        // A result body that isn't JSON.
        val l = link(Frames.frame("""{"id":1,"ok":true,"body":3}""", "{x}".toByteArray()))
        l.rpc("Search", "[]") {}
        assertTrue(runCatching { l.run() }.exceptionOrNull() is ProtocolViolation)
        // An asset's content type must look like one.
        val l2 = link(Frames.frame("""{"id":1,"ok":true,"type":"text/html\r\nX: y","body":0}"""))
        Thread { l2.asset("/", 2_000) }.start()
        Thread.sleep(100)
        assertTrue(runCatching { l2.run() }.exceptionOrNull() is ProtocolViolation)
    }

    @Test
    fun badCallsRefusedHereAndPendingBounded() {
        val l = link(ByteArray(0))
        val got = ArrayList<LiveLink.Reply>()
        rpcInto(l, "../Search", "[]", got)
        rpcInto(l, "Search", """{"a":1}""", got)
        rpcInto(l, "Search", "[1,", got)
        rpcInto(l, "Search", "[" + (1..17).joinToString(",") + "]", got)
        assertEquals(List(4) { "bad_call" }, got.map { it.error })
        assertTrue(sent().isEmpty())
        assertEquals("bad_path", l.asset("relative").error)
        assertEquals("bad_path", l.asset("/a\\b").error)
        got.clear()
        repeat(LiveLink.MAX_PENDING + 2) { rpcInto(l, "Topics", """["owner"]""", got) }
        assertEquals(listOf("busy", "busy"), got.map { it.error })
        // Unanswered calls time out; a closed link fails the rest at once.
        now += LiveLink.RPC_TIMEOUT_MS
        l.expire()
        assertEquals(LiveLink.MAX_PENDING + 2, got.size)
        assertTrue(got.drop(2).all { it.error == "timeout" })
        l.run() // end of stream
        val after = ArrayList<LiveLink.Reply>()
        rpcInto(l, "Topics", """["owner"]""", after)
        assertEquals("closed", after.single().error)
    }

    @Test
    fun endSendsLiveEnd() {
        val l = link(Frames.frame("""{"id":1,"ok":true}"""))
        l.end()
        l.run()
        assertEquals("live_end", sent().single().json.getString("op"))
    }
}

class ExploreRequestsTest {
    private val bridge = "/*bridge*/".toByteArray()
    private val fetched = ArrayList<String>()
    private fun fetch(type: String?, body: String?): (String) -> LiveLink.Reply = { p ->
        fetched += p
        if (type == null) LiveLink.Reply(false, error = "not_found") else LiveLink.Reply(true, type = type, body = body!!.toByteArray())
    }

    @Test
    fun onlyThePrivateOriginIsServed() {
        for (url in listOf(
            "https://example.com/", "http://musubi.local/", "https://musubi.local:8443/", "https://user@musubi.local/",
            "file:///data/data/com.musubi.phone/files/x", "content://x/y", "https://musubi.local.evil.example/", "javascript:alert(1)",
        )) {
            val r = ExploreRequests.handle("GET", url, bridge, fetch("text/html", "<p>x</p>"))
            assertEquals(url, 403, r.status)
        }
        assertTrue(fetched.isEmpty())
        assertEquals(405, ExploreRequests.handle("POST", "https://musubi.local/", bridge, fetch("text/html", "x")).status)
    }

    @Test
    fun pagesGetTheBridgeFirstAndAPolicy() {
        val r = ExploreRequests.handle("GET", "https://musubi.local/?phone=1", bridge,
            fetch("text/html; charset=utf-8", "<!doctype html><html><head><title>t</title><script type=\"module\" src=\"/assets/a.js\"></script></head></html>"))
        assertEquals(listOf("/?phone=1"), fetched)
        assertEquals(200, r.status)
        assertEquals("text/html", r.mime)
        assertEquals("utf-8", r.charset)
        val html = String(r.body)
        assertTrue(html, html.indexOf(ExploreRequests.BRIDGE_PATH) in 0 until html.indexOf("/assets/a.js"))
        assertTrue(r.headers["Content-Security-Policy"]!!.contains("connect-src 'none'"))
        assertEquals("no-store", r.headers["Cache-Control"])

        val js = ExploreRequests.handle("GET", "https://musubi.local/assets/a.js", bridge, fetch("text/javascript; charset=utf-8", "1"))
        assertEquals("text/javascript", js.mime)
        assertNull(js.headers["Content-Security-Policy"])
        assertEquals(404, ExploreRequests.handle("GET", "https://musubi.local/x.js", bridge, fetch(null, null)).status)
        // The bridge comes from the app, not from the Mac.
        fetched.clear()
        val b = ExploreRequests.handle("GET", "https://musubi.local" + ExploreRequests.BRIDGE_PATH, bridge, fetch("text/html", "x"))
        assertArrayEquals(bridge, b.body)
        assertTrue(fetched.isEmpty())
    }

    @Test
    fun bridgeMessages() {
        val c = BridgeMessages.parseCall("""{"t":"call","id":7,"method":"Search","args":["q","owner",15]}""")!!
        assertEquals(7L, c.pageId)
        assertEquals("Search", c.method)
        assertEquals("""["q","owner",15]""", c.argsJson)
        for (bad in listOf(
            """{"t":"call","id":0,"method":"Search","args":[]}""",
            """{"t":"call","id":1,"method":"search","args":[]}""",
            """{"t":"call","id":1,"method":"Search","args":{}}""",
            """{"t":"call","id":"1","method":"Search","args":[]}""",
            """{"t":"evil","id":1,"method":"Search","args":[]}""",
            """not json""",
            """{"t":"call","id":1,"method":"Search","args":["${"a".repeat(70_000)}"]}""",
        )) assertNull(bad.take(60), BridgeMessages.parseCall(bad))
        val ok = JSONObject(BridgeMessages.reply(7, LiveLink.Reply(true, resultJson = """[{"a":1}]""")))
        assertEquals(1, ok.getJSONArray("result").getJSONObject(0).getInt("a"))
        val no = JSONObject(BridgeMessages.reply(8, LiveLink.Reply(false, error = "not_allowed")))
        assertFalse(no.getBoolean("ok"))
        assertEquals("Not available on the phone", no.getString("message"))
        val ev = JSONObject(BridgeMessages.event("ask:token", """{"id":"1","text":"\"quoted\""}"""))
        assertEquals("\"quoted\"", ev.getJSONObject("data").getString("text"))
    }
}

class LiveStartTest {
    private val phone = FakePhone()

    @Test
    fun liveStartsOnlyWhenAllowedAndPaired() {
        var allowed = true
        fun run(vararg reqs: ByteArray, fp: ByteArray = phone.macFp): FakePhone.Run {
            val input = ByteArrayInputStream(reqs.fold(ByteArray(0)) { a, b -> a + b })
            val output = ByteArrayOutputStream()
            val s = SyncSession(phone.store, phone.agenda, phone.pairing, phone.info, fp, { phone.pin },
                TestUtil.clockAt(TestUtil.T0), liveAllowed = { allowed })
            val end = s.serve(FrameReader(input), FrameWriter(output))
            return FakePhone.Run(end, Frames.replies(output.toByteArray()), s, false)
        }
        val ok = run(Frames.op("live_start"), Frames.op("list"))
        assertEquals(SyncSession.End.LIVE, ok.end)
        assertTrue(ok.replies.single().json.getBoolean("ok"))

        allowed = false
        val refused = run(Frames.op("live_start"))
        assertEquals("not_live", refused.replies.single().json.getString("error"))
        assertEquals(SyncSession.End.CLOSED, refused.end)

        allowed = true
        val stranger = run(Frames.op("live_start"), fp = ByteArray(32) { 9 })
        assertEquals("not_paired", stranger.replies.single().json.getString("error"))
        assertEquals(SyncSession.End.NOT_PAIRED, stranger.end)
    }
}

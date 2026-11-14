package com.musubi.phone.core

import org.json.JSONObject
import java.net.URI

/**
 * What the Explore WebView may load (docs/phone.md, part 3): only `https://musubi.local/`, a
 * private origin that never reaches the network. Each GET there is answered from the Mac's built
 * frontend over the live connection, except the bridge script, which comes from the app itself and
 * is put first into the page. Everything else is refused. Nothing is cached.
 */
object ExploreRequests {
    const val HOST = "musubi.local"
    const val ORIGIN = "https://$HOST"
    const val START_URL = "$ORIGIN/?phone=1"
    const val BRIDGE_PATH = "/__musubi/bridge.js"

    /**
     * The page may load only its own scripts and styles (inline styles for React's), images of its
     * own or as data: URLs (headshots); it may not fetch, frame, embed or submit anything.
     */
    const val CSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
        "font-src 'self'; connect-src 'none'; media-src 'none'; object-src 'none'; frame-src 'none'; " +
        "base-uri 'none'; form-action 'none'"

    class Response(
        val status: Int,
        val reason: String,
        val mime: String,
        val charset: String?,
        val headers: Map<String, String>,
        val body: ByteArray,
    )

    private val noStore = mapOf("Cache-Control" to "no-store", "X-Content-Type-Options" to "nosniff")

    fun handle(method: String, url: String, bridge: ByteArray, fetch: (path: String) -> LiveLink.Reply): Response {
        val u = try {
            URI(url)
        } catch (e: Exception) {
            return refused()
        }
        if (u.scheme != "https" || u.host != HOST || u.port != -1 || u.rawUserInfo != null) return refused()
        if (method != "GET") return Response(405, "Method Not Allowed", "text/plain", "utf-8", noStore, ByteArray(0))
        val path = u.rawPath.takeUnless { it.isNullOrEmpty() } ?: "/"
        if (path == BRIDGE_PATH) return Response(200, "OK", "text/javascript", "utf-8", noStore, bridge)
        val r = fetch(if (u.rawQuery != null) "$path?${u.rawQuery}" else path)
        if (!r.ok || r.type == null || r.body == null) return Response(404, "Not Found", "text/plain", "utf-8", noStore, ByteArray(0))
        val mime = r.type.substringBefore(';').trim()
        val charset = r.type.substringAfter("charset=", "").trim().ifEmpty { null }
        if (mime == "text/html") {
            return Response(200, "OK", mime, charset ?: "utf-8", noStore + ("Content-Security-Policy" to CSP), injectBridge(r.body))
        }
        return Response(200, "OK", mime, charset, noStore, r.body)
    }

    /** The page with the bridge script first in its head, so it runs before the app's own. */
    fun injectBridge(html: ByteArray): ByteArray {
        val s = String(html, Charsets.UTF_8)
        val tag = """<script src="$BRIDGE_PATH"></script>"""
        val i = s.indexOf("<head>", ignoreCase = true)
        val out = if (i >= 0) s.substring(0, i + 6) + tag + s.substring(i + 6) else tag + s
        return out.toByteArray(Charsets.UTF_8)
    }

    private fun refused() = Response(403, "Forbidden", "text/plain", "utf-8", noStore, ByteArray(0))
}

/**
 * Messages between the page's bridge (assets/explore-bridge.js) and the app, over a WebMessagePort
 * handed only to the `https://musubi.local` origin. The page sends `{t: "call", id, method,
 * args}`; the app answers `{t: "r", id, ok, result | error, message}` and passes the Mac's events
 * on as `{t: "e", name, data}`.
 */
object BridgeMessages {
    class Call(val pageId: Long, val method: String, val argsJson: String)

    const val MAX_MESSAGE = 64 * 1024
    private const val MAX_PAGE_ID = 1L shl 53

    /** A call from the page, or null for anything malformed (it is then ignored). */
    fun parseCall(text: String): Call? {
        if (text.length > MAX_MESSAGE) return null
        val m = try {
            StrictJson.parse(text) as? Map<*, *>
        } catch (e: StrictJson.Malformed) {
            null
        } ?: return null
        if (m["t"] != "call") return null
        val id = m["id"] as? Long ?: return null
        val method = m["method"] as? String ?: return null
        val args = m["args"] as? List<*> ?: return null
        if (id !in 1..MAX_PAGE_ID || !LiveLink.METHOD.matches(method) || args.size > LiveLink.MAX_ARGS) return null
        return Call(id, method, JsonOut.write(args))
    }

    fun reply(pageId: Long, r: LiveLink.Reply): String =
        if (r.ok) """{"t":"r","id":$pageId,"ok":true,"result":${r.resultJson ?: "null"}}"""
        else """{"t":"r","id":$pageId,"ok":false,"error":${JSONObject.quote(r.error ?: "failed")},"message":${JSONObject.quote(r.message ?: explain(r.error))}}"""

    fun event(name: String, dataJson: String): String = """{"t":"e","name":${JSONObject.quote(name)},"data":$dataJson}"""

    /** What the screen shows for a refusal the Mac gave no message for. */
    private fun explain(code: String?): String = when (code) {
        "not_allowed" -> "Not available on the phone"
        "timeout" -> "The Mac took too long to answer"
        "closed" -> "The connection to the Mac closed"
        "busy" -> "Too many requests at once"
        else -> "The Mac could not answer (${code ?: "failed"})"
    }
}

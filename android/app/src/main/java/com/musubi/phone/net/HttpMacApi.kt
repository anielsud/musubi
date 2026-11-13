package com.musubi.phone.net

import com.musubi.phone.core.ApiResult
import com.musubi.phone.core.Files
import com.musubi.phone.core.MacApi
import com.musubi.phone.core.RecordingMeta
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.URL
import javax.net.ssl.HttpsURLConnection

/** Validated Mac address entered by the owner: a host name or IP literal and a port. */
data class MacAddress(val host: String, val port: Int) {
    fun url(path: String): URL = URL("https", host, port, path)

    companion object {
        const val DEFAULT_PORT = 47615
        private val HOST_RE = Regex("^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*\\.?$")
        private val IPV6_RE = Regex("^[0-9A-Fa-f:.]{2,45}$")

        /** Returns null when the input is not a plain host/IP (no scheme, path, user info...). */
        fun parse(hostInput: String, portInput: String): MacAddress? {
            var h = hostInput.trim()
            if (h.startsWith("[") && h.endsWith("]")) h = h.substring(1, h.length - 1)
            val ok = (h.length in 1..253 && HOST_RE.matches(h)) || (h.contains(':') && IPV6_RE.matches(h))
            if (!ok) return null
            val p = if (portInput.isBlank()) DEFAULT_PORT else portInput.trim().toIntOrNull() ?: return null
            if (p !in 1..65535) return null
            return MacAddress(h, p)
        }
    }
}

/**
 * The Mac's API over HTTPS with the paired certificate pinned. The token goes only in the
 * Authorization header; nothing here logs URLs, headers or bodies.
 */
class HttpMacApi(
    private val address: MacAddress,
    private val pin: String,
    private val token: String?,
) : MacApi {
    private val factory = Pinning.socketFactory(pin)
    private val verifier = Pinning.hostnameVerifier(pin)

    override fun putChunk(rid: String, n: Int, file: File, sha256: String): ApiResult {
        require(RecordingMeta.isValidId(rid) && n in 0 until RecordingMeta.MAX_CHUNKS)
        val len = file.length()
        if (len > MAX_CHUNK_BYTES) return ApiResult.Http(413, ByteArray(0))
        return call("PUT", "/v1/recordings/$rid/chunks/$n", SMALL_REPLY, mapOf("Content-Type" to "audio/aac", "X-Sha256" to sha256)) { c ->
            c.setFixedLengthStreamingMode(len)
            c.outputStream.use { out -> file.inputStream().use { it.copyTo(out, 64 * 1024) } }
        }
    }

    override fun complete(rid: String, metaJson: ByteArray): ApiResult {
        require(RecordingMeta.isValidId(rid))
        return call("POST", "/v1/recordings/$rid/complete", SMALL_REPLY, JSON) { c ->
            c.setFixedLengthStreamingMode(metaJson.size)
            c.outputStream.use { it.write(metaJson) }
        }
    }

    override fun recordingState(rid: String): ApiResult {
        require(RecordingMeta.isValidId(rid))
        return call("GET", "/v1/recordings/$rid", SMALL_REPLY, emptyMap(), null)
    }

    override fun agenda(days: Int): ApiResult =
        call("GET", "/v1/agenda?days=${days.coerceIn(1, 14)}", AGENDA_REPLY, emptyMap(), null)

    fun pair(code: String, deviceName: String): ApiResult {
        val body = JSONObject().put("code", code).put("device_name", deviceName).toString().toByteArray()
        return call("POST", "/v1/pair", SMALL_REPLY, JSON) { c ->
            c.setFixedLengthStreamingMode(body.size)
            c.outputStream.use { it.write(body) }
        }
    }

    private fun call(
        method: String,
        path: String,
        maxReply: Int,
        headers: Map<String, String>,
        writeBody: ((HttpsURLConnection) -> Unit)?,
    ): ApiResult {
        var c: HttpsURLConnection? = null
        return try {
            // Direct connection: the Mac is on the local network or Tailscale, never via a proxy.
            c = address.url(path).openConnection(Proxy.NO_PROXY) as HttpsURLConnection
            c.sslSocketFactory = factory
            c.hostnameVerifier = verifier
            c.instanceFollowRedirects = false
            c.useCaches = false
            c.connectTimeout = 5_000
            c.readTimeout = 60_000
            c.requestMethod = method
            c.setRequestProperty("Accept", "application/json")
            token?.let { c.setRequestProperty("Authorization", "Bearer $it") }
            headers.forEach { (k, v) -> c.setRequestProperty(k, v) }
            if (writeBody != null) {
                c.doOutput = true
                writeBody(c)
            }
            val code = c.responseCode
            val stream = if (code in 200..299) c.inputStream else c.errorStream
            val body = stream?.use { Files.readBounded(it, maxReply) } ?: ByteArray(0)
            if (code in 200..299) {
                if (body.isNotEmpty() && c.contentType?.startsWith("application/json") != true) {
                    ApiResult.Network("unexpected content type")
                } else {
                    ApiResult.Ok(code, body)
                }
            } else {
                ApiResult.Http(code, body)
            }
        } catch (e: javax.net.ssl.SSLHandshakeException) {
            ApiResult.Network(e.javaClass.simpleName, certificateMismatch = true)
        } catch (e: IOException) {
            // Only the exception class: messages can carry addresses, never anything secret, but keep it terse.
            ApiResult.Network(e.javaClass.simpleName)
        } finally {
            c?.disconnect()
        }
    }

    companion object {
        const val MAX_CHUNK_BYTES = 64L * 1024 * 1024
        const val SMALL_REPLY = 64 * 1024
        const val AGENDA_REPLY = 8 * 1024 * 1024
        private val JSON = mapOf("Content-Type" to "application/json")
    }
}

package com.musubi.phone.ui

import android.annotation.SuppressLint
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.webkit.ConsoleMessage
import android.webkit.PermissionRequest
import android.webkit.WebChromeClient
import android.webkit.WebMessage
import android.webkit.WebMessagePort
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebStorage
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.TextView
import com.musubi.phone.BuildConfig
import com.musubi.phone.MainActivity
import com.musubi.phone.core.BridgeMessages
import com.musubi.phone.core.ExploreRequests
import com.musubi.phone.core.LiveLink
import com.musubi.phone.core.StrictJson
import com.musubi.phone.sync.PhoneSync
import java.util.concurrent.Executors

/**
 * Explore (docs/phone.md, part 3): Musubi's own Explore screens and Ask, served by the Mac over the
 * connection it opened ([LiveLink]). Shown only while that connection lasts, or while the Mac brings
 * it back after a sync: the page stays, says "Syncing with your Mac…", and its calls wait for the new
 * connection (one that the sync cut off is asked again there). The WebView loads only
 * `https://musubi.local/`, answered from the connection; the bridge is reachable from that origin
 * only; no other navigation, no file or content access, nothing cached or stored.
 */
class ExploreScreen(a: MainActivity) : Screen(a) {
    override val title = "Explore"
    private var web: WebView? = null
    private var syncing: TextView? = null
    private var port: WebMessagePort? = null
    private val main = Handler(Looper.getMainLooper())
    // Calls are written to the socket off the main thread.
    private val io = Executors.newSingleThreadExecutor { r -> Thread(r, "musubi-explore").apply { isDaemon = true } }
    private val bridgeJs: ByteArray by lazy { a.assets.open("explore-bridge.js").use { it.readBytes() } }

    override fun build(): View {
        val box = FrameLayout(a)
        if (!PhoneSync.exploreAvailable) {
            box.addView(a.label("Explore opens when your Mac connects.", secondary = true).apply { gravity = Gravity.CENTER },
                FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
            return box
        }
        val w = webView()
        web = w
        box.addView(w, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        syncing = a.label("Syncing with your Mac…", 14f).apply {
            gravity = Gravity.CENTER
            setPadding(a.dp(12), a.dp(8), a.dp(12), a.dp(8))
            setBackgroundColor(0xE0202630.toInt())
            setTextColor(0xFFFFFFFF.toInt())
            visibility = View.GONE
        }
        box.addView(syncing, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT, Gravity.TOP))
        PhoneSync.liveEvents = { name, data ->
            if (name == "ask:done") askId(data, "id")?.let { PhoneSync.askOpen(it, false) }
            main.post { send(BridgeMessages.event(name, data)) }
        }
        w.loadUrl(ExploreRequests.START_URL)
        return box
    }

    override fun tick() {
        syncing?.visibility = if (PhoneSync.exploreSyncing) View.VISIBLE else View.GONE
    }

    /** The connection, waiting up to [ms] while the Mac brings it back after a sync. */
    private fun awaitLink(ms: Long): LiveLink? {
        val end = System.currentTimeMillis() + ms
        while (true) {
            PhoneSync.live?.let { if (it.isOpen) return it }
            if (!PhoneSync.exploreAvailable || System.currentTimeMillis() >= end) return null
            Thread.sleep(100)
        }
    }

    /** Runs a call from the page on the current connection; one cut off by a sync is asked again. */
    private fun call(c: BridgeMessages.Call, again: Boolean) {
        val l = awaitLink(WAIT_MS)
        if (l == null) {
            main.post { send(BridgeMessages.reply(c.pageId, LiveLink.Reply(false, error = "closed"))) }
            return
        }
        val ask = if (c.method == "Ask") askId(c.argsJson, null) else null
        ask?.let { PhoneSync.askOpen(it, true) }
        l.rpc(c.method, c.argsJson) { r ->
            if (!r.ok && r.error == "closed" && !again && PhoneSync.exploreAvailable) {
                runCatching { io.execute { call(c, true) } }
                return@rpc
            }
            if (!r.ok) ask?.let { PhoneSync.askOpen(it, false) }
            main.post { send(BridgeMessages.reply(c.pageId, r)) }
        }
    }

    /** An Ask's id: the first argument of the call, or [key] of an event's data. */
    private fun askId(json: String, key: String?): String? = runCatching {
        val v = StrictJson.parse(json)
        (if (key == null) (v as List<*>).firstOrNull() else (v as Map<*, *>)[key]) as? String
    }.getOrNull()

    @SuppressLint("SetJavaScriptEnabled")
    private fun webView(): WebView {
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        val w = WebView(a)
        w.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = false
            allowFileAccess = false
            allowContentAccess = false
            cacheMode = WebSettings.LOAD_NO_CACHE
            setSupportMultipleWindows(false)
            javaScriptCanOpenWindowsAutomatically = false
            mediaPlaybackRequiresUserGesture = true
            setGeolocationEnabled(false)
            mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
        }
        w.webViewClient = object : WebViewClient() {
            // Links, mailto:, tel: and anything else that would leave the page: refused.
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean = true

            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse {
                val r = ExploreRequests.handle(request.method, request.url.toString(), bridgeJs) { path ->
                    awaitLink(WAIT_MS)?.asset(path) ?: LiveLink.Reply(false, error = "closed")
                }
                return WebResourceResponse(r.mime, r.charset, r.status, r.reason, r.headers, r.body.inputStream())
            }

            override fun onPageFinished(view: WebView, url: String) {
                if (Uri.parse(url).let { it.scheme == "https" && it.host == ExploreRequests.HOST && it.port == -1 }) openPort(view)
            }

            override fun onRenderProcessGone(view: WebView, detail: android.webkit.RenderProcessGoneDetail): Boolean {
                // The page's process died: drop this WebView rather than the app.
                dispose()
                return true
            }
        }
        w.webChromeClient = object : WebChromeClient() {
            // The page's console may hold meeting text: not logged.
            override fun onConsoleMessage(m: ConsoleMessage): Boolean = !BuildConfig.DEBUG

            override fun onPermissionRequest(request: PermissionRequest) = request.deny()
        }
        return w
    }

    /** Hands the page the bridge's port; only the musubi.local origin can receive it. */
    private fun openPort(view: WebView) {
        port?.close()
        val ports = view.createWebMessageChannel()
        val mine = ports[0]
        mine.setWebMessageCallback(object : WebMessagePort.WebMessageCallback() {
            override fun onMessage(p: WebMessagePort, message: WebMessage) {
                val c = BridgeMessages.parseCall(message.data ?: return) ?: return
                runCatching { io.execute { call(c, false) } }
            }
        }, main)
        port = mine
        view.postWebMessage(WebMessage("musubi-bridge", arrayOf(ports[1])), Uri.parse(ExploreRequests.ORIGIN))
    }

    private fun send(json: String) {
        if (web == null) return
        runCatching { port?.postMessage(WebMessage(json)) }
    }

    /** Leaves nothing behind: the page, its port, and anything the WebView kept. */
    fun dispose() {
        if (PhoneSync.liveEvents != null) PhoneSync.liveEvents = null
        runCatching { port?.close() }
        port = null
        web?.let { w ->
            (w.parent as? ViewGroup)?.removeView(w)
            w.stopLoading()
            w.clearCache(true)
            w.clearHistory()
            w.destroy()
        }
        web = null
        WebStorage.getInstance().deleteAllData()
        io.shutdown()
    }

    companion object {
        /** How long a call waits for the Mac to bring Explore back after a sync. */
        private const val WAIT_MS = 30_000L
    }
}

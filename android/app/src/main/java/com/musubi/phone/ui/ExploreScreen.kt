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
import com.musubi.phone.BuildConfig
import com.musubi.phone.MainActivity
import com.musubi.phone.core.BridgeMessages
import com.musubi.phone.core.ExploreRequests
import com.musubi.phone.core.LiveLink
import com.musubi.phone.sync.PhoneSync
import java.util.concurrent.Executors

/**
 * Explore (docs/phone.md, part 3): Musubi's own Explore screens and Ask, served by the Mac over the
 * connection it opened ([LiveLink]). Shown only while that connection lasts. The WebView loads only
 * `https://musubi.local/`, answered from the connection; the bridge is reachable from that origin
 * only; no other navigation, no file or content access, nothing cached or stored.
 */
class ExploreScreen(a: MainActivity) : Screen(a) {
    override val title = "Explore"
    /** The connection this screen shows; MainActivity replaces the screen when it changes. */
    val link: LiveLink? = PhoneSync.live
    private var web: WebView? = null
    private var port: WebMessagePort? = null
    private val main = Handler(Looper.getMainLooper())
    // Calls are written to the socket off the main thread.
    private val io = Executors.newSingleThreadExecutor { r -> Thread(r, "musubi-explore").apply { isDaemon = true } }
    private val bridgeJs: ByteArray by lazy { a.assets.open("explore-bridge.js").use { it.readBytes() } }

    override fun build(): View {
        val box = FrameLayout(a)
        val l = link
        if (l == null) {
            box.addView(a.label("Explore opens when your Mac connects.", secondary = true).apply { gravity = Gravity.CENTER },
                FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
            return box
        }
        val w = webView(l)
        web = w
        box.addView(w, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        PhoneSync.liveEvents = { name, data -> main.post { send(BridgeMessages.event(name, data)) } }
        w.loadUrl(ExploreRequests.START_URL)
        return box
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun webView(l: LiveLink): WebView {
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
                val r = ExploreRequests.handle(request.method, request.url.toString(), bridgeJs) { path -> l.asset(path) }
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
                val call = BridgeMessages.parseCall(message.data ?: return) ?: return
                val l = link ?: return
                io.execute {
                    l.rpc(call.method, call.argsJson) { r -> main.post { send(BridgeMessages.reply(call.pageId, r)) } }
                }
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
}

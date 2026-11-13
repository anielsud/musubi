package com.musubi.phone.update

import android.app.Activity
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInfo
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import com.musubi.phone.BuildConfig
import com.musubi.phone.core.Files
import com.musubi.phone.core.Hashing
import com.musubi.phone.core.UpdateFailure
import com.musubi.phone.core.UpdateFlow
import com.musubi.phone.core.UpdatePlatform
import com.musubi.phone.core.UpdateResult
import com.musubi.phone.core.UpdateSource
import com.musubi.phone.core.UpdateState
import com.musubi.phone.core.Version
import com.musubi.phone.net.Pinning
import com.musubi.phone.rec.RecorderStatus
import java.io.File
import java.io.IOException
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.URL
import java.security.MessageDigest
import java.util.concurrent.Executors
import javax.net.ssl.HttpsURLConnection

/**
 * App updates from GitHub releases (docs/phone.md, "Updates"): checked on start (at most every
 * 6 hours) and on **Check for updates**; installed with PackageInstaller, never while recording.
 */
object Updater {
    private val io = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    @Volatile private var flow: UpdateFlow? = null
    @Volatile private var waiting = false
    @Volatile private var resumed: java.lang.ref.WeakReference<Activity>? = null
    /** The system's "confirm install" screen, held until our activity is in front. */
    @Volatile private var confirm: Intent? = null

    /** Last result as shown in the Mac screen. */
    fun status(ctx: Context): String? = Prefs(ctx).status

    fun hasPending(ctx: Context) = Prefs(ctx).pendingVersion != null

    fun installedVersion(): String = BuildConfig.VERSION_NAME

    fun checkOnStart(ctx: Context) = run(ctx) { it.check(manual = false) }

    fun checkNow(ctx: Context, done: (UpdateResult) -> Unit) = run(ctx) { f ->
        val r = f.check(manual = true)
        main.post { done(r) }
    }

    private fun run(ctx: Context, block: (UpdateFlow) -> Unit) {
        val app = ctx.applicationContext
        io.execute { runCatching { block(flow(app)) } }
    }

    @Synchronized
    private fun flow(ctx: Context): UpdateFlow =
        flow ?: UpdateFlow(AndroidPlatform(ctx), Prefs(ctx), source(), File(ctx.filesDir, "updates")).also { flow = it }

    private fun source(): UpdateSource =
        if (mockPin() != null) UpdateSource.mock(BuildConfig.UPDATES_MOCK_BASE) ?: UpdateSource.GITHUB else UpdateSource.GITHUB

    /** Smoke tests only: a local mock of the releases API, its certificate pinned. Empty in real builds. */
    private fun mockPin(): String? =
        BuildConfig.UPDATES_MOCK_PIN.lowercase().takeIf { BuildConfig.UPDATES_MOCK_BASE.isNotEmpty() && Hashing.isSha256Hex(it) }

    fun onResume(a: Activity) {
        resumed = java.lang.ref.WeakReference(a)
        confirm?.let { confirm = null; launchConfirm(a, it) }
        // Back from the "install unknown apps" setting, or a recording ended while away.
        if (Prefs(a).pendingVersion != null && !RecorderStatus.isRecording) run(a) { it.tryInstall() }
    }

    fun onPause(a: Activity) {
        if (resumed?.get() === a) resumed = null
    }

    fun openInstallSetting(a: Activity) {
        a.startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${a.packageName}")))
    }

    fun needsInstallPermission(ctx: Context) = !ctx.packageManager.canRequestPackageInstalls()

    private fun launchConfirm(ctx: Context, i: Intent) {
        runCatching { ctx.startActivity(i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
    }

    /** Polls the recorder state while an install waits; the recorder runs in this process. */
    private fun waitForRecording(ctx: Context) {
        if (waiting) return
        waiting = true
        val app = ctx.applicationContext
        main.postDelayed(object : Runnable {
            override fun run() {
                if (RecorderStatus.isRecording) {
                    main.postDelayed(this, WAIT_POLL_MS)
                } else {
                    waiting = false
                    run(app) { it.tryInstall() }
                }
            }
        }, WAIT_POLL_MS)
    }

    internal fun onInstallStatus(ctx: Context, intent: Intent) {
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
        when (status) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val i = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                else intent.getParcelableExtra(Intent.EXTRA_INTENT)
                // Only the system's install confirmation, never any other screen.
                if (i == null || i.action != ACTION_CONFIRM_INSTALL) {
                    io.execute { flow(ctx.applicationContext).installFailed("unexpected installer reply") }
                    return
                }
                val a = resumed?.get()
                if (a != null) launchConfirm(a, i) else confirm = i
            }
            PackageInstaller.STATUS_SUCCESS -> Unit // This process is replaced by the new version.
            else -> {
                val reason = if (status == PackageInstaller.STATUS_FAILURE_ABORTED) "install cancelled"
                else "install failed (" + (intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)?.take(120) ?: "status $status") + ")"
                io.execute { flow(ctx.applicationContext).installFailed(reason) }
            }
        }
    }

    private const val WAIT_POLL_MS = 15_000L
    private const val ACTION_CONFIRM_INSTALL = "android.content.pm.action.CONFIRM_INSTALL"
    private const val CONNECT_TIMEOUT_MS = 10_000
    private const val READ_TIMEOUT_MS = 30_000
    private const val MAX_REDIRECTS = 5

    private class Prefs(ctx: Context) : UpdateState {
        private val p = ctx.applicationContext.getSharedPreferences("update", Context.MODE_PRIVATE)
        override var lastCheckAt: Long
            get() = p.getLong("last_check_at", 0)
            set(v) { p.edit().putLong("last_check_at", v).apply() }
        override var pendingVersion: String?
            get() = p.getString("pending_version", null)
            set(v) { p.edit().putString("pending_version", v).apply() }
        override var pendingSha256: String?
            get() = p.getString("pending_sha256", null)
            set(v) { p.edit().putString("pending_sha256", v).apply() }
        override var status: String?
            get() = p.getString("status", null)
            set(v) { p.edit().putString("status", v).apply() }
    }

    private class AndroidPlatform(private val ctx: Context) : UpdatePlatform {
        private val source = source()
        private val pin = mockPin()

        override fun installedVersion(): Version =
            Version.parse(BuildConfig.VERSION_NAME) ?: throw UpdateFailure("installed version unreadable")

        override fun fetchReleases(): ByteArray {
            val c = open(source.apiUrl)
            try {
                c.setRequestProperty("Accept", "application/vnd.github+json")
                val code = c.responseCode
                if (code != 200) throw UpdateFailure("GitHub refused (HTTP $code)")
                return c.inputStream.use { Files.readBounded(it, com.musubi.phone.core.Releases.MAX_REPLY_BYTES) }
            } finally {
                c.disconnect()
            }
        }

        override fun fetchSmall(url: String, maxBytes: Int): ByteArray =
            follow(url) { c -> c.inputStream.use { Files.readBounded(it, maxBytes) } }

        override fun download(url: String, maxBytes: Long, dest: File): String = follow(url) { c ->
            val len = c.contentLengthLong
            if (len > maxBytes) throw UpdateFailure("download too large")
            val md = MessageDigest.getInstance("SHA-256")
            var total = 0L
            c.inputStream.use { input ->
                dest.outputStream().use { out ->
                    val buf = ByteArray(64 * 1024)
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        total += n
                        if (total > maxBytes) throw UpdateFailure("download too large")
                        md.update(buf, 0, n)
                        out.write(buf, 0, n)
                    }
                    out.fd.sync()
                }
            }
            Hashing.hex(md.digest())
        }

        /** Follows redirects by hand so each hop is checked against the allowed GitHub hosts. */
        private fun <T> follow(start: String, read: (HttpsURLConnection) -> T): T {
            var url = start
            repeat(MAX_REDIRECTS + 1) {
                val c = open(url)
                try {
                    val code = c.responseCode
                    when (code) {
                        200 -> return read(c)
                        301, 302, 303, 307, 308 -> {
                            val loc = c.getHeaderField("Location") ?: throw UpdateFailure("redirect without a target")
                            val next = runCatching { URL(URL(url), loc).toString() }.getOrNull()
                            if (next == null || !source.isAllowedRedirect(next)) throw UpdateFailure("redirected to a host that is not GitHub")
                            url = next
                        }
                        else -> throw UpdateFailure("download refused (HTTP $code)")
                    }
                } finally {
                    c.disconnect()
                }
            }
            throw UpdateFailure("too many redirects")
        }

        private fun open(url: String): HttpsURLConnection {
            val u = URL(url)
            if (u.protocol != "https") throw UpdateFailure("not HTTPS")
            val c = u.openConnection(Proxy.NO_PROXY) as? HttpsURLConnection ?: throw UpdateFailure("not HTTPS")
            pin?.let {
                c.sslSocketFactory = Pinning.socketFactory(it)
                c.hostnameVerifier = Pinning.hostnameVerifier(it)
            }
            c.instanceFollowRedirects = false
            c.useCaches = false
            c.connectTimeout = CONNECT_TIMEOUT_MS
            c.readTimeout = READ_TIMEOUT_MS
            c.setRequestProperty("User-Agent", "musubi-phone")
            return c
        }

        override fun verifyApk(apk: File, expected: Version): String? {
            val pm = ctx.packageManager
            val archive = archiveInfo(pm, apk) ?: return "not a readable app"
            if (archive.packageName != ctx.packageName) return "it is another app"
            if (Version.parse(archive.versionName) != expected) return "version inside does not match the release"
            val installed = installedInfo(pm)
            if (archive.longVersionCodeCompat() <= installed.longVersionCodeCompat()) return "not newer than the installed app"
            val a = signers(archive)
            val b = signers(installed)
            if (a.isEmpty() || a != b) return "not signed by the same key as this app"
            return null
        }

        @Suppress("DEPRECATION")
        private fun archiveInfo(pm: PackageManager, apk: File): PackageInfo? {
            val flags = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
            return if (Build.VERSION.SDK_INT >= 33) pm.getPackageArchiveInfo(apk.path, PackageManager.PackageInfoFlags.of(flags.toLong()))
            else pm.getPackageArchiveInfo(apk.path, flags)
        }

        @Suppress("DEPRECATION")
        private fun installedInfo(pm: PackageManager): PackageInfo {
            val flags = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
            return if (Build.VERSION.SDK_INT >= 33) pm.getPackageInfo(ctx.packageName, PackageManager.PackageInfoFlags.of(flags.toLong()))
            else pm.getPackageInfo(ctx.packageName, flags)
        }

        @Suppress("DEPRECATION")
        private fun signers(p: PackageInfo): Set<String> {
            val sigs = if (Build.VERSION.SDK_INT >= 28) p.signingInfo?.apkContentsSigners else p.signatures
            return sigs?.map { Hashing.sha256Hex(it.toByteArray()) }?.toSet() ?: emptySet()
        }

        @Suppress("DEPRECATION")
        private fun PackageInfo.longVersionCodeCompat(): Long = if (Build.VERSION.SDK_INT >= 28) longVersionCode else versionCode.toLong()

        override fun isRecording() = RecorderStatus.isRecording

        override fun canInstallPackages() = ctx.packageManager.canRequestPackageInstalls()

        override fun install(apk: File) {
            val installer = ctx.packageManager.packageInstaller
            val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
                setAppPackageName(ctx.packageName)
                setSize(apk.length())
                if (Build.VERSION.SDK_INT >= 31) setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
            }
            val id = installer.createSession(params)
            try {
                installer.openSession(id).use { s ->
                    s.openWrite("base.apk", 0, apk.length()).use { out ->
                        apk.inputStream().use { it.copyTo(out, 64 * 1024) }
                        s.fsync(out)
                    }
                    val flags = PendingIntent.FLAG_UPDATE_CURRENT or (if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0)
                    val pi = PendingIntent.getBroadcast(ctx, id, Intent(ctx, InstallReceiver::class.java), flags)
                    s.commit(pi.intentSender)
                }
            } catch (e: IOException) {
                runCatching { installer.abandonSession(id) }
                throw UpdateFailure("install could not start")
            } catch (e: SecurityException) {
                runCatching { installer.abandonSession(id) }
                throw UpdateFailure("Android refused the install")
            }
        }

        override fun waitForRecordingToStop() = waitForRecording(ctx)
    }
}

/** Receives the PackageInstaller session result (not exported: only our PendingIntent reaches it). */
class InstallReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) = Updater.onInstallStatus(context, intent)
}

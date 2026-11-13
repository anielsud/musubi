package com.musubi.phone.core

import org.json.JSONArray
import java.io.File
import java.io.IOException
import java.net.URI

/** A dotted numeric version ("0.2.0"). Missing parts count as 0, so 0.2 == 0.2.0. */
class Version private constructor(val parts: List<Int>, private val text: String) : Comparable<Version> {
    override fun compareTo(other: Version): Int {
        for (i in 0 until maxOf(parts.size, other.parts.size)) {
            val c = parts.getOrElse(i) { 0 }.compareTo(other.parts.getOrElse(i) { 0 })
            if (c != 0) return c
        }
        return 0
    }

    override fun equals(other: Any?) = other is Version && compareTo(other) == 0
    override fun hashCode() = parts.dropLastWhile { it == 0 }.hashCode()
    override fun toString() = text

    companion object {
        private val RE = Regex("^\\d{1,6}(\\.\\d{1,6}){0,3}$")

        fun parse(s: String?): Version? {
            if (s == null || !RE.matches(s)) return null
            return Version(s.split('.').map { it.toInt() }, s)
        }
    }
}

data class ReleaseAsset(val name: String, val size: Long, val url: String)

data class UpdateCandidate(val version: Version, val tag: String, val apk: ReleaseAsset, val sha: ReleaseAsset)

/**
 * Where updates may come from. Production: the GitHub releases API for one repo, assets on
 * github.com, redirects only to GitHub's asset hosts, port 443, HTTPS only.
 */
data class UpdateSource(
    val apiUrl: String,
    val repo: String,
    val assetHosts: Set<String>,
    val redirectHosts: Set<String>,
    /** -1 = 443. */
    val port: Int = -1,
) {
    private fun checkBase(u: URI, hosts: Set<String>): Boolean {
        if (u.scheme != "https" || u.rawUserInfo != null || u.rawFragment != null) return false
        val host = u.host?.lowercase() ?: return false
        if (host !in hosts) return false
        val p = if (u.port == 443) -1 else u.port
        return p == port || (port == 443 && p == -1)
    }

    /** The asset URL in the release JSON: exactly https://<asset host>/<repo>/releases/download/<tag>/<name>. */
    fun isAllowedAssetUrl(url: String, tag: String, name: String): Boolean {
        val u = runCatching { URI(url) }.getOrNull() ?: return false
        if (!checkBase(u, assetHosts) || u.rawQuery != null) return false
        return u.rawPath == "/$repo/releases/download/$tag/$name"
    }

    /** A redirect target while downloading: HTTPS, an allowed asset host, no credentials. */
    fun isAllowedRedirect(url: String): Boolean {
        val u = runCatching { URI(url) }.getOrNull() ?: return false
        return checkBase(u, redirectHosts + assetHosts)
    }

    companion object {
        const val REPO = "anielsud/musubi-releases"

        val GITHUB = UpdateSource(
            apiUrl = "https://api.github.com/repos/$REPO/releases?per_page=100",
            repo = REPO,
            assetHosts = setOf("github.com"),
            redirectHosts = setOf("objects.githubusercontent.com", "release-assets.githubusercontent.com"),
        )

        /** A local mock for smoke tests; certificate pinned by the caller. */
        fun mock(base: String): UpdateSource? {
            val u = runCatching { URI(base) }.getOrNull() ?: return null
            if (u.scheme != "https" || u.host == null || u.rawUserInfo != null || u.rawPath.let { !it.isNullOrEmpty() && it != "/" }) return null
            val host = u.host.lowercase()
            return UpdateSource("https://${u.rawAuthority}/repos/$REPO/releases?per_page=100", REPO, setOf(host), setOf(host), u.port)
        }
    }
}

/**
 * Parsing of the GitHub releases list. The reply is untrusted: types are checked, lists are
 * capped, names and URLs must be exactly what we publish, sizes are bounded.
 */
object Releases {
    const val TAG_PREFIX = "android-v"
    const val MAX_APK_BYTES = 100L * 1024 * 1024
    const val MAX_SHA_BYTES = 4 * 1024
    const val MAX_REPLY_BYTES = 4 * 1024 * 1024
    private const val MAX_RELEASES = 300
    private const val MAX_ASSETS = 50

    fun apkName(v: String) = "musubi-phone-$v.apk"
    fun shaName(v: String) = "musubi-phone-$v.apk.sha256"

    /** The newest valid `android-v*` release above [installed], or null. Throws on a malformed reply. */
    fun pick(body: ByteArray, installed: Version, source: UpdateSource): UpdateCandidate? {
        require(body.size <= MAX_REPLY_BYTES) { "reply too large" }
        val arr = JSONArray(String(body, Charsets.UTF_8))
        var best: UpdateCandidate? = null
        for (i in 0 until minOf(arr.length(), MAX_RELEASES)) {
            val r = arr.optJSONObject(i) ?: continue
            val c = runCatching { candidate(r, source) }.getOrNull() ?: continue
            if (c.version > installed && (best == null || c.version > best.version)) best = c
        }
        return best
    }

    private fun candidate(r: org.json.JSONObject, source: UpdateSource): UpdateCandidate? {
        if (r.optBoolean("draft", false)) return null
        val tag = r.get("tag_name") as? String ?: return null
        if (!tag.startsWith(TAG_PREFIX)) return null
        val vText = tag.removePrefix(TAG_PREFIX)
        val version = Version.parse(vText) ?: return null
        val assets = r.optJSONArray("assets") ?: return null
        var apk: ReleaseAsset? = null
        var sha: ReleaseAsset? = null
        for (j in 0 until minOf(assets.length(), MAX_ASSETS)) {
            val a = assets.optJSONObject(j) ?: continue
            val name = a.opt("name") as? String ?: continue
            val url = a.opt("browser_download_url") as? String ?: continue
            val size = (a.opt("size") as? Number)?.toLong() ?: continue
            when (name) {
                apkName(vText) -> if (size in 1..MAX_APK_BYTES && source.isAllowedAssetUrl(url, tag, name)) apk = ReleaseAsset(name, size, url)
                shaName(vText) -> if (size in 1..MAX_SHA_BYTES.toLong() && source.isAllowedAssetUrl(url, tag, name)) sha = ReleaseAsset(name, size, url)
            }
        }
        if (apk == null || sha == null) return null
        return UpdateCandidate(version, tag, apk, sha)
    }

    /** `shasum -a 256` output ("<hex>  <name>") or a bare hex digest. Returns lower-case hex. */
    fun parseSha256File(bytes: ByteArray, expectedName: String): String {
        require(bytes.size <= MAX_SHA_BYTES) { "checksum file too large" }
        val line = String(bytes, Charsets.US_ASCII).lineSequence().firstOrNull { it.isNotBlank() }?.trim()
            ?: throw IllegalArgumentException("empty checksum file")
        val parts = line.split(Regex("\\s+"))
        require(parts.size <= 2) { "malformed checksum file" }
        val hex = parts[0].lowercase()
        require(Hashing.isSha256Hex(hex)) { "malformed checksum" }
        if (parts.size == 2) require(parts[1].removePrefix("*") == expectedName) { "checksum is for another file" }
        return hex
    }
}

/** What the phone does with updates; the Android side implements it (update/Updater.kt). */
interface UpdatePlatform {
    fun installedVersion(): Version
    /** The releases list; throws [UpdateFailure] or IOException. */
    fun fetchReleases(): ByteArray
    fun fetchSmall(url: String, maxBytes: Int): ByteArray
    /** Streams [url] into [dest] (at most [maxBytes]) and returns the SHA-256 hex of what was written. */
    fun download(url: String, maxBytes: Long, dest: File): String
    /** Null when [apk] is our package, version [expected], newer, signed by the installed app's certificate; else the reason. */
    fun verifyApk(apk: File, expected: Version): String?
    fun isRecording(): Boolean
    fun canInstallPackages(): Boolean
    fun install(apk: File)
    /** Called when an install waits for the recording; must call back [UpdateFlow.tryInstall] once it stops. */
    fun waitForRecordingToStop()
}

/** Persisted state: when we last checked, the verified APK waiting to install, the last result. */
interface UpdateState {
    var lastCheckAt: Long
    var pendingVersion: String?
    var pendingSha256: String?
    var status: String?
}

class UpdateFailure(message: String) : IOException(message)

sealed class UpdateResult(val text: String) {
    class Skipped(text: String) : UpdateResult(text)
    class UpToDate(v: Version) : UpdateResult("Up to date (v$v)")
    class Installing(v: String) : UpdateResult("Found v$v: installing")
    class WaitingForRecording(v: String) : UpdateResult("Found v$v: installs when the recording stops")
    class NeedsPermission(v: String) : UpdateResult("Found v$v: allow Musubi to install apps, then check again")
    class Failed(reason: String) : UpdateResult("Failed: $reason")
}

/**
 * The update decision, independent of Android: check at most every 6 hours (unless asked),
 * download and verify, and install only when no recording is running.
 */
class UpdateFlow(
    private val platform: UpdatePlatform,
    private val state: UpdateState,
    private val source: UpdateSource,
    private val dir: File,
    private val now: () -> Long = System::currentTimeMillis,
) {
    @Synchronized
    fun check(manual: Boolean): UpdateResult {
        // A pending update at or below the installed version was installed (the installer stopped us).
        val pending = Version.parse(state.pendingVersion)
        if (state.pendingVersion != null && (pending == null || runCatching { pending <= platform.installedVersion() }.getOrDefault(true))) {
            if (pending != null && pending == runCatching { platform.installedVersion() }.getOrNull()) state.status = "Updated to v$pending"
            clearPending()
        }
        if (!manual && state.pendingVersion == null && now() - state.lastCheckAt in 0 until CHECK_INTERVAL_MS) {
            return UpdateResult.Skipped(state.status ?: "")
        }
        state.lastCheckAt = now()
        val r = try {
            checkOnce()
        } catch (e: UpdateFailure) {
            UpdateResult.Failed(e.message ?: "unknown")
        } catch (e: IOException) {
            UpdateResult.Failed("GitHub not reachable")
        } catch (e: Exception) {
            UpdateResult.Failed("unexpected reply")
        }
        if (r is UpdateResult.Failed) clearPending()
        state.status = r.text
        return r
    }

    private fun checkOnce(): UpdateResult {
        val installed = platform.installedVersion()
        val body = platform.fetchReleases()
        val c = try {
            Releases.pick(body, installed, source)
        } catch (e: Exception) {
            throw UpdateFailure("unexpected reply from GitHub")
        }
        if (c == null) {
            clearPending()
            return UpdateResult.UpToDate(installed)
        }
        val v = c.version.toString()
        val apk = apkFile(v)
        if (state.pendingVersion != v || state.pendingSha256 == null || !apk.isFile) {
            clearPending()
            val shaText = platform.fetchSmall(c.sha.url, Releases.MAX_SHA_BYTES)
            val expected = try {
                Releases.parseSha256File(shaText, c.apk.name)
            } catch (e: IllegalArgumentException) {
                throw UpdateFailure("bad checksum file")
            }
            dir.mkdirs()
            val part = File(dir, apk.name + ".part")
            val got = try {
                platform.download(c.apk.url, Releases.MAX_APK_BYTES, part)
            } catch (e: IOException) {
                part.delete()
                throw e
            }
            if (got != expected) {
                part.delete()
                throw UpdateFailure("checksum does not match")
            }
            platform.verifyApk(part, c.version)?.let {
                part.delete()
                throw UpdateFailure(it)
            }
            if (!part.renameTo(apk)) {
                part.delete()
                throw UpdateFailure("could not save the download")
            }
            state.pendingVersion = v
            state.pendingSha256 = expected
        }
        return installPending()
    }

    /** Installs the verified download if one waits and nothing is recording. */
    @Synchronized
    fun tryInstall(): UpdateResult? {
        if (state.pendingVersion == null) return null
        val r = try {
            installPending()
        } catch (e: UpdateFailure) {
            UpdateResult.Failed(e.message ?: "unknown")
        } catch (e: Exception) {
            UpdateResult.Failed("install could not start")
        }
        if (r is UpdateResult.Failed) clearPending()
        state.status = r.text
        return r
    }

    private fun installPending(): UpdateResult {
        val v = state.pendingVersion ?: throw UpdateFailure("nothing to install")
        val version = Version.parse(v) ?: throw UpdateFailure("bad pending version")
        if (platform.isRecording()) {
            platform.waitForRecordingToStop()
            return UpdateResult.WaitingForRecording(v)
        }
        if (!platform.canInstallPackages()) return UpdateResult.NeedsPermission(v)
        val apk = apkFile(v)
        // Re-verify: the file sat on disk since it was checked.
        if (!apk.isFile || Hashing.sha256File(apk) != state.pendingSha256) throw UpdateFailure("download changed on the phone")
        platform.verifyApk(apk, version)?.let { throw UpdateFailure(it) }
        // The last moment before the installer replaces (and stops) this app.
        if (platform.isRecording()) {
            platform.waitForRecordingToStop()
            return UpdateResult.WaitingForRecording(v)
        }
        platform.install(apk)
        return UpdateResult.Installing(v)
    }

    @Synchronized
    fun installFailed(reason: String) {
        clearPending()
        state.status = UpdateResult.Failed(reason).text
    }

    private fun apkFile(v: String) = File(dir, Releases.apkName(v))

    private fun clearPending() {
        state.pendingVersion = null
        state.pendingSha256 = null
        dir.listFiles()?.forEach { it.delete() }
    }

    companion object {
        const val CHECK_INTERVAL_MS = 6L * 60 * 60 * 1000
    }
}

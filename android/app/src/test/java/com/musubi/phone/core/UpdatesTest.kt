package com.musubi.phone.core

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.File
import java.io.IOException

class UpdatesTest {
    private val gh = UpdateSource.GITHUB
    private fun v(s: String) = Version.parse(s)!!

    @Test fun versionComparison() {
        assertTrue(v("0.2.1") > v("0.2.0"))
        assertTrue(v("0.10.0") > v("0.9.9"))
        assertTrue(v("1.0") > v("0.99.99"))
        assertEquals(v("0.2"), v("0.2.0"))
        assertTrue(v("0.2.0.1") > v("0.2"))
        for (bad in listOf("", "v0.2.0", "0.2.0-rc1", "0..1", "1.2.3.4.5", "1234567", " 0.2", "0.2 ", "-1", "0x1", "１.2")) {
            assertNull(bad, Version.parse(bad))
        }
    }

    private fun asset(name: String, tag: String, size: Long = 1000, host: String = "github.com", path: String? = null) =
        """{"name":"$name","size":$size,"browser_download_url":"https://$host${path ?: "/anielsud/musubi-releases/releases/download/$tag/$name"}"}"""

    private fun release(ver: String, tag: String = "android-v$ver", extra: String = "", assets: String? = null) =
        """{"tag_name":"$tag"$extra,"assets":[${assets ?: asset("musubi-phone-$ver.apk", tag) + "," + asset("musubi-phone-$ver.apk.sha256", tag, 90)}]}"""

    private fun pick(vararg releases: String, installed: String = "0.2.0") =
        Releases.pick("[${releases.joinToString(",")}]".toByteArray(), v(installed), gh)

    @Test fun picksNewestAndroidTagAboveInstalled() {
        val c = pick(release("0.1.9"), release("0.3.0"), release("0.2.5"), release("0.2.0"), """{"tag_name":"mac-v9.0.0","assets":[]}""")!!
        assertEquals("0.3.0", c.version.toString())
        assertEquals("android-v0.3.0", c.tag)
        assertEquals("https://github.com/anielsud/musubi-releases/releases/download/android-v0.3.0/musubi-phone-0.3.0.apk", c.apk.url)
        assertNull(pick(release("0.2.0"), release("0.1.0")))
        assertNull(pick())
    }

    @Test fun hostileReleasesAreSkipped() {
        val tag = "android-v0.9.0"
        val hostile = listOf(
            release("0.9.0", extra = ""","draft":true"""),
            release("0.9.0", tag = "android-v0.9.0-evil"),
            release("0.9.0", tag = "android-v0.9.0/../../x"),
            release("9", tag = "android-v99999999"),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag)), // no checksum
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk.sha256", tag)), // no apk
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, host = "evil.example") + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, host = "github.com.evil.example") + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, host = "user@github.com") + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, host = "github.com:8443") + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, path = "/someone/else/releases/download/$tag/musubi-phone-0.9.0.apk") + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, path = "/anielsud/musubi-releases/releases/download/$tag/musubi-phone-0.9.0.apk?x=1") + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, size = Releases.MAX_APK_BYTES + 1) + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag, size = 0) + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            release("0.9.0", assets = asset("musubi-phone-0.9.0.apk", tag) + "," + asset("musubi-phone-0.9.0.apk.sha256", tag, size = 1_000_000)),
            release("0.9.0", assets = asset("musubi-phone-0.8.0.apk", tag) + "," + asset("musubi-phone-0.9.0.apk.sha256", tag)),
            """{"tag_name":"android-v0.9.0","assets":[{"name":"musubi-phone-0.9.0.apk","size":"1000","browser_download_url":"https://github.com/anielsud/musubi-releases/releases/download/android-v0.9.0/musubi-phone-0.9.0.apk"}]}""",
            """{"tag_name":12,"assets":[]}""",
            """{"tag_name":"android-v0.9.0","assets":"nope"}""",
            """"just a string"""",
            """null""",
            """{"tag_name":"android-v0.9.0","assets":[{"name":"musubi-phone-0.9.0.apk","size":1000,"browser_download_url":"http://github.com/anielsud/musubi-releases/releases/download/android-v0.9.0/musubi-phone-0.9.0.apk"},${asset("musubi-phone-0.9.0.apk.sha256", tag)}]}""",
        )
        for (h in hostile) assertNull(h, pick(h))
        // A valid older release still wins over hostile newer ones.
        assertEquals("0.3.0", pick(*(hostile + release("0.3.0")).toTypedArray())!!.version.toString())
    }

    @Test fun malformedRootThrows() {
        for (b in listOf("{}", "not json", "", "{\"message\":\"API rate limit exceeded\"}")) {
            try { Releases.pick(b.toByteArray(), v("0.2.0"), gh); fail(b) } catch (_: Exception) {}
        }
        try { Releases.pick(ByteArray(Releases.MAX_REPLY_BYTES + 1) { ' '.code.toByte() }, v("0.2.0"), gh); fail() } catch (_: IllegalArgumentException) {}
    }

    @Test fun manyReleasesAreCapped() {
        val many = (0 until 5000).joinToString(",") { """{"tag_name":"x$it","assets":[]}""" }
        assertNull(Releases.pick("[$many]".toByteArray(), v("0.2.0"), gh))
    }

    @Test fun sha256File() {
        val hex = "a".repeat(64)
        assertEquals(hex, Releases.parseSha256File("$hex  musubi-phone-0.3.0.apk\n".toByteArray(), "musubi-phone-0.3.0.apk"))
        assertEquals(hex, Releases.parseSha256File("${hex.uppercase()} *musubi-phone-0.3.0.apk".toByteArray(), "musubi-phone-0.3.0.apk"))
        assertEquals(hex, Releases.parseSha256File("\n$hex\n".toByteArray(), "musubi-phone-0.3.0.apk"))
        for (bad in listOf("", "xyz", "$hex  other.apk", "${hex}0", "$hex a b", "a".repeat(5000))) {
            try { Releases.parseSha256File(bad.toByteArray(), "musubi-phone-0.3.0.apk"); fail(bad) } catch (_: IllegalArgumentException) {}
        }
    }

    @Test fun redirectsOnlyToGitHubAssetHosts() {
        assertTrue(gh.isAllowedRedirect("https://objects.githubusercontent.com/github-production-release-asset/1?sig=x"))
        assertTrue(gh.isAllowedRedirect("https://release-assets.githubusercontent.com/x"))
        assertTrue(gh.isAllowedRedirect("https://github.com/x"))
        assertFalse(gh.isAllowedRedirect("http://objects.githubusercontent.com/x"))
        assertFalse(gh.isAllowedRedirect("https://evil.example/x"))
        assertFalse(gh.isAllowedRedirect("https://objects.githubusercontent.com.evil.example/x"))
        assertFalse(gh.isAllowedRedirect("https://a@objects.githubusercontent.com/x"))
        assertFalse(gh.isAllowedRedirect("https://objects.githubusercontent.com:444/x"))
        assertFalse(gh.isAllowedRedirect("file:///etc/passwd"))
        assertFalse(gh.isAllowedRedirect("javascript:alert(1)"))
        val m = UpdateSource.mock("https://10.0.2.2:8443")!!
        assertTrue(m.isAllowedRedirect("https://10.0.2.2:8443/assets/a.apk"))
        assertFalse(m.isAllowedRedirect("https://10.0.2.2/assets/a.apk"))
        assertNull(UpdateSource.mock("http://10.0.2.2:8443"))
        assertNull(UpdateSource.mock("https://u@10.0.2.2:8443"))
    }

    // ---- UpdateFlow: gating while recording, intervals, verification ----

    private class FakeState : UpdateState {
        override var lastCheckAt = 0L
        override var pendingVersion: String? = null
        override var pendingSha256: String? = null
        override var status: String? = null
    }

    private val apkBytes = "fake apk".toByteArray()
    private val apkSha = Hashing.sha256Hex(apkBytes)

    private inner class FakePlatform : UpdatePlatform {
        var recording = false
        var canInstall = true
        var verifyResult: String? = null
        var releases = "[${release("0.3.0")}]".toByteArray()
        var shaFile = "$apkSha  musubi-phone-0.3.0.apk".toByteArray()
        var served = apkBytes
        var fetches = 0
        var waits = 0
        val installed = ArrayList<File>()
        override fun installedVersion() = v("0.2.0")
        override fun fetchReleases(): ByteArray { fetches++; return releases }
        override fun fetchSmall(url: String, maxBytes: Int) = shaFile
        override fun download(url: String, maxBytes: Long, dest: File): String { dest.writeBytes(served); return Hashing.sha256Hex(served) }
        override fun verifyApk(apk: File, expected: Version) = verifyResult
        override fun isRecording() = recording
        override fun canInstallPackages() = canInstall
        override fun install(apk: File) { installed += apk }
        override fun waitForRecordingToStop() { waits++ }
    }

    private fun flow(p: UpdatePlatform, s: UpdateState, now: () -> Long = { 1_000_000_000L }) =
        UpdateFlow(p, s, gh, TestUtil.tempDir(), now)

    @Test fun installsWhenIdle() {
        val p = FakePlatform(); val s = FakeState()
        val r = flow(p, s).check(manual = true)
        assertTrue(r.text, r is UpdateResult.Installing)
        assertEquals(1, p.installed.size)
        assertEquals("musubi-phone-0.3.0.apk", p.installed[0].name)
        assertEquals("Found v0.3.0: installing", s.status)
    }

    @Test fun neverInstallsWhileRecordingThenInstallsAfter() {
        val p = FakePlatform().apply { recording = true }
        val s = FakeState()
        val f = flow(p, s)
        val r = f.check(manual = true)
        assertTrue(r is UpdateResult.WaitingForRecording)
        assertEquals("Found v0.3.0: installs when the recording stops", s.status)
        assertTrue(p.installed.isEmpty())
        assertEquals(1, p.waits)
        assertEquals("0.3.0", s.pendingVersion)
        // Still recording: still waiting, still nothing installed.
        assertTrue(f.tryInstall() is UpdateResult.WaitingForRecording)
        assertTrue(p.installed.isEmpty())
        p.recording = false
        assertTrue(f.tryInstall() is UpdateResult.Installing)
        assertEquals(1, p.installed.size)
    }

    @Test fun recordingThatStartsDuringVerificationStillBlocksInstall() {
        val s = FakeState()
        var calls = 0
        val p = object : UpdatePlatform by FakePlatform() {
            val inner = FakePlatform()
            override fun isRecording() = ++calls > 1 // idle at the first check, recording at the last moment
            override fun install(apk: File) = fail("installed while recording")
            override fun download(url: String, maxBytes: Long, dest: File) = inner.download(url, maxBytes, dest)
            override fun fetchReleases() = inner.fetchReleases()
            override fun fetchSmall(url: String, maxBytes: Int) = inner.fetchSmall(url, maxBytes)
        }
        assertTrue(flow(p, s).check(manual = true) is UpdateResult.WaitingForRecording)
    }

    @Test fun startCheckAtMostEverySixHours() {
        val p = FakePlatform().apply { releases = "[]".toByteArray() }
        val s = FakeState()
        var t = 100_000_000L
        val f = flow(p, s) { t }
        assertTrue(f.check(manual = false) is UpdateResult.UpToDate)
        t += UpdateFlow.CHECK_INTERVAL_MS - 1
        assertTrue(f.check(manual = false) is UpdateResult.Skipped)
        assertEquals(1, p.fetches)
        assertTrue(f.check(manual = true) is UpdateResult.UpToDate)
        assertEquals(2, p.fetches)
        t += UpdateFlow.CHECK_INTERVAL_MS
        f.check(manual = false)
        assertEquals(3, p.fetches)
        assertEquals("Up to date (v0.2.0)", s.status)
    }

    @Test fun checksumMismatchIsRefused() {
        val p = FakePlatform().apply { served = "tampered".toByteArray() }
        val s = FakeState()
        val r = flow(p, s).check(manual = true)
        assertEquals("Failed: checksum does not match", r.text)
        assertTrue(p.installed.isEmpty())
        assertNull(s.pendingVersion)
    }

    @Test fun wrongSignerIsRefused() {
        val p = FakePlatform().apply { verifyResult = "not signed by the same key as this app" }
        val s = FakeState()
        assertEquals("Failed: not signed by the same key as this app", flow(p, s).check(manual = true).text)
        assertTrue(p.installed.isEmpty())
    }

    @Test fun badChecksumFileAndNetworkErrors() {
        val p = FakePlatform().apply { shaFile = "garbage".toByteArray() }
        assertEquals("Failed: bad checksum file", flow(p, FakeState()).check(manual = true).text)
        val q = object : UpdatePlatform by FakePlatform() {
            override fun fetchReleases(): ByteArray = throw IOException("down")
        }
        assertEquals("Failed: GitHub not reachable", flow(q, FakeState()).check(manual = true).text)
        val r = object : UpdatePlatform by FakePlatform() {
            override fun fetchReleases(): ByteArray = throw UpdateFailure("GitHub refused (HTTP 403)")
        }
        assertEquals("Failed: GitHub refused (HTTP 403)", flow(r, FakeState()).check(manual = true).text)
        val g = FakePlatform().apply { releases = "{\"message\":\"x\"}".toByteArray() }
        assertEquals("Failed: unexpected reply from GitHub", flow(g, FakeState()).check(manual = true).text)
    }

    @Test fun needsPermissionKeepsDownload() {
        val p = FakePlatform().apply { canInstall = false }
        val s = FakeState()
        val f = flow(p, s)
        assertTrue(f.check(manual = true) is UpdateResult.NeedsPermission)
        assertEquals("0.3.0", s.pendingVersion)
        p.canInstall = true
        assertTrue(f.tryInstall() is UpdateResult.Installing)
    }

    @Test fun changedDownloadIsNotInstalled() {
        val p = FakePlatform().apply { recording = true }
        val s = FakeState()
        val dir = TestUtil.tempDir()
        val f = UpdateFlow(p, s, gh, dir) { 1L }
        f.check(manual = true)
        File(dir, "musubi-phone-0.3.0.apk").writeBytes("swapped".toByteArray())
        p.recording = false
        assertEquals("Failed: download changed on the phone", f.tryInstall()!!.text)
        assertTrue(p.installed.isEmpty())
    }

    @Test fun pendingAtInstalledVersionIsClearedOnStart() {
        val p = FakePlatform().apply { releases = "[]".toByteArray() }
        val s = FakeState().apply { pendingVersion = "0.2.0"; pendingSha256 = "a".repeat(64); lastCheckAt = 1L }
        val r = flow(p, s) { 2L }.check(manual = false)
        assertTrue(r is UpdateResult.Skipped)
        assertNull(s.pendingVersion)
        assertEquals("Updated to v0.2.0", s.status)
        assertEquals(0, p.fetches)
    }
}

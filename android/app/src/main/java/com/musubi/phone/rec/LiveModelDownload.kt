package com.musubi.phone.rec

import android.content.Context
import com.musubi.phone.core.LiveModel
import com.musubi.phone.core.ModelDownloader
import java.util.concurrent.Executors

/** The one model download, shared by Settings and the Recording screen. Main process only. */
object LiveModelDownload {
    @Volatile var running = false
        private set
    @Volatile var done = 0L
        private set
    /** Why the last download failed, until the next one starts. */
    @Volatile var error: String? = null
        private set
    @Volatile private var current: ModelDownloader? = null
    private val io = Executors.newSingleThreadExecutor()

    @Synchronized
    fun start(ctx: Context) {
        if (running) return
        val dir = LiveModel.dir(ctx.applicationContext.filesDir)
        val d = ModelDownloader(dir, LiveModel.FILES, LiveModel::url)
        current = d
        running = true
        error = null
        done = 0
        io.execute {
            try {
                d.run { n, _ -> done = n }
            } catch (e: ModelDownloader.DownloadError) {
                error = e.message
            } catch (e: Exception) {
                error = "Download failed: check the connection and try again"
            } finally {
                current = null
                running = false
            }
        }
    }

    fun cancel() {
        current?.cancelled = true
    }
}

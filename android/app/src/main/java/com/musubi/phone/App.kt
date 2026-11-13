package com.musubi.phone

import android.app.Application
import android.content.Context
import android.os.Build
import com.musubi.phone.core.AgendaStore
import com.musubi.phone.core.RecordingStore
import com.musubi.phone.rec.RecorderService
import com.musubi.phone.sync.SyncScheduler
import java.io.File

class App : Application() {
    lateinit var store: RecordingStore
        private set
    lateinit var agendaStore: AgendaStore
        private set

    override fun onCreate() {
        super.onCreate()
        // The live-text process (":live") runs only the recognizer: it must not touch recordings.
        if (isLiveProcess()) return
        // App-private storage only (no external or shared storage).
        store = RecordingStore(File(filesDir, "recordings"))
        agendaStore = AgendaStore(File(filesDir, "agenda.json"))
        // A new process means no recorder is running: close anything a killed process left open.
        runCatching { store.recoverInterrupted() }
        RecorderService.createChannel(this)
        SyncScheduler.schedulePeriodic(this)
    }

    private fun isLiveProcess(): Boolean {
        val name = if (Build.VERSION.SDK_INT >= 28) getProcessName()
        else runCatching { File("/proc/self/cmdline").readText().trimEnd('\u0000') }.getOrDefault("")
        return name.endsWith(":live")
    }

    companion object {
        fun store(ctx: Context): RecordingStore = (ctx.applicationContext as App).store
        fun agenda(ctx: Context): AgendaStore = (ctx.applicationContext as App).agendaStore
    }
}

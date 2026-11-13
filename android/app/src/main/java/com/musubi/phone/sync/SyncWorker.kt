package com.musubi.phone.sync

import android.content.Context
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.musubi.phone.App
import com.musubi.phone.core.SyncEngine
import com.musubi.phone.core.SyncOutcome
import com.musubi.phone.net.HttpMacApi
import com.musubi.phone.net.MacSettings
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.util.concurrent.TimeUnit

class SyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result = withContext(Dispatchers.IO) {
        SyncScheduler.runOnce(applicationContext)
        Result.success()
    }
}

object SyncScheduler {
    private const val PERIODIC = "sync-periodic"
    private const val NOW = "sync-now"
    private val lock = Any()

    @Volatile var running = false
        private set

    private val network = Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build()

    /** Every 15 minutes (WorkManager's minimum) while a network is up; cheap when the Mac is away. */
    fun schedulePeriodic(ctx: Context) {
        val req = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES).setConstraints(network).build()
        WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(PERIODIC, ExistingPeriodicWorkPolicy.KEEP, req)
    }

    /** Sync now (button) and after each recording stops. */
    fun syncSoon(ctx: Context) {
        val req = OneTimeWorkRequestBuilder<SyncWorker>().setConstraints(network).build()
        WorkManager.getInstance(ctx).enqueueUniqueWork(NOW, ExistingWorkPolicy.REPLACE, req)
    }

    /** One run; never two at once. Records the outcome for the Mac screen. */
    fun runOnce(ctx: Context) {
        synchronized(lock) {
            val settings = MacSettings(ctx)
            val addr = settings.address
            val pin = settings.pin
            val token = settings.token()
            if (addr == null || pin == null || token == null) return
            running = true
            try {
                val report = SyncEngine(App.store(ctx), App.agenda(ctx), HttpMacApi(addr, pin, token)).run()
                if (report.outcome == SyncOutcome.DONE) settings.lastSyncAt = System.currentTimeMillis()
                settings.lastResult = report.message
            } catch (e: Exception) {
                settings.lastResult = "Sync error"
            } finally {
                running = false
            }
        }
    }
}

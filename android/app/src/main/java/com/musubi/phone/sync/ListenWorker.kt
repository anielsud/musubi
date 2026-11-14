package com.musubi.phone.sync

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.Worker
import androidx.work.WorkerParameters
import com.musubi.phone.core.ListenWindows
import java.util.concurrent.TimeUnit

/**
 * The periodic listening window: every 15 minutes while a network is up (Android may space runs
 * out when the phone is idle), the phone listens for up to 3 minutes. A job, not a foreground
 * service: jobs may run and use the network from the background on Android 14/15, and a job's
 * 10-minute limit covers the window plus a sync in progress.
 */
class ListenWorker(ctx: Context, params: WorkerParameters) : Worker(ctx, params) {
    override fun doWork(): Result {
        if (!PhoneSync.openWindow(ListenWindows.Kind.PERIODIC, ListenWindows.PERIODIC_MS)) return Result.success()
        PhoneSync.awaitWindowEnd(ListenWindows.Kind.PERIODIC, MAX_MS)
        PhoneSync.closeWindow(ListenWindows.Kind.PERIODIC)
        return Result.success()
    }

    override fun onStopped() {
        PhoneSync.closeWindow(ListenWindows.Kind.PERIODIC)
    }

    companion object {
        private const val NAME = "listen-periodic"
        /** Window plus a sync that is still running, inside the job's 10-minute limit. */
        private const val MAX_MS = 9 * 60_000L

        fun schedule(ctx: Context) {
            val req = PeriodicWorkRequestBuilder<ListenWorker>(15, TimeUnit.MINUTES)
                .setConstraints(Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build())
                .build()
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(NAME, ExistingPeriodicWorkPolicy.KEEP, req)
        }
    }
}

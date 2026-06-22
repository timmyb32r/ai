package com.crimobile.offline

import android.content.Context
import android.util.Log
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import java.util.Calendar
import java.util.concurrent.TimeUnit

/**
 * Manages WorkManager periodic scheduling for offline audio sync.
 *
 * Usage:
 *   SyncScheduler.schedule(context, config)   // arm the daily sync
 *   SyncScheduler.cancel(context)              // disarm
 */
object SyncScheduler {
    private const val WORK_NAME = "cri_offline_sync"
    private const val TAG = "CRIRadio:SyncScheduler"

    /** Enqueue a periodic sync job driven by [config]. */
    fun schedule(context: Context, config: SyncConfig) {
        if (!config.enabled) {
            cancel(context)
            return
        }

        val constraints = Constraints.Builder()
            .setRequiredNetworkType(
                if (config.wifiOnly) NetworkType.UNMETERED
                else NetworkType.CONNECTED
            )
            .build()

        val initialDelayMs = computeInitialDelayMs(config.syncHourOfDay, config.syncMinute)

        val request = PeriodicWorkRequestBuilder<SyncWorker>(
            24, TimeUnit.HOURS  // daily
        )
            .setConstraints(constraints)
            .setInitialDelay(initialDelayMs, TimeUnit.MILLISECONDS)
            .addTag(WORK_NAME)
            .build()

        WorkManager.getInstance(context)
            .enqueueUniquePeriodicWork(
                WORK_NAME,
                ExistingPeriodicWorkPolicy.UPDATE,
                request
            )

        Log.i(TAG, "Scheduled daily sync at ${"%02d".format(config.syncHourOfDay)}:" +
            "${"%02d".format(config.syncMinute)} " +
            "wifiOnly=${config.wifiOnly} initialDelay=${initialDelayMs}ms")
    }

    /** Remove the scheduled sync job. */
    fun cancel(context: Context) {
        WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME)
        Log.i(TAG, "Sync cancelled")
    }

    // ── Internal ───────────────────────────────────────────────────────

    /**
     * Returns milliseconds until the next occurrence of [hour]:[minute].
     * If that time is already past today, returns delay until tomorrow.
     */
    private fun computeInitialDelayMs(hour: Int, minute: Int): Long {
        val now = Calendar.getInstance()
        val target = Calendar.getInstance().apply {
            set(Calendar.HOUR_OF_DAY, hour)
            set(Calendar.MINUTE, minute)
            set(Calendar.SECOND, 0)
            set(Calendar.MILLISECOND, 0)
        }

        if (target.timeInMillis <= now.timeInMillis) {
            target.add(Calendar.DAY_OF_YEAR, 1)
        }

        return target.timeInMillis - now.timeInMillis
    }
}

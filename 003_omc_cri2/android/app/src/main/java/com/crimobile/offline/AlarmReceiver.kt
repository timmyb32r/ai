package com.crimobile.offline

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import com.crimobile.debug.DebugLogger
import java.util.concurrent.TimeUnit

/**
 * Receives the exact alarm from [SyncScheduler] and enqueues a one-shot
 * [SyncWorker] to download offline content.
 *
 * The alarm fires even in Doze ([AlarmManager.setExactAndAllowWhileIdle]),
 * so the sync starts at the scheduled time regardless of device state.
 */
class AlarmReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        DebugLogger.i(TAG, "Alarm fired — enqueuing sync worker")

        val prefs = context.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)
        val config = SyncConfig.fromPrefs(prefs)

        if (!config.enabled) {
            DebugLogger.i(TAG, "Sync disabled, skipping")
            return
        }

        val constraints = Constraints.Builder()
            .setRequiredNetworkType(
                if (config.wifiOnly) NetworkType.UNMETERED
                else NetworkType.CONNECTED
            )
            .build()

        val work = OneTimeWorkRequestBuilder<SyncWorker>()
            .setConstraints(constraints)
            // Exponential backoff so a persistently unreachable server does not
            // cause a retry storm; the SyncWorker attempt cap is the second guard.
            .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.MINUTES)
            .setInitialDelay(0, TimeUnit.SECONDS)
            .addTag(WORK_NAME)
            .build()

        // KEEP: if a previous sync is still running (a large download can exceed
        // the 24h interval), do not cancel it mid-flight — a mid-download REPLACE
        // previously left sessions with partial data and no segment index.
        WorkManager.getInstance(context)
            .enqueueUniqueWork(WORK_NAME, ExistingWorkPolicy.KEEP, work)

        DebugLogger.i(TAG, "Sync worker enqueued (wifiOnly=${config.wifiOnly})")
    }

    companion object {
        private const val TAG = "CRIRadio:AlarmReceiver"
        private const val WORK_NAME = "cri_offline_sync"
    }
}

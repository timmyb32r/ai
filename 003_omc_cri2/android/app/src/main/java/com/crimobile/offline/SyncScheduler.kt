package com.crimobile.offline

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import java.util.Calendar
import java.util.concurrent.TimeUnit
import com.crimobile.debug.DebugLogger

/**
 * Schedules daily offline sync using [AlarmManager.setExactAndAllowWhileIdle]
 * so the alarm fires at the exact time even when the device is in Doze.
 *
 * Architecture:
 *   1. [schedule] sets an exact alarm → [AlarmReceiver] fires
 *   2. [AlarmReceiver] enqueues a one-shot [SyncWorker]
 *   3. [SyncWorker] re-arms the alarm for tomorrow on success
 *   4. [BootReceiver] re-arms after device reboot
 *
 * Fallback: if the device lacks [android.Manifest.permission.SCHEDULE_EXACT_ALARM]
 * (Android 12+, user may deny), falls back to WorkManager periodic work
 * (less reliable but still works in maintenance windows).
 */
object SyncScheduler {
    private const val WORK_NAME = "cri_offline_sync"
    private const val TAG = "CRIRadio:SyncScheduler"

    /** Enqueue a daily sync job driven by [config]. */
    fun schedule(context: Context, config: SyncConfig) {
        if (!config.enabled) {
            cancel(context)
            return
        }

        // Attempt exact alarm first; fall back to periodic WorkManager
        if (canScheduleExactAlarms(context)) {
            scheduleExactAlarm(context, config)
        } else {
            DebugLogger.w(TAG, "SCHEDULE_EXACT_ALARM not granted — falling back to periodic WorkManager")
            schedulePeriodicFallback(context, config)
        }
    }

    /** Remove the scheduled sync job. */
    fun cancel(context: Context) {
        cancelExactAlarm(context)
        WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME)
        DebugLogger.i(TAG, "Sync cancelled")
    }

    // ── Exact alarm path ────────────────────────────────────────────────

    private fun scheduleExactAlarm(context: Context, config: SyncConfig) {
        val fireAtMs = computeNextFireMs(config.syncHourOfDay, config.syncMinute)

        val intent = Intent(context, AlarmReceiver::class.java)
        val pending = PendingIntent.getBroadcast(
            context, 0, intent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val am = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
        am.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, fireAtMs, pending)

        DebugLogger.i(TAG, "Exact alarm set for ${"%02d".format(config.syncHourOfDay)}:" +
            "${"%02d".format(config.syncMinute)} fireAt=$fireAtMs " +
            "(in ${(fireAtMs - System.currentTimeMillis()) / 1000 / 60} min) " +
            "wifiOnly=${config.wifiOnly}")
    }

    private fun cancelExactAlarm(context: Context) {
        val intent = Intent(context, AlarmReceiver::class.java)
        val pending = PendingIntent.getBroadcast(
            context, 0, intent,
            PendingIntent.FLAG_NO_CREATE or PendingIntent.FLAG_IMMUTABLE
        ) ?: return
        val am = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
        am.cancel(pending)
        pending.cancel()
    }

    // ── Periodic WorkManager fallback ───────────────────────────────────

    private fun schedulePeriodicFallback(context: Context, config: SyncConfig) {
        val constraints = Constraints.Builder()
            .setRequiredNetworkType(
                if (config.wifiOnly) NetworkType.UNMETERED
                else NetworkType.CONNECTED
            )
            .build()

        val request = androidx.work.PeriodicWorkRequestBuilder<SyncWorker>(
            24, TimeUnit.HOURS
        )
            .setConstraints(constraints)
            .setInitialDelay(computeDelayToNext(config.syncHourOfDay, config.syncMinute), TimeUnit.MILLISECONDS)
            .addTag(WORK_NAME)
            .build()

        WorkManager.getInstance(context)
            .enqueueUniquePeriodicWork(WORK_NAME, androidx.work.ExistingPeriodicWorkPolicy.UPDATE, request)

        DebugLogger.i(TAG, "Periodic fallback scheduled at ${"%02d".format(config.syncHourOfDay)}:" +
            "${"%02d".format(config.syncMinute)} wifiOnly=${config.wifiOnly}")
    }

    // ── Public helpers ──────────────────────────────────────────────────

    /** Call this from [SyncWorker] after a successful sync to re-arm for tomorrow. */
    fun rearmForTomorrow(context: Context) {
        val prefs = context.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)
        val config = SyncConfig.fromPrefs(prefs)
        if (!config.enabled) return
        // Shift the fire time to tomorrow to avoid re-firing immediately
        val fireAtMs = computeTomorrowMs(config.syncHourOfDay, config.syncMinute)
        if (canScheduleExactAlarms(context)) {
            val intent = Intent(context, AlarmReceiver::class.java)
            val pending = PendingIntent.getBroadcast(
                context, 0, intent,
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
            )
            val am = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
            am.setExactAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, fireAtMs, pending)
            DebugLogger.i(TAG, "Re-armed exact alarm for tomorrow at " +
                "${"%02d".format(config.syncHourOfDay)}:${"%02d".format(config.syncMinute)}")
        }
    }

    fun canScheduleExactAlarms(context: Context): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.S) return true
        val am = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
        return am.canScheduleExactAlarms()
    }

    // ── Time computation ────────────────────────────────────────────────

    /** Milliseconds until the next occurrence of [hour]:[minute] today or tomorrow. */
    fun computeNextFireMs(hour: Int, minute: Int): Long {
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
        return target.timeInMillis
    }

    private fun computeTomorrowMs(hour: Int, minute: Int): Long {
        val target = Calendar.getInstance().apply {
            add(Calendar.DAY_OF_YEAR, 1)
            set(Calendar.HOUR_OF_DAY, hour)
            set(Calendar.MINUTE, minute)
            set(Calendar.SECOND, 0)
            set(Calendar.MILLISECOND, 0)
        }
        return target.timeInMillis
    }

    private fun computeDelayToNext(hour: Int, minute: Int): Long {
        return computeNextFireMs(hour, minute) - System.currentTimeMillis()
    }
}

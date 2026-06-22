package com.crimobile.offline

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.work.CoroutineWorker
import androidx.work.ForegroundInfo
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.crimobile.ServerConfig

/**
 * WorkManager CoroutineWorker that downloads the configured sync window
 * of audio + subtitles for offline playback.
 *
 * Scheduled by [SyncScheduler]. Runs with WiFi-constraint when configured.
 */
class SyncWorker(
    context: Context,
    params: WorkerParameters
) : CoroutineWorker(context, params) {

    override suspend fun doWork(): Result {
        val prefs = applicationContext.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)
        val config = SyncConfig.fromPrefs(prefs)

        if (!config.enabled) {
            Log.i(TAG, "Sync disabled — skipping")
            return Result.success()
        }

        val serverUrl = ServerConfig.defaultUrl
        val storageManager = OfflineStorageManager(applicationContext)
        val engine = DownloadEngine(applicationContext, serverUrl, storageManager)

        // Show foreground notification on Android 14+ for data sync
        setForeground(createForegroundInfo())

        return try {
            // Determine sync window: [now - duration, now]
            val nowSec = System.currentTimeMillis() / 1000.0
            var startSec = nowSec - config.syncDurationSec
            val endSec = nowSec

            // Validate against server archive
            val archive = engine.fetchArchiveInfo()
            if (archive.oldestStartSec > 0.0) {
                if (startSec < archive.oldestStartSec) {
                    Log.w(TAG, "Sync window start clamped to archive: " +
                        "${startSec} → ${archive.oldestStartSec}")
                    startSec = archive.oldestStartSec
                }
                if (startSec >= endSec) {
                    Log.w(TAG, "No new content in archive for configured window")
                    return Result.success()
                }
            }

            val result = engine.downloadRange(startSec, endSec) { progress ->
                // Update foreground notification progress
                if (progress.isRunning && progress.totalSegments > 0) {
                    setForeground(createForegroundInfo(
                        current = progress.downloadedSegments,
                        total = progress.totalSegments
                    ))
                }
            }

            if (result.isSuccess) {
                SyncConfig.save(prefs, config.copy(
                    lastSyncTimestamp = System.currentTimeMillis(),
                    initialSyncDone = true
                ))
                Log.i(TAG, "Sync complete — ${storageManager.countSegments()} segments stored")
                Result.success()
            } else {
                Log.w(TAG, "Sync failed: ${result.exceptionOrNull()?.message}")
                Result.retry()
            }
        } catch (e: Exception) {
            Log.e(TAG, "Sync error: ${e.message}", e)
            Result.retry()
        }
    }

    private fun createForegroundInfo(
        current: Int = 0,
        total: Int = 0
    ): ForegroundInfo {
        createNotificationChannel()

        val cancelIntent = WorkManager.getInstance(applicationContext)
            .createCancelPendingIntent(id)

        val notification = NotificationCompat.Builder(applicationContext, CHANNEL_ID)
            .setContentTitle("Syncing CRI Radio")
            .setContentText(
                if (total > 0) "Downloading segment $current of $total…"
                else "Preparing download…"
            )
            .setSmallIcon(android.R.drawable.stat_sys_download)
            .setOngoing(true)
            .addAction(android.R.drawable.ic_menu_close_clear_cancel, "Cancel", cancelIntent)
            .apply {
                if (total > 0) {
                    setProgress(total, current, false)
                } else {
                    setProgress(0, 0, true)
                }
            }
            .build()

        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            ForegroundInfo(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            ForegroundInfo(NOTIFICATION_ID, notification)
        }
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID,
                "CRI Sync",
                NotificationManager.IMPORTANCE_LOW
            ).apply {
                description = "Offline audio sync progress"
                setShowBadge(false)
            }
            val nm = applicationContext.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            nm.createNotificationChannel(channel)
        }
    }

    companion object {
        private const val TAG = "CRIRadio:SyncWorker"
        private const val CHANNEL_ID = "cri_sync"
        private const val NOTIFICATION_ID = 202
    }
}

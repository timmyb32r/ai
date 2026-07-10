package com.crimobile

import android.app.Application
import android.content.Intent
import android.util.Log

/**
 * Bootstraps crash handler, then starts PlayerService so the player
 * is ready before the ViewModel needs it.
 *
 * Uses [startForegroundService] because [PlayerService] calls
 * [startForeground] within the 5-second window — this is the
 * correct API on Android 8+ and avoids [android.app.BackgroundServiceStartNotAllowedException]
 * on Android 12+ when the process restarts.
 */
class CriApplication : Application() {
    override fun onCreate() {
        super.onCreate()

        // ── First: install global crash handler ──
        CrashHandler.install(this)

        // ── Then: start the foreground media service ──
        try {
            startForegroundService(Intent(this, PlayerService::class.java))
        } catch (e: Exception) {
            // Survive service start failures (e.g. app restarted in background
            // after a crash). The ViewModel handles a missing player gracefully.
            Log.e(TAG, "Failed to start PlayerService: ${e.message}")
        }
    }

    companion object {
        private const val TAG = "CRIRadio:app"
    }
}

package com.crimobile

import android.app.Application
import android.content.Intent
import android.os.Build
import com.crimobile.debug.DebugLogger

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

        // Log to LogCat BEFORE touching DebugLogger — so even if
        // the file logger is completely broken, this line IS in LogCat.
        android.util.Log.i(TAG, "onCreate BEGIN — about to init DebugLogger")

        // ── Init file logger and enable it unconditionally ──
        DebugLogger.init(this)
        DebugLogger.enabled = true
        DebugLogger.i(TAG, "========== APP START ==========")
        DebugLogger.i(TAG, "device=${Build.MODEL} sdk=${Build.VERSION.SDK_INT}")

        // ── Install global crash handler ──
        // Logging/crash infrastructure must never take down the app at startup.
        try {
            CrashHandler.install(this)
        } catch (t: Throwable) {
            android.util.Log.e(TAG, "CrashHandler.install failed: ${t.message}", t)
        }

        // ── Start the foreground media service ──
        try {
            DebugLogger.i(TAG, "starting PlayerService…")
            startForegroundService(Intent(this, PlayerService::class.java))
            DebugLogger.i(TAG, "PlayerService start command sent")
        } catch (e: Throwable) {
            DebugLogger.e(TAG, "Failed to start PlayerService: ${e.message}", e)
        }
    }

    companion object {
        private const val TAG = "CRIRadio:app"
    }
}

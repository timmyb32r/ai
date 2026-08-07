package com.crimobile

import android.app.Application
import android.os.Build
import com.crimobile.debug.DebugLogger

/**
 * Bootstraps the file logger and the global crash handler.
 *
 * PlayerService is NOT started here — on Android 12+ a foreground-service start
 * from Application.onCreate can be deferred by the system, which left the
 * ViewModel waiting 10s for the player on every cold start. The service is now
 * started from MainActivity.onCreate (foreground), where the start is honoured
 * immediately.
 */
class CriApplication : Application() {
    override fun onCreate() {
        super.onCreate()

        // Log to LogCat BEFORE touching DebugLogger — so even if
        // the file logger is completely broken, this line IS in LogCat.
        android.util.Log.i(TAG, "onCreate BEGIN — about to init DebugLogger")

        // ── Init file logger ──
        DebugLogger.init(this)
        // Sync the enable flag with the persisted prefs value so the Settings
        // switch ("Write logs to file") reflects reality. Default ON so logs are
        // captured for diagnostics on a fresh install (was unconditionally true,
        // which left the switch showing OFF while logs were actually written).
        val prefs = getSharedPreferences("cri_prefs", MODE_PRIVATE)
        DebugLogger.enabled = prefs.getBoolean("log_to_file_enabled", true)
        DebugLogger.i(TAG, "========== APP START ==========")
        DebugLogger.i(TAG, "device=${Build.MODEL} sdk=${Build.VERSION.SDK_INT}")

        // ── Install global crash handler ──
        // Logging/crash infrastructure must never take down the app at startup.
        try {
            CrashHandler.install(this)
        } catch (t: Throwable) {
            android.util.Log.e(TAG, "CrashHandler.install failed: ${t.message}", t)
        }
    }

    companion object {
        private const val TAG = "CRIRadio:app"
    }
}

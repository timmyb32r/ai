package com.crimobile

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.core.content.ContextCompat
import com.crimobile.debug.DebugLogger
import com.crimobile.ui.CriApp
import com.crimobile.viewmodel.CriViewModel
import com.crimobile.PlayerService

class MainActivity : ComponentActivity() {

    private val viewModel: CriViewModel by viewModels()

    /**
     * On Android 13+ (API 33+) notifications are opt-in.
     * Foreground-service notifications are technically exempt, but the
     * media widget (MediaStyle) relies on the notification channel being
     * visible — asking for permission ensures the widget is shown
     * regardless of manufacturer-specific behaviour.
     */
    private val notificationPermissionLauncher =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            DebugLogger.i(TAG, "POST_NOTIFICATIONS granted=$granted")
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // File logger already initialised in CriApplication; re-init is harmless.
        DebugLogger.i(TAG, "MainActivity.onCreate — UI starting")

        requestNotificationPermission()
        requestBatteryOptimizationExemption()

        // Start the foreground media service from the Activity (foreground), not
        // from Application.onCreate — on Android 12+ the latter can be deferred
        // by the system, which caused a 10s "player not ready" timeout on cold
        // start. From the Activity the start is honoured immediately.
        try {
            startForegroundService(Intent(this, PlayerService::class.java))
            DebugLogger.i(TAG, "PlayerService start sent from MainActivity")
        } catch (e: Exception) {
            DebugLogger.e(TAG, "Failed to start PlayerService: ${e.message}", e)
        }

        setContent {
            val state by viewModel.state.collectAsState()
            DebugLogger.i(TAG, "MainActivity.setContent — Compose tree rendered")
            CriApp(
                state = state,
                segmentCache = viewModel.segmentCache,
                onAction = viewModel::dispatch
            )
        }
    }

    private fun requestNotificationPermission() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS)
            == PackageManager.PERMISSION_GRANTED
        ) {
            DebugLogger.d(TAG, "POST_NOTIFICATIONS already granted")
            return
        }
        DebugLogger.i(TAG, "requesting POST_NOTIFICATIONS")
        notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
    }

    /**
     * Ask the user to exempt CRI Radio from battery optimization.
     * Without this, Doze mode ignores [android.os.PowerManager.WAKE_LOCK]
     * and defers network access — the HLS stream stalls after a few
     * minutes with the screen off.
     *
     * Only shows the system dialog once; if already exempt, does nothing.
     */
    private fun requestBatteryOptimizationExemption() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return
        val pm = getSystemService(POWER_SERVICE) as PowerManager
        if (pm.isIgnoringBatteryOptimizations(packageName)) {
            DebugLogger.d(TAG, "already exempt from battery optimization")
            return
        }
        // Ask at most once — repeatedly showing the system dialog on every cold
        // start is intrusive and a Play Store policy concern for this permission.
        val prefs = getSharedPreferences("cri_prefs", MODE_PRIVATE)
        if (prefs.getBoolean("battery_exempt_asked", false)) {
            DebugLogger.d(TAG, "battery optimization prompt already shown — not asking again")
            return
        }
        DebugLogger.i(TAG, "requesting battery optimization exemption")
        try {
            val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS).apply {
                data = Uri.parse("package:$packageName")
            }
            startActivity(intent)
            prefs.edit().putBoolean("battery_exempt_asked", true).apply()
        } catch (e: Exception) {
            DebugLogger.e(TAG, "failed to open battery optimization settings: ${e.message}")
        }
    }

    companion object {
        private const val TAG = "CRIRadio:main"
    }
}

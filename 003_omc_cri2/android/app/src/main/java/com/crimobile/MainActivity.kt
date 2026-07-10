package com.crimobile

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.activity.viewModels
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.core.content.ContextCompat
import com.crimobile.ui.CriApp
import com.crimobile.viewmodel.CriViewModel

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
            Log.i(TAG, "POST_NOTIFICATIONS granted=$granted")
        }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        requestNotificationPermission()

        setContent {
            val state by viewModel.state.collectAsState()
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
            Log.d(TAG, "POST_NOTIFICATIONS already granted")
            return
        }
        Log.i(TAG, "requesting POST_NOTIFICATIONS")
        notificationPermissionLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
    }

    companion object {
        private const val TAG = "CRIRadio:main"
    }
}

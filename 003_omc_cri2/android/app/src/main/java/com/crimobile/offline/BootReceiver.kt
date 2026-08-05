package com.crimobile.offline

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.crimobile.debug.DebugLogger

/**
 * Re-arms the sync alarm after device reboot.
 *
 * Exact alarms are cleared on reboot — this receiver ensures
 * the daily sync schedule is restored without user action.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return

        DebugLogger.i(TAG, "Boot completed — re-arming sync alarm")

        val prefs = context.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)
        val config = SyncConfig.fromPrefs(prefs)

        if (config.enabled) {
            SyncScheduler.schedule(context, config)
        }
    }

    companion object {
        private const val TAG = "CRIRadio:BootReceiver"
    }
}

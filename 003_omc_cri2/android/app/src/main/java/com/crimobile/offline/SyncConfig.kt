package com.crimobile.offline

import android.content.Context
import android.content.SharedPreferences

data class SyncConfig(
    val enabled: Boolean = false,
    val syncHourOfDay: Int = 0,       // 0–23, default midnight
    val syncMinute: Int = 0,          // 0–59
    val syncDurationSec: Int = 10800,  // default 2.5 hours (10800 seconds)
    val wifiOnly: Boolean = true,
    val lastSyncTimestamp: Long = 0L, // epoch millis of last successful sync
    val initialSyncDone: Boolean = false
) {
    companion object {
        private const val PREF_PREFIX = "cri_offline_"

        fun fromPrefs(prefs: SharedPreferences): SyncConfig = SyncConfig(
            enabled = prefs.getBoolean("${PREF_PREFIX}enabled", false),
            syncHourOfDay = prefs.getInt("${PREF_PREFIX}sync_hour", 0),
            syncMinute = prefs.getInt("${PREF_PREFIX}sync_minute", 0),
            syncDurationSec = prefs.getInt("${PREF_PREFIX}sync_duration_sec", 10800),
            wifiOnly = prefs.getBoolean("${PREF_PREFIX}wifi_only", true),
            lastSyncTimestamp = prefs.getLong("${PREF_PREFIX}last_sync_ts", 0L),
            initialSyncDone = prefs.getBoolean("${PREF_PREFIX}initial_sync_done", false)
        )

        fun save(prefs: SharedPreferences, config: SyncConfig) {
            prefs.edit()
                .putBoolean("${PREF_PREFIX}enabled", config.enabled)
                .putInt("${PREF_PREFIX}sync_hour", config.syncHourOfDay)
                .putInt("${PREF_PREFIX}sync_minute", config.syncMinute)
                .putInt("${PREF_PREFIX}sync_duration_sec", config.syncDurationSec)
                .putBoolean("${PREF_PREFIX}wifi_only", config.wifiOnly)
                .putLong("${PREF_PREFIX}last_sync_ts", config.lastSyncTimestamp)
                .putBoolean("${PREF_PREFIX}initial_sync_done", config.initialSyncDone)
                .apply()
        }
    }
}

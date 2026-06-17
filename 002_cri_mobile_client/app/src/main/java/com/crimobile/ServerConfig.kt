package com.crimobile

import android.content.Context
import android.content.SharedPreferences
import android.os.Build

/**
 * Persists server connection settings and UI preferences in [SharedPreferences].
 *
 * === Infrastructure config ===
 * [host] / [port] — server connection parameters.
 * Default on emulator: 10.0.2.2:8080 (loopback to host machine).
 * Default on real device: china-radio-international.duckdns.org:8080.
 *
 * === UI preferences ===
 * [showPinyin] / [showTranslation] — toggles for subtitle display.
 * Stored here for convenience; read by [CriViewModel] at render time.
 */
class ServerConfig(context: Context) {

    private val prefs: SharedPreferences =
        context.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)

    // -- infrastructure --------------------------------------------------

    var host: String
        get() = prefs.getString(KEY_HOST, defaultHost()) ?: defaultHost()
        set(value) = prefs.edit().putString(KEY_HOST, value).apply()

    var port: Int
        get() = prefs.getInt(KEY_PORT, DEFAULT_PORT)
        set(value) = prefs.edit().putInt(KEY_PORT, value).apply()

    val baseUrl: String
        get() = "http://$host:$port"

    // -- UI preferences -------------------------------------------------

    var showPinyin: Boolean
        get() = prefs.getBoolean(KEY_SHOW_PINYIN, true)
        set(value) = prefs.edit().putBoolean(KEY_SHOW_PINYIN, value).apply()

    var showTranslation: Boolean
        get() = prefs.getBoolean(KEY_SHOW_TRANSLATION, true)
        set(value) = prefs.edit().putBoolean(KEY_SHOW_TRANSLATION, value).apply()

    fun save(host: String, port: Int) {
        prefs.edit()
            .putString(KEY_HOST, host)
            .putInt(KEY_PORT, port)
            .apply()
    }

    companion object {
        private const val KEY_HOST = "server_host"
        private const val KEY_PORT = "server_port"
        private const val KEY_SHOW_PINYIN = "show_pinyin"
        private const val KEY_SHOW_TRANSLATION = "show_translation"
        private const val EMULATOR_HOST = "10.0.2.2"
        private const val DEVICE_HOST = "china-radio-international.duckdns.org"
        const val DEFAULT_PORT = 8080

        fun isEmulator(): Boolean =
            (Build.FINGERPRINT.startsWith("generic") ||
             Build.FINGERPRINT.startsWith("unknown") ||
             Build.MODEL.contains("google_sdk") ||
             Build.MODEL.contains("Emulator") ||
             Build.MODEL.contains("Android SDK built for x86") ||
             Build.MANUFACTURER.contains("Genymotion") ||
             Build.HARDWARE.contains("goldfish") ||
             Build.HARDWARE.contains("ranchu") ||
             Build.PRODUCT.contains("sdk") ||
             Build.PRODUCT.contains("sdk_gphone") ||
             Build.BRAND.startsWith("generic"))

        fun defaultHost(): String = if (isEmulator()) EMULATOR_HOST else DEVICE_HOST
    }
}

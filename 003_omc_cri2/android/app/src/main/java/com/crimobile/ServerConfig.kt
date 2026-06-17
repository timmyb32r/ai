package com.crimobile

import android.os.Build

object ServerConfig {
    /** Default server URL — detects emulator vs real device. */
    val defaultUrl: String
        get() = if (isEmulator) {
            "http://10.0.2.2:8080"
        } else {
            "http://china-radio-international.duckdns.org:8080"
        }

    private val isEmulator: Boolean
        get() {
            val fingerprint = Build.FINGERPRINT
            val model = Build.MODEL
            val product = Build.PRODUCT
            val hardware = Build.HARDWARE

            return (fingerprint.startsWith("generic") ||
                    fingerprint.startsWith("unknown") ||
                    model.contains("google_sdk") ||
                    model.contains("Emulator") ||
                    model.contains("Android SDK built for x86") ||
                    product.contains("sdk_gphone") ||
                    product.contains("sdk") ||
                    product.contains("emulator") ||
                    hardware.contains("goldfish") ||
                    hardware.contains("ranchu"))
        }
}

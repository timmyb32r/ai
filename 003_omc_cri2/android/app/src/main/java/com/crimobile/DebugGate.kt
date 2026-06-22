package com.crimobile

import android.content.Context
import android.os.Environment
import java.io.File

object DebugGate {
    fun isEnabled(context: Context): Boolean {
        val downloadsDir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
        if (File(downloadsDir, ".cri_debug").exists()) return true
        val appDir = context.getExternalFilesDir(null)
        if (appDir != null && File(appDir, ".cri_debug").exists()) return true
        return false
    }
}

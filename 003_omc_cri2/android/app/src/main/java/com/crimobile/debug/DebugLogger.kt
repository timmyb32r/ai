package com.crimobile.debug

import android.content.Context
import android.util.Log
import java.io.File
import java.io.FileOutputStream
import java.io.OutputStreamWriter
import java.io.PrintWriter
import java.io.StringWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * File-based debug logger.  Writes to internal storage (always works):
 *   /data/data/com.crimobile/files/cri_logs.txt
 *
 * Access: adb pull, Android Studio Device Explorer, or Share button in UI.
 */
object DebugLogger {
    private const val TAG = "CRIRadio:debuglog"
    private const val FILENAME = "cri_logs.txt"
    /** Rotate the log file once it reaches this size (25 MB). */
    private const val MAX_LOG_BYTES = 25L * 1024 * 1024

    @Volatile var enabled: Boolean = false

    private var output: PrintWriter? = null
    private var file: File? = null
    private val dateFmt = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US)
    private val lock = Any()

    var logFilePath: String = ""
        private set

    @Volatile private var ready: Boolean = false

    fun init(context: Context) {
        if (ready) return

        try {
            // Internal filesDir — guaranteed writable, API 1+.
            file = File(context.applicationContext.filesDir, FILENAME)
            output = PrintWriter(
                OutputStreamWriter(FileOutputStream(file, true), Charsets.UTF_8),
                true /* autoFlush */
            )
            logFilePath = file!!.absolutePath
            Log.i(TAG, "OK: $logFilePath")
        } catch (e: Exception) {
            Log.e(TAG, "FATAL: ${e.message}", e)
            logFilePath = "(error: ${e.message})"
        }
        ready = true
    }

    /** Returns the log file for sharing (may be null if init failed). */
    fun logFile(): File? = file

    /**
     * Copy the current log to Downloads/cri_logs.txt so the user can find it.
     * Uses MediaStore on API 29+, direct file on older APIs.
     */
    fun copyToDownloads(context: Context): String {
        val src = file ?: return "no log file"
        if (!src.exists()) return "log file not found"

        val text = try { src.readText() } catch (e: Exception) { return "read error: ${e.message}" }

        return try {
            if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.Q) {
                val resolver = context.applicationContext.contentResolver
                val collection = android.provider.MediaStore.Downloads
                    .getContentUri(android.provider.MediaStore.VOLUME_EXTERNAL_PRIMARY)
                val cv = android.content.ContentValues().apply {
                    put(android.provider.MediaStore.Downloads.DISPLAY_NAME, "cri_logs.txt")
                    put(android.provider.MediaStore.Downloads.MIME_TYPE, "text/plain")
                    put(android.provider.MediaStore.Downloads.IS_PENDING, 1)
                }
                val uri = resolver.insert(collection, cv)
                    ?: return "MediaStore insert failed"
                resolver.openOutputStream(uri, "w")?.use { os ->
                    os.write(text.toByteArray(Charsets.UTF_8))
                } ?: return "cannot open output stream"
                android.content.ContentValues().apply {
                    put(android.provider.MediaStore.Downloads.IS_PENDING, 0)
                }.also { resolver.update(uri, it, null, null) }
            } else {
                val dir = android.os.Environment.getExternalStoragePublicDirectory(
                    android.os.Environment.DIRECTORY_DOWNLOADS
                )
                dir.mkdirs()
                java.io.File(dir, "cri_logs.txt").writeText(text)
            }
            "copied to Downloads/cri_logs.txt"
        } catch (e: Exception) {
            "copy error: ${e.message}"
        }
    }

    /**
     * Erase the current log file (and any rotated archive) and reopen a fresh
     * empty log. Called from the UI "Rotate log" action (with user confirmation).
     * Safe to call before [init] / when no file is open (no-op).
     */
    fun clearLog() {
        synchronized(lock) {
            try { output?.close() } catch (_: Exception) {}
            output = null
            val f = file
            if (f != null) {
                File(f.parentFile, "$FILENAME.1").delete()
                f.delete()
                try {
                    output = PrintWriter(
                        OutputStreamWriter(FileOutputStream(f, true), Charsets.UTF_8),
                        true /* autoFlush */
                    )
                    Log.i(TAG, "log cleared and reopened: $logFilePath")
                } catch (e: Exception) {
                    Log.e(TAG, "clearLog reopen failed: ${e.message}")
                }
            }
        }
    }

    fun log(tag: String, message: String) { i(tag, message) }
    fun log(tag: String, message: String, throwable: Throwable) { i(tag, message, throwable) }

    fun v(tag: String, msg: String) { Log.v(tag, msg); writeLine("V", tag, msg) }
    fun d(tag: String, msg: String) { Log.d(tag, msg); writeLine("D", tag, msg) }
    fun i(tag: String, msg: String) { Log.i(tag, msg); writeLine("I", tag, msg) }
    fun w(tag: String, msg: String) { Log.w(tag, msg); writeLine("W", tag, msg) }
    fun e(tag: String, msg: String) { Log.e(tag, msg); writeLine("E", tag, msg) }

    fun i(tag: String, msg: String, tr: Throwable) { Log.i(tag, msg, tr); writeLine("I", tag, msg); writeThrowable(tr) }
    fun w(tag: String, msg: String, tr: Throwable) { Log.w(tag, msg, tr); writeLine("W", tag, msg); writeThrowable(tr) }
    fun e(tag: String, msg: String, tr: Throwable) { Log.e(tag, msg, tr); writeLine("E", tag, msg); writeThrowable(tr) }

    fun close() {
        synchronized(lock) {
            try { output?.close() } catch (_: Exception) {}
            output = null
            ready = false
        }
    }

    private fun writeLine(level: String, tag: String, msg: String) {
        if (!ready) return
        if (!enabled) return
        synchronized(lock) {
            var w = output ?: return
            // Rotate when the file has grown past the cap. Without this the log
            // grew without bound in internal storage and could eventually take
            // down CrashHandler / OfflineStorageManager / VocabularyStore.
            val f = file
            if (f != null && LogRotation.shouldRotate(f.length(), MAX_LOG_BYTES)) {
                try {
                    w.close()
                    val archive = File(f.parentFile, "$FILENAME.1")
                    LogRotation.rotate(f, archive)
                    w = PrintWriter(OutputStreamWriter(FileOutputStream(f, true), Charsets.UTF_8), true)
                    output = w
                } catch (_: Exception) { /* keep going with the existing writer */ }
            }
            // If the writer is dead, try to reopen.
            if (w.checkError()) {
                try {
                    val ff = file ?: return
                    w = PrintWriter(OutputStreamWriter(FileOutputStream(ff, true), Charsets.UTF_8), true)
                    output = w
                } catch (_: Exception) {
                    return
                }
            }
            try {
                w.println("${dateFmt.format(Date())} $level/$tag: $msg")
                w.flush()
            } catch (_: Exception) {}
        }
    }

    private fun writeThrowable(tr: Throwable) {
        synchronized(lock) {
            val w = output ?: return
            try {
                tr.printStackTrace(PrintWriter(w))
                w.flush()
            } catch (_: Exception) {}
        }
    }
}

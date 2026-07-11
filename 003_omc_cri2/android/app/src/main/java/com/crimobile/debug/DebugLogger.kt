package com.crimobile.debug

import android.content.ContentValues
import android.content.Context
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
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
 * Simple file-based debug logger. When enabled, writes every [log] call
 * to Downloads/cri_logs.txt in addition to Logcat. Thread-safe.
 *
 * On API 29+ uses MediaStore for scoped-storage compliance;
 * falls back to app-specific external storage if that fails.
 */
object DebugLogger {
    private const val TAG = "CRIRadio:debuglog"
    private const val FILENAME = "cri_logs.txt"

    @Volatile var enabled: Boolean = false

    // Output target — set by init().
    private var output: PrintWriter? = null
    private var mediaStoreUri: android.net.Uri? = null
    private val dateFmt = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US)
    private val lock = Any()

    var logFilePath: String = ""
        private set

    @Volatile private var ready: Boolean = false

    fun init(context: Context) {
        if (ready) return
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                initMediaStore(context)
            } else {
                initDirectFile()
            }
        } catch (e: Exception) {
            Log.w(TAG, "init failed, using cache fallback: ${e.message}")
            initCacheFallback(context)
        }
        ready = true
    }

    private fun initMediaStore(context: Context) {
        val resolver = context.contentResolver
        val collection = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q)
            MediaStore.Downloads.getContentUri(MediaStore.VOLUME_EXTERNAL_PRIMARY)
        else
            MediaStore.Downloads.EXTERNAL_CONTENT_URI

        // Look for existing file to append to.
        val existing = resolver.query(
            collection,
            arrayOf(MediaStore.Downloads._ID, MediaStore.Downloads.DISPLAY_NAME),
            "${MediaStore.Downloads.DISPLAY_NAME} = ?",
            arrayOf(FILENAME),
            null
        )
        existing?.use { cursor ->
            if (cursor.moveToFirst()) {
                val id = cursor.getLong(0)
                mediaStoreUri = android.content.ContentUris.withAppendedId(collection, id)
            }
        }

        if (mediaStoreUri == null) {
            // Create new file.
            val cv = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, FILENAME)
                put(MediaStore.Downloads.MIME_TYPE, "text/plain")
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            mediaStoreUri = resolver.insert(collection, cv)
        }

        if (mediaStoreUri != null) {
            // Open for append via content resolver.
            val os = resolver.openOutputStream(mediaStoreUri!!, "wa")
            if (os != null) {
                // Mark non-pending for new files.
                val cv = ContentValues().apply {
                    put(MediaStore.Downloads.IS_PENDING, 0)
                }
                resolver.update(mediaStoreUri!!, cv, null, null)

                output = PrintWriter(OutputStreamWriter(os, Charsets.UTF_8), true)
                logFilePath = "Downloads/$FILENAME (MediaStore)"
                Log.i(TAG, "log via MediaStore OK")
                return
            }
        }
        throw IllegalStateException("MediaStore insert failed")
    }

    private fun initDirectFile() {
        val dir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
        dir.mkdirs()
        val file = File(dir, FILENAME)
        output = PrintWriter(file.outputStream().writer().buffered(), true)
        logFilePath = file.absolutePath
        Log.i(TAG, "log file: $logFilePath")
    }

    private fun initCacheFallback(context: Context) {
        val file = File(context.cacheDir, FILENAME)
        output = PrintWriter(file.outputStream().writer().buffered(), true)
        logFilePath = file.absolutePath
        Log.i(TAG, "log file (cache): $logFilePath")
    }

    fun log(tag: String, message: String) {
        Log.i(tag, message)
        if (!enabled || !ready) return
        val w = output ?: return
        synchronized(lock) {
            try {
                val ts = dateFmt.format(Date())
                w.println("$ts $tag $message")
                w.flush()
            } catch (_: Exception) { }
        }
    }

    fun log(tag: String, message: String, throwable: Throwable) {
        Log.i(tag, message, throwable)
        if (!enabled || !ready) return
        val w = output ?: return
        synchronized(lock) {
            try {
                val ts = dateFmt.format(Date())
                w.println("$ts $tag $message")
                val sw = StringWriter()
                throwable.printStackTrace(PrintWriter(sw))
                w.println(sw.toString())
                w.flush()
            } catch (_: Exception) { }
        }
    }

    fun close() {
        synchronized(lock) {
            try { output?.close() } catch (_: Exception) { }
            output = null
            ready = false
        }
    }
}

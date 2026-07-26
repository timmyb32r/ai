package com.crimobile

import android.app.Application
import android.content.ContentValues
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import java.io.File
import java.io.FileOutputStream
import java.io.PrintWriter
import java.io.StringWriter
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import com.crimobile.debug.DebugLogger

/**
 * Global uncaught-exception handler.
 *
 * On any unhandled throwable (crash):
 * 1. Writes a timestamped crash dump to Downloads/cri_crash.txt.
 * 2. Falls back to app-private external storage if Downloads is unreachable.
 * 3. Passes the exception to the OS default handler so the crash dialog still appears.
 *
 * Install via [install] — call once from [Application.onCreate].
 */
object CrashHandler : Thread.UncaughtExceptionHandler {

    private const val TAG = "CRIRadio:crash"
    private const val FILE_NAME = "cri_crash.txt"

    private lateinit var app: Application
    private lateinit var defaultHandler: Thread.UncaughtExceptionHandler
    private var installed = false

    /** Must be called before any other code that might crash. */
    fun install(application: Application) {
        if (installed) return
        app = application
        defaultHandler = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler(this)
        installed = true
        DebugLogger.i(TAG, "crash handler installed")
    }

    override fun uncaughtException(thread: Thread, throwable: Throwable) {
        writeCrashDump(throwable)
        // Pass to OS — shows the standard "app has stopped" dialog.
        defaultHandler.uncaughtException(thread, throwable)
    }

    // ── write ────────────────────────────────────────────────────────

    private fun writeCrashDump(throwable: Throwable) {
        try {
            val dump = buildDump(throwable)
            // Best-effort: public Downloads first, fall back to app-private.
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                writeViaMediaStore(dump)
            } else {
                writeToPublicDownloads(dump)
            }
        } catch (e: Exception) {
            // Last resort — internal cache, no permissions needed.
            writeToInternalFallback(throwable, e)
        }
    }

    // ── API 29+ : MediaStore ─────────────────────────────────────────

    private fun writeViaMediaStore(dump: String) {
        val values = ContentValues().apply {
            put(MediaStore.Downloads.DISPLAY_NAME, FILE_NAME)
            put(MediaStore.Downloads.MIME_TYPE, "text/plain")
            put(MediaStore.Downloads.IS_PENDING, 1)
        }
        val resolver = app.contentResolver
        val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values)
            ?: throw IllegalStateException("MediaStore.insert returned null")

        resolver.openOutputStream(uri)?.use { out ->
            out.write(dump.toByteArray(Charsets.UTF_8))
        } ?: throw IllegalStateException("openOutputStream returned null")

        values.clear()
        values.put(MediaStore.Downloads.IS_PENDING, 0)
        resolver.update(uri, values, null, null)

        DebugLogger.i(TAG, "crash dump written to Downloads/$FILE_NAME")
    }

    // ── API < 29 : public Downloads ──────────────────────────────────

    @Suppress("DEPRECATION")
    private fun writeToPublicDownloads(dump: String) {
        val dir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)
        dir.mkdirs()
        val file = File(dir, FILE_NAME)
        FileOutputStream(file).use { it.write(dump.toByteArray(Charsets.UTF_8)) }
        DebugLogger.i(TAG, "crash dump written to ${file.absolutePath}")
    }

    // ── Fallback ─────────────────────────────────────────────────────

    private fun writeToInternalFallback(original: Throwable, writeError: Exception) {
        try {
            val dir = app.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS)
                ?: app.cacheDir
            val file = File(dir, FILE_NAME)
            val dump = buildString {
                append(buildDump(original))
                append("\n\n=== Write-to-Downloads also failed ===\n")
                append(throwableToString(writeError))
            }
            file.writeText(dump, Charsets.UTF_8)
            DebugLogger.w(TAG, "crash dump written to fallback: ${file.absolutePath}")
        } catch (_: Exception) {
            DebugLogger.e(TAG, "could not write crash dump anywhere", original)
        }
    }

    // ── Formatting ───────────────────────────────────────────────────

    private fun buildDump(throwable: Throwable): String = buildString {
        val ts = SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS", Locale.US).format(Date())
        val pair: Pair<String, Long> = try {
            val pi = app.packageManager.getPackageInfo(app.packageName, 0)
            val code = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
                pi.longVersionCode
            } else {
                @Suppress("DEPRECATION")
                pi.versionCode.toLong()
            }
            (pi.versionName ?: "?") to code
        } catch (_: Exception) {
            "?" to 0L
        }
        val vName = pair.first
        val vCode = pair.second
        appendLine("=== CRI Radio Crash Dump ===")
        appendLine("Time: $ts")
        appendLine("App: ${app.packageName} $vName ($vCode)")
        appendLine("Device: ${Build.MANUFACTURER} ${Build.MODEL} | SDK ${Build.VERSION.SDK_INT}")
        appendLine("Thread: ${Thread.currentThread().name}")
        appendLine()
        appendLine(throwableToString(throwable))
    }

    private fun throwableToString(t: Throwable): String {
        val sw = StringWriter()
        t.printStackTrace(PrintWriter(sw))
        return sw.toString()
    }
}

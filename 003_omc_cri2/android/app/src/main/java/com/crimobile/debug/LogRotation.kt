package com.crimobile.debug

import java.io.File

/**
 * Pure, testable log-rotation helpers.
 *
 * Extracted from [DebugLogger] so the size-based rotation decision can be
 * unit-tested. Previously the log file was opened in append mode and never
 * rotated, so a long-running session (the sync loop logs ~10×/s) could grow it
 * without bound in internal storage — eventually filling `filesDir` and taking
 * down CrashHandler / OfflineStorageManager / VocabularyStore with it.
 */
object LogRotation {

    /** True when [fileSizeBytes] has reached [thresholdBytes]. */
    fun shouldRotate(fileSizeBytes: Long, thresholdBytes: Long): Boolean =
        thresholdBytes > 0 && fileSizeBytes >= thresholdBytes

    /**
     * Rotate [current] to [archive] (replacing any existing archive), so a fresh
     * [current] can be reopened. Best-effort: returns true on success.
     */
    fun rotate(current: File, archive: File): Boolean {
        archive.delete()
        return current.renameTo(archive)
    }
}

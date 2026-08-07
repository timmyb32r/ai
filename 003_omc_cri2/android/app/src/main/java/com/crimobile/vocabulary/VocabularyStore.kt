package com.crimobile.vocabulary

import android.content.Context
import com.crimobile.model.WordEntry
import com.crimobile.debug.DebugLogger
import java.io.File

/**
 * Saves vocabulary words to app-private storage and reads them back.
 *
 * Previously this wrote to public Downloads (falling back to app-private on the
 * API 29+ SecurityException, since WRITE_EXTERNAL_STORAGE is maxSdkVersion=28)
 * but read ONLY from public Downloads — so on any API ≥ 29 device every saved
 * word was written to internal storage and then read back as an empty list.
 *
 * Both paths now use the same app-private directory, so save/read round-trips
 * reliably regardless of SDK level. An explicit export function can copy the
 * file to Downloads via MediaStore if user-facing access is needed.
 */
class VocabularyStore private constructor(private val dir: File) {

    constructor(context: Context) : this(File(context.filesDir, "vocabulary"))

    companion object {
        private const val TAG = "CRIRadio:vocab"
        private const val FILENAME = "cri_vocabulary.txt"
        internal fun forDir(dir: File) = VocabularyStore(dir)
    }

    @Synchronized
    fun appendWord(word: WordEntry, context: String) {
        val line = "${word.text}\n"
        try {
            if (!dir.exists()) dir.mkdirs()
            File(dir, FILENAME).appendText(line)
        } catch (e: Exception) {
            DebugLogger.w(TAG, "appendWord failed: ${e.message}")
        }
    }

    @Synchronized
    fun getSavedWords(): List<String> {
        val file = File(dir, FILENAME)
        return if (!file.exists()) emptyList() else file.readLines()
    }
}

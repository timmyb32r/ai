package com.crimobile.vocabulary

import android.content.ContentValues
import android.content.Context
import android.os.Environment
import android.provider.MediaStore
import com.crimobile.model.WordEntry
import java.io.File

/**
 * Saves vocabulary words to Downloads/cri_vocabulary.txt via MediaStore.
 */
class VocabularyStore(private val context: Context) {

    private val filename = "cri_vocabulary.txt"
    private val dir = Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS)

    fun appendWord(word: WordEntry, context: String) {
        val line = "${word.text}\t${word.pinyin}\t${word.translation}\t$context\n"
        val file = File(dir, filename)

        try {
            if (!dir.exists()) dir.mkdirs()
            file.appendText(line)
        } catch (e: Exception) {
            // Fallback to app-local storage
            val localFile = File(this.context.filesDir, filename)
            localFile.appendText(line)
        }
    }

    fun getSavedWords(): List<String> {
        val file = File(dir, filename)
        if (!file.exists()) return emptyList()
        return file.readLines()
    }
}

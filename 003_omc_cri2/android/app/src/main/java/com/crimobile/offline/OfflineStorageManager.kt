package com.crimobile.offline

import android.content.Context
import android.util.Log
import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * Manages locally stored subtitle segments and audio files for offline playback.
 *
 * Directory layout:
 *   {filesDir}/cri_offline/
 *     index.json             -- lightweight ordered index
 *     metadata/{id}.json     -- per-segment metadata (same format as server)
 *     audio/{id}.ts          -- raw .ts audio file (local copy)
 */
class OfflineStorageManager(private val context: Context) {

    private val rootDir: File = File(context.filesDir, "cri_offline")
    private val metaDir: File = File(rootDir, "metadata")
    private val audioDir: File = File(rootDir, "audio")
    private val indexFile: File = File(rootDir, "index.json")
    private val lock = Any()

    init {
        metaDir.mkdirs()
        audioDir.mkdirs()
    }

    // ── Write ──────────────────────────────────────────────────────────

    /** Persist segment metadata and audio bytes. */
    fun saveSegment(segment: SubtitleSegment, tsBytes: ByteArray) {
        synchronized(lock) {
            val id = segment.segment_id
            // Metadata
            val metaJson = segmentToJson(segment)
            File(metaDir, fileName(id, "json")).writeText(metaJson)
            // Audio
            File(audioDir, fileName(id, "ts")).writeBytes(tsBytes)
            // Update index in-place
            appendToIndex(id, segment.timeline_start_sec, segment.timeline_end_sec)
        }
    }

    // ── Read ───────────────────────────────────────────────────────────

    fun loadSegment(segmentId: Int): SubtitleSegment? {
        synchronized(lock) {
            val file = File(metaDir, fileName(segmentId, "json"))
            if (!file.exists()) return null
            return parseSegment(file.readText())
        }
    }

    fun loadAllSegments(): List<SubtitleSegment> {
        synchronized(lock) {
            val index = readIndex()
            return index.mapNotNull { ref ->
                val file = File(metaDir, fileName(ref.id, "json"))
                if (file.exists()) parseSegment(file.readText()) else null
            }
        }
    }

    fun hasSegment(segmentId: Int): Boolean {
        synchronized(lock) {
            return File(metaDir, fileName(segmentId, "json")).exists() &&
                   File(audioDir, fileName(segmentId, "ts")).exists()
        }
    }

    fun getAudioFile(segmentId: Int): File? {
        val file = File(audioDir, fileName(segmentId, "ts"))
        return if (file.exists() && file.length() > 0) file else null
    }

    fun countSegments(): Int {
        synchronized(lock) {
            return readIndex().size
        }
    }

    /**
     * Returns (oldest_start_sec, newest_end_sec) of locally stored segments
     * or null if storage is empty.
     */
    fun computeLocalRange(): Pair<Double, Double>? {
        synchronized(lock) {
            val index = readIndex()
            if (index.isEmpty()) return null
            val first = index.first()
            val last = index.last()
            return first.startSec to last.endSec
        }
    }

    // ── Delete ─────────────────────────────────────────────────────────

    fun deleteSegment(segmentId: Int) {
        synchronized(lock) {
            File(metaDir, fileName(segmentId, "json")).delete()
            File(audioDir, fileName(segmentId, "ts")).delete()
            removeFromIndex(segmentId)
        }
    }

    fun deleteAll() {
        synchronized(lock) {
            rootDir.deleteRecursively()
            metaDir.mkdirs()
            audioDir.mkdirs()
        }
    }

    fun getStorageUsedBytes(): Long {
        synchronized(lock) {
            return rootDir.walkTopDown().filter { it.isFile }.sumOf { it.length() }
        }
    }

    // ── Internal ───────────────────────────────────────────────────────

    private fun fileName(id: Int, ext: String) = "${zeroPad(id)}.$ext"

    private data class IndexEntry(
        val id: Int,
        val startSec: Double,
        val endSec: Double
    )

    private fun readIndex(): List<IndexEntry> {
        if (!indexFile.exists()) return emptyList()
        return try {
            val arr = JSONArray(indexFile.readText())
            (0 until arr.length()).map { i ->
                val obj = arr.getJSONObject(i)
                IndexEntry(
                    id = obj.getInt("id"),
                    startSec = obj.getDouble("start_sec"),
                    endSec = obj.getDouble("end_sec")
                )
            }
        } catch (e: Exception) {
            Log.w(TAG, "Failed to read index, rebuilding: ${e.message}")
            rebuildIndex()
        }
    }

    private fun writeIndex(entries: List<IndexEntry>) {
        val arr = JSONArray()
        entries.forEach { entry ->
            arr.put(JSONObject().apply {
                put("id", entry.id)
                put("start_sec", entry.startSec)
                put("end_sec", entry.endSec)
            })
        }
        indexFile.writeText(arr.toString(2))
    }

    private fun appendToIndex(id: Int, startSec: Double, endSec: Double) {
        val entries = readIndex().toMutableList()
        val newEntry = IndexEntry(id, startSec, endSec)
        val existingIdx = entries.indexOfFirst { it.id == id }
        if (existingIdx >= 0) {
            entries[existingIdx] = newEntry
        } else {
            entries.add(newEntry)
        }
        entries.sortBy { it.id }
        writeIndex(entries)
    }

    private fun removeFromIndex(id: Int) {
        writeIndex(readIndex().filter { it.id != id })
    }

    private fun rebuildIndex(): List<IndexEntry> {
        val entries = metaDir.listFiles()
            ?.mapNotNull { file ->
                val id = parseId(file.name) ?: return@mapNotNull null
                val seg = parseSegment(file.readText()) ?: return@mapNotNull null
                IndexEntry(id, seg.timeline_start_sec, seg.timeline_end_sec)
            }
            ?.sortedBy { it.id }
            ?: emptyList()
        writeIndex(entries)
        return entries
    }

    private fun segmentToJson(seg: SubtitleSegment): String {
        val wordsArr = JSONArray()
        seg.words.forEach { w ->
            wordsArr.put(JSONObject().apply {
                put("text", w.text)
                put("char_start", w.char_start)
                put("char_end", w.char_end)
                put("start_sec", w.start_sec)
                put("end_sec", w.end_sec)
                put("pinyin", w.pinyin)
                put("translation", w.translation)
            })
        }
        return JSONObject().apply {
            put("segment_id", seg.segment_id)
            put("timeline_start_sec", seg.timeline_start_sec)
            put("timeline_end_sec", seg.timeline_end_sec)
            put("ts_file", seg.ts_file)
            put("text_zh", seg.text_zh)
            put("text_pinyin", seg.text_pinyin)
            put("text_en", seg.text_en)
            put("words", wordsArr)
        }.toString(2)
    }

    private fun parseSegment(json: String): SubtitleSegment? {
        return try {
            val obj = JSONObject(json)
            val wordsArr = obj.optJSONArray("words") ?: JSONArray()
            val words = (0 until wordsArr.length()).map { i ->
                val w = wordsArr.getJSONObject(i)
                WordEntry(
                    text = w.optString("text", ""),
                    char_start = w.optInt("char_start", 0),
                    char_end = w.optInt("char_end", 0),
                    start_sec = w.optDouble("start_sec", 0.0),
                    end_sec = w.optDouble("end_sec", 0.0),
                    pinyin = w.optString("pinyin", ""),
                    translation = w.optString("translation", "")
                )
            }
            SubtitleSegment(
                segment_id = obj.optInt("segment_id", 0),
                timeline_start_sec = obj.optDouble("timeline_start_sec", 0.0),
                timeline_end_sec = obj.optDouble("timeline_end_sec", 0.0),
                ts_file = obj.optString("ts_file", ""),
                text_zh = obj.optString("text_zh", ""),
                text_pinyin = obj.optString("text_pinyin", ""),
                text_en = obj.optString("text_en", ""),
                words = words
            )
        } catch (e: Exception) {
            Log.w(TAG, "Failed to parse segment JSON: ${e.message}")
            null
        }
    }

    companion object {
        private const val TAG = "CRIRadio:offlineStore"

        private fun zeroPad(id: Int) = id.toString().padStart(9, '0')

        private fun parseId(name: String): Int? {
            val digits = name.takeWhile { it in '0'..'9' }
            return digits.toIntOrNull()
        }
    }
}

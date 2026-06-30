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
 * Session-based directory layout:
 *   {filesDir}/cri_offline/
 *     sessions/
 *       index.json                    -- atomic-written session index
 *       {startSec}_{durationSec}/
 *         metadata/{id}.json          -- per-segment metadata
 *         audio/{id}.ts               -- raw .ts audio file
 */
class OfflineStorageManager(private val context: Context) {

    // Shared lock across all instances (CriViewModel + SyncWorker may coexist).
    companion object {
        private val lock = Any()
        private const val TAG = "CRIRadio:offlineStore"
        private fun zeroPad(id: Int) = id.toString().padStart(9, '0')
    }

    private val rootDir: File = File(context.filesDir, "cri_offline")
    private val sessionsDir: File = File(rootDir, "sessions")
    private val sessionsIndexFile: File = File(sessionsDir, "index.json")

    init {
        // Clean break: delete old flat directories if they still exist
        val oldMeta = File(rootDir, "metadata")
        val oldAudio = File(rootDir, "audio")
        val oldIndex = File(rootDir, "index.json")
        if (oldMeta.exists() || oldAudio.exists()) {
            Log.i(TAG, "Deleting old flat storage structure")
            rootDir.deleteRecursively()
        }
        oldIndex.delete()  // safety: remove stale root-level index
        sessionsDir.mkdirs()
    }

    // ── Session metadata ────────────────────────────────────────────────

    data class SessionMeta(
        val startSec: Long,
        val durationSec: Int,
        val segmentCount: Int,
        val createdAt: Long
    )

    fun sessionId(startSec: Long, durationSec: Int): String = "${startSec}_${durationSec}"

    fun sessionDir(sessionId: String): File = File(sessionsDir, sessionId)
    fun sessionMetaDir(sessionId: String): File = File(sessionDir(sessionId), "metadata")
    fun sessionAudioDir(sessionId: String): File = File(sessionDir(sessionId), "audio")

    fun createSession(startSec: Long, durationSec: Int): String {
        val sid = sessionId(startSec, durationSec)
        synchronized(lock) {
            val d = sessionDir(sid)
            if (!d.exists()) {
                sessionMetaDir(sid).mkdirs()
                sessionAudioDir(sid).mkdirs()
            }
        }
        return sid
    }

    // ── Write ──────────────────────────────────────────────────────────

    fun saveSegment(segment: SubtitleSegment, tsBytes: ByteArray, sessionId: String) {
        synchronized(lock) {
            val id = segment.segment_id
            val metaJson = segmentToJson(segment)
            File(sessionMetaDir(sessionId), fileName(id, "json")).writeText(metaJson)
            File(sessionAudioDir(sessionId), fileName(id, "ts")).writeBytes(tsBytes)
        }
    }

    // ── Read ───────────────────────────────────────────────────────────

    fun loadSegment(sessionId: String, segmentId: Int): SubtitleSegment? {
        synchronized(lock) {
            val file = File(sessionMetaDir(sessionId), fileName(segmentId, "json"))
            if (!file.exists()) return null
            return parseSegment(file.readText())
        }
    }

    fun loadSegmentsForSession(sessionId: String): List<SubtitleSegment> {
        synchronized(lock) {
            val metaDir = sessionMetaDir(sessionId)
            if (!metaDir.exists()) return emptyList()
            return metaDir.listFiles()
                ?.mapNotNull { parseSegment(it.readText()) }
                ?.sortedBy { it.segment_id }
                ?: emptyList()
        }
    }

    fun getAudioFile(sessionId: String, segmentId: Int): File? {
        val file = File(sessionAudioDir(sessionId), fileName(segmentId, "ts"))
        return if (file.exists() && file.length() > 0) file else null
    }

    fun hasSegment(sessionId: String, segmentId: Int): Boolean {
        synchronized(lock) {
            return File(sessionMetaDir(sessionId), fileName(segmentId, "json")).exists() &&
                   getAudioFile(sessionId, segmentId) != null
        }
    }

    fun countSegmentsInSession(sessionId: String): Int {
        synchronized(lock) {
            val d = sessionMetaDir(sessionId)
            return if (d.exists()) d.listFiles()?.size ?: 0 else 0
        }
    }

    fun totalSegmentCount(): Int = loadAllSessions().sumOf { it.segmentCount }

    /**
     * Returns (oldest_start_sec, newest_end_sec) across all stored sessions.
     */
    fun computeLocalRange(): Pair<Double, Double>? {
        synchronized(lock) {
            val sessions = loadAllSessions()
            if (sessions.isEmpty()) return null
            var minStart = Double.MAX_VALUE
            var maxEnd = Double.MIN_VALUE
            for (s in sessions) {
                // Load segments for each session to find the actual timeline bounds
                val segs = loadSegmentsForSession(sessionId(s.startSec, s.durationSec))
                if (segs.isNotEmpty()) {
                    minStart = minOf(minStart, segs.first().timeline_start_sec)
                    maxEnd = maxOf(maxEnd, segs.last().timeline_end_sec)
                }
            }
            return if (minStart < Double.MAX_VALUE) minStart to maxEnd else null
        }
    }

    // ── Session index ──────────────────────────────────────────────────

    fun loadAllSessions(): List<SessionMeta> {
        synchronized(lock) {
            if (!sessionsIndexFile.exists()) return emptyList()
            return try {
                parseSessionsIndex(sessionsIndexFile.readText())
            } catch (e: Exception) {
                Log.w(TAG, "Failed to read sessions index, rebuilding: ${e.message}")
                rebuildSessionsIndex()
            }
        }
    }

    fun writeSessionsIndex(sessions: List<SessionMeta>) {
        synchronized(lock) {
            val arr = JSONArray()
            sessions.forEach { s ->
                arr.put(JSONObject().apply {
                    put("start_sec", s.startSec)
                    put("duration_sec", s.durationSec)
                    put("segment_count", s.segmentCount)
                    put("created_at", s.createdAt)
                })
            }
            // Atomic write: .tmp → rename
            val tmpFile = File(sessionsDir, ".index.json.tmp")
            tmpFile.writeText(arr.toString(2))
            if (!tmpFile.renameTo(sessionsIndexFile)) {
                // Fallback: write directly if rename fails (cross-filesystem edge case)
                sessionsIndexFile.writeText(arr.toString(2))
                tmpFile.delete()
            }
        }
    }

    fun rebuildSessionsIndex(): List<SessionMeta> {
        synchronized(lock) {
            val result = mutableListOf<SessionMeta>()
            sessionsDir.listFiles()?.forEach { sessionDir ->
                if (!sessionDir.isDirectory || sessionDir.name.startsWith(".")) return@forEach
                val metaDir = File(sessionDir, "metadata")
                val count = if (metaDir.exists()) metaDir.listFiles()?.size ?: 0 else 0
                if (count > 0) {
                    // Parse sessionId: {startSec}_{durationSec}
                    val parts = sessionDir.name.split("_")
                    if (parts.size >= 2) {
                        val startSec = parts[0].toLongOrNull() ?: return@forEach
                        val durationSec = parts[1].toIntOrNull() ?: return@forEach
                        val createdAt = sessionDir.lastModified()
                        result.add(SessionMeta(startSec, durationSec, count, createdAt))
                    }
                }
            }
            result.sortBy { it.createdAt }
            writeSessionsIndex(result)
            return result
        }
    }

    // ── Delete / Prune ─────────────────────────────────────────────────

    fun deleteSession(sessionId: String) {
        synchronized(lock) {
            val d = sessionDir(sessionId)
            if (d.exists()) {
                d.deleteRecursively()
                Log.i(TAG, "Deleted session: $sessionId")
            }
            // Remove from index
            val sessions = loadAllSessions().filter {
                sessionId(it.startSec, it.durationSec) != sessionId
            }
            writeSessionsIndex(sessions)
        }
    }

    fun pruneOldSessions(keepLastN: Int) {
        val n = keepLastN.coerceAtLeast(1)
        synchronized(lock) {
            val sessions = loadAllSessions()
            if (sessions.size <= n) return
            val toDelete = sessions.sortedBy { it.createdAt }.dropLast(n)
            for (s in toDelete) {
                val sid = sessionId(s.startSec, s.durationSec)
                val d = sessionDir(sid)
                if (d.exists()) {
                    d.deleteRecursively()
                    Log.i(TAG, "Pruned old session: $sid")
                }
            }
            val remaining = sessions.filter { s ->
                val sid = sessionId(s.startSec, s.durationSec)
                sessionDir(sid).exists()
            }
            writeSessionsIndex(remaining)
        }
    }

    fun deleteAll() {
        synchronized(lock) {
            rootDir.deleteRecursively()
            sessionsDir.mkdirs()
        }
    }

    fun getStorageUsedBytes(): Long {
        synchronized(lock) {
            return rootDir.walkTopDown().filter { it.isFile }.sumOf { it.length() }
        }
    }

    // ── Internal ───────────────────────────────────────────────────────

    private fun fileName(id: Int, ext: String) = "${zeroPad(id)}.$ext"

    private fun parseSessionsIndex(json: String): List<SessionMeta> {
        val arr = JSONArray(json)
        return (0 until arr.length()).map { i ->
            val obj = arr.getJSONObject(i)
            SessionMeta(
                startSec = obj.optLong("start_sec", 0L),
                durationSec = obj.optInt("duration_sec", 0),
                segmentCount = obj.optInt("segment_count", 0),
                createdAt = obj.optLong("created_at", 0L)
            )
        }
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
}

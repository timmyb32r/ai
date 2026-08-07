package com.crimobile.offline

import android.content.Context
import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import com.crimobile.debug.DebugLogger

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
class OfflineStorageManager private constructor(private val rootDir: File) {

    constructor(context: Context) : this(File(context.filesDir, "cri_offline"))

    // Shared lock across all instances (CriViewModel + SyncWorker may coexist).
    companion object {
        private val lock = Any()
        private const val TAG = "CRIRadio:offlineStore"
        private fun zeroPad(id: Int) = id.toString().padStart(9, '0')

        /** Test-only factory: operate directly on [rootDir] without a Context. */
        internal fun forRoot(rootDir: File) = OfflineStorageManager(rootDir)
    }

    private val sessionsDir: File = File(rootDir, "sessions")
    private val sessionsIndexFile: File = File(sessionsDir, "index.json")

    init {
        // Clean break: delete old flat directories if they still exist
        val oldMeta = File(rootDir, "metadata")
        val oldAudio = File(rootDir, "audio")
        val oldIndex = File(rootDir, "index.json")
        if (oldMeta.exists() || oldAudio.exists()) {
            DebugLogger.i(TAG, "Deleting old flat storage structure")
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
            // MUST use the canonical SubtitleParser serializer so every persisted
            // field (char_pinyin_uncertain, cedict_meanings, wiktionary_meanings, …)
            // round-trips back via parseSegment. A previous private serializer here
            // dropped those three fields → offline word popups lost CEDICT/Wiktionary
            // glosses and probabilistic-fill flags (offline/live data drift).
            val metaJson = com.crimobile.subtitles.SubtitleParser.segmentToJson(segment).toString(2)
            File(sessionMetaDir(sessionId), fileName(id, "json")).writeText(metaJson)
            File(sessionAudioDir(sessionId), fileName(id, "ts")).writeBytes(tsBytes)
        }
    }

    /**
     * Write the lightweight segment index under the session's metadata dir.
     * Wrapped in the shared lock so a concurrent [loadSegmentsForSession] /
     * [SegmentIndex.read] cannot observe a partially-written index.
     */
    fun writeSegmentIndex(sessionId: String, segments: List<SubtitleSegment>) {
        synchronized(lock) {
            SegmentIndex.write(sessionMetaDir(sessionId), segments)
        }
    }

    // ── Read ───────────────────────────────────────────────────────────

    fun loadSegment(sessionId: String, segmentId: Int): SubtitleSegment? {
        synchronized(lock) {
            val file = File(sessionMetaDir(sessionId), fileName(segmentId, "json"))
            if (!file.exists()) return null
            val obj = org.json.JSONObject(file.readText())
            return com.crimobile.subtitles.SubtitleParser.parseSegment(obj)
        }
    }

    fun loadFullSegment(sessionId: String, segmentId: Int): SubtitleSegment? {
        synchronized(lock) {
            val file = File(sessionMetaDir(sessionId), fileName(segmentId, "json"))
            if (!file.exists()) return null
            return try {
                val obj = org.json.JSONObject(file.readText())
                com.crimobile.subtitles.SubtitleParser.parseSegment(obj)
            } catch (e: Exception) {
                DebugLogger.w(TAG, "loadFullSegment: ${e.message}")
                null
            }
        }
    }

    fun loadSegmentsForSession(sessionId: String): List<SegmentMeta> {
        synchronized(lock) {
            val metaDir = sessionMetaDir(sessionId)
            if (!metaDir.exists()) return emptyList()

            // First try the lightweight SegmentIndex.
            val fromIndex = SegmentIndex.read(metaDir)
            if (fromIndex.isNotEmpty()) return fromIndex

            // Build from individual JSON files, write SegmentIndex for next time.
            val segmentFiles = metaDir.listFiles { f ->
                f.name.endsWith(".json") &&
                    f.name != "_segments_cache.json" &&
                    f.name != SegmentIndex.INDEX_FILE_NAME
            } ?: return emptyList()
            if (segmentFiles.isEmpty()) return emptyList()

            val fullSegments = segmentFiles.toList().parallelStream()
                .map { f ->
                    try {
                        val obj = org.json.JSONObject(f.readText())
                        com.crimobile.subtitles.SubtitleParser.parseSegment(obj)
                    } catch (e: Exception) {
                        DebugLogger.w(TAG, "parse segment ${f.name}: ${e.message}")
                        null
                    }
                }
                .filter { it != null }
                .sorted(Comparator.comparingInt { s -> s!!.segment_id })
                .toList()
                .filterNotNull()

            if (fullSegments.isNotEmpty()) {
                SegmentIndex.write(metaDir, fullSegments)
            }

            // Clean up legacy _segments_cache.json if present
            File(metaDir, "_segments_cache.json").delete()

            return fullSegments.map { seg ->
                SegmentMeta(
                    segment_id = seg.segment_id,
                    timeline_start_sec = seg.timeline_start_sec,
                    timeline_end_sec = seg.timeline_end_sec,
                    ts_file = seg.ts_file,
                    text_zh = seg.text_zh,
                    text_pinyin = seg.text_pinyin
                )
            }
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

    fun invalidateCache(sessionId: String) {
        File(sessionMetaDir(sessionId), "_segments_cache.json").delete()
    }

    fun countSegmentsInSession(sessionId: String): Int {
        synchronized(lock) {
            val d = sessionMetaDir(sessionId)
            return if (d.exists()) countSegmentMetaFiles(d) else 0
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
                val sid = sessionId(s.startSec, s.durationSec)
                val segs = SegmentIndex.read(sessionMetaDir(sid))
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
                DebugLogger.w(TAG, "Failed to read sessions index, rebuilding: ${e.message}")
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
                val count = if (metaDir.exists()) countSegmentMetaFiles(metaDir) else 0
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

    fun deleteAllData() {
        synchronized(lock) {
            if (rootDir.exists()) {
                rootDir.deleteRecursively()
                DebugLogger.i(TAG, "All offline data deleted")
            }
        }
    }

    fun deleteSession(sessionId: String) {
        synchronized(lock) {
            val d = sessionDir(sessionId)
            if (d.exists()) {
                d.deleteRecursively()
                DebugLogger.i(TAG, "Deleted session: $sessionId")
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
                    DebugLogger.i(TAG, "Pruned old session: $sid")
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

    /**
     * Concatenates all .ts audio files for [sessionId] into a single
     * continuous file.  MPEG-TS packets (188 bytes, 0x47 sync) can be
     * naively concatenated — the demuxer handles PAT/PMT changes.
     *
     * Returns the concatenated file, or null if no audio files exist.
     *
     * Called after [DownloadEngine.downloadRange] completes so that
     * [OfflineRadioPlayer] can play a single gapless stream instead of
     * stitching per-segment files with [ConcatenatingMediaSource].
     */
    fun concatAudioFiles(sessionId: String): File? {
        synchronized(lock) {
            val audioDir = sessionAudioDir(sessionId)
            if (!audioDir.exists()) return null

            val tsFiles = audioDir.listFiles { f -> f.name.endsWith(".ts") }
                ?.sortedBy { it.name } ?: return null
            if (tsFiles.isEmpty()) return null

            val outFile = File(audioDir, "continuous.ts")
            val tmpFile = File(audioDir, ".continuous.ts.tmp")

            try {
                tmpFile.outputStream().use { out ->
                    val buf = ByteArray(65536)
                    for (f in tsFiles) {
                        f.inputStream().use { inp ->
                            var n: Int
                            while (inp.read(buf).also { n = it } > 0) {
                                out.write(buf, 0, n)
                            }
                        }
                    }
                }
                if (!tmpFile.renameTo(outFile)) {
                    tmpFile.copyTo(outFile, overwrite = true)
                    tmpFile.delete()
                }
                DebugLogger.i(TAG, "concatenated ${tsFiles.size} .ts files → continuous.ts (${outFile.length()} bytes)")
                return outFile
            } catch (e: Exception) {
                DebugLogger.w(TAG, "concatAudioFiles failed: ${e.message}")
                tmpFile.delete()
                return null
            }
        }
    }

    /** Returns the concatenated audio file for a session, or null. */
    fun getConcatenatedAudioFile(sessionId: String): File? {
        val file = File(sessionAudioDir(sessionId), "continuous.ts")
        return if (file.exists() && file.length() > 0) file else null
    }

    // ── Internal ───────────────────────────────────────────────────────

    private fun fileName(id: Int, ext: String) = "${zeroPad(id)}.$ext"
    private fun segmentIdFromName(name: String): Int = name.substringBefore('.').toIntOrNull() ?: -1

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

    /** Count per-segment metadata files, excluding the index and temp files. */
    private fun countSegmentMetaFiles(metaDir: File): Int {
        return metaDir.listFiles { f ->
            f.name.endsWith(".json") &&
                f.name != SegmentIndex.INDEX_FILE_NAME &&
                f.name != "_segments_cache.json" &&
                !f.name.startsWith(".")
        }?.size ?: 0
    }

}

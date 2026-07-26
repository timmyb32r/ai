package com.crimobile.offline

import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import com.crimobile.debug.DebugLogger

/**
 * Writes and reads the lightweight segment index for a session.
 *
 * The index is a single JSON file (`segment_index.json`) containing
 * a compact JSON array of [SegmentMeta] objects — no pretty-print,
 * streamed one segment at a time to keep RAM low.
 *
 * After the index is built, individual per-segment JSON files are
 * still kept for on-demand full-segment loading via [OfflineStorageManager.loadFullSegment].
 */
object SegmentIndex {

    private const val TAG = "CRIRadio:segIndex"
    const val INDEX_FILE_NAME = "segment_index.json"

    /** Stream-write a compact JSON array of SegmentMeta. */
    fun write(sessionMetaDir: File, segments: List<SubtitleSegment>) {
        val tmpFile = File(sessionMetaDir, ".$INDEX_FILE_NAME.tmp")
        try {
            tmpFile.bufferedWriter().use { writer ->
                writer.write("[")
                for ((i, seg) in segments.withIndex()) {
                    if (i > 0) writer.write(",")
                    writer.write(segmentToJson(seg))
                }
                writer.write("]")
            }
            val target = File(sessionMetaDir, INDEX_FILE_NAME)
            if (!tmpFile.renameTo(target)) {
                target.writeText(tmpFile.readText())
                tmpFile.delete()
            }
            DebugLogger.i(TAG, "wrote ${segments.size} entries to $INDEX_FILE_NAME")
        } catch (e: Exception) {
            DebugLogger.w(TAG, "write failed: ${e.message}")
            tmpFile.delete()
        }
    }

    /** Read the full index into RAM. Returns empty list on any error. */
    fun read(sessionMetaDir: File): List<SegmentMeta> {
        val file = File(sessionMetaDir, INDEX_FILE_NAME)
        if (!file.exists()) return emptyList()
        return try {
            val arr = JSONArray(file.readText())
            (0 until arr.length()).map { i ->
                val obj = arr.getJSONObject(i)
                SegmentMeta(
                    segment_id = obj.getInt("segment_id"),
                    timeline_start_sec = obj.getDouble("timeline_start_sec"),
                    timeline_end_sec = obj.getDouble("timeline_end_sec"),
                    ts_file = obj.optString("ts_file", ""),
                    text_zh = obj.optString("text_zh", ""),
                    text_pinyin = obj.optString("text_pinyin", "")
                )
            }
        } catch (e: Exception) {
            DebugLogger.w(TAG, "read failed: ${e.message}")
            emptyList()
        }
    }

    // ── internal ──────────────────────────────────────────────────

    private fun segmentToJson(seg: SubtitleSegment): String {
        return JSONObject().apply {
            put("segment_id", seg.segment_id)
            put("timeline_start_sec", seg.timeline_start_sec)
            put("timeline_end_sec", seg.timeline_end_sec)
            put("ts_file", seg.ts_file)
            put("text_zh", seg.text_zh)
            put("text_pinyin", seg.text_pinyin)
        }.toString() // compact, no indent
    }
}

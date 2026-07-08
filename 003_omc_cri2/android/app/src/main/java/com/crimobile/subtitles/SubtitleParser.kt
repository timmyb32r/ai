package com.crimobile.subtitles

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import org.json.JSONObject

/**
 * Shared JSON → SubtitleSegment parser used by all subtitle sources.
 * Extracted from SseSubtitleSource so HttpSubtitleSource can reuse
 * the identical deserialization without duplication.
 */
object SubtitleParser {

    fun parseSegment(json: JSONObject): SubtitleSegment {
        val wordsArray = json.optJSONArray("words") ?: org.json.JSONArray()
        val words = mutableListOf<WordEntry>()
        for (i in 0 until wordsArray.length()) {
            val w = wordsArray.getJSONObject(i)
            words.add(
                WordEntry(
                    text = w.optString("text", ""),
                    char_start = w.optInt("char_start", 0),
                    char_end = w.optInt("char_end", 0),
                    start_sec = w.optDouble("start_sec", 0.0),
                    end_sec = w.optDouble("end_sec", 0.0),
                    pinyin = w.optString("pinyin", ""),
                    translation = w.optString("translation", "")
                )
            )
        }

        return SubtitleSegment(
            segment_id = json.optInt("segment_id", 0),
            timeline_start_sec = json.optDouble("timeline_start_sec", 0.0),
            timeline_end_sec = json.optDouble("timeline_end_sec", 0.0),
            ts_file = json.optString("ts_file", ""),
            text_zh = json.optString("text_zh", ""),
            text_pinyin = json.optString("text_pinyin", ""),
            text_en = json.optString("text_en", ""),
            words = words
        )
    }
}

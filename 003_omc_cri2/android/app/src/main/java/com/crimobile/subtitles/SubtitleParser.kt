package com.crimobile.subtitles

import android.util.Log
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
            // Parse structured senses (BKRS)
            val sensesArray = w.optJSONArray("senses")
            val senses = mutableListOf<com.crimobile.model.WordSense>()
            if (sensesArray != null) {
                for (j in 0 until sensesArray.length()) {
                    val so = sensesArray.getJSONObject(j)
                    val labelsArray = so.optJSONArray("labels")
                    val labels = mutableListOf<String>()
                    if (labelsArray != null) {
                        for (k in 0 until labelsArray.length()) {
                            labels.add(labelsArray.optString(k, ""))
                        }
                    }
                    senses.add(
                        com.crimobile.model.WordSense(
                            number = so.optInt("number", 0),
                            labels = labels,
                            text = so.optString("text", ""),
                            notes = so.optString("notes", "")
                        )
                    )
                }
            }
            // Parse per-character pinyin
            val charPinyinArray = w.optJSONArray("char_pinyin")
            val charPinyin = mutableListOf<String>()
            if (charPinyinArray != null) {
                for (j in 0 until charPinyinArray.length()) {
                    charPinyin.add(charPinyinArray.optString(j, ""))
                }
            }
            if (charPinyin.isEmpty() && !w.optString("pinyin", "").isNullOrBlank()) {
                Log.w("CRIRadio:parse", "word=${w.optString("text")} — char_pinyin MISSING, pinyin=${w.optString("pinyin")}")
            }
            // Parse per-character uncertainty flags (probabilistic fills).
            val uncertainArray = w.optJSONArray("char_pinyin_uncertain")
            val charUncertain = mutableListOf<Boolean>()
            if (uncertainArray != null) {
                for (j in 0 until uncertainArray.length()) {
                    charUncertain.add(uncertainArray.optBoolean(j, false))
                }
            }
            words.add(
                WordEntry(
                    text = w.optString("text", ""),
                    char_start = w.optInt("char_start", 0),
                    char_end = w.optInt("char_end", 0),
                    start_sec = w.optDouble("start_sec", 0.0),
                    end_sec = w.optDouble("end_sec", 0.0),
                    pinyin = w.optString("pinyin", ""),
                    char_pinyin = charPinyin,
                    char_pinyin_uncertain = charUncertain,
                    translation = w.optString("translation", ""),
                    senses = senses
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

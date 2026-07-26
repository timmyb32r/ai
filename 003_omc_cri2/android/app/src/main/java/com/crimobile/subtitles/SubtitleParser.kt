package com.crimobile.subtitles

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import org.json.JSONObject
import com.crimobile.debug.DebugLogger

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
                DebugLogger.w("CRIRadio:parse", "word=${w.optString("text")} — char_pinyin MISSING, pinyin=${w.optString("pinyin")}")
            }
            // Parse per-character uncertainty flags (probabilistic fills).
            val uncertainArray = w.optJSONArray("char_pinyin_uncertain")
            val charUncertain = mutableListOf<Boolean>()
            if (uncertainArray != null) {
                for (j in 0 until uncertainArray.length()) {
                    charUncertain.add(uncertainArray.optBoolean(j, false))
                }
            }
            // Parse CC-CEDICT glosses (second dictionary).
            val cedictArray = w.optJSONArray("cedict_meanings")
            val cedictMeanings = mutableListOf<String>()
            if (cedictArray != null) {
                for (j in 0 until cedictArray.length()) {
                    val m = cedictArray.optString(j, "")
                    if (m.isNotBlank()) cedictMeanings.add(m)
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
                    senses = senses,
                    cedict_meanings = cedictMeanings
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

    /**
     * Canonical serializer — the exact inverse of [parseSegment]. ALL subtitle
     * persistence (offline cache, downloads) must use this so offline data can
     * never drift from live data. Any field added to [parseSegment] must be
     * added here too; the round-trip parity test enforces this.
     */
    fun segmentToJson(seg: SubtitleSegment): JSONObject {
        val wordsArr = org.json.JSONArray()
        for (w in seg.words) {
            val wj = JSONObject().apply {
                put("text", w.text)
                put("char_start", w.char_start)
                put("char_end", w.char_end)
                put("start_sec", w.start_sec)
                put("end_sec", w.end_sec)
                put("pinyin", w.pinyin)
                put("translation", w.translation)
                if (w.char_pinyin.isNotEmpty()) {
                    put("char_pinyin", org.json.JSONArray(w.char_pinyin))
                }
                if (w.char_pinyin_uncertain.isNotEmpty()) {
                    put("char_pinyin_uncertain", org.json.JSONArray(w.char_pinyin_uncertain))
                }
                if (w.senses.isNotEmpty()) {
                    val sa = org.json.JSONArray()
                    for (s in w.senses) {
                        sa.put(JSONObject().apply {
                            put("number", s.number)
                            put("labels", org.json.JSONArray(s.labels))
                            put("text", s.text)
                            put("notes", s.notes)
                        })
                    }
                    put("senses", sa)
                }
                if (w.cedict_meanings.isNotEmpty()) {
                    put("cedict_meanings", org.json.JSONArray(w.cedict_meanings))
                }
            }
            wordsArr.put(wj)
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
        }
    }
}

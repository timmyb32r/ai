package com.crimobile.infrastructure

import com.crimobile.model.SubtitleEvent
import com.crimobile.model.WordBoundary

/**
 * Lightweight SSE subtitle JSON parser — zero org.json allocations.
 *
 * Parses the CRI server's SubtitleEvent JSON payload using indexOf +
 * substring extraction.  No HashMap, JSONObject, or JSONArray objects
 * are created; only the target [SubtitleEvent] / [WordBoundary] domain
 * objects survive.
 *
 * Replace with a streaming or code-gen parser if the schema grows
 * complex enough to warrant it.
 */
object SubtitleParser {

    /**
     * Parses a complete subtitle JSON payload.
     *
     * @param payload  raw JSON string from the SSE `data:` line.
     * @return parsed [SubtitleEvent], or null if the payload is malformed
     *         or missing required fields.
     */
    fun parseSubtitle(payload: String): SubtitleEvent? {
        try {
            val s = extractDouble(payload, "\"start\"") ?: return null
            val e = extractDouble(payload, "\"end\"") ?: return null
            val t = extractString(payload, "\"text_zh\"") ?: return null
            val py = extractString(payload, "\"pinyin\"") ?: ""
            val en = extractString(payload, "\"en\"") ?: ""
            val words = parseWordsArray(payload)
            return SubtitleEvent(s, e, t, words, py, en)
        } catch (_: Exception) {
            return null
        }
    }

    /**
     * Parses a sync event payload: `{"audioTimelineStart": 123.456}`.
     *
     * @return the timeline start in seconds, or null.
     */
    fun parseSync(payload: String): Double? {
        try {
            return extractDouble(payload, "\"audioTimelineStart\"")
        } catch (_: Exception) {
            return null
        }
    }

    /**
     * Extracts the `"words": [...]` array from [payload] and parses
     * each word object without intermediate JSON wrappers.
     */
    private fun parseWordsArray(payload: String): List<WordBoundary>? {
        val arrStart = payload.indexOf("\"words\"")
        if (arrStart < 0) return null
        val open = payload.indexOf('[', arrStart)
        if (open < 0) return null
        val close = payload.lastIndexOf(']')
        if (close <= open) return null

        val result = mutableListOf<WordBoundary>()
        var pos = open + 1
        while (pos < close) {
            val objStart = payload.indexOf('{', pos)
            if (objStart < 0 || objStart >= close) break
            val objEnd = payload.indexOf('}', objStart)
            if (objEnd < 0) break
            val wordObj = payload.substring(objStart + 1, objEnd)

            val cs = extractInt(wordObj, "\"charStart\"") ?: 0
            val ce = extractInt(wordObj, "\"charEnd\"") ?: 0
            val ss = extractDouble(wordObj, "\"startSec\"") ?: 0.0
            val es = extractDouble(wordObj, "\"endSec\"") ?: 0.0
            val wp = extractString(wordObj, "\"pinyin\"") ?: ""
            val we = extractString(wordObj, "\"en\"") ?: ""

            result.add(WordBoundary(cs, ce, ss, es, wp, we))
            pos = objEnd + 1
        }
        return if (result.isEmpty()) null else result
    }

    // ── Lightweight JSON extraction helpers ──────────────────────────────

    /** Returns the character right after `"key":`, skipping whitespace. */
    private fun valueStart(json: String, key: String): Int {
        val ki = json.indexOf(key)
        if (ki < 0) return -1
        var pos = json.indexOf(':', ki + key.length)
        if (pos < 0) return -1
        pos++
        while (pos < json.length && json[pos] == ' ') pos++
        return if (pos < json.length) pos else -1
    }

    private fun extractString(json: String, key: String): String? {
        val pos = valueStart(json, key)
        if (pos < 0 || json[pos] != '"') return null
        val close = json.indexOf('"', pos + 1)
        if (close < 0) return null
        return json.substring(pos + 1, close)
    }

    private fun extractDouble(json: String, key: String): Double? {
        val pos = valueStart(json, key)
        if (pos < 0) return null
        return if (json[pos] == '"') {
            val close = json.indexOf('"', pos + 1)
            if (close < 0) null else json.substring(pos + 1, close).toDoubleOrNull()
        } else {
            extractRawNumberAt(json, pos)?.toDoubleOrNull()
        }
    }

    private fun extractInt(json: String, key: String): Int? {
        val pos = valueStart(json, key)
        if (pos < 0) return null
        return if (json[pos] == '"') {
            val close = json.indexOf('"', pos + 1)
            if (close < 0) null else json.substring(pos + 1, close).toIntOrNull()
        } else {
            extractRawNumberAt(json, pos)?.toIntOrNull()
        }
    }

    private fun extractRawNumberAt(json: String, start: Int): String? {
        var end = start
        while (end < json.length && (json[end].isDigit() || json[end] == '.' || json[end] == '-')) end++
        return if (end > start) json.substring(start, end) else null
    }
}
